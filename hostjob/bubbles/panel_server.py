# Panel detector as an HTTP service, for a whole-library redo on a rented GPU (run_panels.py PANEL_REMOTE).
# POST /predict with a tar of page images -> {name: [[x0, y0, x1, y1, conf], ...]} in the sent images' pixels.
# Bind to localhost and reach it through an SSH tunnel: it has no auth.
#     python panel_server.py panel.pt [port] [models]
import io, json, os, queue, sys, tarfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import cv2, numpy as np
from ultralytics import YOLO

model_path = sys.argv[1]
device = os.environ.get("PANEL_DEVICE", "0")  # mps for a local check
port = int(sys.argv[2]) if len(sys.argv) > 2 else 8000
pool = queue.Queue()
for _ in range(int(sys.argv[3]) if len(sys.argv) > 3 else 3):  # a few copies keep the GPU busy between requests
    pool.put(YOLO(model_path))


class H(BaseHTTPRequestHandler):
    def do_POST(self):
        try:
            body = self.rfile.read(int(self.headers["Content-Length"]))
            with tarfile.open(fileobj=io.BytesIO(body)) as t:
                pages = [(m.name, cv2.imdecode(np.frombuffer(t.extractfile(m).read(), np.uint8), cv2.IMREAD_COLOR))
                         for m in t.getmembers() if m.isfile()]
            m = pool.get()
            try:  # one page at a time, as run_panels.py does locally: a batch would pad pages to one shape
                res = {name: [[round(float(v), 2) for v in b.xyxy[0].tolist()] + [round(float(b.conf[0]), 3)]
                              for b in m.predict(img, conf=0.25, verbose=False, device=device)[0].boxes]
                       for name, img in pages}
            finally:
                pool.put(m)
            out, code = json.dumps(res).encode(), 200
        except Exception as e:
            out, code = json.dumps({"error": f"{type(e).__name__}: {e}"}).encode(), 500
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, *a):
        pass


ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
