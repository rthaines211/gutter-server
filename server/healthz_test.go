package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthzOK(t *testing.T) {
	srv := &Server{ComicDirs: []string{t.TempDir()}}
	rec := httptest.NewRecorder()
	srv.HandleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHealthzUnreadableRoot(t *testing.T) {
	srv := &Server{ComicDirs: []string{"/nonexistent-media-root"}}
	rec := httptest.NewRecorder()
	srv.HandleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 503 {
		t.Fatalf("want 503 for unreadable root, got %d", rec.Code)
	}
}

func TestHealthzDBDown(t *testing.T) {
	st := newIntegrationTestStore(t)
	st.DB().Close()
	srv := &Server{ComicDirs: []string{t.TempDir()}, Store: st}
	rec := httptest.NewRecorder()
	srv.HandleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "db") {
		t.Fatalf("want 503 mentioning db, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHealthzReportsWritebackErrors(t *testing.T) {
	srv := newTestServer(t)
	id := pathToID("s/1.cbz")
	srv.Store.DB().Exec(`INSERT INTO series(id,name,folder_path) VALUES('s','s','s')`)
	srv.Store.DB().Exec(`INSERT INTO issues(id,series_id,filename) VALUES(?,'s','1.cbz')`, id)
	srv.Store.EnqueueWriteback(id)
	for range 3 {
		srv.Store.MarkWritebackError(id, "boom")
	}
	rec := httptest.NewRecorder()
	srv.HandleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "writeback_errors: 1") {
		t.Fatalf("want 200 with writeback_errors: 1, got %d: %s", rec.Code, rec.Body.String())
	}
}
