package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

// makeFakeCBZ writes a minimal zip archive to path so os.Rename works on real files.
func makeFakeCBZ(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("page001.jpg")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("fake"))
	w.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

// setupApplyTemplateServer creates a test server with a temp library root,
// a "Test Series" subdirectory, two cbz files, and DB rows for them.
// Returns the server, series ID, and the library root dir.
func setupApplyTemplateServer(t *testing.T) (*Server, string, string) {
	t.Helper()

	libRoot := t.TempDir()
	seriesDir := filepath.Join(libRoot, "Test Series")
	if err := os.Mkdir(seriesDir, 0755); err != nil {
		t.Fatal(err)
	}

	makeFakeCBZ(t, filepath.Join(seriesDir, "old1.cbz"))
	makeFakeCBZ(t, filepath.Join(seriesDir, "old2.cbz"))

	// Build store and seed data.
	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	seriesID := "test-series-id"
	if err := s.UpsertSeries(store.Series{
		ID:         seriesID,
		Name:       "Test Series",
		FolderPath: seriesDir,
		IssueCount: 2,
		Year:       2023,
	}); err != nil {
		t.Fatal(err)
	}

	// IDs must match PathToID(relPath) that the handler will compute.
	// We use simple IDs here; the handler uses MigrateComicIDFull so any ID works.
	id1 := store.PathToID("Test Series/old1.cbz")
	id2 := store.PathToID("Test Series/old2.cbz")
	if err := s.ReplaceIssues(seriesID, []store.Issue{
		{ID: id1, SeriesID: seriesID, Filename: "old1.cbz", Number: "1", Position: 0},
		{ID: id2, SeriesID: seriesID, Filename: "old2.cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		ComicDirs: []string{libRoot},
		Store:     s,
	}
	return srv, seriesID, libRoot
}

func TestApplyTemplateDryRun(t *testing.T) {
	srv, seriesID, libRoot := setupApplyTemplateServer(t)

	req := httptest.NewRequest(http.MethodPost,
		"/series/"+seriesID+"/apply-template?dry_run=1", nil)
	req.SetPathValue("id", seriesID)
	rec := httptest.NewRecorder()
	srv.HandleApplyTemplate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Results []struct {
			OldName  string `json:"old_name"`
			NewName  string `json:"new_name"`
			Applied  bool   `json:"applied"`
			Conflict bool   `json:"conflict"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(resp.Results))
	}

	// Both should be dry-run (not applied).
	for _, r := range resp.Results {
		if r.Applied {
			t.Errorf("dry run: applied should be false, got true for %s", r.OldName)
		}
	}

	// New names should match the default template "{series} #{number:03}".
	names := map[string]string{}
	for _, r := range resp.Results {
		names[r.OldName] = r.NewName
	}
	if names["old1.cbz"] != "Test Series #001.cbz" {
		t.Errorf("issue 1: want 'Test Series #001.cbz', got %q", names["old1.cbz"])
	}
	if names["old2.cbz"] != "Test Series #002.cbz" {
		t.Errorf("issue 2: want 'Test Series #002.cbz', got %q", names["old2.cbz"])
	}

	// Originals must still exist, new files must NOT exist.
	seriesDir := filepath.Join(libRoot, "Test Series")
	mustExist(t, filepath.Join(seriesDir, "old1.cbz"))
	mustExist(t, filepath.Join(seriesDir, "old2.cbz"))
	mustNotExist(t, filepath.Join(seriesDir, "Test Series #001.cbz"))
	mustNotExist(t, filepath.Join(seriesDir, "Test Series #002.cbz"))
}

func TestApplyTemplateApplies(t *testing.T) {
	srv, seriesID, libRoot := setupApplyTemplateServer(t)

	// Seed a reading_progress row for old1 so we can verify migration.
	id1 := store.PathToID("Test Series/old1.cbz")
	if _, err := srv.Store.DB().Exec(
		`INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at) VALUES (?, 3, 10, 0)`,
		id1,
	); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/series/"+seriesID+"/apply-template", nil)
	req.SetPathValue("id", seriesID)
	rec := httptest.NewRecorder()
	srv.HandleApplyTemplate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Results []struct {
			OldName string `json:"old_name"`
			NewName string `json:"new_name"`
			Applied bool   `json:"applied"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(resp.Results))
	}
	for _, r := range resp.Results {
		if !r.Applied {
			t.Errorf("apply: applied should be true for %s", r.OldName)
		}
	}

	// Old files gone, new files present on disk.
	seriesDir := filepath.Join(libRoot, "Test Series")
	mustNotExist(t, filepath.Join(seriesDir, "old1.cbz"))
	mustNotExist(t, filepath.Join(seriesDir, "old2.cbz"))
	mustExist(t, filepath.Join(seriesDir, "Test Series #001.cbz"))
	mustExist(t, filepath.Join(seriesDir, "Test Series #002.cbz"))

	// DB issues rows should have new filenames.
	issues, err := srv.Store.ListIssues(seriesID)
	if err != nil {
		t.Fatal(err)
	}
	filenames := map[string]bool{}
	for _, iss := range issues {
		filenames[iss.Filename] = true
	}
	if !filenames["Test Series #001.cbz"] {
		t.Error("DB: missing 'Test Series #001.cbz'")
	}
	if !filenames["Test Series #002.cbz"] {
		t.Error("DB: missing 'Test Series #002.cbz'")
	}

	// reading_progress must have migrated to the new ID for old1.
	newID1 := store.PathToID("Test Series/Test Series #001.cbz")
	var cnt int
	if err := srv.Store.DB().QueryRow(
		`SELECT COUNT(*) FROM reading_progress WHERE comic_id = ?`, newID1,
	).Scan(&cnt); err != nil {
		t.Fatalf("query progress: %v", err)
	}
	if cnt != 1 {
		t.Errorf("progress not migrated to new ID %s (count=%d)", newID1, cnt)
	}
	// Old ID must be gone.
	if err := srv.Store.DB().QueryRow(
		`SELECT COUNT(*) FROM reading_progress WHERE comic_id = ?`, id1,
	).Scan(&cnt); err != nil {
		t.Fatalf("query old progress: %v", err)
	}
	if cnt != 0 {
		t.Errorf("progress still exists under old ID %s", id1)
	}
}

func TestApplyTemplateSkipsConflicts(t *testing.T) {
	srv, seriesID, libRoot := setupApplyTemplateServer(t)

	seriesDir := filepath.Join(libRoot, "Test Series")
	// Pre-create the target for issue 1 to force a disk conflict.
	makeFakeCBZ(t, filepath.Join(seriesDir, "Test Series #001.cbz"))

	req := httptest.NewRequest(http.MethodPost,
		"/series/"+seriesID+"/apply-template", nil)
	req.SetPathValue("id", seriesID)
	rec := httptest.NewRecorder()
	srv.HandleApplyTemplate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Results []struct {
			OldName  string `json:"old_name"`
			NewName  string `json:"new_name"`
			Applied  bool   `json:"applied"`
			Conflict bool   `json:"conflict"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(resp.Results))
	}

	byOld := map[string]struct {
		Applied  bool
		Conflict bool
	}{}
	for _, r := range resp.Results {
		byOld[r.OldName] = struct {
			Applied  bool
			Conflict bool
		}{r.Applied, r.Conflict}
	}

	r1 := byOld["old1.cbz"]
	if !r1.Conflict {
		t.Error("issue 1: want conflict=true")
	}
	if r1.Applied {
		t.Error("issue 1: want applied=false")
	}
	// old1.cbz must still exist on disk.
	mustExist(t, filepath.Join(seriesDir, "old1.cbz"))

	// DB row for old1 must be unchanged (still has old filename).
	id1 := store.PathToID("Test Series/old1.cbz")
	var dbFilename string
	if err := srv.Store.DB().QueryRow(
		`SELECT filename FROM issues WHERE id = ?`, id1,
	).Scan(&dbFilename); err != nil {
		t.Fatalf("query issue1 filename: %v", err)
	}
	if dbFilename != "old1.cbz" {
		t.Errorf("issue1 DB filename should be 'old1.cbz', got %q", dbFilename)
	}

	r2 := byOld["old2.cbz"]
	if !r2.Applied {
		t.Error("issue 2: want applied=true")
	}
	mustExist(t, filepath.Join(seriesDir, "Test Series #002.cbz"))
	mustNotExist(t, filepath.Join(seriesDir, "old2.cbz"))
}

func TestApplyTemplateDuplicateTargetsBothConflict(t *testing.T) {
	libRoot := t.TempDir()
	seriesDir := filepath.Join(libRoot, "Test Series")
	if err := os.Mkdir(seriesDir, 0755); err != nil {
		t.Fatal(err)
	}

	makeFakeCBZ(t, filepath.Join(seriesDir, "old1.cbz"))
	makeFakeCBZ(t, filepath.Join(seriesDir, "old2.cbz"))

	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	seriesID := "dup-series-id"
	if err := s.UpsertSeries(store.Series{
		ID:         seriesID,
		Name:       "Test Series",
		FolderPath: seriesDir,
		IssueCount: 2,
		Year:       2023,
	}); err != nil {
		t.Fatal(err)
	}

	// Both issues have the SAME number → both resolve to "Test Series #001.cbz".
	id1 := store.PathToID("Test Series/old1.cbz")
	id2 := store.PathToID("Test Series/old2.cbz")
	if err := s.ReplaceIssues(seriesID, []store.Issue{
		{ID: id1, SeriesID: seriesID, Filename: "old1.cbz", Number: "1", Position: 0},
		{ID: id2, SeriesID: seriesID, Filename: "old2.cbz", Number: "1", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		ComicDirs: []string{libRoot},
		Store:     s,
	}

	req := httptest.NewRequest(http.MethodPost,
		"/series/"+seriesID+"/apply-template", nil)
	req.SetPathValue("id", seriesID)
	rec := httptest.NewRecorder()
	srv.HandleApplyTemplate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Results []struct {
			OldName  string `json:"old_name"`
			NewName  string `json:"new_name"`
			Applied  bool   `json:"applied"`
			Conflict bool   `json:"conflict"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(resp.Results))
	}

	for _, r := range resp.Results {
		if !r.Conflict {
			t.Errorf("%s: want conflict=true (duplicate target), got false", r.OldName)
		}
		if r.Applied {
			t.Errorf("%s: want applied=false (duplicate target), got true", r.OldName)
		}
	}

	// Both original files must be untouched on disk.
	mustExist(t, filepath.Join(seriesDir, "old1.cbz"))
	mustExist(t, filepath.Join(seriesDir, "old2.cbz"))
	mustNotExist(t, filepath.Join(seriesDir, "Test Series #001.cbz"))
}

// helpers

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to exist: %s", path)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected file to NOT exist: %s", path)
	}
}
