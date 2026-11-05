package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

// TestHandleGetSeriesStripsHTMLFromDescriptions verifies that HTML tags stored
// raw in series_enrichment.description and issue_enrichment.summary (the
// offline-dump path never strips them, unlike the live ComicVine API path)
// are stripped and entities decoded at serve time in the series-detail JSON.
func TestHandleGetSeriesStripsHTMLFromDescriptions(t *testing.T) {
	const raw = "<p><em>Hello</em> &amp; <b>bye</b></p>"

	srv := newTestServer(t)
	if err := srv.Store.UpsertSeries(store.Series{
		ID: "html-series", Name: "HTML Series", FolderPath: "/html-series", IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertSeriesEnrichment(store.SeriesEnrichment{
		SeriesID:    "html-series",
		Description: raw,
		Status:      "auto",
		EnrichedAt:  1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.ReplaceIssues("html-series", []store.Issue{
		{ID: "html-series-001", SeriesID: "html-series", Filename: "HTML Series 001.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertIssueEnrichment(store.IssueEnrichment{
		IssueID:    "html-series-001",
		Summary:    raw,
		EnrichedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/library/html-series", nil)
	req.SetPathValue("id", "html-series")
	w := httptest.NewRecorder()
	srv.HandleGetSeries(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var detail struct {
		Description string `json:"description"`
		Issues      []struct {
			Summary string `json:"summary"`
		} `json:"issues"`
	}
	if err := json.NewDecoder(w.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}

	if strings.ContainsAny(detail.Description, "<>") {
		t.Errorf("expected series description to have HTML tags stripped, got %q", detail.Description)
	}
	if detail.Description != "Hello & bye" {
		t.Errorf("expected series description %q, got %q", "Hello & bye", detail.Description)
	}

	if len(detail.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(detail.Issues))
	}
	if strings.ContainsAny(detail.Issues[0].Summary, "<>") {
		t.Errorf("expected issue summary to have HTML tags stripped, got %q", detail.Issues[0].Summary)
	}
	if detail.Issues[0].Summary != "Hello & bye" {
		t.Errorf("expected issue summary %q, got %q", "Hello & bye", detail.Issues[0].Summary)
	}
}
