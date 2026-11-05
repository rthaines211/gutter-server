package server

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rthaines211/gutter-server/cache"
	"github.com/rthaines211/gutter-server/metadata"
)

func setupComicTest(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	// Copy test fixture
	src, err := os.ReadFile(filepath.Join("..", "archive", "testdata", "test.cbz"))
	if err != nil {
		t.Fatalf("test fixture not found: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "test.cbz"), src, 0644)
	srv := &Server{ComicDirs: []string{dir}, Cache: cache.NewLRU(10)}
	return srv, "test.cbz"
}

func TestHandleComicInfo(t *testing.T) {
	srv, comicFile := setupComicTest(t)
	id := pathToID(comicFile)
	req := httptest.NewRequest("GET", "/comics/"+id+"/info", nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.HandleComicInfo(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var info metadata.ComicInfo
	json.NewDecoder(w.Body).Decode(&info)
	if info.Title != "Test Issue" {
		t.Errorf("expected 'Test Issue', got %q", info.Title)
	}
}

func TestHandleComicPage(t *testing.T) {
	srv, comicFile := setupComicTest(t)
	id := pathToID(comicFile)
	req := httptest.NewRequest("GET", "/comics/"+id+"/pages/0", nil)
	req.SetPathValue("id", id)
	req.SetPathValue("num", "0")
	w := httptest.NewRecorder()
	srv.HandleComicPage(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("expected image/png, got %s", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty body")
	}
}

func TestHandleComicPageOutOfRange(t *testing.T) {
	srv, comicFile := setupComicTest(t)
	id := pathToID(comicFile)
	req := httptest.NewRequest("GET", "/comics/"+id+"/pages/99", nil)
	req.SetPathValue("id", id)
	req.SetPathValue("num", "99")
	w := httptest.NewRecorder()
	srv.HandleComicPage(w, req)
	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleComicCover(t *testing.T) {
	srv, comicFile := setupComicTest(t)
	id := pathToID(comicFile)
	req := httptest.NewRequest("GET", "/comics/"+id+"/cover", nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.HandleComicCover(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if len(body) == 0 {
		t.Error("expected non-empty cover")
	}
}
