# Run the YOLO panel detector over a page dir; write {page file: [[x0, y0, x1, y1, conf], ...]} to one JSON.
# With PANEL_REMOTE=http://host:port (panel_server.py on a rented GPU, through an SSH tunnel), each page is
# shrunk here exactly as ultralytics' LetterBox would (long side 640, INTER_LINEAR), sent as PNG, and the
# boxes scaled back to the page: the model sees the same pixels (~490 KB a page instead of the page).
import sys, json, os, glob
model_path, pages_dir, out_path = sys.argv[1:4]
files = sorted(f for f in glob.glob(f"{pages_dir}/*") if f.lower().endswith((".jpg", ".jpeg", ".png", ".webp")))
IMGSZ = 640  # the size both panel models were trained and run at


def shrink(f):
    """(name, png bytes, x scale, y scale) for one page, the scales mapping sent pixels back to the page's."""
    import cv2
    img = cv2.imread(f, cv2.IMREAD_COLOR)
    if img is None:
        raise ValueError(f"unreadable page {f}")
    h, w = img.shape[:2]
    r = min(IMGSZ / h, IMGSZ / w)
    nw, nh = round(w * r), round(h * r)  # LetterBox's own rounding, so the server's LetterBox doesn't resize again
    if (w, h) != (nw, nh):
        img = cv2.resize(img, (nw, nh), interpolation=cv2.INTER_LINEAR)
    ok, buf = cv2.imencode(".png", img, [cv2.IMWRITE_PNG_COMPRESSION, 1])  # lossless: JPEG q95 changed 3 of 40 pages
    return os.path.basename(f), buf.tobytes(), w / nw, h / nh


def remote(url):
    import io, tarfile, urllib.request
    from concurrent.futures import ThreadPoolExecutor
    with ThreadPoolExecutor(4) as ex:
        pages = list(ex.map(shrink, files))
    body = io.BytesIO()
    with tarfile.open(fileobj=body, mode="w") as t:
        for name, data, _, _ in pages:
            info = tarfile.TarInfo(name)
            info.size = len(data)
            t.addfile(info, io.BytesIO(data))
    req = urllib.request.Request(url.rstrip("/") + "/predict", data=body.getvalue(), method="POST")
    with urllib.request.urlopen(req, timeout=600) as r:
        boxes = json.load(r)
    return {name: [[round(b[0] * sx, 1), round(b[1] * sy, 1), round(b[2] * sx, 1), round(b[3] * sy, 1), b[4]]
                   for b in boxes[name]] for name, _, sx, sy in pages}


def local():
    from ultralytics import YOLO
    m, res = YOLO(model_path), {}
    for f in files:
        r = m.predict(f, conf=0.25, verbose=False, device="mps")[0]
        res[os.path.basename(f)] = [[round(float(v), 1) for v in b.xyxy[0].tolist()] + [round(float(b.conf[0]), 3)]
                                    for b in r.boxes]
    return res


res = remote(os.environ["PANEL_REMOTE"]) if os.environ.get("PANEL_REMOTE") and files else local()
with open(out_path, "w") as o:
    json.dump(res, o)
