package server

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Writeback's in-flight temp files (.rewrite-*.cbz, .convert-*.cbz) must
// never be indexed as comics.
func TestListComicsSkipsDotfiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"Saga #001.cbz", ".rewrite-123.cbz", ".convert-9.cbz", "._Saga #001.cbz"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if got := listComics(dir); !reflect.DeepEqual(got, []string{"Saga #001.cbz"}) {
		t.Errorf("listComics = %q, want only the real comic", got)
	}
}
