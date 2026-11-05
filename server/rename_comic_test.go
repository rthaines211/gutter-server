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

// Renaming one comic must carry its issues row and match/enrichment rows to
// the new ID; otherwise the next scan deletes the old row and the FK cascade
// wipes them.
func TestRenameComicKeepsMatchRows(t *testing.T) {
	libRoot := t.TempDir()
	dir := filepath.Join(libRoot, "Saga")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	makeFakeCBZ(t, filepath.Join(dir, "a.cbz"))

	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	seriesID := slugify("Saga")
	oldID := store.PathToID("Saga/a.cbz")
	if err := s.UpsertSeries(store.Series{ID: seriesID, Name: "Saga", FolderPath: dir, IssueCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssues(seriesID, []store.Issue{{ID: oldID, SeriesID: seriesID, Filename: "a.cbz", Number: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO issue_match(issue_id, upstream_id, state) VALUES(?, '123', 'confirmed')`, oldID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssueEnrichment(store.IssueEnrichment{IssueID: oldID, VineID: 123, Title: "Chapter One"}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{ComicDirs: []string{libRoot}, Store: s}
	body, _ := json.Marshal(map[string]string{"name": "Saga #001"})
	req := httptest.NewRequest(http.MethodPut, "/comics/"+oldID+"/rename", bytes.NewReader(body))
	req.SetPathValue("id", oldID)
	rec := httptest.NewRecorder()
	srv.HandleRenameComic(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	newID := store.PathToID("Saga/Saga #001.cbz")
	for _, q := range []string{
		`SELECT COUNT(*) FROM issues WHERE id = ?`,
		`SELECT COUNT(*) FROM issue_match WHERE issue_id = ?`,
		`SELECT COUNT(*) FROM issue_enrichment WHERE issue_id = ?`,
	} {
		var n int
		if err := s.DB().QueryRow(q, newID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s: want 1 row under new ID, got %d", q, n)
		}
	}
}
