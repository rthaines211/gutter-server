package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Series struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	FolderPath string    `json:"folder_path"`
	IssueCount int       `json:"issue_count"`
	CoverPath  string    `json:"cover_path"`
	Writer     string    `json:"writer"`
	Penciller  string    `json:"penciller"`
	Publisher  string    `json:"publisher"`
	Year       int       `json:"year"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// seriesSelect is the shared projection for Series rows. publisher and year
// prefer a confirmed match's series_enrichment values over the scanner's
// file-tag ones, which are blank/zero whenever the files carry no ComicInfo.xml
// <Publisher>/<Year>. Same precedence WritebackSource already uses; kept in one
// place so the three call sites can't drift apart again.
const seriesSelect = `
		SELECT s.id, s.name, s.folder_path, s.issue_count, s.cover_path, s.writer, s.penciller,
		       COALESCE(NULLIF(se.publisher, ''), s.publisher, ''),
		       COALESCE(NULLIF(se.start_year, 0), s.year, 0),
		       s.created_at, s.updated_at
		FROM series s LEFT JOIN series_enrichment se ON se.series_id = s.id`

// UpsertSeries inserts a new series row or, for an existing row, updates
// structural fields (folder_path, issue_count, cover_path, updated_at) plus any
// metadata field that is currently blank.
//
// folder_path is structural — it tracks where the series lives on disk — and
// is always updated on rescan. Metadata fields (writer, penciller, publisher,
// year) are seeded on first scan and preserved afterward so in-app edits
// survive rescans; a fresh DB re-reads those from files.
//
// The blank-field exception exists because a row written by a run whose
// extraction failed (scan timeout, unavailable drive, older scanner) would
// otherwise stay empty forever — preserving "the user's value" over a blank
// preserves nothing, and the file identity checks in the scanner mean such a
// row is never reconsidered. Only genuinely empty columns are filled, so a real
// in-app edit is still never overwritten. Name is excluded: it is derived from
// the folder, not the file, and is renameable in-app.
func (s *Store) UpsertSeries(series Series) error {
	// created_at is set once at insert and never touched on conflict — it's the
	// series' add date. The scanner supplies the folder's oldest file mtime;
	// when absent (zero), fall back to now via COALESCE.
	var createdAt interface{}
	if !series.CreatedAt.IsZero() {
		createdAt = series.CreatedAt.UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO series (id, name, folder_path, issue_count, cover_path, writer, penciller, publisher, year, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			folder_path = excluded.folder_path,
			issue_count = excluded.issue_count,
			cover_path = excluded.cover_path,
			writer     = COALESCE(NULLIF(series.writer, ''), excluded.writer),
			penciller  = COALESCE(NULLIF(series.penciller, ''), excluded.penciller),
			publisher  = COALESCE(NULLIF(series.publisher, ''), excluded.publisher),
			year       = COALESCE(NULLIF(series.year, 0), excluded.year),
			updated_at = CURRENT_TIMESTAMP
		WHERE excluded.folder_path IS NOT series.folder_path
		   OR excluded.issue_count IS NOT series.issue_count
		   OR excluded.cover_path IS NOT series.cover_path
		   OR (COALESCE(series.writer, '') = '' AND COALESCE(excluded.writer, '') <> '')
		   OR (COALESCE(series.penciller, '') = '' AND COALESCE(excluded.penciller, '') <> '')
		   OR (COALESCE(series.publisher, '') = '' AND COALESCE(excluded.publisher, '') <> '')
		   OR (COALESCE(series.year, 0) = 0 AND COALESCE(excluded.year, 0) <> 0)
	`, series.ID, series.Name, series.FolderPath, series.IssueCount, series.CoverPath,
		series.Writer, series.Penciller, series.Publisher, series.Year, createdAt)
	return err
}

func (s *Store) GetSeries(id string) (*Series, error) {
	var series Series
	err := s.db.QueryRow(seriesSelect+`
		WHERE s.id = ?
	`, id).Scan(&series.ID, &series.Name, &series.FolderPath, &series.IssueCount,
		&series.CoverPath, &series.Writer, &series.Penciller,
		&series.Publisher, &series.Year, &series.CreatedAt, &series.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &series, nil
}

// SeriesExists reports whether a series row with the given id exists.
// It uses a minimal SELECT 1 query for the cheapest possible existence check.
func (s *Store) SeriesExists(id string) (bool, error) {
	var dummy int
	err := s.db.QueryRow(`SELECT 1 FROM series WHERE id = ? LIMIT 1`, id).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) SeriesCount() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM series").Scan(&count)
	return count, err
}

func (s *Store) ListSeries() ([]Series, error) {
	rows, err := s.db.Query(seriesSelect + `
		ORDER BY s.name COLLATE NOCASE ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Series
	for rows.Next() {
		var series Series
		if err := rows.Scan(&series.ID, &series.Name, &series.FolderPath, &series.IssueCount,
			&series.CoverPath, &series.Writer, &series.Penciller,
			&series.Publisher, &series.Year, &series.CreatedAt, &series.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, series)
	}
	return list, rows.Err()
}

type Issue struct {
	ID        string
	SeriesID  string
	Filename  string
	Number    string
	Position  int
	Format    string
	Writer    string
	Penciller string
}

// ReplaceIssues performs a differential sync of issues for a series inside one
// transaction. Existing rows keep their number, format, writer, and penciller
// (preserving in-app edits); only filename and position are updated on conflict.
// Rows for files that are no longer present on disk are deleted (cascading into
// issue_enrichment and other comic-keyed tables via FK cascade or the migrate
// helper).
func (s *Store) ReplaceIssues(seriesID string, issues []Issue) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// UPSERT each scanned issue. On conflict only update filename and position so
	// that editable fields (number, format, writer, penciller) survive rescans.
	for _, iss := range issues {
		if _, err := tx.Exec(`
			INSERT INTO issues (id, series_id, filename, number, position)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				filename = excluded.filename,
				position = excluded.position
		`, iss.ID, seriesID, iss.Filename, iss.Number, iss.Position); err != nil {
			return err
		}
	}

	// Delete rows for this series whose id is no longer in the scanned set.
	if len(issues) == 0 {
		if _, err := tx.Exec("DELETE FROM issues WHERE series_id = ?", seriesID); err != nil {
			return err
		}
	} else {
		// Build placeholder list for the IN clause.
		placeholders := make([]byte, 0, len(issues)*2)
		args := make([]interface{}, 0, len(issues)+1)
		args = append(args, seriesID)
		for i, iss := range issues {
			if i > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, '?')
			args = append(args, iss.ID)
		}
		if _, err := tx.Exec(
			"DELETE FROM issues WHERE series_id = ? AND id NOT IN ("+string(placeholders)+")",
			args...,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SetIssueFormatIfBlank records a parsed collection format ("tpb", "hc",
// "omnibus") on an issue that has none. An existing value — user-edited or
// scanner-set — is never overwritten.
func (s *Store) SetIssueFormatIfBlank(issueID, format string) error {
	if format == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE issues SET format = ? WHERE id = ? AND COALESCE(format, '') = ''`,
		strings.ToLower(format), issueID)
	return err
}

// SetIssueNumberIfBlank records a parsed collection's volume number on an issue
// that has none. The scanner's issueNumberFromFilename strips "v01" as a volume
// marker and returns "" — correct for singles ("Batman v2 755" is issue 755),
// wrong for a trade, where the volume number IS the number. parse.ParseFile
// gets it right, so persist that here alongside the format.
//
// Blank-only, like the format above: a user-edited or scanner-set number is
// never overwritten. Needed because UpsertIssues writes number on INSERT only,
// so a row that landed blank can never heal itself on rescan.
func (s *Store) SetIssueNumberIfBlank(issueID, number string) error {
	if number == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE issues SET number = ? WHERE id = ? AND COALESCE(number, '') = ''`,
		number, issueID)
	return err
}

func (s *Store) ListIssues(seriesID string) ([]Issue, error) {
	rows, err := s.db.Query(
		`SELECT id, series_id, filename, number, position,
		        COALESCE(format,''), COALESCE(writer,''), COALESCE(penciller,'')
		 FROM issues WHERE series_id = ? ORDER BY position ASC`,
		seriesID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Issue
	for rows.Next() {
		var iss Issue
		if err := rows.Scan(&iss.ID, &iss.SeriesID, &iss.Filename, &iss.Number, &iss.Position,
			&iss.Format, &iss.Writer, &iss.Penciller); err != nil {
			return nil, err
		}
		list = append(list, iss)
	}
	return list, rows.Err()
}

// UpdateSeriesCoverPath sets cover_path for a series and bumps updated_at.
func (s *Store) UpdateSeriesCoverPath(seriesID, path string) error {
	_, err := s.db.Exec(
		`UPDATE series SET cover_path = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		path, seriesID,
	)
	return err
}

func (s *Store) DeleteSeriesNotIn(ids []string) error {
	if len(ids) == 0 {
		_, err := s.db.Exec("DELETE FROM series")
		return err
	}
	args := make([]interface{}, len(ids))
	placeholders := make([]byte, 0, len(ids)*2)
	for i, id := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = id
	}
	_, err := s.db.Exec("DELETE FROM series WHERE id NOT IN ("+string(placeholders)+")", args...)
	return err
}

// ReplaceSeriesGenres replaces all genres for a series within a transaction.
// Passing nil or empty slice clears all genres.
func (s *Store) ReplaceSeriesGenres(seriesID string, genres []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM series_genres WHERE series_id = ?", seriesID); err != nil {
		return err
	}
	for _, genre := range genres {
		if _, err := tx.Exec("INSERT INTO series_genres (series_id, genre) VALUES (?, ?)", seriesID, genre); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListGenresForSeries returns genres for every given series id in one batch
// (chunked at idChunkSize), grouped by series id and sorted alphabetically
// within each — the same shape ListSeriesGenres would return per id. A
// series id with no genres is absent from the map, matching
// ListSeriesGenres' nil-on-empty behavior.
func (s *Store) ListGenresForSeries(seriesIDs []string) (map[string][]string, error) {
	result := make(map[string][]string, len(seriesIDs))
	for _, chunk := range chunkIDs(seriesIDs) {
		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.Query(
			"SELECT series_id, genre FROM series_genres WHERE series_id IN ("+placeholders(len(chunk))+") ORDER BY series_id ASC, genre ASC",
			args...,
		)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var seriesID, genre string
			if err := rows.Scan(&seriesID, &genre); err != nil {
				rows.Close()
				return nil, err
			}
			result[seriesID] = append(result[seriesID], genre)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListSeriesGenres returns genres for a series sorted alphabetically.
func (s *Store) ListSeriesGenres(seriesID string) ([]string, error) {
	rows, err := s.db.Query(
		"SELECT genre FROM series_genres WHERE series_id = ? ORDER BY genre ASC",
		seriesID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var genres []string
	for rows.Next() {
		var genre string
		if err := rows.Scan(&genre); err != nil {
			return nil, err
		}
		genres = append(genres, genre)
	}
	return genres, rows.Err()
}
