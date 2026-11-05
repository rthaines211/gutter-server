package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/store"
)

func TestSyncFullPull(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp SyncResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.ServerTime == 0 {
		t.Error("server_time missing")
	}
	if len(resp.Series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(resp.Series))
	}
	if len(resp.SeriesIDs) != 2 {
		t.Errorf("expected 2 series_ids, got %d", len(resp.SeriesIDs))
	}
	for _, s := range resp.Series {
		if len(s.Issues) == 0 {
			t.Errorf("series %s has no inline issues", s.ID)
		}
		if s.UpdatedAt == "" {
			t.Errorf("series %s missing updated_at", s.ID)
		}
	}
}

func TestSyncDelta(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	// A `since` far in the future returns no changes but still full ID lists.
	req := httptest.NewRequest("GET", fmt.Sprintf("/sync?since=%d", (int64(1)<<40)*1000), nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Series) != 0 {
		t.Errorf("future since: expected 0 series, got %d", len(resp.Series))
	}
	if len(resp.SeriesIDs) != 2 {
		t.Errorf("future since: expected series_ids anyway, got %d", len(resp.SeriesIDs))
	}
}

// TestSyncStripsHTMLFromDescription verifies the /sync endpoint (the mobile
// client's primary sync path) strips HTML from series_enrichment.description,
// same as the /library endpoints. The offline-dump path stores this raw.
func TestSyncStripsHTMLFromDescription(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.Store.UpsertSeries(store.Series{
		ID: "html-series", Name: "HTML Series", FolderPath: "/html-series", IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertSeriesEnrichment(store.SeriesEnrichment{
		SeriesID:    "html-series",
		Description: "<p><em>Hi</em> &amp; bye</p>",
		Status:      "auto",
		EnrichedAt:  1,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp SyncResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(resp.Series))
	}
	desc := resp.Series[0].Description
	if strings.ContainsAny(desc, "<>") {
		t.Errorf("expected synced description to have HTML tags stripped, got %q", desc)
	}
	if desc != "Hi & bye" {
		t.Errorf("expected synced description %q, got %q", "Hi & bye", desc)
	}
}

func TestSyncIncludesProgress(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	id := pathToID("Batman (2016)/Batman 001.cbz")
	body := `{"current_page": 5, "total_pages": 20}`
	preq := httptest.NewRequest("PUT", "/reading/"+id, bytes.NewBufferString(body))
	preq.SetPathValue("id", id)
	pw := httptest.NewRecorder()
	srv.HandlePutReading(pw, preq)
	if pw.Code != 200 {
		t.Fatalf("PUT progress failed: %d", pw.Code)
	}

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Progress) != 1 || resp.Progress[0].ComicID != id {
		t.Fatalf("expected progress for %s, got %v", id, resp.Progress)
	}
	if len(resp.ProgressIDs) != 1 || resp.ProgressIDs[0] != id {
		t.Errorf("expected progress_ids [%s], got %v", id, resp.ProgressIDs)
	}
}

func TestSyncIncludesQueues(t *testing.T) {
	srv, _ := setupLibraryTest(t)
	if err := srv.Store.UpsertQueue(store.Queue{ID: "q1", Name: "Weekend"}, []string{"x"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SyncResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Queues) != 1 || resp.Queues[0].ID != "q1" {
		t.Fatalf("expected queue q1 in sync, got %+v", resp.Queues)
	}
	if len(resp.Queues[0].SeriesIDs) != 1 || resp.Queues[0].SeriesIDs[0] != "x" {
		t.Errorf("expected queue membership [x], got %v", resp.Queues[0].SeriesIDs)
	}
	if len(resp.QueueIDs) != 1 || resp.QueueIDs[0] != "q1" {
		t.Errorf("expected queue_ids [q1], got %v", resp.QueueIDs)
	}
}

func TestSyncIncludesArcs(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	issueID := pathToID("Batman (2016)/Batman 001.cbz")
	if err := srv.Store.UpsertIssueEnrichment(store.IssueEnrichment{IssueID: issueID, VineID: 111}); err != nil {
		t.Fatal(err)
	}

	yes := true
	if err := srv.Store.ImportArcs([]store.ArcInput{{
		Key: "arc1", Name: "Arc One", TotalIssues: 1, SelfContained: &yes,
		Issues: []store.ArcIssueInput{{CVIssueID: "111", Order: 1}},
	}}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)

	var resp SyncResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Arcs) != 1 || resp.Arcs[0].ID != "arc1" ||
		len(resp.Arcs[0].IssueIDs) != 1 || resp.Arcs[0].IssueIDs[0] != issueID {
		t.Fatalf("arc not in sync: %+v", resp.Arcs)
	}
	if len(resp.ArcIDs) != 1 || resp.ArcIDs[0] != "arc1" {
		t.Fatalf("arc_ids wrong: %v", resp.ArcIDs)
	}
}

func TestSyncArcFields(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	issueID := pathToID("Batman (2016)/Batman 001.cbz")
	if err := srv.Store.UpsertIssueEnrichment(store.IssueEnrichment{IssueID: issueID, VineID: 111}); err != nil {
		t.Fatal(err)
	}

	yes := true
	if err := srv.Store.ImportArcs([]store.ArcInput{{
		Key: "arc1", Name: "Arc One", TotalIssues: 1, SelfContained: &yes,
		Issues: []store.ArcIssueInput{{CVIssueID: "111", Order: 1}},
	}}); err != nil {
		t.Fatal(err)
	}

	// Update the arc with specific field values
	if _, err := srv.Store.DB().Exec(
		`UPDATE story_arcs SET source = 'comicvine', publisher = 'DC Comics', start_year = 2019, end_year = 2020 WHERE id = 'arc1'`); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.SetArcDescription("arc1", "A ComicVine description.", 1700000000000, 1); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)

	var resp SyncResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Arcs) != 1 {
		t.Fatalf("expected 1 arc, got %d", len(resp.Arcs))
	}
	arc := resp.Arcs[0]

	if arc.Source != "comicvine" {
		t.Errorf("expected source 'comicvine', got %q", arc.Source)
	}
	if arc.Publisher != "DC Comics" {
		t.Errorf("expected publisher 'DC Comics', got %q", arc.Publisher)
	}
	if arc.StartYear != 2019 {
		t.Errorf("expected start_year 2019, got %d", arc.StartYear)
	}
	if arc.EndYear != 2020 {
		t.Errorf("expected end_year 2020, got %d", arc.EndYear)
	}
	if arc.Description != "A ComicVine description." {
		t.Errorf("expected description, got %q", arc.Description)
	}
}

// TestSyncChaptersBatchedMatchesPerRow pins the batched /sync chapter lookup
// (ListChaptersForComics) against the still-present per-row function
// (ListChapters) it replaced in the handler: for every progress row in the
// response, the batched result must equal what asking per-comic would give,
// in the same order, including comics with no chapters at all (where the
// field must be omitted, same as before).
func TestSyncChaptersBatchedMatchesPerRow(t *testing.T) {
	srv := newTestServer(t)

	comics := []string{"c1", "c2", "c3"}
	for _, id := range comics {
		if err := srv.Store.UpsertProgress(store.Progress{ComicID: id, CurrentPage: 3, TotalPages: 10}); err != nil {
			t.Fatal(err)
		}
	}
	// c1: two chapters (page_num order matters). c2: one chapter. c3: none.
	if err := srv.Store.AddChapter(store.Chapter{ComicID: "c1", PageNum: 10, Title: "Chapter Two"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.AddChapter(store.Chapter{ComicID: "c1", PageNum: 0, Title: "Chapter One"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.AddChapter(store.Chapter{ComicID: "c2", PageNum: 0, Title: "Only Chapter"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	raw := w.Body.Bytes()
	var resp SyncResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Progress) != len(comics) {
		t.Fatalf("expected %d progress rows, got %d", len(comics), len(resp.Progress))
	}

	byComic := make(map[string]ReadingProgressResponse, len(resp.Progress))
	for _, p := range resp.Progress {
		byComic[p.ComicID] = p
	}

	for _, id := range comics {
		want, err := srv.Store.ListChapters(id) // old per-row path, kept as ground truth
		if err != nil {
			t.Fatal(err)
		}
		got := byComic[id].Chapters
		if len(want) != len(got) {
			t.Fatalf("comic %s: want %d chapters, got %d (%+v vs %+v)", id, len(want), len(got), want, got)
		}
		for i := range want {
			if want[i].PageNum != got[i].PageNum || want[i].Title != got[i].Title {
				t.Errorf("comic %s chapter %d: want %+v, got %+v", id, i, want[i], got[i])
			}
		}
	}

	// c3 has no chapters: the batched path must omit the field exactly like
	// the old per-row path did (nil slice + omitempty), not substitute [].
	var rawResp struct {
		Progress []json.RawMessage `json:"progress"`
	}
	if err := json.Unmarshal(raw, &rawResp); err != nil {
		t.Fatal(err)
	}
	foundC3 := false
	for _, rp := range rawResp.Progress {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(rp, &m); err != nil {
			t.Fatal(err)
		}
		var comicID string
		json.Unmarshal(m["comic_id"], &comicID)
		if comicID == "c3" {
			foundC3 = true
			if _, ok := m["chapters"]; ok {
				t.Errorf(`comic c3: expected "chapters" key omitted, got %s`, rp)
			}
		}
	}
	if !foundC3 {
		t.Fatal("c3 progress row not found in response")
	}
}

// TestSyncGenresBatchedMatchesPerRow pins the batched series-genre lookup
// (ListGenresForSeries) against the per-row ListSeriesGenres it replaced.
func TestSyncGenresBatchedMatchesPerRow(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv) // batman, saga, xmen with distinct genre sets

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	var resp SyncResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]SyncSeriesResponse, len(resp.Series))
	for _, s := range resp.Series {
		byID[s.ID] = s
	}
	for _, id := range []string{"batman", "saga", "xmen"} {
		want, err := srv.Store.ListSeriesGenres(id)
		if err != nil {
			t.Fatal(err)
		}
		got := byID[id].Genres
		if len(want) != len(got) {
			t.Fatalf("series %s: want genres %v, got %v", id, want, got)
		}
		for i := range want {
			if want[i] != got[i] {
				t.Errorf("series %s genre %d: want %q, got %q", id, i, want[i], got[i])
			}
		}
	}
}

// TestSyncArcIssueIDsBatchedMatchesPerRow pins the batched arc-issue lookup
// (ListIssueIDsForArcs) against the per-row ListArcIssueIDs it replaced,
// across multiple arcs including one with no issues.
func TestSyncArcIssueIDsBatchedMatchesPerRow(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	issueID := pathToID("Batman (2016)/Batman 001.cbz")
	if err := srv.Store.UpsertIssueEnrichment(store.IssueEnrichment{IssueID: issueID, VineID: 111}); err != nil {
		t.Fatal(err)
	}
	issueID2 := pathToID("Batman (2016)/Batman 002.cbz")
	if err := srv.Store.UpsertIssueEnrichment(store.IssueEnrichment{IssueID: issueID2, VineID: 222}); err != nil {
		t.Fatal(err)
	}

	if err := srv.Store.ImportArcs([]store.ArcInput{
		{
			Key: "arc1", Name: "Arc One", TotalIssues: 2,
			Issues: []store.ArcIssueInput{
				{CVIssueID: "222", Order: 1},
				{CVIssueID: "111", Order: 2},
			},
		},
		{Key: "arc2", Name: "Arc Two (empty)", TotalIssues: 0},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	var resp SyncResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]ArcResponse, len(resp.Arcs))
	for _, a := range resp.Arcs {
		byID[a.ID] = a
	}
	for _, id := range []string{"arc1", "arc2"} {
		want, err := srv.Store.ListArcIssueIDs(id)
		if err != nil {
			t.Fatal(err)
		}
		got := byID[id].IssueIDs
		if len(want) != len(got) {
			t.Fatalf("arc %s: want issue_ids %v, got %v", id, want, got)
		}
		for i := range want {
			if want[i] != got[i] {
				t.Errorf("arc %s issue %d: want %q, got %q", id, i, want[i], got[i])
			}
		}
	}
}

// TestSyncIssueProgressBatchedMatchesPerRow pins buildIssueList's batched
// progress lookup (GetProgressForComics), as used by GET /sync's inline
// issue lists, against the per-issue GetProgress it replaced — same check
// as TestHandleGetSeriesProgressBatchedMatchesPerRow, but through /sync.
func TestSyncIssueProgressBatchedMatchesPerRow(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	batman1 := pathToID("Batman (2016)/Batman 001.cbz")
	xmen1 := pathToID("X-Men/X-Men 001.cbz")
	if err := srv.Store.UpsertProgress(store.Progress{ComicID: batman1, CurrentPage: 20, TotalPages: 20}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertProgress(store.Progress{ComicID: xmen1, CurrentPage: 1, TotalPages: 20}); err != nil {
		t.Fatal(err)
	}
	// Batman 002.cbz is left with no progress row at all.

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()
	srv.HandleSync(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp SyncResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, series := range resp.Series {
		for _, iss := range series.Issues {
			checked++
			want, err := srv.Store.GetProgress(iss.ID) // old per-row path, kept as ground truth
			wantStatus := "unread"
			var wantCurrent, wantTotal *int
			if err == nil {
				wantStatus = "in_progress"
				if want.CurrentPage >= want.TotalPages {
					wantStatus = "completed"
				}
				cp, tp := want.CurrentPage, want.TotalPages
				wantCurrent, wantTotal = &cp, &tp
			}
			if iss.Status != wantStatus {
				t.Errorf("issue %s: want status %q, got %q", iss.ID, wantStatus, iss.Status)
			}
			if (wantCurrent == nil) != (iss.CurrentPage == nil) {
				t.Errorf("issue %s: want current_page nil=%v, got nil=%v", iss.ID, wantCurrent == nil, iss.CurrentPage == nil)
			} else if wantCurrent != nil && *wantCurrent != *iss.CurrentPage {
				t.Errorf("issue %s: want current_page %d, got %d", iss.ID, *wantCurrent, *iss.CurrentPage)
			}
			if (wantTotal == nil) != (iss.TotalPages == nil) {
				t.Errorf("issue %s: want total_pages nil=%v, got nil=%v", iss.ID, wantTotal == nil, iss.TotalPages == nil)
			} else if wantTotal != nil && *wantTotal != *iss.TotalPages {
				t.Errorf("issue %s: want total_pages %d, got %d", iss.ID, *wantTotal, *iss.TotalPages)
			}
		}
	}
	if checked != 3 {
		t.Fatalf("expected to check 3 issues across series, checked %d", checked)
	}
}
