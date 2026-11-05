package store

import (
	"testing"
	"time"
)

func TestCreateAndListSessions(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().UTC()
	sess1 := ReadingSession{
		ComicID:   "comic-1",
		SeriesID:  "series-1",
		StartedAt: now.Add(-2 * time.Hour),
		EndedAt:   now.Add(-1 * time.Hour),
		PagesRead: 20,
		DurationS: 3600,
	}
	sess2 := ReadingSession{
		ComicID:   "comic-2",
		SeriesID:  "series-1",
		StartedAt: now.Add(-30 * time.Minute),
		EndedAt:   now,
		PagesRead: 10,
		DurationS: 1800,
	}

	id1, err := s.CreateSession(sess1)
	if err != nil {
		t.Fatalf("CreateSession 1 failed: %v", err)
	}
	if id1 <= 0 {
		t.Errorf("expected positive id, got %d", id1)
	}

	id2, err := s.CreateSession(sess2)
	if err != nil {
		t.Fatalf("CreateSession 2 failed: %v", err)
	}
	if id2 <= id1 {
		t.Errorf("expected id2 > id1, got %d <= %d", id2, id1)
	}

	sessions, total, err := s.ListSessions(1, 10)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if total != 2 {
		t.Errorf("expected total 2, got %d", total)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	// Most recent first
	if sessions[0].ComicID != "comic-2" {
		t.Errorf("expected most recent session first (comic-2), got %s", sessions[0].ComicID)
	}
	if sessions[1].ComicID != "comic-1" {
		t.Errorf("expected second session (comic-1), got %s", sessions[1].ComicID)
	}
}

func TestListSessionsEmpty(t *testing.T) {
	s := newTestStore(t)

	sessions, total, err := s.ListSessions(1, 10)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if total != 0 {
		t.Errorf("expected total 0, got %d", total)
	}
	if len(sessions) != 0 {
		t.Errorf("expected empty list, got %d", len(sessions))
	}
}

func TestSessionStats(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().UTC()

	// Session from 3 days ago (within week and month)
	s.CreateSession(ReadingSession{
		ComicID:   "comic-1",
		SeriesID:  "series-1",
		StartedAt: now.Add(-3 * 24 * time.Hour),
		EndedAt:   now.Add(-3*24*time.Hour + time.Hour),
		PagesRead: 10,
		DurationS: 600,
	})

	// Session from 15 days ago (within month, not week)
	s.CreateSession(ReadingSession{
		ComicID:   "comic-2",
		SeriesID:  "series-1",
		StartedAt: now.Add(-15 * 24 * time.Hour),
		EndedAt:   now.Add(-15*24*time.Hour + time.Hour),
		PagesRead: 20,
		DurationS: 1200,
	})

	// Session from 60 days ago (all time only)
	s.CreateSession(ReadingSession{
		ComicID:   "comic-3",
		SeriesID:  "series-1",
		StartedAt: now.Add(-60 * 24 * time.Hour),
		EndedAt:   now.Add(-60*24*time.Hour + time.Hour),
		PagesRead: 30,
		DurationS: 1800,
	})

	stats, err := s.SessionStats()
	if err != nil {
		t.Fatalf("SessionStats failed: %v", err)
	}

	if stats.AllTimePagesRead != 60 {
		t.Errorf("AllTimePagesRead: expected 60, got %d", stats.AllTimePagesRead)
	}
	if stats.AllTimeTimeS != 3600 {
		t.Errorf("AllTimeTimeS: expected 3600, got %d", stats.AllTimeTimeS)
	}
	if stats.WeekPagesRead != 10 {
		t.Errorf("WeekPagesRead: expected 10, got %d", stats.WeekPagesRead)
	}
	if stats.WeekTimeS != 600 {
		t.Errorf("WeekTimeS: expected 600, got %d", stats.WeekTimeS)
	}
	if stats.MonthPagesRead != 30 {
		t.Errorf("MonthPagesRead: expected 30, got %d", stats.MonthPagesRead)
	}
	if stats.MonthTimeS != 1800 {
		t.Errorf("MonthTimeS: expected 1800, got %d", stats.MonthTimeS)
	}
}

func TestPagesPerDay(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().UTC()
	s.CreateSession(ReadingSession{
		ComicID:   "comic-1",
		SeriesID:  "series-1",
		StartedAt: now,
		EndedAt:   now.Add(time.Hour),
		PagesRead: 15,
		DurationS: 3600,
	})

	days, err := s.PagesPerDay(30)
	if err != nil {
		t.Fatalf("PagesPerDay failed: %v", err)
	}

	if len(days) != 30 {
		t.Fatalf("expected 30 days, got %d", len(days))
	}

	// Last entry should be today with 15 pages
	today := now.Format("2006-01-02")
	last := days[len(days)-1]
	if last.Date != today {
		t.Errorf("expected last date %s, got %s", today, last.Date)
	}
	if last.Pages != 15 {
		t.Errorf("expected 15 pages today, got %d", last.Pages)
	}

	// Verify zero-fill: first entry should have 0 pages (29 days ago)
	first := days[0]
	if first.Pages != 0 {
		t.Errorf("expected 0 pages for oldest day, got %d", first.Pages)
	}
}

func TestTopSessionGenres(t *testing.T) {
	s := newTestStore(t)

	// Create series with genres
	s.UpsertSeries(Series{ID: "s1", Name: "Batman", FolderPath: "/batman", IssueCount: 1})
	s.UpsertSeries(Series{ID: "s2", Name: "Spider-Man", FolderPath: "/spiderman", IssueCount: 1})

	s.ReplaceSeriesGenres("s1", []string{"Action", "Superhero"})
	s.ReplaceSeriesGenres("s2", []string{"Action", "Comedy"})

	now := time.Now().UTC()

	// 2 sessions for s1, 1 for s2
	s.CreateSession(ReadingSession{ComicID: "c1", SeriesID: "s1", StartedAt: now, EndedAt: now.Add(time.Hour), PagesRead: 10, DurationS: 600})
	s.CreateSession(ReadingSession{ComicID: "c2", SeriesID: "s1", StartedAt: now, EndedAt: now.Add(time.Hour), PagesRead: 10, DurationS: 600})
	s.CreateSession(ReadingSession{ComicID: "c3", SeriesID: "s2", StartedAt: now, EndedAt: now.Add(time.Hour), PagesRead: 10, DurationS: 600})

	genres, err := s.TopSessionGenres(10)
	if err != nil {
		t.Fatalf("TopSessionGenres failed: %v", err)
	}

	if len(genres) != 3 {
		t.Fatalf("expected 3 genres, got %d", len(genres))
	}

	// Action should be first with count 3 (2 from s1 + 1 from s2)
	if genres[0].Name != "Action" {
		t.Errorf("expected top genre Action, got %s", genres[0].Name)
	}
	if genres[0].Count != 3 {
		t.Errorf("expected Action count 3, got %d", genres[0].Count)
	}

	// Superhero should have count 2
	found := false
	for _, g := range genres {
		if g.Name == "Superhero" {
			found = true
			if g.Count != 2 {
				t.Errorf("expected Superhero count 2, got %d", g.Count)
			}
		}
	}
	if !found {
		t.Error("Superhero genre not found in results")
	}
}

func TestTopSeriesByPages(t *testing.T) {
	s := newTestStore(t)

	s.UpsertSeries(Series{ID: "series-a", Name: "Series A", FolderPath: "/a", IssueCount: 1})
	s.UpsertSeries(Series{ID: "series-b", Name: "Series B", FolderPath: "/b", IssueCount: 1})
	s.UpsertSeries(Series{ID: "series-c", Name: "Series C", FolderPath: "/c", IssueCount: 1})

	now := time.Now().UTC()
	mkSess := func(comicID, seriesID string, pages, dur int) ReadingSession {
		return ReadingSession{
			ComicID:   comicID,
			SeriesID:  seriesID,
			StartedAt: now,
			EndedAt:   now.Add(time.Duration(dur) * time.Second),
			PagesRead: pages,
			DurationS: dur,
		}
	}

	// series-a: two sessions totaling 80 pages / 1000 seconds
	s.CreateSession(mkSess("c1", "series-a", 50, 600))
	s.CreateSession(mkSess("c2", "series-a", 30, 400))
	// series-b: one session of 100 pages / 1200 seconds
	s.CreateSession(mkSess("c3", "series-b", 100, 1200))
	// series-c: one session of 10 pages / 120 seconds
	s.CreateSession(mkSess("c4", "series-c", 10, 120))

	got, err := s.TopSeriesByPages(10)
	if err != nil {
		t.Fatalf("TopSeriesByPages failed: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("want 3 series, got %d: %+v", len(got), got)
	}
	if got[0].SeriesID != "series-b" || got[0].Pages != 100 || got[0].Name != "Series B" {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].SeriesID != "series-a" || got[1].Pages != 80 || got[1].TimeReadS != 1000 || got[1].Name != "Series A" {
		t.Errorf("second: %+v", got[1])
	}
	if got[2].SeriesID != "series-c" || got[2].Pages != 10 {
		t.Errorf("third: %+v", got[2])
	}
}

func TestTopSessionWriters(t *testing.T) {
	s := newTestStore(t)

	s.UpsertSeries(Series{ID: "series-a", Name: "Series A", FolderPath: "/a", IssueCount: 1, Writer: "Tom King, Scott Snyder"})
	s.UpsertSeries(Series{ID: "series-b", Name: "Series B", FolderPath: "/b", IssueCount: 1, Writer: "Tom King [12345]"})
	s.UpsertSeries(Series{ID: "series-c", Name: "Series C", FolderPath: "/c", IssueCount: 1, Writer: "Mark Waid"})

	now := time.Now().UTC()
	mkSess := func(comicID, seriesID string) ReadingSession {
		return ReadingSession{
			ComicID:   comicID,
			SeriesID:  seriesID,
			StartedAt: now,
			EndedAt:   now.Add(time.Minute),
			PagesRead: 10,
			DurationS: 60,
		}
	}

	// 2 sessions for series-a (writers: Tom King + Scott Snyder)
	s.CreateSession(mkSess("c1", "series-a"))
	s.CreateSession(mkSess("c2", "series-a"))
	// 1 session for series-b (writer: Tom King after stripping [12345])
	s.CreateSession(mkSess("c3", "series-b"))
	// 1 session for series-c (writer: Mark Waid)
	s.CreateSession(mkSess("c4", "series-c"))

	got, err := s.TopSessionWriters(10)
	if err != nil {
		t.Fatalf("TopSessionWriters failed: %v", err)
	}

	// Expected counts (sessions per writer, after splitting & stripping):
	//   "Tom King":     3 (2 from series-a, 1 from series-b)
	//   "Scott Snyder": 2 (from series-a)
	//   "Mark Waid":    1 (from series-c)
	wantTop := []NameCount{
		{Name: "Tom King", Count: 3},
		{Name: "Scott Snyder", Count: 2},
		{Name: "Mark Waid", Count: 1},
	}
	if len(got) != len(wantTop) {
		t.Fatalf("want %d writers, got %d (%+v)", len(wantTop), len(got), got)
	}
	for i, w := range wantTop {
		if got[i] != w {
			t.Errorf("position %d: got %+v want %+v", i, got[i], w)
		}
	}
}

func TestAvgSessionDuration(t *testing.T) {
	s := newTestStore(t)

	// Empty case
	avg, err := s.AvgSessionDuration()
	if err != nil {
		t.Fatalf("AvgSessionDuration empty: %v", err)
	}
	if avg != 0 {
		t.Errorf("empty avg: want 0, got %d", avg)
	}

	s.UpsertSeries(Series{ID: "s1", Name: "S1", FolderPath: "/s1", IssueCount: 1})
	now := time.Now().UTC()
	mkSess := func(comicID string, dur int) ReadingSession {
		return ReadingSession{
			ComicID:   comicID,
			SeriesID:  "s1",
			StartedAt: now,
			EndedAt:   now.Add(time.Duration(dur) * time.Second),
			PagesRead: 10,
			DurationS: dur,
		}
	}
	s.CreateSession(mkSess("c1", 60))
	s.CreateSession(mkSess("c2", 120))
	s.CreateSession(mkSess("c3", 180))

	avg, err = s.AvgSessionDuration()
	if err != nil {
		t.Fatalf("AvgSessionDuration: %v", err)
	}
	if avg != 120 {
		t.Errorf("avg: want 120, got %d", avg)
	}
}

func TestSeriesStarted(t *testing.T) {
	s := newTestStore(t)

	// Empty
	n, err := s.SeriesStarted()
	if err != nil {
		t.Fatalf("SeriesStarted empty: %v", err)
	}
	if n != 0 {
		t.Errorf("empty: want 0, got %d", n)
	}

	s.UpsertSeries(Series{ID: "s1", Name: "S1", FolderPath: "/s1", IssueCount: 1})
	s.UpsertSeries(Series{ID: "s2", Name: "S2", FolderPath: "/s2", IssueCount: 1})
	s.UpsertSeries(Series{ID: "s3", Name: "S3-untouched", FolderPath: "/s3", IssueCount: 1})

	now := time.Now().UTC()
	mkSess := func(comicID, seriesID string) ReadingSession {
		return ReadingSession{
			ComicID:   comicID,
			SeriesID:  seriesID,
			StartedAt: now,
			EndedAt:   now.Add(time.Minute),
			PagesRead: 10,
			DurationS: 60,
		}
	}

	// s1 has 2 sessions, s2 has 1, s3 has 0 — should count 2 started.
	s.CreateSession(mkSess("c1", "s1"))
	s.CreateSession(mkSess("c2", "s1"))
	s.CreateSession(mkSess("c3", "s2"))
	// Edge case: a session with empty series_id should not count.
	s.CreateSession(mkSess("c4", ""))

	n, err = s.SeriesStarted()
	if err != nil {
		t.Fatalf("SeriesStarted: %v", err)
	}
	if n != 2 {
		t.Errorf("started: want 2, got %d", n)
	}
}

func TestSessionDays(t *testing.T) {
	s := newTestStore(t)
	s.UpsertSeries(Series{ID: "s1", Name: "S1", FolderPath: "/s1", IssueCount: 1})

	// Three distinct days, with duplicate sessions on one of them.
	t1 := time.Date(2026, 5, 25, 10, 0, 0, 0, time.Local)
	t2 := time.Date(2026, 5, 26, 11, 0, 0, 0, time.Local)
	t3 := time.Date(2026, 5, 26, 22, 0, 0, 0, time.Local)
	t4 := time.Date(2026, 5, 27, 9, 0, 0, 0, time.Local)
	mk := func(comicID string, started time.Time) ReadingSession {
		return ReadingSession{
			ComicID:   comicID,
			SeriesID:  "s1",
			StartedAt: started.UTC(),
			EndedAt:   started.UTC().Add(time.Minute),
			PagesRead: 10,
			DurationS: 60,
		}
	}
	s.CreateSession(mk("c1", t1))
	s.CreateSession(mk("c2", t2))
	s.CreateSession(mk("c3", t3))
	s.CreateSession(mk("c4", t4))

	days, err := s.SessionDays()
	if err != nil {
		t.Fatalf("SessionDays: %v", err)
	}
	if len(days) != 3 {
		t.Fatalf("want 3 distinct days, got %d (%v)", len(days), days)
	}
	got := []string{
		days[0].Format("2006-01-02"),
		days[1].Format("2006-01-02"),
		days[2].Format("2006-01-02"),
	}
	want := []string{"2026-05-25", "2026-05-26", "2026-05-27"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("day %d: got %s want %s", i, got[i], want[i])
		}
	}
}

func TestHourHistogram(t *testing.T) {
	s := newTestStore(t)
	s.UpsertSeries(Series{ID: "s1", Name: "S1", FolderPath: "/s1", IssueCount: 1})

	mk := func(comicID string, h, m, pages int) ReadingSession {
		started := time.Date(2026, 5, 27, h, m, 0, 0, time.Local).UTC()
		return ReadingSession{
			ComicID:   comicID,
			SeriesID:  "s1",
			StartedAt: started,
			EndedAt:   started.Add(time.Minute),
			PagesRead: pages,
			DurationS: 60,
		}
	}
	s.CreateSession(mk("c1", 8, 30, 5))
	s.CreateSession(mk("c2", 8, 45, 3))
	s.CreateSession(mk("c3", 22, 0, 20))

	hist, err := s.HourHistogram()
	if err != nil {
		t.Fatalf("HourHistogram: %v", err)
	}
	if len(hist) != 24 {
		t.Fatalf("want 24 entries (zero-filled), got %d", len(hist))
	}
	if hist[0].Hour != 0 || hist[0].Pages != 0 {
		t.Errorf("hour 0: %+v", hist[0])
	}
	if hist[8].Hour != 8 || hist[8].Pages != 8 {
		t.Errorf("hour 8: %+v", hist[8])
	}
	if hist[22].Hour != 22 || hist[22].Pages != 20 {
		t.Errorf("hour 22: %+v", hist[22])
	}
}

func TestTopSessionPublishers(t *testing.T) {
	s := newTestStore(t)

	s.UpsertSeries(Series{ID: "s1", Name: "Batman", FolderPath: "/batman", IssueCount: 1, Publisher: "DC Comics"})
	s.UpsertSeries(Series{ID: "s2", Name: "Spider-Man", FolderPath: "/spiderman", IssueCount: 1, Publisher: "Marvel"})
	s.UpsertSeries(Series{ID: "s3", Name: "Indie", FolderPath: "/indie", IssueCount: 1, Publisher: ""})

	now := time.Now().UTC()

	// 2 sessions for DC, 1 for Marvel, 1 for empty publisher
	s.CreateSession(ReadingSession{ComicID: "c1", SeriesID: "s1", StartedAt: now, EndedAt: now.Add(time.Hour), PagesRead: 10, DurationS: 600})
	s.CreateSession(ReadingSession{ComicID: "c2", SeriesID: "s1", StartedAt: now, EndedAt: now.Add(time.Hour), PagesRead: 10, DurationS: 600})
	s.CreateSession(ReadingSession{ComicID: "c3", SeriesID: "s2", StartedAt: now, EndedAt: now.Add(time.Hour), PagesRead: 10, DurationS: 600})
	s.CreateSession(ReadingSession{ComicID: "c4", SeriesID: "s3", StartedAt: now, EndedAt: now.Add(time.Hour), PagesRead: 10, DurationS: 600})

	publishers, err := s.TopSessionPublishers(10)
	if err != nil {
		t.Fatalf("TopSessionPublishers failed: %v", err)
	}

	// s3 has empty publisher, should be excluded
	if len(publishers) != 2 {
		t.Fatalf("expected 2 publishers, got %d: %+v", len(publishers), publishers)
	}

	if publishers[0].Name != "DC Comics" {
		t.Errorf("expected top publisher DC Comics, got %s", publishers[0].Name)
	}
	if publishers[0].Count != 2 {
		t.Errorf("expected DC Comics count 2, got %d", publishers[0].Count)
	}
	if publishers[1].Name != "Marvel" {
		t.Errorf("expected second publisher Marvel, got %s", publishers[1].Name)
	}
	if publishers[1].Count != 1 {
		t.Errorf("expected Marvel count 1, got %d", publishers[1].Count)
	}
}

func TestSeriesTotalDurationS(t *testing.T) {
	s := newTestStore(t)

	now := time.Now()
	if _, err := s.db.Exec(`INSERT INTO series (id, name, folder_path, issue_count) VALUES ('s1', 'Series 1', '/x/s1', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO series (id, name, folder_path, issue_count) VALUES ('s2', 'Series 2', '/x/s2', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO issues (id, series_id, filename, number, position) VALUES ('c1', 's1', '1.cbz', '1', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO issues (id, series_id, filename, number, position) VALUES ('c2', 's2', '1.cbz', '1', 0)`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.db.Exec(`INSERT INTO reading_sessions (comic_id, series_id, started_at, ended_at, pages_read, duration_s) VALUES ('c1', 's1', ?, ?, 5, 120)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO reading_sessions (comic_id, series_id, started_at, ended_at, pages_read, duration_s) VALUES ('c1', 's1', ?, ?, 3, 90)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO reading_sessions (comic_id, series_id, started_at, ended_at, pages_read, duration_s) VALUES ('c2', 's2', ?, ?, 2, 60)`, now, now); err != nil {
		t.Fatal(err)
	}

	dur1, err := s.SeriesTotalDurationS("s1")
	if err != nil {
		t.Fatalf("SeriesTotalDurationS: %v", err)
	}
	if dur1 != 210 {
		t.Errorf("s1 total: want 210, got %d", dur1)
	}

	dur2, _ := s.SeriesTotalDurationS("s2")
	if dur2 != 60 {
		t.Errorf("s2 total: want 60, got %d", dur2)
	}

	durNone, _ := s.SeriesTotalDurationS("nonexistent")
	if durNone != 0 {
		t.Errorf("nonexistent: want 0, got %d", durNone)
	}
}
