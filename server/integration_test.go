package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/store"
)

// seedComicForServer inserts a minimal series + issue + reading_progress row,
// mirroring the fixture pattern established in rename_folder_test.go.
func seedComicForServer(t *testing.T, s *store.Store, seriesName, comicID, filename string) {
	t.Helper()
	if err := s.UpsertSeries(store.Series{
		ID:         slugify(seriesName),
		Name:       seriesName,
		FolderPath: "/lib/" + seriesName,
		IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssues(slugify(seriesName), []store.Issue{
		{ID: comicID, SeriesID: slugify(seriesName), Filename: filename, Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at) VALUES (?, 5, 20, 0)`,
		comicID,
	); err != nil {
		t.Fatalf("seed progress: %v", err)
	}
}

func newIntegrationTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestFileMoveRemapsID(t *testing.T) {
	s := newIntegrationTestStore(t)
	oldID := store.PathToID("Batman/Batman 012.cbz")
	seedComicForServer(t, s, "Batman", oldID, "Batman 012.cbz")
	srv := &Server{ComicDirs: []string{"/lib"}, Store: s}

	body := `{"old_path":"/lib/Batman/Batman 012.cbz","new_path":"/lib/Batman (2016)/Batman (2016) 012.cbz","fingerprint":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/integration/file-move", strings.NewReader(body))
	srv.HandleFileMove(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	newID := store.PathToID("Batman (2016)/Batman (2016) 012.cbz")
	if !s.ComicExists(newID) || s.ComicExists(oldID) {
		t.Fatalf("ID not remapped")
	}

	// Progress followed: reading_progress row now keyed by newID, old gone.
	var cnt int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM reading_progress WHERE comic_id = ?`, newID).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("reading_progress not migrated to newID (count=%d)", cnt)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM reading_progress WHERE comic_id = ?`, oldID).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("old reading_progress row still present (count=%d)", cnt)
	}
}

func TestFileMoveUnknownOld404(t *testing.T) {
	s := newIntegrationTestStore(t)
	srv := &Server{ComicDirs: []string{"/lib"}, Store: s}

	body := `{"old_path":"/lib/Batman/Batman 012.cbz","new_path":"/lib/Batman (2016)/Batman (2016) 012.cbz","fingerprint":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/integration/file-move", strings.NewReader(body))
	srv.HandleFileMove(rec, req)
	if rec.Code != 404 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFileMoveOutsideDirs400(t *testing.T) {
	s := newIntegrationTestStore(t)
	srv := &Server{ComicDirs: []string{"/lib"}, Store: s}

	body := `{"old_path":"/elsewhere/x.cbz","new_path":"/lib/Batman (2016)/Batman (2016) 012.cbz","fingerprint":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/integration/file-move", strings.NewReader(body))
	srv.HandleFileMove(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFileMoveConflict409(t *testing.T) {
	s := newIntegrationTestStore(t)
	oldID := store.PathToID("Batman/Batman 012.cbz")
	newID := store.PathToID("Batman (2016)/Batman (2016) 012.cbz")
	seedComicForServer(t, s, "Batman", oldID, "Batman 012.cbz")
	seedComicForServer(t, s, "Batman (2016)", newID, "Batman (2016) 012.cbz")
	srv := &Server{ComicDirs: []string{"/lib"}, Store: s}

	body := `{"old_path":"/lib/Batman/Batman 012.cbz","new_path":"/lib/Batman (2016)/Batman (2016) 012.cbz","fingerprint":"abc"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/integration/file-move", strings.NewReader(body))
	srv.HandleFileMove(rec, req)
	if rec.Code != 409 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFileStateActiveWindow(t *testing.T) {
	srv := &Server{ComicDirs: []string{"/lib"}}
	id := store.PathToID("Batman/Batman 012.cbz")
	srv.noteComicServed(id)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/integration/file-state?path="+url.QueryEscape("/lib/Batman/Batman 012.cbz"), nil)
	srv.HandleFileState(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Active         bool  `json:"active"`
		LastAccessedAt int64 `json:"last_accessed_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Active {
		t.Fatalf("expected active=true, got %+v", resp)
	}
	if resp.LastAccessedAt == 0 {
		t.Fatalf("expected non-zero last_accessed_at, got %+v", resp)
	}

	// A path never served -> inactive.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/integration/file-state?path="+url.QueryEscape("/lib/Other/Other 001.cbz"), nil)
	srv.HandleFileState(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("status %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp2 struct {
		Active         bool  `json:"active"`
		LastAccessedAt int64 `json:"last_accessed_at"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.Active || resp2.LastAccessedAt != 0 {
		t.Fatalf("expected inactive/zero for unserved path, got %+v", resp2)
	}

	// A path outside ComicDirs -> inactive too.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "/integration/file-state?path=/elsewhere/x.cbz", nil)
	srv.HandleFileState(rec3, req3)
	if rec3.Code != 200 {
		t.Fatalf("status %d: %s", rec3.Code, rec3.Body.String())
	}
	var resp3 struct {
		Active bool `json:"active"`
	}
	if err := json.Unmarshal(rec3.Body.Bytes(), &resp3); err != nil {
		t.Fatal(err)
	}
	if resp3.Active {
		t.Fatalf("expected inactive for out-of-dir path, got %+v", resp3)
	}
}
