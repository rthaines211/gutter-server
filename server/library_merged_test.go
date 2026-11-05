package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

// TestHandleGetSeriesMergedEnrichmentFields verifies Task 7: the series-detail
// JSON payload surfaces issue page_count/characters from issue_enrichment, and
// folds series_enrichment.genre into the existing genres list.
func TestHandleGetSeriesMergedEnrichmentFields(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.Store.UpsertSeries(store.Series{
		ID: "batman", Name: "Batman", FolderPath: "/batman", IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.ReplaceSeriesGenres("batman", []string{"Action"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertSeriesEnrichment(store.SeriesEnrichment{
		SeriesID:   "batman",
		Genre:      "Superhero",
		Status:     "auto",
		EnrichedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.ReplaceIssues("batman", []store.Issue{
		{ID: "batman-001", SeriesID: "batman", Filename: "Batman 001.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertIssueEnrichment(store.IssueEnrichment{
		IssueID:    "batman-001",
		PageCount:  22,
		Characters: `[{"name":"Batman"},{"name":"Alfred Pennyworth"}]`,
		EnrichedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/library/batman", nil)
	req.SetPathValue("id", "batman")
	w := httptest.NewRecorder()
	srv.HandleGetSeries(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var detail struct {
		Genres []string `json:"genres"`
		Issues []struct {
			PageCount  float64  `json:"page_count"`
			Characters []string `json:"characters"`
		} `json:"issues"`
	}
	if err := json.NewDecoder(w.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}

	foundGenre := false
	for _, g := range detail.Genres {
		if g == "Superhero" {
			foundGenre = true
		}
	}
	if !foundGenre {
		t.Errorf("expected genres to include enrichment genre 'Superhero', got %v", detail.Genres)
	}
	if len(detail.Genres) != 2 {
		t.Errorf("expected 2 genres (existing 'Action' + enrichment 'Superhero'), got %v", detail.Genres)
	}

	if len(detail.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(detail.Issues))
	}
	if detail.Issues[0].PageCount != 22 {
		t.Errorf("expected page_count 22, got %v", detail.Issues[0].PageCount)
	}
	foundBatman := false
	for _, c := range detail.Issues[0].Characters {
		if c == "Batman" {
			foundBatman = true
		}
	}
	if !foundBatman {
		t.Errorf("expected characters to include 'Batman', got %v", detail.Issues[0].Characters)
	}
}

// TestHandleGetSeriesMergedGenreDedupCaseInsensitive verifies the genre fold
// does not add a case-insensitive duplicate of an existing genre.
func TestHandleGetSeriesMergedGenreDedupCaseInsensitive(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.Store.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga", FolderPath: "/saga", IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.ReplaceSeriesGenres("saga", []string{"sci-fi"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertSeriesEnrichment(store.SeriesEnrichment{
		SeriesID:   "saga",
		Genre:      "Sci-Fi",
		Status:     "auto",
		EnrichedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/library/saga", nil)
	req.SetPathValue("id", "saga")
	w := httptest.NewRecorder()
	srv.HandleGetSeries(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var detail struct {
		Genres []string `json:"genres"`
	}
	if err := json.NewDecoder(w.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Genres) != 1 {
		t.Errorf("expected no duplicate genre (case-insensitive fold), got %v", detail.Genres)
	}
}
