package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Match states. The reviewer's job differs in each, so they are distinct
// rather than collapsed into a confidence number.
const (
	MatchStateConfirmed      = "confirmed"       // human-approved
	MatchStateHighConfidence = "high_confidence" // passed floor and margin; bulk-confirmable
	MatchStateAmbiguous      = "ambiguous"       // several plausible, or a pin that disagreed
	MatchStateLowConfidence  = "low_confidence"  // one candidate, weak score
	MatchStateNoMatch        = "no_match"        // nothing above the floor
	MatchStateCollection     = "collection"      // collection folder: per-file volumes, no folder volume
)

const SourceComicVine = "comicvine"
const SourceMetron = "metron"
const SourceGCD = "gcd"

type SeriesMatch struct {
	SeriesID    string  `json:"series_id"`
	Source      string  `json:"source"`
	VolumeID    string  `json:"volume_id"`
	State       string  `json:"state"`
	Confidence  float64 `json:"confidence"`
	Margin      float64 `json:"margin"`
	Method      string  `json:"method"`
	Candidates  string  `json:"candidates"` // raw JSON array
	Penalties   string  `json:"penalties"`  // raw JSON object
	DontMatch   bool    `json:"dont_match"`
	MatchFailed bool    `json:"match_failed"`
	ConfirmedAt int64   `json:"confirmed_at"`
	ConfirmedBy string  `json:"confirmed_by"`
	UpdatedAt   int64   `json:"updated_at"`
}

type IssueMatch struct {
	IssueID    string  `json:"issue_id"`
	Source     string  `json:"source"`
	UpstreamID string  `json:"upstream_id"`
	State      string  `json:"state"`
	Confidence float64 `json:"confidence"`
	Method     string  `json:"method"`
}

// UpsertSeriesMatch writes match state for a series. It deliberately preserves
// dont_match and confirmed_at: those record human decisions, and a later
// matcher pass must never silently discard them.
func (s *Store) UpsertSeriesMatch(m SeriesMatch) error {
	if m.Candidates == "" {
		m.Candidates = "[]"
	}
	if m.Penalties == "" {
		m.Penalties = "{}"
	}
	_, err := s.db.Exec(`
		INSERT INTO series_match
			(series_id, source, volume_id, state, confidence, margin, method,
			 candidates, penalties, dont_match, match_failed, confirmed_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(series_id) DO UPDATE SET
			source       = excluded.source,
			volume_id    = excluded.volume_id,
			state        = excluded.state,
			confidence   = excluded.confidence,
			margin       = excluded.margin,
			method       = excluded.method,
			candidates   = excluded.candidates,
			penalties    = excluded.penalties,
			match_failed = excluded.match_failed,
			updated_at   = excluded.updated_at`,
		m.SeriesID, m.Source, m.VolumeID, m.State, m.Confidence, m.Margin, m.Method,
		m.Candidates, m.Penalties, m.DontMatch, m.MatchFailed, m.ConfirmedAt, m.UpdatedAt)
	return err
}

const seriesMatchSelect = `
	SELECT series_id, source, volume_id, state, confidence, margin, method,
	       candidates, penalties, dont_match, match_failed, confirmed_at, confirmed_by, updated_at
	  FROM series_match`

func scanSeriesMatch(sc interface{ Scan(...any) error }) (*SeriesMatch, error) {
	var m SeriesMatch
	err := sc.Scan(&m.SeriesID, &m.Source, &m.VolumeID, &m.State, &m.Confidence,
		&m.Margin, &m.Method, &m.Candidates, &m.Penalties, &m.DontMatch,
		&m.MatchFailed, &m.ConfirmedAt, &m.ConfirmedBy, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) GetSeriesMatch(seriesID string) (*SeriesMatch, error) {
	m, err := scanSeriesMatch(s.db.QueryRow(seriesMatchSelect+` WHERE series_id = ?`, seriesID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) ListSeriesMatchesByState(state string) ([]SeriesMatch, error) {
	rows, err := s.db.Query(seriesMatchSelect+` WHERE state = ? ORDER BY series_id`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SeriesMatch
	for rows.Next() {
		m, err := scanSeriesMatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ReviewStates are the match states a human still has to look at.
var ReviewStates = []string{MatchStateHighConfidence, MatchStateAmbiguous, MatchStateNoMatch, MatchStateCollection}

// ListReviewQueue returns matches in any of states that are not flagged
// dont_match, ordered by series_id. The one definition of "needs review".
func (s *Store) ListReviewQueue(states ...string) ([]SeriesMatch, error) {
	if len(states) == 0 {
		return nil, nil
	}
	args := make([]any, len(states))
	for i, st := range states {
		args[i] = st
	}
	rows, err := s.db.Query(seriesMatchSelect+` WHERE dont_match = 0 AND state IN (?`+
		strings.Repeat(",?", len(states)-1)+`) ORDER BY series_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SeriesMatch
	for rows.Next() {
		m, err := scanSeriesMatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *Store) CountSeriesMatchesByState() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT state, COUNT(*) FROM series_match GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		counts[state] = n
	}
	return counts, rows.Err()
}

func (s *Store) ConfirmSeriesMatch(seriesID string, nowMs int64) error {
	return s.ConfirmSeriesMatchBy(seriesID, "human", nowMs)
}

// ConfirmSeriesMatchBy records who confirmed: "human" (console) or
// "pipeline" (auto-confirm), so the console can badge the latter.
func (s *Store) ConfirmSeriesMatchBy(seriesID, by string, nowMs int64) error {
	res, err := s.db.Exec(`
		UPDATE series_match SET state = ?, confirmed_at = ?, updated_at = ?, confirmed_by = ?
		 WHERE series_id = ?`, MatchStateConfirmed, nowMs, nowMs, by, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSeriesMatchState sets a series match back to a specific state, e.g. when
// a human unconfirms a previously-confirmed match. confirmed_at is zeroed
// since the series is no longer human-approved.
func (s *Store) SetSeriesMatchState(seriesID, state string, nowMs int64) error {
	res, err := s.db.Exec(`
		UPDATE series_match SET state = ?, confirmed_at = 0, updated_at = ?
		 WHERE series_id = ?`, state, nowMs, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSeriesMatchVolume records a human's volume choice from the console's
// volume picker and the dump it came from. Source travels with the id:
// ComicVine and GCD id spaces overlap. Penalties/state are recomputed
// separately (match.Reverify).
func (s *Store) SetSeriesMatchVolume(seriesID, source, volumeID string, nowMs int64) error {
	res, err := s.db.Exec(`
		UPDATE series_match SET source = ?, volume_id = ?, updated_at = ?
		 WHERE series_id = ?`, source, volumeID, nowMs, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSeriesMatchReview writes the recomputed review fields after a human
// action changed the underlying issue matches. It deliberately leaves method,
// confidence, dont_match and confirmed_at alone.
func (s *Store) UpdateSeriesMatchReview(seriesID, volumeID, state, penalties string, nowMs int64) error {
	if penalties == "" {
		penalties = "{}"
	}
	res, err := s.db.Exec(`
		UPDATE series_match SET volume_id = ?, state = ?, penalties = ?, updated_at = ?
		 WHERE series_id = ?`, volumeID, state, penalties, nowMs, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceIssueMatch swaps out all of an issue's matches for the source in one
// transaction. Plain UpsertIssueMatch can't do this: its conflict key includes
// upstream_id, so re-pinning to a different upstream would add a second row
// instead of replacing the wrong one.
func (s *Store) ReplaceIssueMatch(m IssueMatch) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM issue_match WHERE issue_id = ? AND source = ?`,
		m.IssueID, m.Source); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO issue_match (issue_id, source, upstream_id, state, confidence, method)
		VALUES (?, ?, ?, ?, ?, ?)`,
		m.IssueID, m.Source, m.UpstreamID, m.State, m.Confidence, m.Method); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetDontMatch(seriesID string, dontMatch bool) error {
	res, err := s.db.Exec(`UPDATE series_match SET dont_match = ? WHERE series_id = ?`,
		dontMatch, seriesID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FirstMatchedIssue is the earliest-position issue in a series that has a
// recorded issue match, used by the queue list to show a representative
// cover pair per row.
type FirstMatchedIssue struct {
	SeriesID string
	IssueID  string
	// Source names the dump UpstreamID lives in: the id spaces overlap, so
	// resolving it against the wrong one silently returns another issue.
	Source     string
	UpstreamID string
}

// ListFirstMatchedIssuesByState returns, for every series currently in the
// given queue state, its earliest-position matched issue. It is one query for
// the whole queue rather than one per row, so the list page can show cover
// thumbnails without an N+1 against issues/issue_match.
func (s *Store) ListFirstMatchedIssuesByState(state string) (map[string]FirstMatchedIssue, error) {
	rows, err := s.db.Query(`
		SELECT i.series_id, i.id, im.source, im.upstream_id
		  FROM series_match sm
		  JOIN issues i ON i.series_id = sm.series_id
		  JOIN issue_match im ON im.issue_id = i.id
		 WHERE sm.state = ?
		 ORDER BY i.series_id, i.position`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]FirstMatchedIssue)
	for rows.Next() {
		var fm FirstMatchedIssue
		if err := rows.Scan(&fm.SeriesID, &fm.IssueID, &fm.Source, &fm.UpstreamID); err != nil {
			return nil, err
		}
		// Rows arrive ordered by position, so the first one seen per series is
		// its earliest matched issue; later rows for the same series are noise.
		if _, ok := out[fm.SeriesID]; !ok {
			out[fm.SeriesID] = fm
		}
	}
	return out, rows.Err()
}

func (s *Store) UpsertIssueMatch(m IssueMatch) error {
	_, err := s.db.Exec(`
		INSERT INTO issue_match (issue_id, source, upstream_id, state, confidence, method)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(issue_id, source, upstream_id) DO UPDATE SET
			state      = excluded.state,
			confidence = excluded.confidence,
			method     = excluded.method`,
		m.IssueID, m.Source, m.UpstreamID, m.State, m.Confidence, m.Method)
	return err
}

// DeleteIssueMatchesForSeries drops every issue_match row under a series. A
// human re-pinning a folder into a different dump replaces the machine's
// answer for the whole folder, rather than leaving the old source's rows
// beside the new ones.
func (s *Store) DeleteIssueMatchesForSeries(seriesID string) error {
	_, err := s.db.Exec(`
		DELETE FROM issue_match
		 WHERE issue_id IN (SELECT id FROM issues WHERE series_id = ?)`, seriesID)
	return err
}

func (s *Store) ListIssueMatchesForSeries(seriesID string) ([]IssueMatch, error) {
	rows, err := s.db.Query(`
		SELECT im.issue_id, im.source, im.upstream_id, im.state, im.confidence, im.method
		  FROM issue_match im
		  JOIN issues i ON i.id = im.issue_id
		 WHERE i.series_id = ?
		 ORDER BY i.position, im.upstream_id`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []IssueMatch
	for rows.Next() {
		var m IssueMatch
		if err := rows.Scan(&m.IssueID, &m.Source, &m.UpstreamID, &m.State,
			&m.Confidence, &m.Method); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
