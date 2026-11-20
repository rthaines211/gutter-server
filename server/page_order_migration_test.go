package server

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/rthaines211/gutter-server/store"
)

func writeTestZip(t *testing.T, path string, entries ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, _ := zw.Create(e)
		w.Write([]byte(e))
	}
	zw.Close()
	f.Close()
}

func TestMigratePageOrder(t *testing.T) {
	srv := newTestServer(t)
	root := srv.ComicDirs[0]
	writeTestZip(t, filepath.Join(root, "S/a.cbz"), "page1.jpg", "page10.jpg", "page2.jpg")
	writeTestZip(t, filepath.Join(root, "S/b.cbz"), "001.jpg", "002.jpg")
	a, b, gone := pathToID("S/a.cbz"), pathToID("S/b.cbz"), pathToID("S/gone.cbz")
	for _, id := range []string{a, b, gone} {
		// page 1 of 3 in old (byte) order: page10.jpg for a
		srv.Store.UpsertProgress(store.Progress{ComicID: id, CurrentPage: 1, TotalPages: 3})
	}
	// Rows must predate the pass (updated_at has 1s resolution).
	srv.Store.DB().Exec(`UPDATE reading_progress SET updated_at = datetime('now', '-1 hour')`)

	srv.MigratePageOrder()

	page := func(id string) int {
		p, _ := srv.Store.GetProgress(id)
		return p.CurrentPage
	}
	if got := page(a); got != 2 {
		t.Fatalf("a: page10.jpg should move to index 2, got %d", got)
	}
	if got := page(b); got != 1 {
		t.Fatalf("b: order unchanged, page should stay 1, got %d", got)
	}
	if done, _ := srv.Store.MigrationDone(pageOrderMigration); !done {
		t.Fatal("migration not marked done")
	}

	srv.Store.UpsertProgress(store.Progress{ComicID: a, CurrentPage: 1, TotalPages: 3})
	srv.Store.DB().Exec(`UPDATE reading_progress SET updated_at = datetime('now', '-1 hour')`)
	srv.MigratePageOrder()
	if got := page(a); got != 1 {
		t.Fatalf("second run must do nothing, a moved to %d", got)
	}
}

// A pass that stopped before marking itself done must not remap again the
// comics it already finished.
func TestMigratePageOrderInterruptedRetry(t *testing.T) {
	srv := newTestServer(t)
	root := srv.ComicDirs[0]
	writeTestZip(t, filepath.Join(root, "S/a.cbz"), "page1.jpg", "page10.jpg", "page11.jpg", "page2.jpg")
	a := pathToID("S/a.cbz")
	srv.Store.UpsertProgress(store.Progress{ComicID: a, CurrentPage: 1, TotalPages: 4})
	srv.Store.DB().Exec(`UPDATE reading_progress SET updated_at = datetime('now', '-2 hour')`)

	// m = [0 2 3 1]: page10.jpg goes 1 -> 2; a second remap would take it to 3.
	srv.MigratePageOrder()
	// Simulate a crash before the final flag: drop it, and age the row so it
	// looks older than the next pass's start.
	srv.Store.DB().Exec(`DELETE FROM migrations WHERE name = ?`, pageOrderMigration)
	srv.Store.DB().Exec(`UPDATE reading_progress SET updated_at = datetime('now', '-1 hour')`)
	srv.MigratePageOrder()

	if p, _ := srv.Store.GetProgress(a); p.CurrentPage != 2 {
		t.Fatalf("retry remapped a finished comic again: page %d, want 2", p.CurrentPage)
	}
}

// An unmounted library must not mark the pass done.
func TestMigratePageOrderLibraryOffline(t *testing.T) {
	srv := newTestServer(t)
	root := srv.ComicDirs[0]
	writeTestZip(t, filepath.Join(root, "S/a.cbz"), "page1.jpg", "page10.jpg", "page11.jpg", "page2.jpg")
	a := pathToID("S/a.cbz")
	srv.Store.UpsertProgress(store.Progress{ComicID: a, CurrentPage: 1, TotalPages: 4})
	srv.Store.DB().Exec(`UPDATE reading_progress SET updated_at = datetime('now', '-1 hour')`)

	srv.ComicDirs = []string{filepath.Join(t.TempDir(), "unmounted")}
	srv.MigratePageOrder()
	if done, _ := srv.Store.MigrationDone(pageOrderMigration); done {
		t.Fatal("pass marked done while the library was offline")
	}
	srv.ComicDirs = []string{root}
	srv.MigratePageOrder()
	if p, _ := srv.Store.GetProgress(a); p.CurrentPage != 2 {
		t.Fatalf("remap after the library came back: page %d, want 2", p.CurrentPage)
	}
}

// A retried pass keeps the first pass's cutover: progress the app wrote in the
// new order after the first start is not remapped.
func TestMigratePageOrderCutoverFixed(t *testing.T) {
	srv := newTestServer(t)
	root := srv.ComicDirs[0]
	cbz := filepath.Join(root, "S/c.cbz")
	os.MkdirAll(filepath.Dir(cbz), 0o755)
	os.WriteFile(cbz, []byte("not a zip"), 0o644) // unreadable on the first pass
	c := pathToID("S/c.cbz")
	srv.Store.UpsertProgress(store.Progress{ComicID: c, CurrentPage: 1, TotalPages: 4})
	srv.Store.DB().Exec(`UPDATE reading_progress SET updated_at = datetime('now', '-2 hour')`)

	srv.MigratePageOrder()
	if done, _ := srv.Store.MigrationDone(pageOrderMigration); done {
		t.Fatal("pass marked done with an unreadable comic")
	}
	// Pretend the first pass ran an hour ago; the app then wrote page 1 in
	// the new order, and the file became readable.
	srv.Store.DB().Exec(`UPDATE migrations SET done_at = done_at - 3600 WHERE name = ?`, pageOrderMigration+":start")
	srv.Store.DB().Exec(`UPDATE reading_progress SET updated_at = datetime('now', '-30 minutes')`)
	writeTestZip(t, cbz, "page1.jpg", "page10.jpg", "page11.jpg", "page2.jpg")

	srv.MigratePageOrder()
	if p, _ := srv.Store.GetProgress(c); p.CurrentPage != 1 {
		t.Fatalf("progress written after the cutover was remapped to %d", p.CurrentPage)
	}
	if done, _ := srv.Store.MigrationDone(pageOrderMigration); !done {
		t.Fatal("pass should finish once every comic is readable")
	}
}
