import base64, hashlib, json, os, shutil, signal, sqlite3, subprocess, sys, tempfile, textwrap, time, unittest, zipfile
from unittest import mock
import bubblejob


def sha(b): return hashlib.sha1(b).hexdigest()


def make_cbz(path, pages):
    with zipfile.ZipFile(path, "w") as z:
        for name, data in pages.items():
            z.writestr(name, data)


class FakeTools:
    """Stands in for YOLO + Swift: one bubble per page (none for pages named in `empty`).
    `raise_` (an exception) is raised whenever a batch contains a page starting with `fail_if_contains`."""
    def __init__(self, fail_if_contains=None, empty=(), raise_=None, bubbles=None, panels=None, texts=None):
        self.calls, self.fail, self.empty = [], fail_if_contains, set(empty)
        self.bubbles, self.panels, self.texts = bubbles or [[1, 2, 3, 4]], panels, texts
        self.raise_ = raise_ or subprocess.CalledProcessError(1, "bubbles", stderr=b"boom")

    def __call__(self, batch_dir, work):
        files = sorted(os.listdir(batch_dir))
        self.calls.append(files)
        if self.fail and any(f.startswith(self.fail) for f in files):
            raise self.raise_
        out = os.path.join(work, "out")
        os.makedirs(out)
        with open(os.path.join(out, "bubbles.jsonl"), "w") as j:
            for f in files:
                stem = os.path.splitext(f)[0]
                bubbles = [] if stem in self.empty else self.bubbles
                row = {"page": f, "w": 100, "h": 150, "bubbles": bubbles}
                if self.texts is not None and bubbles:
                    row["texts"] = self.texts
                j.write(json.dumps(row) + "\n")
                with open(os.path.join(out, stem + "_labels.png"), "wb") as m:
                    m.write(b"PNG" + stem.encode())
        if self.panels is not None:
            with open(os.path.join(out, "panels.json"), "w") as p:
                json.dump({f: self.panels for f in files}, p)
        return out


class FakePanels:
    """Stands in for run_panels.py alone: the same panels on every page."""
    def __init__(self, panels):
        self.calls, self.panels = [], panels

    def __call__(self, batch_dir, work):
        files = sorted(os.listdir(batch_dir))
        self.calls.append(files)
        out = os.path.join(work, "out")
        os.makedirs(out)
        with open(os.path.join(out, "panels.json"), "w") as p:
            json.dump({f: self.panels for f in files}, p)
        return out


class DetectorCutoffTest(unittest.TestCase):
    def test_reads_the_cutoff_installed_with_the_model(self):
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "model.conf"), "w") as f:
            f.write("0.5\n")
        self.assertEqual(bubblejob.detector_cutoff(d), "0.5")

    def test_without_one_keeps_the_original_models_cutoff(self):
        self.assertEqual(bubblejob.detector_cutoff(tempfile.mkdtemp()), "0.6")

    def test_a_garbled_file_keeps_the_original_cutoff(self):
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "model.conf"), "w") as f:
            f.write("half")
        self.assertEqual(bubblejob.detector_cutoff(d), "0.6")

    def test_panel_cutoff_reads_panel_conf(self):
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "panel.conf"), "w") as f:
            f.write("0.4\n")
        self.assertEqual(bubblejob.panel_cutoff(d), 0.4)

    def test_panel_cutoff_is_half_without_a_file_or_with_a_garbled_one(self):
        self.assertEqual(bubblejob.panel_cutoff(tempfile.mkdtemp()), 0.5)
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "panel.conf"), "w") as f:
            f.write("most")
        self.assertEqual(bubblejob.panel_cutoff(d), 0.5)


class BubbleJobTest(unittest.TestCase):
    def setUp(self):
        self.lib = tempfile.mkdtemp()
        self.state = os.path.join(tempfile.mkdtemp(), "state.sqlite")
        self.root = os.path.join(self.lib, ".bubbles")
        self.logs = []

    def tearDown(self):
        shutil.rmtree(self.lib, ignore_errors=True)

    def run_job(self, tools):
        return bubblejob.run(self.lib, self.state, tools, self.logs.append)

    def sidecar(self, data):
        with open(bubblejob.sidecar_path(self.root, sha(data))) as f:
            return json.load(f)

    def test_panel_conf_sets_the_cutoff_for_the_sidecars_panels(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        home = tempfile.mkdtemp()
        with open(os.path.join(home, "panel.conf"), "w") as f:
            f.write("0.7")
        panels = [[0, 0, 48, 150, 0.6], [52, 0, 100, 150, 0.6]]
        with mock.patch.object(bubblejob, "HOME", home):
            self.run_job(FakeTools(panels=panels))
        self.assertNotIn("panels", self.sidecar(b"one"))  # 0.6 < 0.7: nothing usable
        with open(os.path.join(home, "panel.conf"), "w") as f:
            f.write("0.5")
        make_cbz(os.path.join(self.lib, "b.cbz"), {"p1.jpg": b"two"})
        with mock.patch.object(bubblejob, "HOME", home):
            self.run_job(FakeTools(panels=panels))
        self.assertEqual(self.sidecar(b"two")["panels"], [[0, 0, 48, 150], [52, 0, 100, 150]])

    def status(self, name):
        db = sqlite3.connect(self.state)
        row = db.execute("SELECT status FROM cbz WHERE path = ?", (os.path.join(self.lib, name),)).fetchone()
        return row and row[0]

    def test_writes_v1_sidecars_atomically_at_sharded_paths(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one", "p2.png": b"two", "ComicInfo.xml": b"<x/>"})
        self.assertEqual(self.run_job(FakeTools(empty={sha(b"two")})), 0)
        sc = self.sidecar(b"one")
        self.assertEqual(sc["v"], 1)
        self.assertEqual(sc["bubbles"], [{"r": [1, 2, 3, 4], "label": 1}])
        self.assertEqual(base64.b64decode(sc["mask"]), b"PNG" + sha(b"one").encode())
        self.assertEqual(self.sidecar(b"two")["bubbles"], [])
        self.assertNotIn("mask", self.sidecar(b"two"))
        tmps = [f for _, _, fs in os.walk(self.root) for f in fs if f.endswith(".tmp")]
        self.assertEqual(tmps, [])
        self.assertEqual(self.status("a.cbz"), "done")

    @unittest.skipUnless(shutil.which("rar") and shutil.which("unrar"), "needs rar + unrar")
    def test_cbr_pages_get_sidecars(self):
        src = tempfile.mkdtemp()
        os.makedirs(os.path.join(src, "sub"))
        for name, data in {"p1.jpg": b"one", "sub/p2.png": b"two"}.items():
            with open(os.path.join(src, name), "wb") as f:
                f.write(data)
        subprocess.run(["rar", "a", "-idq", "-r", os.path.join(self.lib, "a.cbr"), "."], cwd=src, check=True)
        self.assertEqual(self.run_job(FakeTools()), 0)
        self.assertEqual(self.sidecar(b"one")["v"], 1)
        self.assertEqual(self.sidecar(b"two")["v"], 1)
        self.assertEqual(self.status("a.cbr"), "done")

    def test_second_run_reads_nothing(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        self.run_job(FakeTools())
        tools = FakeTools()
        self.run_job(tools)
        self.assertEqual(tools.calls, [])
        self.assertIn("skipped=1", self.logs[-1])

    def test_renamed_copy_writes_nothing(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        self.run_job(FakeTools())
        shutil.copy(os.path.join(self.lib, "a.cbz"), os.path.join(self.lib, "renamed.cbz"))
        tools = FakeTools()
        self.run_job(tools)
        self.assertEqual(tools.calls, [])
        self.assertEqual(self.status("renamed.cbz"), "done")

    def test_corrupt_cbz_is_failed_and_others_still_processed(self):
        with open(os.path.join(self.lib, "bad.cbz"), "wb") as f:
            f.write(b"not a zip")
        make_cbz(os.path.join(self.lib, "good.cbz"), {"p1.jpg": b"one"})
        self.run_job(FakeTools())
        self.assertEqual(self.status("bad.cbz"), "failed")
        self.assertEqual(self.status("good.cbz"), "done")
        self.assertTrue(os.path.exists(bubblejob.sidecar_path(self.root, sha(b"one"))))

    def test_tool_crash_isolated_to_its_book(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"poison"})
        make_cbz(os.path.join(self.lib, "b.cbz"), {"p1.jpg": b"fine"})
        self.run_job(FakeTools(fail_if_contains=sha(b"poison")))
        self.assertEqual(self.status("a.cbz"), "failed")
        self.assertEqual(self.status("b.cbz"), "done")
        self.assertTrue(os.path.exists(bubblejob.sidecar_path(self.root, sha(b"fine"))))
        tools = FakeTools()
        self.run_job(tools)  # failed book is not retried while unchanged
        self.assertEqual(tools.calls, [])

    def test_duplicates_dotfiles_and_non_images_skipped(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {
            "p1.jpg": b"same", "p2.jpg": b"same", "__MACOSX/._p1.jpg": b"junk", "p3.gif": b"gif"})
        tools = FakeTools()
        self.run_job(tools)
        self.assertEqual(tools.calls, [[sha(b"same") + ".jpg"]])

    def test_dot_dirs_not_walked(self):
        os.makedirs(os.path.join(self.lib, ".hidden"))
        make_cbz(os.path.join(self.lib, ".hidden", "x.cbz"), {"p1.jpg": b"x"})
        tools = FakeTools()
        self.run_job(tools)
        self.assertEqual(tools.calls, [])

    def test_unmounted_library_is_a_quiet_no_op(self):
        self.assertEqual(bubblejob.run(os.path.join(self.lib, "nope"), self.state, FakeTools(), self.logs.append), 0)
        self.assertIn("not mounted", self.logs[-1])

    def test_unreadable_library_fails_loudly(self):
        os.chmod(self.lib, 0)
        try:
            self.assertEqual(self.run_job(FakeTools()), 1)
            self.assertIn("Full Disk Access", self.logs[-1])
        finally:
            os.chmod(self.lib, 0o755)

    def test_failures_are_logged(self):
        with open(os.path.join(self.lib, "bad.cbz"), "wb") as f:
            f.write(b"not a zip")
        self.run_job(FakeTools())
        self.assertTrue(any("FAILED" in l and "bad.cbz" in l for l in self.logs), self.logs)

    def test_transient_tool_errors_leave_the_book_for_next_run(self):
        for err in (OSError(28, "No space left on device"),
                    subprocess.CalledProcessError(-9, "run_yolo.py", stderr=b"")):  # killed by a signal
            with self.subTest(err=type(err).__name__):
                shutil.rmtree(self.lib); os.makedirs(self.lib); self.logs.clear()
                if os.path.exists(self.state):
                    os.remove(self.state)
                make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
                self.run_job(FakeTools(fail_if_contains=sha(b"one"), raise_=err))
                self.assertIsNone(self.status("a.cbz"))
                self.assertTrue(any("will retry" in l for l in self.logs), self.logs)
                tools = FakeTools()
                self.run_job(tools)
                self.assertEqual(self.status("a.cbz"), "done")

    def test_unreadable_book_is_retried_not_failed(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        os.chmod(os.path.join(self.lib, "a.cbz"), 0)
        try:
            self.run_job(FakeTools())
            self.assertIsNone(self.status("a.cbz"))
        finally:
            os.chmod(os.path.join(self.lib, "a.cbz"), 0o644)

    def test_volume_gone_mid_run_stops_without_marking(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        make_cbz(os.path.join(self.lib, "b.cbz"), {"p1.jpg": b"two"})
        gone = os.path.join(self.lib + "-gone")

        def unmount(*_):
            os.rename(self.lib, gone)  # the volume disappears under us
            raise OSError(5, "Input/output error")

        try:
            with mock.patch.object(bubblejob, "stage", side_effect=unmount):
                self.assertEqual(self.run_job(FakeTools()), 1)
            self.assertTrue(any("disappeared" in l for l in self.logs), self.logs)
            db = sqlite3.connect(self.state)
            self.assertEqual(db.execute("SELECT count(*) FROM cbz").fetchone()[0], 0)
        finally:
            if os.path.exists(gone):
                os.rename(gone, self.lib)

    def test_sigterm_cleans_up_staged_pages(self):
        self.stop_cleans_up(signal.SIGTERM)

    def test_sighup_from_a_closed_ssh_session_cleans_up_staged_pages(self):
        self.stop_cleans_up(signal.SIGHUP)

    def test_under_nohup_a_hangup_does_not_stop_the_run(self):
        child = textwrap.dedent(f"""
            import signal, sys
            signal.signal(signal.SIGHUP, signal.SIG_IGN)
            sys.path.insert(0, {os.path.dirname(os.path.abspath(bubblejob.__file__))!r})
            import bubblejob
            bubblejob.handle_sigterm()
            print(signal.getsignal(signal.SIGHUP) is signal.SIG_IGN)
        """)
        out = subprocess.run([sys.executable, "-c", child], capture_output=True, text=True).stdout
        self.assertEqual(out.strip(), "True")

    def stop_cleans_up(self, sig):
        tmpdir = tempfile.mkdtemp()
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        child = textwrap.dedent(f"""
            import sys, time
            sys.path.insert(0, {os.path.dirname(os.path.abspath(bubblejob.__file__))!r})
            import bubblejob
            bubblejob.handle_sigterm()
            def slow(batch_dir, work):
                print("staged", flush=True); time.sleep(30)
            bubblejob.run({self.lib!r}, {self.state!r}, slow)
        """)
        p = subprocess.Popen([sys.executable, "-c", child], stdout=subprocess.PIPE,
                             env={**os.environ, "TMPDIR": tmpdir})
        self.assertEqual(p.stdout.readline().strip(), b"staged")
        p.send_signal(sig)
        p.wait(timeout=10)
        self.assertEqual(os.listdir(tmpdir), [])
        shutil.rmtree(tmpdir)

    def test_only_limits_the_walk_but_writes_to_the_library_sidecars(self):
        os.makedirs(os.path.join(self.lib, "Saga"))
        os.makedirs(os.path.join(self.lib, "Other"))
        make_cbz(os.path.join(self.lib, "Saga", "s1.cbz"), {"p1.jpg": b"saga"})
        make_cbz(os.path.join(self.lib, "Other", "o1.cbz"), {"p1.jpg": b"other"})
        tools = FakeTools()
        self.assertEqual(bubblejob.run(self.lib, self.state, tools, self.logs.append, only="Saga"), 0)
        self.assertEqual(tools.calls, [[sha(b"saga") + ".jpg"]])
        self.assertTrue(os.path.exists(bubblejob.sidecar_path(self.root, sha(b"saga"))))
        self.assertIsNone(self.status(os.path.join("Other", "o1.cbz")))

    def test_only_with_a_missing_folder_fails_loudly(self):
        self.assertEqual(bubblejob.run(self.lib, self.state, FakeTools(), self.logs.append, only="Nope"), 1)
        self.assertIn("Nope", self.logs[-1])

    def book_index(self, rel):
        path = bubblejob.book_index_path(self.root, rel)
        with open(path) as f:
            return json.load(f)

    def test_writes_a_book_index_the_server_can_trust(self):
        path = os.path.join(self.lib, "Saga", "s1.cbz")
        os.makedirs(os.path.dirname(path))
        make_cbz(path, {"p1.jpg": b"one", "p2.jpg": b"one", "x/p3.png": b"three", "ComicInfo.xml": b"<x/>"})
        self.run_job(FakeTools())
        idx = self.book_index(os.path.join("Saga", "s1.cbz"))
        st = os.stat(path)
        self.assertEqual((idx["size"], idx["mtime"]), (st.st_size, int(st.st_mtime)))
        self.assertEqual(idx["pages"], {"p1.jpg": sha(b"one"), "p2.jpg": sha(b"one"), "x/p3.png": sha(b"three")})

    def test_renamed_copy_gets_its_own_index_without_rerunning_tools(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        self.run_job(FakeTools())
        shutil.copy(os.path.join(self.lib, "a.cbz"), os.path.join(self.lib, "b.cbz"))
        tools = FakeTools()
        self.run_job(tools)
        self.assertEqual(tools.calls, [])
        self.assertEqual(self.book_index("b.cbz")["pages"], {"p1.jpg": sha(b"one")})

    def test_failed_book_gets_no_index(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"poison"})
        self.run_job(FakeTools(fail_if_contains=sha(b"poison")))
        self.assertFalse(os.path.exists(bubblejob.book_index_path(self.root, "a.cbz")))

    def test_panels_reorder_balloons_but_labels_stay_with_their_mask_pixels(self):
        # Swift order: right panel's balloon first. Panels say the left panel reads first.
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        right, left = [60, 10, 20, 10], [10, 10, 20, 10]
        panels = [[0, 0, 48, 150, 0.9], [52, 0, 100, 150, 0.9]]
        self.run_job(FakeTools(bubbles=[right, left], panels=panels))
        self.assertEqual(self.sidecar(b"one")["bubbles"], [{"r": left, "label": 2}, {"r": right, "label": 1}])

    def test_without_panels_the_swift_order_stands(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        right, left = [60, 10, 20, 10], [10, 10, 20, 10]
        self.run_job(FakeTools(bubbles=[right, left]))
        self.assertEqual(self.sidecar(b"one")["bubbles"], [{"r": right, "label": 1}, {"r": left, "label": 2}])

    def test_panels_land_in_the_sidecar_in_reading_order(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        panels = [[52.4, 0, 100, 150, 0.9], [0, 0, 47.6, 150, 0.9], [0, 0, 100, 150, 0.3]]
        self.run_job(FakeTools(panels=panels))
        self.assertEqual(self.sidecar(b"one")["panels"], [[0, 0, 48, 150], [52, 0, 100, 150]])

    def test_without_usable_panels_the_sidecar_has_no_panels_key(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        self.run_job(FakeTools(panels=[[0, 0, 100, 150, 0.3]]))
        self.assertNotIn("panels", self.sidecar(b"one"))

    def test_without_panels_json_the_sidecar_has_no_panels_key(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        self.run_job(FakeTools())
        self.assertNotIn("panels", self.sidecar(b"one"))

    def test_balloon_text_travels_with_its_balloon(self):
        make_cbz(os.path.join(self.lib, "a.cbz"), {"p1.jpg": b"one"})
        right, left = [60, 10, 20, 10], [10, 10, 20, 10]
        panels = [[0, 0, 48, 150, 0.9], [52, 0, 100, 150, 0.9]]
        self.run_job(FakeTools(bubbles=[right, left], panels=panels, texts=["I'M ON THE RIGHT.", "LEFT FIRST!"]))
        self.assertEqual(self.sidecar(b"one")["bubbles"],
                         [{"r": left, "label": 2, "t": "LEFT FIRST!"}, {"r": right, "label": 1, "t": "I'M ON THE RIGHT."}])

    def test_redo_reprocesses_a_done_folder(self):
        os.makedirs(os.path.join(self.lib, "Saga"))
        make_cbz(os.path.join(self.lib, "Saga", "s1.cbz"), {"p1.jpg": b"saga"})
        bubblejob.run(self.lib, self.state, FakeTools(), self.logs.append, only="Saga")
        tools = FakeTools(bubbles=[[5, 5, 5, 5]])
        self.assertEqual(bubblejob.run(self.lib, self.state, tools, self.logs.append, only="Saga", redo=True), 0)
        self.assertEqual(tools.calls, [[sha(b"saga") + ".jpg"]])
        self.assertEqual(self.sidecar(b"saga")["bubbles"], [{"r": [5, 5, 5, 5], "label": 1}])

    def panels_only(self, panels):
        tools = FakePanels(panels)
        self.assertEqual(bubblejob.run(self.lib, self.state, tools, self.logs.append, only="Saga", panels_only=True), 0)
        return tools

    def test_panels_only_reorders_balloons_and_keeps_their_text_and_mask(self):
        os.makedirs(os.path.join(self.lib, "Saga"))
        make_cbz(os.path.join(self.lib, "Saga", "s1.cbz"), {"p1.jpg": b"saga"})
        right, left = [60, 10, 20, 10], [10, 10, 20, 10]
        self.run_job(FakeTools(bubbles=[right, left], texts=["RIGHT", "LEFT"]))  # no panels: Swift order
        mask = self.sidecar(b"saga")["mask"]
        tools = self.panels_only([[0, 0, 48, 150, 0.9], [52, 0, 100, 150, 0.9]])
        self.assertEqual(tools.calls, [[sha(b"saga") + ".jpg"]])
        sc = self.sidecar(b"saga")
        self.assertEqual(sc["bubbles"], [{"r": left, "label": 2, "t": "LEFT"}, {"r": right, "label": 1, "t": "RIGHT"}])
        self.assertEqual((sc["panels"], sc["mask"]), ([[0, 0, 48, 150], [52, 0, 100, 150]], mask))

    def test_panels_only_drops_panels_the_new_model_no_longer_keeps(self):
        os.makedirs(os.path.join(self.lib, "Saga"))
        make_cbz(os.path.join(self.lib, "Saga", "s1.cbz"), {"p1.jpg": b"saga"})
        right, left = [60, 10, 20, 10], [10, 10, 20, 10]
        self.run_job(FakeTools(bubbles=[right, left], panels=[[0, 0, 48, 150, 0.9], [52, 0, 100, 150, 0.9]]))
        self.panels_only([[0, 0, 100, 150, 0.3]])
        sc = self.sidecar(b"saga")
        self.assertNotIn("panels", sc)
        self.assertEqual(sc["bubbles"], [{"r": right, "label": 1}, {"r": left, "label": 2}])  # back to Swift order

    def test_panels_only_redoes_a_done_book_whose_file_changed_but_not_its_pages(self):
        os.makedirs(os.path.join(self.lib, "Saga"))
        p = os.path.join(self.lib, "Saga", "s1.cbz")
        make_cbz(p, {"p1.jpg": b"saga"})
        self.run_job(FakeTools())
        os.utime(p, (1, 1))  # metadata writeback rewrote ComicInfo.xml: new mtime, same pages
        tools = self.panels_only([[0, 0, 48, 150, 0.9]])
        self.assertEqual(tools.calls, [[sha(b"saga") + ".jpg"]])
        self.assertEqual(self.sidecar(b"saga")["panels"], [[0, 0, 48, 150]])

    def test_panels_only_leaves_a_book_with_a_page_missing_its_sidecar_alone(self):
        os.makedirs(os.path.join(self.lib, "Saga"))
        p = os.path.join(self.lib, "Saga", "s1.cbz")
        make_cbz(p, {"p1.jpg": b"saga"})
        self.run_job(FakeTools())
        make_cbz(p, {"p1.jpg": b"saga", "p2.jpg": b"new page"})
        tools = self.panels_only([[0, 0, 48, 150, 0.9]])
        self.assertEqual(tools.calls, [])
        self.assertNotIn("panels", self.sidecar(b"saga"))

    def test_panels_only_leaves_books_never_done_to_the_full_run(self):
        os.makedirs(os.path.join(self.lib, "Saga"))
        make_cbz(os.path.join(self.lib, "Saga", "s1.cbz"), {"p1.jpg": b"saga"})
        tools = self.panels_only([[0, 0, 48, 150, 0.9]])
        self.assertEqual(tools.calls, [])
        self.assertFalse(os.path.exists(bubblejob.sidecar_path(self.root, sha(b"saga"))))
        self.assertIsNone(self.status("Saga/s1.cbz"))


if __name__ == "__main__":
    unittest.main()
