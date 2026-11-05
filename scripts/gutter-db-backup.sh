#!/usr/bin/env bash
#
# comic-reader-db-backup.sh — nightly consistent snapshot of comic-reader.db.
#
# The DB lives on the named volume `comic-reader-db` (ext4 inside the colima VM)
# rather than the virtiofs bind mount, because SQLite WAL corrupts over virtiofs
# (see docker-compose.yml). That puts it out of reach of host-level backups, so
# this dumps a consistent copy back onto the bind-mounted data dir, which IS
# covered by the host's normal backups.
#
# Uses sqlite3's online .backup API, not cp: it takes the right locks, so it is
# safe against a running server and never captures a torn WAL.
set -euo pipefail
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"

VOLUME="comic-reader-db"
OUT_DIR="${OUT_DIR:-$HOME/docker/stacks/media-servers/comic-reader/data/db-backups}"
KEEP=7
# deploy.sh passes NAME=predeploy so its snapshots prune separately from nightly ones.
NAME="${NAME:-comic-reader}"

mkdir -p "$OUT_DIR"
STAMP="$(date +%Y%m%d-%H%M%S)"

docker run --rm \
  -v "$VOLUME":/db:ro \
  -v "$OUT_DIR":/out \
  alpine:3 sh -c "
    apk add -q --no-cache sqlite &&
    sqlite3 /db/comic-reader.db \".backup /out/$NAME-$STAMP.db\" &&
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
