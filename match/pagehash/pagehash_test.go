package pagehash

import (
	"archive/zip"
	"crypto/md5"
	"encoding/hex"
	"os"
	"testing"
)

// newCBZ writes a CBZ containing the given entries and returns its path.
func newCBZ(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	path := t.TempDir() + "/test.cbz"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFirstPageMD5(t *testing.T) {
	first := []byte("page-one-bytes")
	path := newCBZ(t, map[string][]byte{
		"002.jpg":       []byte("page-two-bytes"),
		"001.jpg":       first,
		"ComicInfo.xml": []byte("<ComicInfo/>"),
	})

	got, err := FirstPageMD5(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(first)
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

// ComicInfo.xml must never be treated as page zero even though it sorts first.
func TestFirstPageMD5IgnoresComicInfo(t *testing.T) {
	first := []byte("real-first-page")
	path := newCBZ(t, map[string][]byte{
		"AAA_ComicInfo.xml": []byte("<ComicInfo/>"),
		"ComicInfo.xml":     []byte("<ComicInfo/>"),
		"zzz.jpg":           first,
	})
	got, err := FirstPageMD5(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(first)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestFirstPageMD5NoPages(t *testing.T) {
	path := newCBZ(t, map[string][]byte{"ComicInfo.xml": []byte("<ComicInfo/>")})
	if _, err := FirstPageMD5(path); err != ErrNoPages {
		t.Fatalf("expected ErrNoPages, got %v", err)
	}
}
