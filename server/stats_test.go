package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ryanhaines/comic-reader/store"
)

func seedSessionData(t *testing.T, srv *Server) {
	t.Helper()
	seedLibraryData(t, srv)

	now := time.Now().UTC()
	sessions := []store.ReadingSession{
		{
			ComicID:   pathToID("Batman/Batman 001.cbz"),
			SeriesID:  "batman",
			StartedAt: now.Add(-2 * time.Hour),
			EndedAt:   now.Add(-2*time.Hour + 15*time.Minute),
			PagesRead: 24,
			DurationS: 900,
		},
		{
			ComicID:   pathToID("Saga/Saga 001.cbz"),
			SeriesID:  "saga",
			StartedAt: now.Add(-1 * time.Hour),
			EndedAt:   now.Add(-1*time.Hour + 10*time.Minute),
			PagesRead: 30,
			DurationS: 600,
		},
		{
			ComicID:   pathToID("X-Men/X-Men 001.cbz"),
			SeriesID:  "xmen",
			StartedAt: now.Add(-30 * time.Minute),
			EndedAt:   now.Add(-30*time.Minute + 20*time.Minute),
			PagesRead: 22,
			DurationS: 1200,
		},
	}
	for _, sess := range sessions {
		if _, err := srv.Store.CreateSession(sess); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostSession(t *testing.T) {
	srv := newTestServer(t)
	body := `{
		"comic_id": "abc123",
		"series_id": "batman",
		"started_at": "2026-04-21T14:30:00Z",
		"pages_read": 24,
		"duration_s": 900
	}`
	req := httptest.NewRequest("POST", "/reading/sessions", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateSession(w, req)

	if w.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp CreateSessionResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID <= 0 {
		t.Errorf("expected positive id, got %d", resp.ID)
	}
}

func TestPostSessionRejectShort(t *testing.T) {
	srv := newTestServer(t)
	body := `{
		"comic_id": "abc123",
		"series_id": "batman",
		"started_at": "2026-04-21T14:30:00Z",
		"pages_read": 1,
		"duration_s": 3
	}`
	req := httptest.NewRequest("POST", "/reading/sessions", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateSession(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var body2 map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body2); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body2["error"] != "session too short" {
		t.Errorf("expected error 'session too short', got %q", body2["error"])
	}
}

func TestStatsSummary(t *testing.T) {
	srv := newTestServer(t)
	seedSessionData(t, srv)

	req := httptest.NewRequest("GET", "/stats/summary", nil)
	w := httptest.NewRecorder()
	srv.HandleStatsSummary(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp StatsSummaryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	// All time totals: 24+30+22 = 76 pages, 900+600+1200 = 2700s
	if resp.AllTime.PagesRead != 76 {
		t.Errorf("expected 76 all-time pages, got %d", resp.AllTime.PagesRead)
	}
	if resp.AllTime.TimeReadS != 2700 {
		t.Errorf("expected 2700 all-time seconds, got %d", resp.AllTime.TimeReadS)
	}

	// Week and month should match all-time since all sessions are recent
	if resp.ThisWeek.PagesRead != 76 {
		t.Errorf("expected 76 week pages, got %d", resp.ThisWeek.PagesRead)
	}
	if resp.ThisMonth.PagesRead != 76 {
		t.Errorf("expected 76 month pages, got %d", resp.ThisMonth.PagesRead)
	}

	// pages_per_day should be populated (365 days)
	if len(resp.PagesPerDay) != 365 {
		t.Errorf("expected 365 days in pages_per_day, got %d", len(resp.PagesPerDay))
	}

	// top_genres should have entries (from seeded series genres)
	if len(resp.TopGenres) == 0 {
		t.Errorf("expected top_genres to be non-empty")
	}

	// top_publishers should have entries
	if len(resp.TopPublishers) == 0 {
		t.Errorf("expected top_publishers to be non-empty")
	}

	// No finished issues or series
	if resp.AllTime.IssuesFinished != 0 {
		t.Errorf("expected 0 issues finished, got %d", resp.AllTime.IssuesFinished)
	}
	if resp.AllTime.SeriesCompleted != 0 {
		t.Errorf("expected 0 series completed, got %d", resp.AllTime.SeriesCompleted)
	}
}

func TestStatsHistory(t *testing.T) {
	srv := newTestServer(t)
	seedSessionData(t, srv)

	req := httptest.NewRequest("GET", "/stats/history?page=1&limit=2", nil)
	w := httptest.NewRecorder()
	srv.HandleStatsHistory(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp StatsHistoryResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Total != 3 {
		t.Errorf("expected total=3, got %d", resp.Total)
	}
	if resp.Page != 1 {
		t.Errorf("expected page=1, got %d", resp.Page)
	}
	if resp.Limit != 2 {
		t.Errorf("expected limit=2, got %d", resp.Limit)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(resp.Sessions))
	}
}

func TestStatsHistoryEnrichesSeries(t *testing.T) {
	srv := newTestServer(t)
	seedSessionData(t, srv)

	req := httptest.NewRequest("GET", "/stats/history?page=1&limit=10", nil)
	w := httptest.NewRecorder()
	srv.HandleStatsHistory(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp StatsHistoryResponse
	json.NewDecoder(w.Body).Decode(&resp)

	for _, entry := range resp.Sessions {
		if entry.SeriesName == "" {
			t.Errorf("session %d: expected series_name to be populated, got empty", entry.ID)
		}
		if entry.CoverURL == "" {
			t.Errorf("session %d: expected cover_url to be populated, got empty", entry.ID)
		}
		if entry.ComicName == "" {
			t.Errorf("session %d: expected comic_name to be populated, got empty", entry.ID)
		}
	}
}

func TestComputeStreaks(t *testing.T) {
	d := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad test date %q: %v", s, err)
		}
		return v
	}
	now := d("2026-05-27") // a Wednesday

	cases := []struct {
		name    string
		days    []time.Time
		current int
		longest int
	}{
		{name: "empty", days: nil, current: 0, longest: 0},
		{name: "today only", days: []time.Time{d("2026-05-27")}, current: 1, longest: 1},
		{name: "yesterday only", days: []time.Time{d("2026-05-26")}, current: 1, longest: 1},
		{name: "two days ago only", days: []time.Time{d("2026-05-25")}, current: 0, longest: 1},
		{
			name:    "five-day past run, gap, three-day current run",
			days:    []time.Time{d("2026-05-10"), d("2026-05-11"), d("2026-05-12"), d("2026-05-13"), d("2026-05-14"), d("2026-05-25"), d("2026-05-26"), d("2026-05-27")},
			current: 3,
			longest: 5,
		},
		{
			name:    "seven straight days ending today",
			days:    []time.Time{d("2026-05-21"), d("2026-05-22"), d("2026-05-23"), d("2026-05-24"), d("2026-05-25"), d("2026-05-26"), d("2026-05-27")},
			current: 7,
			longest: 7,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeStreaks(tc.days, now)
			if got.Current != tc.current || got.Longest != tc.longest {
				t.Errorf("got Current=%d Longest=%d, want Current=%d Longest=%d",
					got.Current, got.Longest, tc.current, tc.longest)
			}
		})
	}
}

func TestStatsSummaryEmpty(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest("GET", "/stats/summary", nil)
	w := httptest.NewRecorder()
	srv.HandleStatsSummary(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp StatsSummaryResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.AllTime.PagesRead != 0 {
		t.Errorf("expected 0 pages, got %d", resp.AllTime.PagesRead)
	}
	if resp.AllTime.TimeReadS != 0 {
		t.Errorf("expected 0 seconds, got %d", resp.AllTime.TimeReadS)
	}
	if resp.AllTime.IssuesFinished != 0 {
		t.Errorf("expected 0 issues finished, got %d", resp.AllTime.IssuesFinished)
	}
	if resp.AllTime.SeriesCompleted != 0 {
		t.Errorf("expected 0 series completed, got %d", resp.AllTime.SeriesCompleted)
	}
	if resp.ThisWeek.PagesRead != 0 {
		t.Errorf("expected 0 week pages, got %d", resp.ThisWeek.PagesRead)
	}
	if resp.ThisMonth.PagesRead != 0 {
		t.Errorf("expected 0 month pages, got %d", resp.ThisMonth.PagesRead)
	}
	if len(resp.PagesPerDay) != 365 {
		t.Errorf("expected 365 days in pages_per_day, got %d", len(resp.PagesPerDay))
	}
	if len(resp.TopGenres) != 0 {
		t.Errorf("expected 0 top genres, got %d", len(resp.TopGenres))
	}
	if len(resp.TopPublishers) != 0 {
		t.Errorf("expected 0 top publishers, got %d", len(resp.TopPublishers))
	}
	if len(resp.HourHistogram) != 24 {
		t.Errorf("expected 24 hours in hour_histogram, got %d", len(resp.HourHistogram))
	}
}

func TestHandleStatsSummary_NewFields(t *testing.T) {
	srv := newTestServer(t)
	seedSessionData(t, srv)

	req := httptest.NewRequest("GET", "/stats/summary", nil)
	w := httptest.NewRecorder()
	srv.HandleStatsSummary(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp StatsSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// pages_per_day now extends to 365 entries.
	if len(resp.PagesPerDay) != 365 {
		t.Errorf("pages_per_day: len=%d want 365", len(resp.PagesPerDay))
	}

	// New fields present and populated by the seeded data.
	if len(resp.TopSeries) == 0 {
		t.Errorf("top_series should be populated")
	}
	if len(resp.TopSeries) > 0 && resp.TopSeries[0].CoverURL == "" {
		t.Errorf("top_series[0] CoverURL should be set, got empty")
	}
	if resp.AvgSessionS <= 0 {
		t.Errorf("avg_session_s should be > 0, got %d", resp.AvgSessionS)
	}
	if resp.CompletionRate.Started < 1 {
		t.Errorf("completion_rate.started should be >= 1, got %d", resp.CompletionRate.Started)
	}
	if resp.CompletionRate.RatePercent < 0 || resp.CompletionRate.RatePercent > 100 {
		t.Errorf("rate_percent out of range: %d", resp.CompletionRate.RatePercent)
	}
	if resp.Streak.Current < 1 {
		// seedSessionData puts all 3 sessions in the past few hours (same day), so streak should be 1.
		t.Errorf("streak.current should be >= 1, got %d", resp.Streak.Current)
	}
	if len(resp.HourHistogram) != 24 {
		t.Errorf("hour_histogram: len=%d want 24", len(resp.HourHistogram))
	}
}
