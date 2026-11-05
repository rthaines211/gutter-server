package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ryanhaines/comic-reader/cache"
)

func TestBrowseRoot(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Marvel"), 0755)
	os.WriteFile(filepath.Join(dir, "test.cbz"), []byte("fake"), 0644)
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore"), 0644)

	srv := &Server{ComicDirs: []string{dir}, Cache: cache.NewLRU(10)}
	handler := http.HandlerFunc(srv.HandleBrowse)

	req := httptest.NewRequest("GET", "/browse?path=", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var items []BrowseItem
	json.NewDecoder(w.Body).Decode(&items)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %+v", len(items), items)
	}
	if items[0].Type != "dir" || items[0].Name != "Marvel" {
		t.Errorf("expected dir 'Marvel', got %+v", items[0])
	}
	if items[1].Type != "comic" || items[1].Name != "test.cbz" {
		t.Errorf("expected comic 'test.cbz', got %+v", items[1])
	}
}

func TestBrowsePathTraversal(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{ComicDirs: []string{dir}, Cache: cache.NewLRU(10)}
	handler := http.HandlerFunc(srv.HandleBrowse)
	req := httptest.NewRequest("GET", "/browse?path=../../etc", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
