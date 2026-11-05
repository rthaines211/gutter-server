package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

// TestSyncScaleWithManyProgressRows times a full /sync pull (since=0, what
// every fresh app install does) against a large reading-progress table, to
// catch a regression back to the per-row chapter/genre/arc-issue lookups
// that batching replaced. It isn't a correctness test (see
// TestSyncChaptersBatchedMatchesPerRow for that) — it's a budget: a full
// sync over this many rows should complete well under a second on batched
// queries. See .superpowers/sync-batch-report.md for the actual
// before/after numbers measured against the old per-row code.
func TestSyncScaleWithManyProgressRows(t *testing.T) {
	srv := newTestServer(t)

	const n = 3000
	items := make([]store.Progress, n)
	for i := 0; i < n; i++ {
		items[i] = store.Progress{ComicID: fmt.Sprintf("comic-%04d", i), CurrentPage: 3, TotalPages: 20}
	}
	if err := srv.Store.BatchUpsertProgress(items); err != nil {
		t.Fatal(err)
	}
	// A third of comics carry a chapter, to exercise the chapters join at scale.
	for i := 0; i < n; i += 3 {
		if err := srv.Store.AddChapter(store.Chapter{ComicID: fmt.Sprintf("comic-%04d", i), PageNum: 0, Title: "Ch 1"}); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()

	start := time.Now()
	srv.HandleSync(w, req)
	elapsed := time.Since(start)
	t.Logf("GET /sync?since=0 with %d progress rows took %s", n, elapsed)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SyncResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Progress) != n {
		t.Fatalf("expected %d progress rows, got %d", n, len(resp.Progress))
	}
	if elapsed > 2*time.Second {
		t.Errorf("sync took %s for %d progress rows, expected well under 2s with batched queries", elapsed, n)
	}
}

// TestSyncScaleWithManySeriesAndIssues times a full /sync pull against a
// large series/issue table, to catch a regression back to buildIssueList's
// old per-issue GetProgress call — the biggest query count in a full sync
// (series x issues), fixed by batching it per series via
// GetProgressForComics. See .superpowers/sync-batch-report.md for the
// actual before/after numbers measured against the old per-row code.
func TestSyncScaleWithManySeriesAndIssues(t *testing.T) {
	srv := newTestServer(t)

	const numSeries = 500
	const issuesPerSeries = 10
	progressItems := make([]store.Progress, 0, numSeries*issuesPerSeries)
	for si := 0; si < numSeries; si++ {
		seriesID := fmt.Sprintf("series-%04d", si)
		if err := srv.Store.UpsertSeries(store.Series{
			ID: seriesID, Name: seriesID, FolderPath: "/library/" + seriesID, IssueCount: issuesPerSeries,
		}); err != nil {
			t.Fatal(err)
		}
		issues := make([]store.Issue, issuesPerSeries)
		for ii := 0; ii < issuesPerSeries; ii++ {
			issueID := fmt.Sprintf("%s-issue-%02d", seriesID, ii)
			issues[ii] = store.Issue{ID: issueID, SeriesID: seriesID, Filename: issueID + ".cbz", Position: ii}
			// Every other issue has a progress row, to exercise the
			// batched-vs-missing map lookup inside buildIssueList.
			if ii%2 == 0 {
				progressItems = append(progressItems, store.Progress{ComicID: issueID, CurrentPage: 3, TotalPages: 20})
			}
		}
		if err := srv.Store.ReplaceIssues(seriesID, issues); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.Store.BatchUpsertProgress(progressItems); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/sync?since=0", nil)
	w := httptest.NewRecorder()

	start := time.Now()
	srv.HandleSync(w, req)
	elapsed := time.Since(start)
	t.Logf("GET /sync?since=0 with %d series x %d issues took %s", numSeries, issuesPerSeries, elapsed)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SyncResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Series) != numSeries {
		t.Fatalf("expected %d series, got %d", numSeries, len(resp.Series))
	}
	for _, s := range resp.Series {
		if len(s.Issues) != issuesPerSeries {
			t.Fatalf("series %s: expected %d issues, got %d", s.ID, issuesPerSeries, len(s.Issues))
		}
	}
	if elapsed > 5*time.Second {
		t.Errorf("sync took %s for %d series x %d issues, expected well under 5s with batched queries", elapsed, numSeries, issuesPerSeries)
	}
}
