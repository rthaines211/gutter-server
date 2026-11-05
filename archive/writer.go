package archive

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RewriteComicInfoXML replaces (or inserts) ComicInfo.xml inside a cbz,
// preserving all other entries. The rewrite is atomic: it writes to a
// temp file in the same directory and renames over the original.
func RewriteComicInfoXML(cbzPath string, xmlData []byte) error {
	src, err := zip.OpenReader(cbzPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", cbzPath, err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp(filepath.Dir(cbzPath), ".rewrite-*.cbz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	// CreateTemp makes the file 0600; keep the original's permissions so the
	// rename doesn't leave the library owner-only.
	if err := keepMode(tmp, cbzPath); err != nil {
		cleanup()
		return err
	}

	zw := zip.NewWriter(tmp)
	for _, f := range src.File {
		if strings.EqualFold(filepath.Base(f.Name), "comicinfo.xml") {
			continue
		}
		// Copy the entry's compressed bytes verbatim: inflating and
		// re-deflating every page to swap one XML entry made writeback
		// CPU-bound, and left pages no longer byte-identical.
		if err := zw.Copy(f); err != nil {
			cleanup()
			return err
		}
	}
	w, err := zw.Create("ComicInfo.xml")
	if err != nil {
		cleanup()
		return err
	}
	if _, err := w.Write(xmlData); err != nil {
		cleanup()
		return err
	}
	if err := zw.Close(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, cbzPath)
}

// keepMode copies src's permission bits onto f.
func keepMode(f *os.File, src string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	return f.Chmod(fi.Mode().Perm())
}
