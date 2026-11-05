package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// Rewrites go through a CreateTemp file (0600); the result must keep the
// original's permissions, not become owner-only.
func TestRewriteComicInfoXMLKeepsFileMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "book.cbz")
	writeTestCbz(t, p, true)
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if err := RewriteComicInfoXML(p, []byte("<ComicInfo/>")); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, p); got != 0644 {
		t.Errorf("mode after rewrite = %o, want 644", got)
	}
}

func TestConvertToCbzKeepsFileMode(t *testing.T) {
	src := filepath.Join(t.TempDir(), "book.cbr")
	writeTestCbz(t, src, true)
	if err := os.Chmod(src, 0644); err != nil {
		t.Fatal(err)
	}
	dst, err := ConvertToCbz(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := mode(t, dst); got != 0644 {
		t.Errorf("mode after convert = %o, want 644", got)
	}
}
