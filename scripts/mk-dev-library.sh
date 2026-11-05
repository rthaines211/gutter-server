#!/usr/bin/env bash
#
# mk-dev-library.sh — build an isolated dev/test comic library that mirrors the
# real library's folder + filename structure, but where every archive is a
# hardlink to ONE tiny valid template .cbz. Zero real archive opens, ~0 bytes.
#
# Why: the dev instance (config.test.yaml, port 8082) must share NOTHING with
# the production container (port 8081). Pointing comic_dirs at the real library
# meant dev scans hammered the same USB drive that wedges the container, and
# organize/rename/writeback acted on real comics. This fixture severs that.
#
# Rerun any time the real library's structure changes. Idempotent.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEV_LIB="${DEV_LIB:-$REPO_DIR/testdata/dev-library}"
TEMPLATE="$DEV_LIB/.template.cbz"

# Real library roots (source of structure only — never written to, never opened).
# SRC_LIBRARY: colon-separated list of library root dirs, e.g. SRC_LIBRARY=/data/comics/A:/data/comics/B
[[ -n "${SRC_LIBRARY:-}" ]] || { echo "usage: SRC_LIBRARY=/path/to/root[:/another/root] $0" >&2; exit 2; }
IFS=: read -r -a REAL_ROOTS <<< "$SRC_LIBRARY"

echo "==> Dev library: $DEV_LIB"
mkdir -p "$DEV_LIB"

# --- 1. Template archive: a valid cbz with one decodable page + ComicInfo.xml.
# Generated with Go (this is a Go repo) so it needs no external image tooling.
if [[ ! -f "$TEMPLATE" ]]; then
  echo "==> Generating template archive"
  gen="$(mktemp -d)"
  cat > "$gen/main.go" <<'GO'
package main

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
)

func main() {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(0, 0, color.RGBA{200, 60, 60, 255})
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, nil); err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("001.jpg")
	w.Write(jpg.Bytes())
	w, _ = zw.Create("ComicInfo.xml")
	w.Write([]byte(`<?xml version="1.0"?><ComicInfo><Series>DEV FIXTURE</Series><Publisher>DEV</Publisher></ComicInfo>`))
	zw.Close()
	os.WriteFile(os.Args[1], buf.Bytes(), 0644)
}
GO
  ( cd "$REPO_DIR" && go run "$gen/main.go" "$TEMPLATE" )
  rm -rf "$gen"
fi

# --- 2. Mirror structure: recreate every series dir, hardlink every archive.
echo "==> Mirroring library structure"
linked=0
for root in "${REAL_ROOTS[@]}"; do
  [[ -d "$root" ]] || { echo "   skip (not mounted): $root"; continue; }
  base="$(basename "$root")"
  # Enumerate archives by NAME only (readdir), never opening them.
  while IFS= read -r -d '' f; do
    rel="${f#"$root"/}"                  # e.g. "1776 (2026)/1776 #1.cbz"
    dest="$DEV_LIB/$base/$rel"
    mkdir -p "$(dirname "$dest")"
    if [[ ! -e "$dest" ]]; then
      ln "$TEMPLATE" "$dest"             # hardlink: shares the one inode
      linked=$((linked+1))
    fi
  done < <(find "$root" -maxdepth 2 \( -iname '*.cbz' -o -iname '*.cbr' \) -print0)
done
echo "==> Linked $linked new archive(s)"

# --- 3. Safety assertion: the dev config must not reference the real library.
# Strip comments first — only a live config value pointing at the source library counts.
if grep -v '^[[:space:]]*#' "$REPO_DIR/config.test.yaml" 2>/dev/null | grep -qF "${REAL_ROOTS[0]}"; then
  echo "!! WARNING: config.test.yaml still references the source library — dev is NOT isolated." >&2
  exit 1
fi
echo "==> OK. Point config.test.yaml comic_dirs at: $DEV_LIB/<root>"
