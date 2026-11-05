package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/store"
)

func seedEnrichmentSeries(t *testing.T, srv *Server) {
	t.Helper()
	if err := srv.Store.UpsertSeries(store.Series{
		ID: "batman", Name: "Batman", FolderPath: "/batman", IssueCount: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Store.EnqueueMissingEnrichment(); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.SetEnrichmentCandidates("batman",
		`[{"volume_id":1,"name":"Batman","start_year":2011,"publisher":"DC Comics","issue_count":57,"cover_url":"https://cv/x.jpg","description_snippet":"The New 52."}]`,
	); err != nil {
		t.Fatal(err)
	}
}

func TestEnrichmentStatus(t *testing.T) {
	srv := newTestServer(t)
	seedEnrichmentSeries(t, srv)

	req := httptest.NewRequest("GET", "/enrichment/status", nil)
	w := httptest.NewRecorder()
	srv.HandleEnrichmentStatus(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Worker string         `json:"worker"`
		Counts map[string]int `json:"counts"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Worker != "no-key" {
		t.Fatalf("expected no-key (test server has no worker), got %q", resp.Worker)
	}
	if resp.Counts["pending_review"] != 1 {
		t.Fatalf("expected 1 pending_review, got %v", resp.Counts)
	}
}

func TestEnrichmentPending(t *testing.T) {
	srv := newTestServer(t)
	seedEnrichmentSeries(t, srv)

	req := httptest.NewRequest("GET", "/enrichment/pending", nil)
	w := httptest.NewRecorder()
	srv.HandleEnrichmentPending(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp []struct {
		SeriesID   string `json:"series_id"`
		Name       string `json:"name"`
		IssueCount int    `json:"issue_count"`
		Candidates []struct {
			VolumeID int    `json:"volume_id"`
			Name     string `json:"name"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 1 || resp[0].SeriesID != "batman" || resp[0].Name != "Batman" {
		t.Fatalf("unexpected pending %+v", resp)
	}
	if len(resp[0].Candidates) != 1 || resp[0].Candidates[0].VolumeID != 1 {
		t.Fatalf("unexpected candidates %+v", resp[0].Candidates)
	}
}

func TestEnrichmentMatchSkip(t *testing.T) {
	srv := newTestServer(t)
	seedEnrichmentSeries(t, srv)

	req := httptest.NewRequest("POST", "/enrichment/batman/match",
		strings.NewReader(`{"volume_id": 0}`))
	req.SetPathValue("id", "batman")
	w := httptest.NewRecorder()
	srv.HandleEnrichmentMatch(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	state, err := srv.Store.GetEnrichmentState("batman")
	if err != nil || state.Status != "skipped" {
		t.Fatalf("expected skipped, got %+v err %v", state, err)
	}
}

func TestEnrichmentMatchChoose(t *testing.T) {
	srv := newTestServer(t)
	seedEnrichmentSeries(t, srv)

	req := httptest.NewRequest("POST", "/enrichment/batman/match",
		strings.NewReader(`{"volume_id": 1}`))
	req.SetPathValue("id", "batman")
	w := httptest.NewRecorder()
	srv.HandleEnrichmentMatch(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	state, err := srv.Store.GetEnrichmentState("batman")
	if err != nil || state.Status != "queued" || state.ChosenVolumeID != 1 {
		t.Fatalf("expected queued with chosen volume, got %+v err %v", state, err)
	}
}

func TestEnrichmentRequeue(t *testing.T) {
	srv := newTestServer(t)
	seedEnrichmentSeries(t, srv)
	if err := srv.Store.UpsertSeriesEnrichment(store.SeriesEnrichment{
		SeriesID: "batman", VolumeID: 1, Status: "auto", EnrichedAt: 1, Creators: "[]",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/enrichment/batman/requeue", nil)
	req.SetPathValue("id", "batman")
	w := httptest.NewRecorder()
	srv.HandleEnrichmentRequeue(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	state, _ := srv.Store.GetEnrichmentState("batman")
	if state.Status != "queued" {
		t.Fatalf("expected queued, got %+v", state)
	}
	if _, err := srv.Store.GetSeriesEnrichment("batman"); err != store.ErrNotFound {
		t.Fatalf("expected enrichment row deleted, got %v", err)
	}
}

func TestEnrichmentMatchNegativeVolumeRejected(t *testing.T) {
	srv := newTestServer(t)
	seedEnrichmentSeries(t, srv)

	req := httptest.NewRequest("POST", "/enrichment/batman/match",
		strings.NewReader(`{"volume_id": -5}`))
	req.SetPathValue("id", "batman")
	w := httptest.NewRecorder()
	srv.HandleEnrichmentMatch(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	state, err := srv.Store.GetEnrichmentState("batman")
	if err != nil || state.Status != "pending_review" {
		t.Fatalf("expected state still pending_review, got %+v err %v", state, err)
	}
}

func TestEnrichmentMatchUnknownSeries404(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("POST", "/enrichment/nope/match",
		strings.NewReader(`{"volume_id": 1}`))
	req.SetPathValue("id", "nope")
	w := httptest.NewRecorder()
	srv.HandleEnrichmentMatch(w, req)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func applyTestEnrichment(t *testing.T, srv *Server) {
	t.Helper()
	if err := srv.Store.UpsertSeriesEnrichment(store.SeriesEnrichment{
		SeriesID:    "batman",
		VolumeID:    1,
		Description: "The Dark Knight.",
		Publisher:   "DC Comics",
		StartYear:   2011,
		Creators:    `[{"name":"Scott Snyder","role":"writer"},{"name":"Greg Capullo","role":"penciler, cover"}]`,
		Status:      "auto",
		EnrichedAt:  1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.ReplaceIssues("batman", []store.Issue{
		{ID: "batman-001", SeriesID: "batman", Filename: "Batman 001.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertIssueEnrichment(store.IssueEnrichment{
		IssueID: "batman-001", VineID: 9, Title: "Knife Trick",
		Summary: "Gotham is mine.", CoverDate: "2011-11-30", EnrichedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncIncludesEnrichment(t *testing.T) {
	srv := newTestServer(t)
	seedEnrichmentSeries(t, srv)
	applyTestEnrichment(t, srv)

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Series []struct {
			ID               string          `json:"id"`
			Publisher        string          `json:"publisher"`
			Year             int             `json:"year"`
			Description      string          `json:"description"`
			StartYear        int             `json:"start_year"`
			Writer           string          `json:"writer"`
			Penciller        string          `json:"penciller"`
			EnrichmentStatus string          `json:"enrichment_status"`
			Creators         []store.Creator `json:"creators"`
			Issues           []struct {
				ID        string `json:"id"`
				Title     string `json:"title"`
				Summary   string `json:"summary"`
				CoverDate string `json:"cover_date"`
			} `json:"issues"`
		} `json:"series"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(resp.Series))
	}
	s := resp.Series[0]
	if s.Publisher != "DC Comics" || s.Description != "The Dark Knight." ||
		s.StartYear != 2011 || s.Year != 2011 || s.EnrichmentStatus != "auto" {
		t.Fatalf("enrichment not merged: %+v", s)
	}
	if s.Writer != "Scott Snyder" || s.Penciller != "Greg Capullo" {
		t.Fatalf("derived writer/penciller wrong: writer=%q penciller=%q", s.Writer, s.Penciller)
	}
	if len(s.Creators) != 2 {
		t.Fatalf("expected 2 creators, got %+v", s.Creators)
	}
	if len(s.Issues) != 1 || s.Issues[0].Title != "Knife Trick" ||
		s.Issues[0].CoverDate != "2011-11-30" {
		t.Fatalf("issue enrichment not merged: %+v", s.Issues)
	}
}

func TestFacetsUseMergedPublisher(t *testing.T) {
	srv := newTestServer(t)
	seedEnrichmentSeries(t, srv)
	applyTestEnrichment(t, srv)

	req := httptest.NewRequest("GET", "/facets/publisher", nil)
	req.SetPathValue("facet", "publisher")
	w := httptest.NewRecorder()
	srv.HandleListFacets(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var items []struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Value != "DC Comics" {
		t.Fatalf("expected merged publisher facet, got %+v", items)
	}
}
