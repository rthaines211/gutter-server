package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

func TestRenameFolder(t *testing.T) {
	libRoot := t.TempDir()
	coverDir := t.TempDir()

	// Create Old Name/a.cbz on disk.
	oldDir := filepath.Join(libRoot, "Old Name")
	if err := os.Mkdir(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	makeFakeCBZ(t, filepath.Join(oldDir, "a.cbz"))

	// Build store.
	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Compute IDs the same way the scanner does.
	oldSeriesID := slugify("Old Name")
	oldIssueID := store.PathToID("Old Name/a.cbz")

	// Insert series row with absolute folder_path (same as scanner).
	if err := s.UpsertSeries(store.Series{
		ID:         oldSeriesID,
		Name:       "Old Name",
		FolderPath: oldDir,
		IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// Insert issue row.
	if err := s.ReplaceIssues(oldSeriesID, []store.Issue{
		{ID: oldIssueID, SeriesID: oldSeriesID, Filename: "a.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	// Insert reading_progress for the issue.
	if _, err := s.DB().Exec(
		`INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at) VALUES (?, 5, 20, 0)`,
		oldIssueID,
	); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	// Create fake cover thumbnails.
	seriesThumb := filepath.Join(coverDir, "series_"+oldSeriesID+".jpg")
	issueThumb := filepath.Join(coverDir, "comic_"+oldIssueID+".jpg")
	if err := os.WriteFile(seriesThumb, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(issueThumb, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		ComicDirs: []string{libRoot},
		Store:     s,
		CoverDir:  coverDir,
	}

	body, _ := json.Marshal(map[string]string{"name": "New Name"})
	req := httptest.NewRequest(http.MethodPut, "/series/"+oldSeriesID+"/rename-folder", bytes.NewReader(body))
	req.SetPathValue("id", oldSeriesID)
	rec := httptest.NewRecorder()
	srv.HandleRenameFolder(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	newSeriesID := slugify("New Name")
	if resp.ID != newSeriesID {
		t.Errorf("want id=%q, got %q", newSeriesID, resp.ID)
	}
	if resp.Name != "New Name" {
		t.Errorf("want name=%q, got %q", "New Name", resp.Name)
	}

	// Old folder gone, new folder exists with a.cbz.
	mustNotExist(t, oldDir)
	mustExist(t, filepath.Join(libRoot, "New Name"))
	mustExist(t, filepath.Join(libRoot, "New Name", "a.cbz"))

	// Series row updated.
	series, err := s.GetSeries(newSeriesID)
	if err != nil {
		t.Fatalf("GetSeries new: %v", err)
	}
	if series.Name != "New Name" {
		t.Errorf("series name: want %q, got %q", "New Name", series.Name)
	}

	// Issue row migrated to new ID.
	newIssueID := store.PathToID("New Name/a.cbz")
	issues, err := s.ListIssues(newSeriesID)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != newIssueID {
		t.Errorf("issue ID: want %q, got %+v", newIssueID, issues)
	}

	// Reading progress migrated to new issue ID.
	var cnt int
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM reading_progress WHERE comic_id = ?`, newIssueID,
	).Scan(&cnt); err != nil {
		t.Fatalf("query progress: %v", err)
	}
	if cnt != 1 {
		t.Errorf("progress not migrated to new ID %s (count=%d)", newIssueID, cnt)
	}
	// Old progress gone.
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM reading_progress WHERE comic_id = ?`, oldIssueID,
	).Scan(&cnt); err != nil {
		t.Fatalf("query old progress: %v", err)
	}
	if cnt != 0 {
		t.Errorf("old progress still exists (count=%d)", cnt)
	}

	// Old thumbnails removed.
	mustNotExist(t, seriesThumb)
	mustNotExist(t, issueThumb)
}

func TestRenameFolderUnknownSeries(t *testing.T) {
	libRoot := t.TempDir()

	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	srv := &Server{
		ComicDirs: []string{libRoot},
		Store:     s,
	}

	body, _ := json.Marshal(map[string]string{"name": "Any Name"})
	req := httptest.NewRequest(http.MethodPut, "/series/does-not-exist/rename-folder", bytes.NewReader(body))
	req.SetPathValue("id", "does-not-exist")
	rec := httptest.NewRecorder()
	srv.HandleRenameFolder(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRenameFolderSlugCollision(t *testing.T) {
	libRoot := t.TempDir()

	// Create folders for both series on disk.
	alphaDir := filepath.Join(libRoot, "Alpha")
	betaDir := filepath.Join(libRoot, "Beta")
	if err := os.Mkdir(alphaDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(betaDir, 0755); err != nil {
		t.Fatal(err)
	}
	makeFakeCBZ(t, filepath.Join(alphaDir, "a.cbz"))
	makeFakeCBZ(t, filepath.Join(betaDir, "b.cbz"))

	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	alphaSeriesID := slugify("Alpha")
	betaSeriesID := slugify("Beta")
	alphaIssueID := store.PathToID("Alpha/a.cbz")
	betaIssueID := store.PathToID("Beta/b.cbz")

	// Seed both series.
	if err := s.UpsertSeries(store.Series{
		ID: alphaSeriesID, Name: "Alpha", FolderPath: alphaDir, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssues(alphaSeriesID, []store.Issue{
		{ID: alphaIssueID, SeriesID: alphaSeriesID, Filename: "a.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSeries(store.Series{
		ID: betaSeriesID, Name: "Beta", FolderPath: betaDir, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssues(betaSeriesID, []store.Issue{
		{ID: betaIssueID, SeriesID: betaSeriesID, Filename: "b.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		ComicDirs: []string{libRoot},
		Store:     s,
	}

	// Attempt to rename Beta's folder to "Alpha" — slug collides with existing series.
	body, _ := json.Marshal(map[string]string{"name": "Alpha"})
	req := httptest.NewRequest(http.MethodPut, "/series/"+betaSeriesID+"/rename-folder", bytes.NewReader(body))
	req.SetPathValue("id", betaSeriesID)
	rec := httptest.NewRecorder()
	srv.HandleRenameFolder(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}

	// Beta's folder must be untouched.
	mustExist(t, betaDir)
	mustExist(t, filepath.Join(betaDir, "b.cbz"))

	// Beta's series row must be unchanged.
	series, err := s.GetSeries(betaSeriesID)
	if err != nil {
		t.Fatalf("GetSeries beta: %v", err)
	}
	if series.Name != "Beta" {
		t.Errorf("beta series should be unchanged, got name=%q", series.Name)
	}
}

func TestRenameFolderConflict(t *testing.T) {
	libRoot := t.TempDir()

	// Create BOTH "Old Name/a.cbz" AND "New Name/" on disk.
	oldDir := filepath.Join(libRoot, "Old Name")
	newDir := filepath.Join(libRoot, "New Name")
	if err := os.Mkdir(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	makeFakeCBZ(t, filepath.Join(oldDir, "a.cbz"))

	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	oldSeriesID := slugify("Old Name")
	oldIssueID := store.PathToID("Old Name/a.cbz")

	if err := s.UpsertSeries(store.Series{
		ID:         oldSeriesID,
		Name:       "Old Name",
		FolderPath: oldDir,
		IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssues(oldSeriesID, []store.Issue{
		{ID: oldIssueID, SeriesID: oldSeriesID, Filename: "a.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		ComicDirs: []string{libRoot},
		Store:     s,
	}

	body, _ := json.Marshal(map[string]string{"name": "New Name"})
	req := httptest.NewRequest(http.MethodPut, "/series/"+oldSeriesID+"/rename-folder", bytes.NewReader(body))
	req.SetPathValue("id", oldSeriesID)
	rec := httptest.NewRecorder()
	srv.HandleRenameFolder(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}

	// Old folder still exists.
	mustExist(t, oldDir)
	mustExist(t, filepath.Join(oldDir, "a.cbz"))

	// Series row unchanged.
	series, err := s.GetSeries(oldSeriesID)
	if err != nil {
		t.Fatalf("GetSeries old: %v", err)
	}
	if series.Name != "Old Name" {
		t.Errorf("series should be unchanged, got name=%q", series.Name)
	}
}
