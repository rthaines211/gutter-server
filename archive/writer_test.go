package archive

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeTestCbz(t *testing.T, path string, withComicInfo bool) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range []string{"001.jpg", "002.jpg"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("fake-image-" + name))
	}
	if withComicInfo {
		w, _ := zw.Create("ComicInfo.xml")
		w.Write([]byte("<ComicInfo><Title>Old</Title></ComicInfo>"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func readEntry(t *testing.T, path, name string) []byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == name {
			rc, _ := f.Open()
			defer rc.Close()
			data, _ := io.ReadAll(rc)
			return data
		}
	}
	return nil
}

func TestRewriteComicInfoXML(t *testing.T) {
	for _, hasExisting := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "book.cbz")
		writeTestCbz(t, path, hasExisting)
		newXML := []byte("<ComicInfo><Title>New</Title></ComicInfo>")
		if err := RewriteComicInfoXML(path, newXML); err != nil {
			t.Fatalf("rewrite (existing=%v): %v", hasExisting, err)
		}
		if got := readEntry(t, path, "ComicInfo.xml"); !bytes.Equal(got, newXML) {
			t.Fatalf("xml not replaced (existing=%v): %s", hasExisting, got)
		}
		if got := readEntry(t, path, "001.jpg"); string(got) != "fake-image-001.jpg" {
			t.Fatalf("page corrupted: %s", got)
		}
		zr, _ := zip.OpenReader(path)
		if len(zr.File) != 3 {
			t.Fatalf("want 3 entries, got %d", len(zr.File))
		}
		zr.Close()
	}
}
