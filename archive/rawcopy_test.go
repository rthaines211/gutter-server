package archive

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rawEntries returns each non-ComicInfo entry's compressed bytes.
func rawEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := map[string][]byte{}
	for _, f := range r.File {
		if strings.EqualFold(f.Name, "ComicInfo.xml") {
			continue
		}
		rc, err := f.OpenRaw()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		out[f.Name] = b
	}
	return out
}

// Pages must be carried over as their original compressed bytes, not
// inflated and re-deflated: recompressing every page of a 75 MB archive to
// replace one XML entry is what made writeback CPU-bound.
func TestRewriteComicInfoXMLCopiesPagesRaw(t *testing.T) {
	p := filepath.Join(t.TempDir(), "book.cbz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// A different deflate level than Go's default, as other tools produce:
	// re-deflating on rewrite would change these bytes.
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestSpeed)
	})
	page := bytes.Repeat([]byte("panel art "), 5000)
	for _, name := range []string{"001.jpg", "002.jpg"} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		w.Write(page)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	before := rawEntries(t, p)
	if err := RewriteComicInfoXML(p, []byte("<ComicInfo><Title>New</Title></ComicInfo>")); err != nil {
		t.Fatal(err)
	}
	after := rawEntries(t, p)
	for name, b := range before {
		if !bytes.Equal(b, after[name]) {
			t.Errorf("%s: compressed bytes changed on rewrite (%d -> %d bytes)", name, len(b), len(after[name]))
		}
	}
	if got := string(readEntry(t, p, "ComicInfo.xml")); !strings.Contains(got, "New") {
		t.Errorf("ComicInfo not replaced: %q", got)
	}
}
