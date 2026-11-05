#!/usr/bin/env python3
"""Bubble-zoom host job. Runs on the macOS host (Vision is macOS-only), never in the container.

Finds CBZ pages with no sidecar, runs the balloon detector (run_yolo.py), the Vision/outline/order
tool (bubbles3.swift) and the panel detector (run_panels.py, which sets the reading order) over them in
batches, and writes one content-addressed sidecar per page to
<library>/.bubbles/<sha[0:2]>/<sha1>.json, which the Go server serves.
Spec: comic_reader_app docs/superpowers/specs/2026-10-10-bubble-zoom-design.md, Part 5.
"""
import argparse, base64, hashlib, json, os, shutil, signal, sqlite3, subprocess, sys, tempfile, time, zipfile
from concurrent.futures import ThreadPoolExecutor
import panel_order

HERE = os.path.dirname(os.path.abspath(__file__))
HOME = os.path.expanduser("~/Library/Application Support/comic-bubbles")
LIB = os.environ.get("BUBBLES_LIB", "/Volumes/Media/comics")
BATCH_PAGES = 200
EXTS = (".jpg", ".jpeg", ".png", ".webp")  # what bubbles3.swift reads


def sidecar_path(root, sha):
    return os.path.join(root, sha[:2], sha + ".json")


def book_index_path(root, rel):
    """Per-book index: entry name -> page sha1, so the server can find sidecars without reading pages."""
    return os.path.join(root, "books", hashlib.sha1(rel.encode()).hexdigest() + ".json")


def write_json(dest, obj):
    """tmp + fsync + rename: the server never sees a half-written file, nor a truncated one after a power cut."""
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    with open(dest + ".tmp", "w") as t:
        json.dump(obj, t, separators=(",", ":"))
        t.flush()
        os.fsync(t.fileno())
    os.replace(dest + ".tmp", dest)


def write_book_index(root, key, names):
    path, size, mtime = key
    rel = os.path.relpath(path, os.path.dirname(root))
    write_json(book_index_path(root, rel), {"v": 1, "size": size, "mtime": mtime, "pages": names})


def list_cbzs(lib):
    """(path, size, mtime) of every CBZ outside dot-dirs, newest first so new books beat the backfill."""
    os.listdir(lib)  # raises PermissionError without Full Disk Access; os.walk alone would hide it
    found = []
    for root, dirs, files in os.walk(lib):
        dirs[:] = [d for d in dirs if not d.startswith(".")]
        for f in files:
            if f.lower().endswith((".cbz", ".cbr")) and not f.startswith("."):
                p = os.path.join(root, f)
                try:
                    st = os.stat(p)
                except OSError:
                    continue
                found.append((p, st.st_size, int(st.st_mtime)))
    found.sort(key=lambda t: t[2], reverse=True)
    return found


def open_state(path):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    db = sqlite3.connect(path)
    db.execute("CREATE TABLE IF NOT EXISTS cbz (path TEXT PRIMARY KEY, size INTEGER, mtime INTEGER, status TEXT, error TEXT)")
    return db


def is_current(db, key):
    """True when this exact (path, size, mtime) was already done or failed."""
    path, size, mtime = key
    return db.execute("SELECT size, mtime FROM cbz WHERE path = ?", (path,)).fetchone() == (size, mtime)


def mark(db, key, status, error=""):
    db.execute("INSERT OR REPLACE INTO cbz VALUES (?, ?, ?, ?, ?)", (*key, status, error[:500]))


def now():
    return time.strftime("%Y-%m-%d %H:%M:%S")


def transient(e):
    """Errors that say nothing about the book itself (disk full, I/O, a tool killed by a signal).
    The book is left unmarked and retried next run; anything else marks it failed until it changes."""
    if isinstance(e, subprocess.CalledProcessError):
        return e.returncode < 0
    return isinstance(e, OSError)


def handle_sigterm():
    """launchd stops the job with SIGTERM, and a closed SSH session sends SIGHUP to a hand run; exit through
    `finally` so staged pages (up to ~1.5 GB) are removed."""
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    if signal.getsignal(signal.SIGHUP) is not signal.SIG_IGN:  # under nohup, keep running
        signal.signal(signal.SIGHUP, lambda *_: sys.exit(129))


def err_text(e):
    stderr = getattr(e, "stderr", None)
    if isinstance(stderr, bytes):
        stderr = stderr.decode(errors="replace")
    return f"{type(e).__name__}: {(stderr or str(e)).strip()[-400:]}"


def entries(path):
    """(entry name, bytes) for each file in a CBZ, or a CBR (RAR) via unrar."""
    if not path.lower().endswith(".cbr"):
        with zipfile.ZipFile(path) as z:
            for info in z.infolist():
                if not info.is_dir():
                    yield info.filename, z.read(info)
        return
    unrar = shutil.which("unrar") or "/opt/homebrew/bin/unrar"  # launchd's PATH lacks Homebrew
    with tempfile.TemporaryDirectory(prefix="cbr-") as d:
        subprocess.run([unrar, "x", "-idq", "-y", path, d + "/"], check=True, capture_output=True)
        for top, _, files in os.walk(d):
            for f in files:
                full = os.path.join(top, f)
                with open(full, "rb") as fh:
                    yield os.path.relpath(full, d), fh.read()


def stage(cbz, root, batch_dir, pending, redo=False):
    """Write each page of cbz that has no sidecar into batch_dir as <sha1><ext>. Returns the staged file
    names and the book's {entry name: sha1} (for its index). `pending` holds shas already staged this
    batch, so a page shared by two books is processed once. With redo, pages with a sidecar are staged too."""
    staged, names = [], {}
    for entry, data in entries(cbz):
        name = entry.lower()
        if os.path.basename(name).startswith(".") or not name.endswith(EXTS):
            continue
        sha = hashlib.sha1(data).hexdigest()
        names[entry] = sha
        if sha in pending or (not redo and os.path.exists(sidecar_path(root, sha))):
            continue
        pending.add(sha)
        fn = sha + os.path.splitext(name)[1]
        with open(os.path.join(batch_dir, fn), "wb") as f:
            f.write(data)
        staged.append(fn)
    return staged, names


def detector_cutoff(home=HOME, name="model.conf", default="0.6"):
    """A detector's confidence cutoff, installed beside its weights by install.sh. Balloons (model.conf):
    the library-tuned detector (2026-10-13) runs at 0.5; an install without the file has the original
    model, at 0.6. Panels (panel.conf): see panel_cutoff."""
    try:
        with open(os.path.join(home, name)) as f:
            v = f.read().strip()
        return v if 0 < float(v) < 1 else default
    except (OSError, ValueError):
        return default


def panel_cutoff(home=HOME):
    """Panel detector cutoff (panel.conf), applied when the sidecar is packed; 0.5 for the public model."""
    return float(detector_cutoff(home, "panel.conf", "0.5"))


def run_tools(batch_dir, work_dir):
    """Detector, then Vision/outline/order, over batch_dir. Returns the dir holding bubbles.jsonl.
    The tools get no stdin: a hand run outlives its SSH session, and Python won't start on a dead one."""
    boxes, out = os.path.join(work_dir, "boxes"), os.path.join(work_dir, "out")
    subprocess.run([os.path.join(HOME, "venv", "bin", "python"), os.path.join(HERE, "run_yolo.py"),
                    os.path.join(HOME, "model.pt"), batch_dir, boxes], check=True, capture_output=True, stdin=subprocess.DEVNULL)
    subprocess.run([os.path.join(HOME, "bubbles"), batch_dir, out, boxes, detector_cutoff(), "--no-render"],
                   check=True, capture_output=True, stdin=subprocess.DEVNULL)
    if os.path.exists(os.path.join(HOME, "panel.pt")):  # an older install without it keeps the Swift order
        run_panel_tool(batch_dir, out)
    return out


def run_panel_tool(batch_dir, out):
    subprocess.run([os.path.join(HOME, "venv", "bin", "python"), os.path.join(HERE, "run_panels.py"),
                    os.path.join(HOME, "panel.pt"), batch_dir, os.path.join(out, "panels.json")],
                   check=True, capture_output=True, stdin=subprocess.DEVNULL)


def run_panels_only(batch_dir, work_dir):
    """The panel detector alone (a new panel model): the balloons and their text stay as they are."""
    out = os.path.join(work_dir, "out")
    os.makedirs(out)
    run_panel_tool(batch_dir, out)
    return out


def pack(out, root):
    """bubbles.jsonl + <sha>_labels.png (+ panels.json) -> one v1 sidecar per page (balloons in
    reading order, and the ordered panels for panel zoom), each written tmp-then-rename. A balloon carries its text as Vision read it ("t"), when the tool wrote it. A balloon's label is its pixel value in the mask, i.e. its place in the Swift
    tool's order, so it stays with the balloon when the panels reorder them."""
    panels = {}
    if os.path.exists(os.path.join(out, "panels.json")):
        with open(os.path.join(out, "panels.json")) as f:
            panels = json.load(f)
    mc = panel_cutoff(HOME)  # HOME passed explicitly: the default is bound at import, tests patch HOME
    n = 0
    with open(os.path.join(out, "bubbles.jsonl")) as f:
        for line in f:
            if not line.strip():
                continue
            row = json.loads(line)
            sha = os.path.splitext(row["page"])[0]
            b = row["bubbles"]
            order = panel_order.order_bubbles(row["w"], row["h"], b, panels.get(row["page"], []), min_conf=mc)
            texts = row.get("texts") or []  # older Swift tools write none
            sc = {"v": 1, "w": row["w"], "h": row["h"],
                  "bubbles": [{"r": b[i], "label": i + 1, **({"t": texts[i]} if i < len(texts) and texts[i] else {})}
                              for i in order]}
            P = panel_order.ordered_panels(row["w"], row["h"], panels.get(row["page"], []), min_conf=mc, fold=False)
            if P:  # panel zoom: the boxes the app steps through, in reading order
                sc["panels"] = [[int(round(v)) for v in p] for p in P]
            if sc["bubbles"]:
                with open(os.path.join(out, sha + "_labels.png"), "rb") as m:
                    sc["mask"] = base64.b64encode(m.read()).decode()
            write_json(sidecar_path(root, sha), sc)
            n += 1
    return n


def repack(out, root):
    """panels.json -> each page's existing sidecar with new panels and its balloons re-read panel by panel.
    A balloon's label is its place in the Swift order, so sorting by it recovers the input order_bubbles wants;
    text and mask stay as they were. A page with no sidecar is skipped (the full run writes it)."""
    with open(os.path.join(out, "panels.json")) as f:
        panels = json.load(f)
    mc = panel_cutoff(HOME)

    def one(item):
        page, P = item
        path = sidecar_path(root, os.path.splitext(page)[0])
        try:
            with open(path) as f:
                sc = json.load(f)
        except FileNotFoundError:
            return 0
        swift = sorted(sc["bubbles"], key=lambda b: b["label"])
        order = panel_order.order_bubbles(sc["w"], sc["h"], [b["r"] for b in swift], P, min_conf=mc)
        sc["bubbles"] = [swift[i] for i in order]
        kept = panel_order.ordered_panels(sc["w"], sc["h"], P, min_conf=mc, fold=False)
        sc.pop("panels", None)
        if kept:
            sc["panels"] = [[int(round(v)) for v in p] for p in kept]
        write_json(path, sc)
        return 1
    return sum(pmap(one, panels.items()))


def pmap(fn, items):
    """fn over items on 8 threads: the media drive is a USB disk, and overlapping its reads and fsyncs about
    halves the wait (measured 2026-10-29)."""
    with ThreadPoolExecutor(8) as ex:
        return list(ex.map(fn, items))


def process(db, root, batch_dir, batch, tools, stats, log, packer=pack):
    """Run tools over a staged batch [(key, files, names)] and mark its books. If the tools fail on a multi-book
    batch, retry each book alone so one bad page can't block the rest of the batch on every run."""
    if any(files for _, files, _ in batch):
        work = tempfile.mkdtemp(prefix="work-", dir=os.path.dirname(batch_dir))
        try:
            stats["pages"] += packer(tools(batch_dir, work), root)
        except Exception as e:
            if transient(e):
                log(f"{now()} {err_text(e)}; {len(batch)} book(s) left for the next run (will retry)")
                return
            if len(batch) == 1:
                mark(db, batch[0][0], "failed", err_text(e))
                log(f"{now()} FAILED {batch[0][0][0]}: {err_text(e)}")
                stats["failed"] += 1
                db.commit()
                return
            for key, files, names in batch:
                solo = tempfile.mkdtemp(prefix="solo-", dir=os.path.dirname(batch_dir))
                for fn in files:
                    os.link(os.path.join(batch_dir, fn), os.path.join(solo, fn))
                process(db, root, solo, [(key, files, names)], tools, stats, log, packer)
                shutil.rmtree(solo, ignore_errors=True)
            return
        finally:
            shutil.rmtree(work, ignore_errors=True)
    for key, _, names in batch:
        try:
            write_book_index(root, key, names)
        except OSError as e:  # sidecars are fine without it; the server falls back to hashing pages
            log(f"{now()} no book index for {key[0]}: {err_text(e)}")
        mark(db, key, "done")
        stats["done"] += 1
    db.commit()


def fda_target():
    """What Full Disk Access must name: the program launchd starts (the venv's python, resolved).
    tccd checks that "responsible" binary, not the Resources/Python.app it re-execs (seen in the
    tccd log 2026-10-21: subject=.../bin/python3.14)."""
    return os.path.realpath(sys.executable)


def run(lib=LIB, state_path=None, tools=None, log=print, only=None, redo=False, panels_only=False):
    """Process every CBZ under lib, or only those under lib/<only> (sidecars still go to lib/.bubbles).
    redo processes books and pages already done (after a detector or ordering change). panels_only redoes
    only books already done, with the panel detector alone (after a panel model change; about half the time)."""
    tools = tools or (run_panels_only if panels_only else run_tools)
    packer, redo = (repack, True) if panels_only else (pack, redo)
    stamp = now()
    if not os.path.isdir(lib):
        log(f"{stamp} {lib} not mounted; nothing to do")
        return 0
    walk_root = os.path.join(lib, only) if only else lib
    if not os.path.isdir(walk_root):
        log(f"{stamp} --only {only}: no such folder under {lib}")
        return 1
    try:
        books = list_cbzs(walk_root)
    except PermissionError:
        log(f"{stamp} no read access to {lib}: grant Full Disk Access to {fda_target()}")
        return 1
    root = os.path.join(lib, ".bubbles")
    db = open_state(state_path or os.path.join(HOME, "state.sqlite"))
    stats = dict(skipped=0, done=0, failed=0, pages=0)
    t0 = time.time()
    tmp = tempfile.mkdtemp(prefix="bz-")
    gone = False
    try:
        batch_dir = os.path.join(tmp, "pages")
        os.makedirs(batch_dir)
        batch, pending = [], set()
        for key in books:
            if not redo and is_current(db, key):
                stats["skipped"] += 1
                continue
            try:
                files, names = stage(key[0], root, batch_dir, pending, redo)
            except Exception as e:
                if not os.path.isdir(lib):
                    log(f"{now()} {lib} disappeared mid-run; stopping (will retry)")
                    gone = True
                    break
                if transient(e):
                    log(f"{now()} {err_text(e)}: {key[0]} (will retry)")
                    continue
                mark(db, key, "failed", err_text(e))  # bad zip, unsupported compression
                log(f"{now()} FAILED {key[0]}: {err_text(e)}")
                db.commit()
                stats["failed"] += 1
                continue
            # panels_only rewrites sidecars, so a book with a page that has none (never done, or a page added
            # since) is left whole to the full run. Its file time doesn't matter: writeback changes it, not the pages.
            if panels_only and not all(pmap(lambda s: os.path.exists(sidecar_path(root, s)), names.values())):
                for fn in files:
                    os.remove(os.path.join(batch_dir, fn))
                    pending.discard(os.path.splitext(fn)[0])
                stats["skipped"] += 1
                continue
            batch.append((key, files, names))
            if len(pending) >= BATCH_PAGES:
                process(db, root, batch_dir, batch, tools, stats, log, packer)
                shutil.rmtree(batch_dir)
                os.makedirs(batch_dir)
                batch, pending = [], set()
        if batch and not gone:
            process(db, root, batch_dir, batch, tools, stats, log, packer)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
        db.close()
    ms = int((time.time() - t0) * 1000 / stats["pages"]) if stats["pages"] else 0
    log(f"{stamp} books={len(books)} skipped={stats['skipped']} done={stats['done']} "
        f"failed={stats['failed']} pages={stats['pages']} ms_per_page={ms}")
    return 1 if gone else 0


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description="Write bubble-zoom sidecars for new CBZ pages.")
    ap.add_argument("--only", help='a folder under the library, e.g. "Comic Continuum/Saga" (for a test run)')
    ap.add_argument("--redo", action="store_true", help="with --only: redo books already done (new reading order)")
    ap.add_argument("--panels-only", action="store_true", help="with --only: redo done books' panels alone (new panel model)")
    a = ap.parse_args()
    if (a.redo or a.panels_only) and not a.only:
        ap.error("--redo and --panels-only need --only")  # ponytail: a whole-library redo is a ~75 h run; ask for it per folder
    handle_sigterm()
    sys.exit(run(only=a.only, redo=a.redo, panels_only=a.panels_only))
