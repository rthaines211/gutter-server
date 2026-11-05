#!/bin/bash
# Installs the bubble-zoom host job on this Mac. Re-runnable. See bubblejob.py.
# --no-load: build everything but don't schedule the job yet (test a title first with --only).
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
case "$REPO" in
  */.claude/worktrees/*)
    echo "Run this from ~/projects/comic-reader after merging: launchd keeps running the checkout it was" >&2
    echo "installed from, and worktrees get deleted." >&2
    exit 1 ;;
esac
APP="$HOME/Library/Application Support/comic-bubbles"
MODEL_SHA=10bc9f702698148e079fb4462a6b910fcd69753e04838b54087ef91d5633097b
FT_SHA=3e10c5d13bf797772bb2971284143cf7d975bbfb064dad2f65248023983732a0
PANEL_SHA=566e9ff59ec146afb695eb48a8aaae983b43bb72c3a57f63afafe12f3b349af4
mkdir -p "$APP" "$HOME/Library/Logs"

swiftc -O "$REPO/hostjob/bubbles/bubbles3.swift" -o "$APP/bubbles"

[ -x "$APP/venv/bin/python" ] || /opt/homebrew/bin/python3 -m venv "$APP/venv"
"$APP/venv/bin/pip" install -q "ultralytics==8.4.175" "huggingface_hub==2.2.0"

# Balloon detector. Preferred: the library-tuned model (fine-tuned 2026-10-13 on 667 hand-labelled
# pages; misses 26 -> 10 and strays 18 -> 11 on the 183-page key), run at 0.5 (model.conf). It isn't
# published anywhere: set BUBBLE_MODEL or leave it at the training path. Otherwise the original
# public model, at 0.6.
FT_SRC="${BUBBLE_MODEL:-$HOME/projects/bubble-detect-train/detector-ft-r2.pt}"
if echo "$FT_SHA  $APP/model.pt" | shasum -a 256 -c --status 2>/dev/null; then
  echo 0.5 > "$APP/model.conf"
elif echo "$FT_SHA  $FT_SRC" | shasum -a 256 -c --status 2>/dev/null; then
  cp "$FT_SRC" "$APP/model.pt.tmp"; mv "$APP/model.pt.tmp" "$APP/model.pt"
  echo 0.5 > "$APP/model.conf"
  echo "Installed the library-tuned balloon detector."
else
  if ! echo "$MODEL_SHA  $APP/model.pt" | shasum -a 256 -c --status 2>/dev/null; then
    "$APP/venv/bin/python" -c "
from huggingface_hub import hf_hub_download; import shutil, sys
p = hf_hub_download('ogkalu/comic-speech-bubble-detector-yolov8m', 'comic-speech-bubble-detector.pt')
shutil.copy(p, sys.argv[1])" "$APP/model.pt"
    echo "$MODEL_SHA  $APP/model.pt" | shasum -a 256 -c   # fail loudly on a changed upstream file
  fi
  rm -f "$APP/model.conf"
  echo "Library-tuned detector not found at $FT_SRC: using the original public model."
fi

# Panel detector (run_panels.py: reading order and panel zoom). Preferred: the library-tuned model
# (fine-tuned on hand-labelled pages of this library; docs/research/panel-detector-ft-*/), run at
# PANEL_CONF (panel.conf). It isn't published: set PANEL_MODEL or leave it at the copy-from-the-mini path.
# Otherwise the public YOLOv12x (mosesb/best-comic-panel-detection, Apache-2.0, 119 MB) at 0.5.
# Round 2 (2026-10-29: round 1 + 30 hard pages, 100 epochs on a rented A100) at 0.4, panel-zoom list unfolded:
# 90 good / 26 wrong on the 150-page key, vs round 1 as shipped (0.5, folded) 77 / 27.
PANEL_FT_SHA=d2a18970be9230ee4f96460c2eff2e072fdf782403d99d3c22cf90431ff6dc4c   # empty never matches
PANEL_CONF=0.4
PANEL_SRC="${PANEL_MODEL:-$HOME/projects/panel-detect-train/panel-ft-r2.pt}"
if [ -n "$PANEL_FT_SHA" ] && echo "$PANEL_FT_SHA  $APP/panel.pt" | shasum -a 256 -c --status 2>/dev/null; then
  echo "$PANEL_CONF" > "$APP/panel.conf"
elif [ -n "$PANEL_FT_SHA" ] && echo "$PANEL_FT_SHA  $PANEL_SRC" | shasum -a 256 -c --status 2>/dev/null; then
  cp "$PANEL_SRC" "$APP/panel.pt.tmp"; mv "$APP/panel.pt.tmp" "$APP/panel.pt"   # not &&: set -e ignores its left side
  echo "$PANEL_CONF" > "$APP/panel.conf"
  echo "Installed the library-tuned panel detector."
else
  if ! echo "$PANEL_SHA  $APP/panel.pt" | shasum -a 256 -c --status 2>/dev/null; then
    curl -fsSL -o "$APP/panel.pt.tmp" \
      "https://huggingface.co/mosesb/best-comic-panel-detection/resolve/main/best.pt"
    echo "$PANEL_SHA  $APP/panel.pt.tmp" | shasum -a 256 -c
    mv "$APP/panel.pt.tmp" "$APP/panel.pt"
  fi
  rm -f "$APP/panel.conf"
  echo "Library-tuned panel detector not found at $PANEL_SRC: using the public model."
fi

if [ "${1:-}" = "--no-load" ]; then
  echo "Built, not scheduled. Test one title from Terminal:"
  echo "  \"$APP/venv/bin/python\" \"$REPO/hostjob/bubbles/bubblejob.py\" --only \"Comic Continuum/<Series>\""
  echo "Then run ./hostjob/bubbles/install.sh (no flag) to schedule the full backfill."
  exit 0
fi

PLIST="$HOME/Library/LaunchAgents/com.ryan.comic-bubbles.plist"
sed -e "s#__HOME__#$HOME#g" -e "s#__REPO__#$REPO#g" "$REPO/hostjob/bubbles/com.ryan.comic-bubbles.plist" > "$PLIST"
launchctl bootout "gui/$(id -u)/com.ryan.comic-bubbles" 2>/dev/null || true
# bootout returns before a busy job has stopped, and bootstrap fails (error 5) until it has.
for i in $(seq 1 30); do
  launchctl bootstrap "gui/$(id -u)" "$PLIST" 2>/dev/null && break
  if [ "$i" = 30 ]; then echo "launchctl bootstrap kept failing; retry: launchctl bootstrap gui/$(id -u) $PLIST" >&2; exit 1; fi
  sleep 1
done

echo
echo "Installed. ONE MANUAL STEP: System Settings > Privacy & Security > Full Disk Access, add:"
echo "  $("$APP/venv/bin/python" -c 'import sys; sys.path.insert(0, sys.argv[1]); import bubblejob; print(bubblejob.fda_target())' "$REPO/hostjob/bubbles")"
# Only Python reads the library; the Swift tool works on page copies in a temp dir.
# The grant is tied to that versioned Homebrew path: keep it with `brew pin python@3.14`.
echo "(brew pin python@3.14, or the next Python upgrade moves the path and drops the grant)"
echo "Then: launchctl kickstart -k gui/$(id -u)/com.ryan.comic-bubbles ; tail -f ~/Library/Logs/comic-bubbles.log"
