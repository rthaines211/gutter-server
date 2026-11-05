package store

import (
	"database/sql"
	"errors"
)

// Creator is one role-tagged creator from ComicVine person_credits.
// Stored as JSON in series_enrichment.creators and serialized as-is
// into library/sync responses.
type Creator struct {
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type SeriesEnrichment struct {
	SeriesID        string
	VolumeID        int
	VolumeName      string
	Description     string
	Publisher       string
	StartYear       int
	Creators        string // JSON: []Creator
	Genre           string
	GCDSeriesID     int
	SecondarySource string
	Status          string // 'auto' | 'confirmed'
	EnrichedAt      int64  // unix ms
}

type IssueEnrichment struct {
	IssueID    string
	VineID     int
	Title      string
	Summary    string
	CoverDate  string // ISO date "2012-03-14"
	PageCount  float64
	Characters string // JSON: [{"name":…}], "[]" when empty
	EnrichedAt int64
}

type EnrichmentState struct {
	SeriesID string
	// Status values:
	//   queued          — eligible for the worker to process
	//   pending_review  — candidates surfaced for user choice
	//   enriched        — worker applied enrichment from ComicVine
	//   unmatched       — no viable ComicVine match found
	//   skipped         — explicitly skipped (e.g. series deleted during processing)
	//   skipped_manual  — series_enrichment has status='manual'; worker will not
	//                     overwrite user edits; terminal state for the worker
	Status         string
	Candidates     string // JSON array, '[]' when none
	ChosenVolumeID int
	Attempts       int
	LastAttemptAt  int64
}

// EnqueueMissingEnrichment inserts a 'queued' state row for every series
// that has no enrichment state yet. Returns the number of rows added.
func (s *Store) EnqueueMissingEnrichment() (int, error) {
	res, err := s.db.Exec(`
		INSERT INTO enrichment_state (series_id, status)
		SELECT id, 'queued' FROM series
		WHERE id NOT IN (SELECT series_id FROM enrichment_state)
	`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// NextQueuedEnrichment returns the oldest eligible queued row, honoring
// exponential backoff (60s << 2*attempts) and a 5-attempt cap.
// Returns nil, nil when nothing is eligible.
func (s *Store) NextQueuedEnrichment(nowMs int64) (*EnrichmentState, error) {
	var e EnrichmentState
	err := s.db.QueryRow(`
		SELECT series_id, status, candidates, chosen_volume_id, attempts, last_attempt_at
		FROM enrichment_state
		WHERE status = 'queued'
		  AND attempts < 5
		  AND last_attempt_at + (60000 << (2 * attempts)) <= ?
		ORDER BY last_attempt_at ASC, series_id ASC
		LIMIT 1
	`, nowMs).Scan(&e.SeriesID, &e.Status, &e.Candidates, &e.ChosenVolumeID, &e.Attempts, &e.LastAttemptAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) GetEnrichmentState(seriesID string) (*EnrichmentState, error) {
	var e EnrichmentState
	err := s.db.QueryRow(`
		SELECT series_id, status, candidates, chosen_volume_id, attempts, last_attempt_at
		FROM enrichment_state WHERE series_id = ?
	`, seriesID).Scan(&e.SeriesID, &e.Status, &e.Candidates, &e.ChosenVolumeID, &e.Attempts, &e.LastAttemptAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) RecordEnrichmentAttempt(seriesID string, nowMs int64) error {
	res, err := s.db.Exec(`
		UPDATE enrichment_state SET attempts = attempts + 1, last_attempt_at = ?
		WHERE series_id = ?
	`, nowMs, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetEnrichmentStatus(seriesID, status string) error {
	res, err := s.db.Exec(`UPDATE enrichment_state SET status = ? WHERE series_id = ?`, status, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetEnrichmentCandidates(seriesID, candidatesJSON string) error {
	res, err := s.db.Exec(`
		UPDATE enrichment_state SET status = 'pending_review', candidates = ?
		WHERE series_id = ?
	`, candidatesJSON, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEnrichmentChosen records a user-confirmed volume and re-queues the
// series so the worker applies it (attempts reset so backoff starts fresh).
func (s *Store) SetEnrichmentChosen(seriesID string, volumeID int) error {
	res, err := s.db.Exec(`
		UPDATE enrichment_state
		SET status = 'queued', chosen_volume_id = ?, attempts = 0, last_attempt_at = 0
		WHERE series_id = ?
	`, volumeID, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RequeueEnrichment resets a series for a fresh match (clears chosen volume
// and candidates). The caller should also DeleteSeriesEnrichment.
func (s *Store) RequeueEnrichment(seriesID string) error {
	_, err := s.db.Exec(`
		INSERT INTO enrichment_state (series_id, status) VALUES (?, 'queued')
		ON CONFLICT(series_id) DO UPDATE SET
			status = 'queued', candidates = '[]', chosen_volume_id = 0,
			attempts = 0, last_attempt_at = 0
	`, seriesID)
	return err
}

func (s *Store) CountEnrichmentByStatus() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM enrichment_state GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[status] = n
	}
	return counts, rows.Err()
}

func (s *Store) ListPendingReview() ([]EnrichmentState, error) {
	rows, err := s.db.Query(`
		SELECT series_id, status, candidates, chosen_volume_id, attempts, last_attempt_at
		FROM enrichment_state WHERE status = 'pending_review'
		ORDER BY series_id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []EnrichmentState
	for rows.Next() {
		var e EnrichmentState
		if err := rows.Scan(&e.SeriesID, &e.Status, &e.Candidates, &e.ChosenVolumeID, &e.Attempts, &e.LastAttemptAt); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, rows.Err()
}

func (s *Store) UpsertSeriesEnrichment(e SeriesEnrichment) error {
	_, err := s.db.Exec(`
		INSERT INTO series_enrichment
			(series_id, comicvine_volume_id, description, publisher, start_year, creators,
			 genre, gcd_series_id, secondary_source, volume_name, status, enriched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(series_id) DO UPDATE SET
			comicvine_volume_id = excluded.comicvine_volume_id,
			description = `+keepManual("series_enrichment", "description")+`,
			publisher = `+keepManual("series_enrichment", "publisher")+`,
			start_year = `+keepManual("series_enrichment", "start_year")+`,
			creators = `+keepManual("series_enrichment", "creators")+`,
			genre = excluded.genre,
			gcd_series_id = excluded.gcd_series_id,
			secondary_source = excluded.secondary_source,
			volume_name = excluded.volume_name,
			status = excluded.status,
			enriched_at = excluded.enriched_at
	`, e.SeriesID, e.VolumeID, e.Description, e.Publisher, e.StartYear, e.Creators,
		e.Genre, e.GCDSeriesID, e.SecondarySource, e.VolumeName, e.Status, e.EnrichedAt)
	return err
}

func (s *Store) GetSeriesEnrichment(seriesID string) (*SeriesEnrichment, error) {
	var e SeriesEnrichment
	err := s.db.QueryRow(`
		SELECT series_id, comicvine_volume_id, description, publisher, start_year, creators,
		       genre, gcd_series_id, secondary_source, volume_name, status, enriched_at
		FROM series_enrichment WHERE series_id = ?
	`, seriesID).Scan(&e.SeriesID, &e.VolumeID, &e.Description, &e.Publisher, &e.StartYear, &e.Creators,
		&e.Genre, &e.GCDSeriesID, &e.SecondarySource, &e.VolumeName, &e.Status, &e.EnrichedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) ListSeriesEnrichmentMap() (map[string]SeriesEnrichment, error) {
	rows, err := s.db.Query(`
		SELECT series_id, comicvine_volume_id, description, publisher, start_year, creators,
		       genre, gcd_series_id, secondary_source, volume_name, status, enriched_at
		FROM series_enrichment
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]SeriesEnrichment)
	for rows.Next() {
		var e SeriesEnrichment
		if err := rows.Scan(&e.SeriesID, &e.VolumeID, &e.Description, &e.Publisher, &e.StartYear, &e.Creators,
			&e.Genre, &e.GCDSeriesID, &e.SecondarySource, &e.VolumeName, &e.Status, &e.EnrichedAt); err != nil {
			return nil, err
		}
		m[e.SeriesID] = e
	}
	return m, rows.Err()
}

// DeleteSeriesEnrichment removes the series row and all issue enrichment
// for issues belonging to that series.
func (s *Store) DeleteSeriesEnrichment(seriesID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM series_enrichment WHERE series_id = ?`, seriesID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		DELETE FROM issue_enrichment
		WHERE issue_id IN (SELECT id FROM issues WHERE series_id = ?)
	`, seriesID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpsertIssueEnrichment(e IssueEnrichment) error {
	characters := e.Characters
	if characters == "" {
		characters = "[]"
	}
	_, err := s.db.Exec(`
		INSERT INTO issue_enrichment
			(issue_id, comicvine_issue_id, title, summary, cover_date, page_count, characters, enriched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(issue_id) DO UPDATE SET
			comicvine_issue_id = excluded.comicvine_issue_id,
			title = `+keepManual("issue_enrichment", "title")+`,
			summary = `+keepManual("issue_enrichment", "summary")+`,
			cover_date = excluded.cover_date,
			page_count = excluded.page_count,
			characters = excluded.characters,
			enriched_at = excluded.enriched_at
	`, e.IssueID, e.VineID, e.Title, e.Summary, e.CoverDate, e.PageCount, characters, e.EnrichedAt)
	return err
}

func (s *Store) GetIssueEnrichment(issueID string) (*IssueEnrichment, error) {
	var e IssueEnrichment
	err := s.db.QueryRow(`
		SELECT issue_id, comicvine_issue_id, title, summary, cover_date, page_count, characters, enriched_at
		FROM issue_enrichment WHERE issue_id = ?
	`, issueID).Scan(&e.IssueID, &e.VineID, &e.Title, &e.Summary, &e.CoverDate, &e.PageCount, &e.Characters, &e.EnrichedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) ListIssueEnrichmentForSeries(seriesID string) (map[string]IssueEnrichment, error) {
	rows, err := s.db.Query(`
		SELECT ie.issue_id, ie.comicvine_issue_id, ie.title, ie.summary, ie.cover_date, ie.page_count, ie.characters, ie.enriched_at
		FROM issue_enrichment ie
		JOIN issues i ON i.id = ie.issue_id
		WHERE i.series_id = ?
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]IssueEnrichment)
	for rows.Next() {
		var e IssueEnrichment
		if err := rows.Scan(&e.IssueID, &e.VineID, &e.Title, &e.Summary, &e.CoverDate, &e.PageCount, &e.Characters, &e.EnrichedAt); err != nil {
			return nil, err
		}
		m[e.IssueID] = e
	}
	return m, rows.Err()
}

// OwnedCVIssues returns every matched issue as ComicVine issue ID -> owning
// series ID. Issues with no ComicVine match (comicvine_issue_id = 0) are
// excluded.
func (s *Store) OwnedCVIssues() (map[int64]string, error) {
	rows, err := s.db.Query(`
		SELECT ie.comicvine_issue_id, i.series_id
		FROM issue_enrichment ie
		JOIN issues i ON i.id = ie.issue_id
		WHERE ie.comicvine_issue_id != 0
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]string)
	for rows.Next() {
		var cvID int64
		var seriesID string
		if err := rows.Scan(&cvID, &seriesID); err != nil {
			return nil, err
		}
		out[cvID] = seriesID
	}
	return out, rows.Err()
}

// IssueComicVineID returns an issue's matched ComicVine issue ID. Returns
// ErrNotFound when the issue has no issue_enrichment row, or its
// comicvine_issue_id is still 0 (unmatched) — the /creators/issues endpoint
// treats both the same way (404).
func (s *Store) IssueComicVineID(issueID string) (int64, error) {
	var cvID int64
	err := s.db.QueryRow(`SELECT comicvine_issue_id FROM issue_enrichment WHERE issue_id = ?`, issueID).Scan(&cvID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if cvID == 0 {
		return 0, ErrNotFound
	}
	return cvID, nil
}

// TouchSeries bumps updated_at so delta sync picks the series up.
func (s *Store) TouchSeries(seriesID string) error {
	_, err := s.db.Exec(`UPDATE series SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, seriesID)
	return err
}
