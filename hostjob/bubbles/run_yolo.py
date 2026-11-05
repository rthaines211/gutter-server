# Run a YOLO balloon detector over a page dir, write one JSON per page: boxes in pixel coords + timing.
import sys, json, time, os, glob
from ultralytics import YOLO
model_path, pages_dir, out_dir = sys.argv[1:4]
imgsz = int(sys.argv[4]) if len(sys.argv) > 4 else 1024
os.makedirs(out_dir, exist_ok=True)
m = YOLO(model_path)
files = sorted(f for f in glob.glob(f"{pages_dir}/*") if f.lower().endswith((".jpg", ".jpeg", ".png", ".webp")))
m.predict(files[0], imgsz=imgsz, verbose=False, device="mps")   # warm-up
tot = 0
for f in files:
    t = time.time(); r = m.predict(f, imgsz=imgsz, conf=0.25, verbose=False, device="mps")[0]; ms = int((time.time() - t) * 1000); tot += ms
    # a sound effect is lettering, not a balloon: the library-tuned model (2026-10-13) has an "sfx" class
    boxes = [[round(float(v), 1) for v in b.xyxy[0].tolist()] + [round(float(b.conf[0]), 3), int(b.cls[0])]
             for b in r.boxes if m.names[int(b.cls[0])] != "sfx"]
    name = os.path.splitext(os.path.basename(f))[0]
    json.dump({"page": os.path.basename(f), "w": r.orig_shape[1], "h": r.orig_shape[0], "ms": ms, "names": m.names, "boxes": boxes}, open(f"{out_dir}/{name}.json", "w"))
    print(name, len(boxes), "boxes", ms, "ms", flush=True)
print("mean ms", tot // len(files))
