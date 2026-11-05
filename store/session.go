package store

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type ReadingSession struct {
	ID        int       `json:"id"`
	ComicID   string    `json:"comic_id"`
	SeriesID  string    `json:"series_id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	PagesRead int       `json:"pages_read"`
	DurationS int       `json:"duration_s"`
	CreatedAt time.Time `json:"created_at"`
}

type SessionStats struct {
	AllTimePagesRead int `json:"all_time_pages_read"`
	AllTimeTimeS     int `json:"all_time_time_s"`
	WeekPagesRead    int `json:"week_pages_read"`
	WeekTimeS        int `json:"week_time_s"`
	MonthPagesRead   int `json:"month_pages_read"`
	MonthTimeS       int `json:"month_time_s"`
}

type DailyPages struct {
	Date  string `json:"date"`
	Pages int    `json:"pages"`
}

type NameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type SeriesPages struct {
	SeriesID  string `json:"series_id"`
	Name      string `json:"name"`
	Pages     int    `json:"pages"`
	TimeReadS int    `json:"time_read_s"`
	CoverURL  string `json:"cover_url"`
}

type HourPages struct {
	Hour  int `json:"hour"`
	Pages int `json:"pages"`
}

const sqliteDatetimeFmt = "2006-01-02T15:04:05Z"

func (s *Store) CreateSession(sess ReadingSession) (int64, error) {
	result, err := s.db.Exec(`
		INSERT INTO reading_sessions (comic_id, series_id, started_at, ended_at, pages_read, duration_s)
		VALUES (?, ?, ?, ?, ?, ?)
	`, sess.ComicID, sess.SeriesID,
		sess.StartedAt.Format(sqliteDatetimeFmt),
		sess.EndedAt.Format(sqliteDatetimeFmt),
		sess.PagesRead, sess.DurationS)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) ListSessions(page, limit int) ([]ReadingSession, int, error) {
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM reading_sessions").Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * limit
	rows, err := s.db.Query(`
		SELECT id, comic_id, COALESCE(series_id, ''), started_at, ended_at, pages_read, duration_s, created_at
		FROM reading_sessions
		ORDER BY started_at DESC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sessions []ReadingSession
	for rows.Next() {
		var sess ReadingSession
		if err := rows.Scan(&sess.ID, &sess.ComicID, &sess.SeriesID,
			&sess.StartedAt, &sess.EndedAt, &sess.PagesRead, &sess.DurationS, &sess.CreatedAt); err != nil {
			return nil, 0, err
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if sessions == nil {
		sessions = []ReadingSession{}
	}
	return sessions, total, nil
}

func (s *Store) SessionStats() (*SessionStats, error) {
	now := time.Now().UTC()
	week := now.AddDate(0, 0, -7).Format(sqliteDatetimeFmt)
	month := now.AddDate(0, 0, -30).Format(sqliteDatetimeFmt)

	var stats SessionStats
	err := s.db.QueryRow(`
		SELECT
			COALESCE(SUM(pages_read), 0),
			COALESCE(SUM(duration_s), 0),
			COALESCE(SUM(CASE WHEN started_at >= ? THEN pages_read ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN started_at >= ? THEN duration_s ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN started_at >= ? THEN pages_read ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN started_at >= ? THEN duration_s ELSE 0 END), 0)
		FROM reading_sessions
	`, week, week, month, month).Scan(
		&stats.AllTimePagesRead, &stats.AllTimeTimeS,
		&stats.WeekPagesRead, &stats.WeekTimeS,
		&stats.MonthPagesRead, &stats.MonthTimeS,
	)
	if err != nil {
		return nil, err
	}
	return &stats, nil
}

func (s *Store) PagesPerDay(days int) ([]DailyPages, error) {
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -(days - 1)).Format(sqliteDatetimeFmt)

	rows, err := s.db.Query(`
		SELECT DATE(started_at) AS day, SUM(pages_read)
		FROM reading_sessions
		WHERE started_at >= ?
		GROUP BY day
		ORDER BY day ASC
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Collect DB results into a map
	dayMap := make(map[string]int)
	for rows.Next() {
		var date string
		var pages int
		if err := rows.Scan(&date, &pages); err != nil {
			return nil, err
		}
		dayMap[date] = pages
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Zero-fill all days in range
	result := make([]DailyPages, days)
	for i := 0; i < days; i++ {
		date := now.AddDate(0, 0, -(days - 1 - i)).Format("2006-01-02")
		pages := dayMap[date]
		result[i] = DailyPages{Date: date, Pages: pages}
	}
	return result, nil
}

func (s *Store) TopSessionGenres(limit int) ([]NameCount, error) {
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT sg.genre, COUNT(*) AS cnt
		FROM reading_sessions rs
		JOIN series_genres sg ON rs.series_id = sg.series_id
		GROUP BY sg.genre
		ORDER BY cnt DESC
		LIMIT %d
	`, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []NameCount
	for rows.Next() {
		var nc NameCount
		if err := rows.Scan(&nc.Name, &nc.Count); err != nil {
			return nil, err
		}
		results = append(results, nc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if results == nil {
		results = []NameCount{}
	}
	return results, nil
}

func (s *Store) TopSeriesByPages(limit int) ([]SeriesPages, error) {
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT rs.series_id,
		       COALESCE(ser.name, '') AS name,
		       SUM(rs.pages_read) AS pages,
		       SUM(rs.duration_s) AS time_s
		FROM reading_sessions rs
		LEFT JOIN series ser ON ser.id = rs.series_id
		WHERE rs.series_id != ''
		GROUP BY rs.series_id
		ORDER BY pages DESC
		LIMIT %d
	`, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SeriesPages
	for rows.Next() {
		var sp SeriesPages
		if err := rows.Scan(&sp.SeriesID, &sp.Name, &sp.Pages, &sp.TimeReadS); err != nil {
			return nil, err
		}
		results = append(results, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if results == nil {
		results = []SeriesPages{}
	}
	return results, nil
}

func (s *Store) AvgSessionDuration() (int, error) {
	var avg int
	err := s.db.QueryRow(`
		SELECT COALESCE(CAST(AVG(duration_s) AS INTEGER), 0)
		FROM reading_sessions
	`).Scan(&avg)
	if err != nil {
		return 0, err
	}
	return avg, nil
}

func (s *Store) SeriesStarted() (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(DISTINCT series_id)
		FROM reading_sessions
		WHERE series_id != ''
	`).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

var comicVineIDSuffix = regexp.MustCompile(`\s*\[\d+\]\s*$`)

func splitCreators(raw string) []string {
	if raw == "" {
		return nil
	}
	var result []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		s = comicVineIDSuffix.ReplaceAllString(s, "")
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}

func (s *Store) TopSessionPublishers(limit int) ([]NameCount, error) {
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT COALESCE(ser.publisher, 'Unknown') AS pub, COUNT(*) AS cnt
		FROM reading_sessions rs
		JOIN series ser ON rs.series_id = ser.id
		WHERE ser.publisher IS NOT NULL AND ser.publisher != ''
		GROUP BY pub
		ORDER BY cnt DESC
		LIMIT %d
	`, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []NameCount
	for rows.Next() {
		var nc NameCount
		if err := rows.Scan(&nc.Name, &nc.Count); err != nil {
			return nil, err
		}
		results = append(results, nc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if results == nil {
		results = []NameCount{}
	}
	return results, nil
}

func (s *Store) SessionDays() ([]time.Time, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT DATE(started_at, 'localtime') AS day
		FROM reading_sessions
		ORDER BY day ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var days []time.Time
	for rows.Next() {
		var dateStr string
		if err := rows.Scan(&dateStr); err != nil {
			return nil, err
		}
		t, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
		if err != nil {
			return nil, err
		}
		days = append(days, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return days, nil
}

func (s *Store) HourHistogram() ([]HourPages, error) {
	rows, err := s.db.Query(`
		SELECT CAST(strftime('%H', started_at, 'localtime') AS INTEGER) AS h,
		       SUM(pages_read) AS pages
		FROM reading_sessions
		GROUP BY h
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pagesByHour := make(map[int]int, 24)
	for rows.Next() {
		var h, pages int
		if err := rows.Scan(&h, &pages); err != nil {
			return nil, err
		}
		pagesByHour[h] = pages
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]HourPages, 24)
	for h := 0; h < 24; h++ {
		result[h] = HourPages{Hour: h, Pages: pagesByHour[h]}
	}
	return result, nil
}

func (s *Store) TopSessionWriters(limit int) ([]NameCount, error) {
	rows, err := s.db.Query(`
		SELECT COALESCE(ser.writer, '') AS writer
		FROM reading_sessions rs
		JOIN series ser ON rs.series_id = ser.id
		WHERE ser.writer IS NOT NULL AND ser.writer != ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		for _, w := range splitCreators(raw) {
			counts[w]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	results := make([]NameCount, 0, len(counts))
	for name, count := range counts {
		results = append(results, NameCount{Name: name, Count: count})
	}
	// Sort: count desc, then name asc for stable ordering on ties.
	sort.Slice(results, func(i, j int) bool {
		if results[i].Count != results[j].Count {
			return results[i].Count > results[j].Count
		}
		return results[i].Name < results[j].Name
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// SeriesTotalDurationS returns the sum of duration_s across all reading_sessions
// rows where series_id = ?. Returns 0 if no sessions.
func (s *Store) SeriesTotalDurationS(seriesID string) (int, error) {
	var total int
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(duration_s), 0) FROM reading_sessions WHERE series_id = ?`,
		seriesID,
	).Scan(&total)
	return total, err
}
