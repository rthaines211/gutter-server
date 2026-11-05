#!/usr/bin/env bash
#
# gutter-db-backup.sh — consistent snapshot of the Gutter database.
#
# For a DB on a Docker named volume (e.g. because SQLite WAL corrupts over some
# bind mounts), out of reach of host-level backups. This dumps a consistent copy
# to OUT_DIR on the host. Settings (env): VOLUME (default gutter-db), DB_FILE
# (default gutter.db), STACK_DIR (default $HOME/gutter), OUT_DIR, NAME, KEEP.
#
# Uses sqlite3's online .backup API, not cp: it takes the right locks, so it is
# safe against a running server and never captures a torn WAL.
set -euo pipefail
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"

STACK_DIR="${STACK_DIR:-$HOME/gutter}"
VOLUME="${VOLUME:-gutter-db}"
DB_FILE="${DB_FILE:-gutter.db}"
OUT_DIR="${OUT_DIR:-$STACK_DIR/data/db-backups}"
KEEP="${KEEP:-7}"
# deploy.sh passes NAME=predeploy so its snapshots prune separately from nightly ones.
NAME="${NAME:-gutter}"

mkdir -p "$OUT_DIR"
STAMP="$(date +%Y%m%d-%H%M%S)"

docker run --rm \
  -v "$VOLUME":/db:ro \
  -v "$OUT_DIR":/out \
  alpine:3 sh -c "
    apk add -q --no-cache sqlite &&
    sqlite3 /db/$DB_FILE \".backup /out/$NAME-$STAMP.db\" &&
    r=\$(sqlite3 /out/$NAME-$STAMP.db 'PRAGMA integrity_check;' | head -1) &&
    echo \$r && [ \"\$r\" = ok ]
  "

# Refuse to advertise a corrupt snapshot: if integrity failed the run already
# exited non-zero above, so reaching here means the copy verified clean.
echo "$(date -Iseconds) backup ok: $OUT_DIR/$NAME-$STAMP.db"

# Keep the newest $KEEP, drop the rest.
ls -1t "$OUT_DIR"/"$NAME"-[0-9]*.db 2>/dev/null | tail -n +$((KEEP + 1)) | while read -r old; do
  rm -f "$old" && echo "  pruned $(basename "$old")"
done
