package server

import (
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

// --- HandleCreateSession ---

type CreateSessionRequest struct {
	ComicID   string `json:"comic_id"`
	SeriesID  string `json:"series_id"`
	StartedAt string `json:"started_at"`
	PagesRead int    `json:"pages_read"`
	DurationS int    `json:"duration_s"`
}

type CreateSessionResponse struct {
	ID int64 `json:"id"`
}

func (s *Server) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.DurationS < 5 {
		writeJSONError(w, http.StatusBadRequest, "session too short")
		return
	}

	startedAt, err := time.Parse(time.RFC3339, req.StartedAt)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid started_at: expected RFC3339")
		return
	}

	endedAt := startedAt.Add(time.Duration(req.DurationS) * time.Second)

	sess := store.ReadingSession{
		ComicID:   req.ComicID,
		SeriesID:  req.SeriesID,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		PagesRead: req.PagesRead,
		DurationS: req.DurationS,
	}

	id, err := s.Store.CreateSession(sess)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(CreateSessionResponse{ID: id})
}

// --- Streak helpers ---

type Streak struct {
	Current int `json:"current"`
	Longest int `json:"longest"`
}

func truncDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func computeStreaks(days []time.Time, now time.Time) Streak {
	if len(days) == 0 {
		return Streak{Current: 0, Longest: 0}
	}

	today := truncDay(now)
	yesterday := today.AddDate(0, 0, -1)

	set := make(map[time.Time]bool, len(days))
	for _, d := range days {
		set[truncDay(d)] = true
	}

	current := 0
	var cursor time.Time
	switch {
	case set[today]:
		cursor = today
	case set[yesterday]:
		cursor = yesterday
	}
	for !cursor.IsZero() && set[cursor] {
		current++
		cursor = cursor.AddDate(0, 0, -1)
	}

	longest := 1
	run := 1
	for i := 1; i < len(days); i++ {
		prev := truncDay(days[i-1])
		curr := truncDay(days[i])
		if curr.Sub(prev) == 24*time.Hour {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 1
		}
	}

	return Streak{Current: current, Longest: longest}
}

// --- HandleStatsSummary ---

type StatsSummaryResponse struct {
	AllTime        AllTimeStats        `json:"all_time"`
	ThisWeek       PeriodStats         `json:"this_week"`
	ThisMonth      PeriodStats         `json:"this_month"`
	PagesPerDay    []store.DailyPages  `json:"pages_per_day"`
	TopGenres      []store.NameCount   `json:"top_genres"`
	TopPublishers  []store.NameCount   `json:"top_publishers"`
	TopSeries      []store.SeriesPages `json:"top_series"`
	TopWriters     []store.NameCount   `json:"top_writers"`
	AvgSessionS    int                 `json:"avg_session_s"`
	CompletionRate CompletionRate      `json:"completion_rate"`
	Streak         Streak              `json:"streak"`
	HourHistogram  []store.HourPages   `json:"hour_histogram"`
}

type CompletionRate struct {
	Started     int `json:"started"`
	Completed   int `json:"completed"`
	RatePercent int `json:"rate_percent"`
}

type AllTimeStats struct {
	PagesRead       int `json:"pages_read"`
	IssuesFinished  int `json:"issues_finished"`
	SeriesCompleted int `json:"series_completed"`
	TimeReadS       int `json:"time_read_s"`
}

type PeriodStats struct {
	PagesRead int `json:"pages_read"`
	TimeReadS int `json:"time_read_s"`
}

func (s *Server) HandleStatsSummary(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Store.SessionStats()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	pagesPerDay, err := s.Store.PagesPerDay(365)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	topGenres, err := s.Store.TopSessionGenres(10)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	topPublishers, err := s.Store.TopSessionPublishers(10)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	topSeries, err := s.Store.TopSeriesByPages(10)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	topWriters, err := s.Store.TopSessionWriters(10)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	avgSession, err := s.Store.AvgSessionDuration()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	seriesStarted, err := s.Store.SeriesStarted()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	sessionDays, err := s.Store.SessionDays()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	hourHistogram, err := s.Store.HourHistogram()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	// issues_finished: comics where current_page >= total_pages AND total_pages > 0
	var issuesFinished int
	err = s.Store.DB().QueryRow(
		"SELECT COUNT(*) FROM reading_progress WHERE current_page >= total_pages AND total_pages > 0",
	).Scan(&issuesFinished)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	allSeries, err := s.Store.ListSeries()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	seriesCompleted := 0
	for _, series := range allSeries {
		if series.IssueCount > 0 && s.countReadIssues(series) >= series.IssueCount {
			seriesCompleted++
		}
	}

	// Annotate top series with their cover URLs.
	for i := range topSeries {
		topSeries[i].CoverURL = "/library/" + topSeries[i].SeriesID + "/cover"
	}

	// Completion rate (clamped to 100 if Completed > Started for any reason).
	ratePercent := 0
	if seriesStarted > 0 {
		ratePercent = 100 * seriesCompleted / seriesStarted
		if ratePercent > 100 {
			ratePercent = 100
		}
	}

	streak := computeStreaks(sessionDays, time.Now())

	resp := StatsSummaryResponse{
		AllTime: AllTimeStats{
			PagesRead:       stats.AllTimePagesRead,
			IssuesFinished:  issuesFinished,
			SeriesCompleted: seriesCompleted,
			TimeReadS:       stats.AllTimeTimeS,
		},
		ThisWeek: PeriodStats{
			PagesRead: stats.WeekPagesRead,
			TimeReadS: stats.WeekTimeS,
		},
		ThisMonth: PeriodStats{
			PagesRead: stats.MonthPagesRead,
			TimeReadS: stats.MonthTimeS,
		},
		PagesPerDay:   pagesPerDay,
		TopGenres:     topGenres,
		TopPublishers: topPublishers,
		TopSeries:     topSeries,
		TopWriters:    topWriters,
		AvgSessionS:   avgSession,
		CompletionRate: CompletionRate{
			Started:     seriesStarted,
			Completed:   seriesCompleted,
			RatePercent: ratePercent,
		},
		Streak:        streak,
		HourHistogram: hourHistogram,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- HandleStatsHistory ---

type StatsHistoryResponse struct {
	Sessions []SessionHistoryEntry `json:"sessions"`
	Total    int                   `json:"total"`
	Page     int                   `json:"page"`
	Limit    int                   `json:"limit"`
}

type SessionHistoryEntry struct {
	ID         int    `json:"id"`
	ComicID    string `json:"comic_id"`
	SeriesID   string `json:"series_id"`
	SeriesName string `json:"series_name"`
	ComicName  string `json:"comic_name"`
	CoverURL   string `json:"cover_url"`
	StartedAt  string `json:"started_at"`
	PagesRead  int    `json:"pages_read"`
	DurationS  int    `json:"duration_s"`
}

func (s *Server) HandleStatsHistory(w http.ResponseWriter, r *http.Request) {
	// No upper clamp historically applied here; math.MaxInt is a no-op
	// bound so parsePageLimit's clamp never triggers (rec 6 finding: the
	// old inline parsing had no max, so behavior must stay unclamped).
	page, limit := parsePageLimit(r, 20, math.MaxInt)

	sessions, total, err := s.Store.ListSessions(page, limit)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	entries := make([]SessionHistoryEntry, 0, len(sessions))
	for _, sess := range sessions {
		entry := SessionHistoryEntry{
			ID:        sess.ID,
			ComicID:   sess.ComicID,
			SeriesID:  sess.SeriesID,
			StartedAt: sess.StartedAt.Format(time.RFC3339),
			PagesRead: sess.PagesRead,
			DurationS: sess.DurationS,
		}

		// Enrich with series info
		if sess.SeriesID != "" {
			if series, err := s.Store.GetSeries(sess.SeriesID); err == nil {
				entry.SeriesName = series.Name
				entry.CoverURL = "/library/" + series.ID + "/cover"
			}
		}

		// Decode comic_id to get comic_name
		if path, err := idToPath(sess.ComicID); err == nil {
			name := filepath.Base(path)
			entry.ComicName = stripComicExtension(name)
		}

		entries = append(entries, entry)
	}

	resp := StatsHistoryResponse{
		Sessions: entries,
		Total:    total,
		Page:     page,
		Limit:    limit,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// stripComicExtension removes common comic file extensions from a filename.
func stripComicExtension(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".cbz", ".cbr", ".cb7", ".pdf":
		return strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name
}
