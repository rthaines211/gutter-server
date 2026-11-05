package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConvertToCbz(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.cbr")
	// A cbz payload with a .cbr name: go-unarr reads zip content fine,
	// so this exercises the exact production path (open .cbr, repack .cbz).
	writeTestCbz(t, src, true)

	newPath, err := ConvertToCbz(src)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if newPath != filepath.Join(dir, "book.cbz") {
		t.Fatalf("unexpected dest: %s", newPath)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source .cbr should be deleted")
	}
	r, err := Open(newPath)
	if err != nil {
		t.Fatalf("open converted: %v", err)
	}
	defer r.Close()
	if r.PageCount() != 2 {
		t.Fatalf("want 2 pages, got %d", r.PageCount())
	}
	if len(r.ComicInfoXML()) == 0 {
		t.Fatal("ComicInfo.xml not carried over")
	}
}

func TestConvertToCbzRefusesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.cbr")
	writeTestCbz(t, src, false)
	writeTestCbz(t, filepath.Join(dir, "book.cbz"), false) // target already exists

	if _, err := ConvertToCbz(src); err == nil {
		t.Fatal("expected error when target exists")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source must be untouched on failure")
	}
}
