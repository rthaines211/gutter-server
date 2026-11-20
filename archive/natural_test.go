package archive

import (
	"archive/zip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPageLessOrder(t *testing.T) {
	for _, c := range [][2]string{
		{"page2.jpg", "page10.jpg"},
		{"xombi_00_fc.jpg", "Xombi_01.jpg"},
		{"p6.jpg", "p07.jpg"},
		{"page-002.jpg", "page-0010.jpg"},
	} {
		if !PageLess(c[0], c[1]) || PageLess(c[1], c[0]) {
			t.Errorf("want %q before %q", c[0], c[1])
		}
	}
	if PageLess("a.jpg", "a.jpg") {
		t.Error("PageLess(x, x) must be false")
	}
	if PageLess("A.jpg", "a.jpg") == PageLess("a.jpg", "A.jpg") {
		t.Error("case-only names need a strict tie-break")
	}
}

// writeZip builds a zip at dir/name holding the given entries (dummy bytes).
func writeZip(t *testing.T, path string, entries ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.Create(e)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(e))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestCBZNaturalPageOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.cbz")
	writeZip(t, p, "page10.jpg", "page2.jpg", "page1.jpg")
	r, err := OpenCBZ(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	want := []string{"page1.jpg", "page2.jpg", "page10.jpg"}
	if got := r.PageNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PageNames = %v, want %v", got, want)
	}
}

func TestExtractedCBRStaleMarkerReextracts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "x")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".extracted"), []byte("ok"), 0o644)
	os.WriteFile(filepath.Join(dir, "page_9999.jpg"), []byte("stale"), 0o644)
	if extractionCurrent(dir) {
		t.Fatal("old-order extraction must not count as current")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("stale extraction dir must be removed")
	}

	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".extracted"), []byte(extractMarkerVersion), 0o644)
	if !extractionCurrent(dir) {
		t.Fatal("current-version extraction must be reused")
	}
}
