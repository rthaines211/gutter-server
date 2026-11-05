package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/cache"
	"github.com/rthaines211/gutter-server/store"
)

func TestHandleLibraryRecap_HappyPath(t *testing.T) {
	srv := newRecapTestServer(t)

	now := time.Now().UTC().Truncate(time.Second)

	srv.Store.UpsertSeries(store.Series{ID: "s1", Name: "Test Series", FolderPath: "/x/s1", IssueCount: 5})
	srv.Store.ReplaceIssues("s1", []store.Issue{
		{ID: "c1", SeriesID: "s1", Filename: "1.cbz", Number: "1", Position: 0},
		{ID: "c2", SeriesID: "s1", Filename: "2.cbz", Number: "2", Position: 1},
		{ID: "c3", SeriesID: "s1", Filename: "3.cbz", Number: "3", Position: 2},
		{ID: "c4", SeriesID: "s1", Filename: "4.cbz", Number: "4", Position: 3},
		{ID: "c5", SeriesID: "s1", Filename: "5.cbz", Number: "5", Position: 4},
	})

	// Seed reading_progress with custom updated_at timestamps (needed for ordering).
	// UpsertProgress always uses CURRENT_TIMESTAMP, so we go through DB() directly.
	db := srv.Store.DB()
	recapSeedProgress(t, db, "c1", 22, 22, now.Add(-4*24*time.Hour))
	recapSeedProgress(t, db, "c2", 22, 22, now.Add(-3*24*time.Hour))
	recapSeedProgress(t, db, "c3", 22, 22, now.Add(-2*24*time.Hour))
	recapSeedProgress(t, db, "c4", 22, 22, now.Add(-1*24*time.Hour))
	// c5 is in progress (page 5 of 22), not finished — should NOT appear in recent
	recapSeedProgress(t, db, "c5", 5, 22, now)

	srv.Store.CreateSession(store.ReadingSession{
		ComicID:   "c1",
		SeriesID:  "s1",
		StartedAt: now.Add(-5 * 24 * time.Hour),
		EndedAt:   now.Add(-5 * 24 * time.Hour),
		PagesRead: 22,
		DurationS: 420,
	})

	req := httptest.NewRequest("GET", "/library/s1/recap", nil)
	req.SetPathValue("id", "s1")
	w := httptest.NewRecorder()
	srv.HandleLibraryRecap(w, req)

	if w.Code != 200 {
		t.Fatalf("status: want 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp RecapResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Series.ID != "s1" {
		t.Errorf("series.id: want s1, got %s", resp.Series.ID)
	}
	if resp.Series.Name != "Test Series" {
		t.Errorf("series.name: want Test Series, got %s", resp.Series.Name)
	}
	if len(resp.RecentIssues) != 3 {
		t.Fatalf("recent_issues: want 3 (capped), got %d", len(resp.RecentIssues))
	}
	// Most-recently-finished first: c4 > c3 > c2 (c1 was oldest finished)
	wantOrder := []string{"c4", "c3", "c2"}
	for i, want := range wantOrder {
		if resp.RecentIssues[i].ID != want {
			t.Errorf("recent_issues[%d].id: want %s, got %s", i, want, resp.RecentIssues[i].ID)
		}
		if !strings.HasPrefix(resp.RecentIssues[i].CoverURL, "/comics/") {
			t.Errorf("cover_url: want /comics/... prefix, got %s", resp.RecentIssues[i].CoverURL)
		}
	}
	if resp.History.IssuesFinished != 4 {
		t.Errorf("issues_finished: want 4, got %d", resp.History.IssuesFinished)
	}
	if resp.History.IssuesTotal != 5 {
		t.Errorf("issues_total: want 5, got %d", resp.History.IssuesTotal)
	}
	if resp.History.TotalMinutesRead != 7 {
		t.Errorf("total_minutes_read: want 7, got %d", resp.History.TotalMinutesRead)
	}
	if resp.History.LastOpenedAt == nil {
		t.Errorf("last_opened_at: want non-nil, got nil")
	}
}

func TestHandleLibraryRecap_NotFound(t *testing.T) {
	srv := newRecapTestServer(t)
	req := httptest.NewRequest("GET", "/library/nonexistent/recap", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	srv.HandleLibraryRecap(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: want 404, got %d", w.Code)
	}
}

func TestHandleLibraryRecap_NoFinishedIssues(t *testing.T) {
	srv := newRecapTestServer(t)
	srv.Store.UpsertSeries(store.Series{ID: "s1", Name: "Test", FolderPath: "/x/s1", IssueCount: 1})
	srv.Store.ReplaceIssues("s1", []store.Issue{
		{ID: "c1", SeriesID: "s1", Filename: "1.cbz", Number: "1", Position: 0},
	})

	req := httptest.NewRequest("GET", "/library/s1/recap", nil)
	req.SetPathValue("id", "s1")
	w := httptest.NewRecorder()
	srv.HandleLibraryRecap(w, req)
	if w.Code != 200 {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var resp RecapResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.RecentIssues) != 0 {
		t.Errorf("expected 0 recent_issues, got %d", len(resp.RecentIssues))
	}
	if resp.History.LastOpenedAt != nil {
		t.Errorf("last_opened_at should be nil")
	}
	if resp.History.IssuesTotal != 1 {
		t.Errorf("issues_total: want 1, got %d", resp.History.IssuesTotal)
	}
}

// newRecapTestServer constructs a *Server with an in-memory store.
// Cache is included so readComicInfo doesn't panic on a nil cache —
// it will simply fail on missing archive files and emit empty summaries.
func newRecapTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return &Server{ComicDirs: []string{t.TempDir()}, Store: s, Cache: cache.NewLRU(10)}
}

// recapSeedProgress inserts a reading_progress row with a specific updated_at.
// UpsertProgress always uses CURRENT_TIMESTAMP, so ordering tests must seed
// directly through the underlying *sql.DB.
func recapSeedProgress(t *testing.T, db *sql.DB, comicID string, currentPage, totalPages int, updatedAt time.Time) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at) VALUES (?, ?, ?, ?)`,
		comicID, currentPage, totalPages, updatedAt.Format("2006-01-02T15:04:05Z"),
	)
	if err != nil {
		t.Fatalf("recapSeedProgress(%s): %v", comicID, err)
	}
}
