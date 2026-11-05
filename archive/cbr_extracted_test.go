package archive

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The bundled libunarr reads RAR4 only and modern `rar` writes RAR5 only, so a
// synthetic fixture can't be built here. Point COMIC_READER_TEST_CBR at any real
// .cbr to run this; it is skipped otherwise.
func TestExtractCBRToDisk_MatchesInMemoryReader(t *testing.T) {
	cbr := os.Getenv("COMIC_READER_TEST_CBR")
	if cbr == "" {
		t.Skip("set COMIC_READER_TEST_CBR=/path/to/real.cbr")
	}
	want, err := openCBR(cbr) // the old whole-archive-in-memory reader is the oracle
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "x")
	if err := extractCBRToDisk(cbr, dir); err != nil {
		t.Fatal(err)
	}
	got, err := loadExtractedCBR(dir)
	if err != nil {
		t.Fatal(err)
	}

	if got.PageCount() != want.PageCount() || got.PageCount() == 0 {
		t.Fatalf("page count = %d, want %d", got.PageCount(), want.PageCount())
	}
	for i := 0; i < want.PageCount(); i++ {
		wr, _, _ := want.PageAt(i)
		gr, _, err := got.PageAt(i)
		if err != nil {
			t.Fatal(err)
		}
		wb, _ := io.ReadAll(wr)
		gb, _ := io.ReadAll(gr)
		gr.Close()
		if !bytes.Equal(wb, gb) {
			t.Fatalf("page %d differs from in-memory reader (%d vs %d bytes)", i, len(gb), len(wb))
		}
	}
	if !bytes.Equal(got.ComicInfoXML(), want.ComicInfoXML()) {
		t.Fatal("comicinfo.xml differs from in-memory reader")
	}
	if _, err := os.Stat(filepath.Join(dir, ".extracted")); err != nil {
		t.Fatal("marker missing")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".raw_*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
