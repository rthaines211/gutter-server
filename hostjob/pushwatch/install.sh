#!/bin/bash
# Installs the push watcher on the host Mac: every 10 minutes it checks /healthz
# and pushes "comic-reader is down"/"is back" to the iPad. Re-runnable. Needs Go
# here, or a binary built elsewhere with:
#   CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o comic-pushwatch ./cmd/pushwatch
# copied to ~/bin/comic-pushwatch (then run this with --no-build).
# No Full Disk Access needed: it only reads the stack config dir and makes HTTP calls.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
case "$REPO" in
  */.claude/worktrees/*)
    echo "Run this from ~/projects/comic-reader after merging: launchd keeps running the checkout it was" >&2
    echo "installed from, and worktrees get deleted." >&2
    exit 1 ;;
esac
mkdir -p "$HOME/bin" "$HOME/Library/Logs"

if [ "${1:-}" != "--no-build" ]; then
  (cd "$REPO" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o "$HOME/bin/comic-pushwatch" ./cmd/pushwatch)
fi
[ -x "$HOME/bin/comic-pushwatch" ] || { echo "no $HOME/bin/comic-pushwatch" >&2; exit 1; }

PLIST="$HOME/Library/LaunchAgents/com.ryan.comic-pushwatch.plist"
sed -e "s#__HOME__#$HOME#g" "$REPO/hostjob/pushwatch/com.ryan.comic-pushwatch.plist" > "$PLIST"
launchctl bootout "gui/$(id -u)/com.ryan.comic-pushwatch" 2>/dev/null || true
for i in $(seq 1 30); do
  launchctl bootstrap "gui/$(id -u)" "$PLIST" 2>/dev/null && break
  if [ "$i" = 30 ]; then echo "launchctl bootstrap kept failing; retry: launchctl bootstrap gui/$(id -u) $PLIST" >&2; exit 1; fi
  sleep 1
done
echo "Installed. tail -f ~/Library/Logs/comic-pushwatch.log"
