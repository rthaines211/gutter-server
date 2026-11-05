package archive

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ConvertToCbz repacks the archive at srcPath into a .cbz with the same
// basename (pages byte-identical, ComicInfo.xml carried over), validates
// the result, deletes the source, and returns the new path. One-way.
func ConvertToCbz(srcPath string) (string, error) {
	destPath := strings.TrimSuffix(srcPath, filepath.Ext(srcPath)) + ".cbz"
	if destPath == srcPath {
		return "", fmt.Errorf("already a cbz: %s", srcPath)
	}
	if _, err := os.Stat(destPath); err == nil {
		return "", fmt.Errorf("target already exists: %s", destPath)
	}

	r, err := Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open source: %w", err)
	}
	defer r.Close()
	wantPages := r.PageCount()

	tmp, err := os.CreateTemp(filepath.Dir(srcPath), ".convert-*.cbz")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	// CreateTemp makes the file 0600; keep the original's permissions so the
	// rename doesn't leave the library owner-only.
	if err := keepMode(tmp, srcPath); err != nil {
		cleanup()
		return "", err
	}

	zw := zip.NewWriter(tmp)
	names := r.PageNames()
	for i := 0; i < wantPages; i++ {
		// PageAt returns (io.ReadCloser, content-type, error)
		rc, _, err := r.PageAt(i)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("read page %d: %w", i, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			cleanup()
			return "", fmt.Errorf("read page %d data: %w", i, err)
		}
		w, err := zw.Create(names[i])
		if err != nil {
			cleanup()
			return "", err
		}
		if _, err := w.Write(data); err != nil {
			cleanup()
			return "", err
		}
	}
	// ComicInfoXML returns []byte (no error)
	if xmlData := r.ComicInfoXML(); len(xmlData) > 0 {
		w, err := zw.Create("ComicInfo.xml")
		if err != nil {
			cleanup()
			return "", err
		}
		if _, err := w.Write(xmlData); err != nil {
			cleanup()
			return "", err
		}
	}
	if err := zw.Close(); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		os.Remove(tmpName)
		return "", err
	}

	// Validate before deleting the source.
	chk, err := Open(destPath)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("validate converted cbz: %w", err)
	}
	gotPages := chk.PageCount()
	chk.Close()
	if gotPages != wantPages {
		os.Remove(destPath)
		return "", fmt.Errorf("page count mismatch after conversion: want %d got %d", wantPages, gotPages)
	}

	if err := os.Remove(srcPath); err != nil {
		return "", fmt.Errorf("converted but failed to remove source: %w", err)
	}
	return destPath, nil
}
