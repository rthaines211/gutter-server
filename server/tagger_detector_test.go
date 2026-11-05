package server

import "testing"

// PageMeta reads page count/names from the archive central directory.
func TestPageMetaReadsCentralDir(t *testing.T) {
	srv, comicFile := setupComicTest(t) // existing helper (cbz fixture has 3 pages)
	id := pathToID(comicFile)
	n, names, err := srv.PageMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || len(names) != 3 {
		t.Fatalf("want 3 pages, got count=%d names=%d", n, len(names))
	}
}

// RecentlyActive flips true right after a page is served, false initially.
func TestRecentlyActive(t *testing.T) {
	srv, _ := setupComicTest(t)
	if srv.RecentlyActive() {
		t.Error("should be idle initially")
	}
	srv.notePageServed()
	if !srv.RecentlyActive() {
		t.Error("should be active right after a page serve")
	}
}
