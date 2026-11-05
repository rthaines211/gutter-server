package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var yearSuffixRe = regexp.MustCompile(`\s*\(\d{4}\)\s*$`)

// stripYearSuffix drops a trailing " (YYYY)" — the folder convention — so a
// folder name can stand in for <Series> when no canonical name is stored.
func stripYearSuffix(name string) string {
	return strings.TrimSpace(yearSuffixRe.ReplaceAllString(name, ""))
}

// IssueDir returns the absolute folder holding an issue's file. An issue id
// is its file's path relative to the library root ("Folder/name.cbz"), and a
// series can span same-root twin folders (server.ScanLibrary), so the folder
// comes from the id, not the series row. seriesFolder (the series' primary
// folder) is the fallback for an id that does not decode to filename.
func IssueDir(seriesFolder, issueID, filename string) string {
	b, err := base64.RawURLEncoding.DecodeString(issueID)
	rel := string(b)
	if err != nil || filepath.Base(rel) != filename || filepath.Dir(rel) == "." {
		return seriesFolder
	}
	return filepath.Join(filepath.Dir(seriesFolder), filepath.Dir(rel))
}

// InTwinFolder reports whether an issue's file lives outside its series'
// primary folder, in a downloader-owned twin folder that must never be
// renamed or reorganized. Case-insensitive: a case-only spelling drift of
// the primary folder is not a twin.
func InTwinFolder(seriesFolder, issueID, filename string) bool {
	return !strings.EqualFold(IssueDir(seriesFolder, issueID, filename), seriesFolder)
}

// PathToID converts a relative comic path to its URL-safe base64 ID.
// This is the canonical path→ID function shared by the server and the writeback worker.
func PathToID(relPath string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(relPath))
}

// ComicExists reports whether an issues row exists for id.
func (s *Store) ComicExists(id string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM issues WHERE id=?`, id).Scan(&one) == nil
}

// WritebackSource holds all DB-side fields needed to write ComicInfo.xml for a comic.
type WritebackSource struct {
	SeriesID   string
	SeriesName string
	// FolderName is the base name of the folder holding the file (IssueDir) —
	// the single directory component that the worker joins with libraryRoot
	// to build the absolute comic path.
	FolderName string
	// InTwinFolder: the file lives in a downloader's twin folder, not the
	// series' primary folder, and must keep its name.
	InTwinFolder bool
	Filename   string
	Number     string
	Format     string
	// IssueWriter/IssuePenciller come from issues.writer/penciller (edited per-issue).
	IssueWriter, IssuePenciller string
	// SeriesWriter/SeriesPenciller/Publisher come from the series row.
	SeriesWriter, SeriesPenciller, Publisher string
	Year                                     int
	// Title and Summary come from issue_enrichment (may be empty).
	Title, Summary string
	Genres         []string
	// Creators is the typed roster from series_enrichment.creators (may be empty).
	Creators []Creator
	// CoverYear/Month/Day are parsed from issue_enrichment.cover_date (0 = absent).
	CoverYear, CoverMonth, CoverDay int
}

// WritebackSource loads all DB metadata needed to write ComicInfo.xml for the given comic ID.
// Enrichment (series_enrichment/issue_enrichment) wins over scanned series.* fields when
// present — this is the same precedence UpdateSeriesMetadata relies on, since it writes to
// both tables, so manual edits and confirmed ComicVine matches take the same path here.
func (s *Store) WritebackSource(comicID string) (*WritebackSource, error) {
	var src WritebackSource
	var creatorsJSON, coverDate, volumeName string
	err := s.db.QueryRow(`
		SELECT
			s.id,
			COALESCE(se.volume_name, ''),
			s.name,
			s.folder_path,
			i.filename,
			i.number,
			COALESCE(i.format, ''),
			COALESCE(i.writer, ''),
			COALESCE(i.penciller, ''),
			COALESCE(s.writer, ''),
			COALESCE(s.penciller, ''),
			COALESCE(NULLIF(se.publisher, ''), COALESCE(s.publisher, '')),
			CASE WHEN COALESCE(se.start_year,0) > 0 THEN se.start_year ELSE COALESCE(s.year,0) END,
			COALESCE(ie.title, ''),
			COALESCE(ie.summary, ''),
			COALESCE(ie.cover_date, ''),
			COALESCE(se.creators, '[]')
		FROM issues i
		JOIN series s ON s.id = i.series_id
		LEFT JOIN issue_enrichment ie ON ie.issue_id = i.id
		LEFT JOIN series_enrichment se ON se.series_id = s.id
		WHERE i.id = ?`, comicID).Scan(
		&src.SeriesID,
		&volumeName,
		&src.SeriesName,
		&src.FolderName, // temporarily holds full folder_path
		&src.Filename,
		&src.Number,
		&src.Format,
		&src.IssueWriter,
		&src.IssuePenciller,
		&src.SeriesWriter,
		&src.SeriesPenciller,
		&src.Publisher,
		&src.Year,
		&src.Title,
		&src.Summary,
		&coverDate,
		&creatorsJSON,
	)
	if err != nil {
		return nil, err
	}
	// Reduce the series' absolute folder_path to the base name of the issue's
	// own folder — the worker joins libraryRoot + FolderName.
	src.InTwinFolder = InTwinFolder(src.FolderName, comicID, src.Filename)
	src.FolderName = filepath.Base(IssueDir(src.FolderName, comicID, src.Filename))
	// <Series> is the canonical volume name, verbatim; a folder name stands
	// in without its "(YYYY)" — the year belongs in <Volume>/<Year>.
	if volumeName != "" {
		src.SeriesName = volumeName
	} else {
		src.SeriesName = stripYearSuffix(src.SeriesName)
	}
	src.CoverYear, src.CoverMonth, src.CoverDay = parseCoverDate(coverDate)
	if creatorsJSON != "" && creatorsJSON != "[]" {
		_ = json.Unmarshal([]byte(creatorsJSON), &src.Creators) // bad JSON -> empty roster, never fatal
	}

	genres, err := s.ListSeriesGenres(src.SeriesID)
	if err != nil {
		return nil, err
	}
	src.Genres = genres
	return &src, nil
}

// parseCoverDate splits an ISO cover date (YYYY, YYYY-MM, or YYYY-MM-DD) into
// numeric parts. Missing or unparseable parts return 0 — never an error, since
// a bad date must not block writeback.
func parseCoverDate(str string) (year, month, day int) {
	parts := strings.SplitN(str, "-", 3)
	atoi := func(i int) int {
		if i >= len(parts) {
			return 0
		}
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return 0
		}
		return n
	}
	return atoi(0), atoi(1), atoi(2)
}

func (s *Store) EnqueueWriteback(comicIDs ...string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, id := range comicIDs {
		if _, err := tx.Exec(`INSERT INTO metadata_writeback(comic_id, status, attempts, error, updated_at)
			VALUES(?, 'pending', 0, NULL, ?)
			ON CONFLICT(comic_id) DO UPDATE SET status='pending', attempts=0, error=NULL, updated_at=excluded.updated_at`,
			id, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EnqueueWritebackForConfirmed queues a writeback job for every issue belonging
// to a human-confirmed series. Uses the same upsert as EnqueueWriteback so
// re-running is idempotent (rows flip back to 'pending'). Returns the count.
func (s *Store) EnqueueWritebackForConfirmed() (int, error) {
	now := time.Now().UnixMilli()
	res, err := s.db.Exec(`
		INSERT INTO metadata_writeback(comic_id, status, attempts, error, updated_at)
		SELECT i.id, 'pending', 0, NULL, ?
		  FROM issues i
		  JOIN series_match sm ON sm.series_id = i.series_id
		 WHERE sm.state = ?
		ON CONFLICT(comic_id) DO UPDATE SET status='pending', attempts=0, error=NULL, updated_at=excluded.updated_at`,
		now, MatchStateConfirmed)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ConfirmedSeriesMissingVolumeName lists confirmed series with a volume but
// no stored canonical volume_name — confirmed before Apply recorded it.
func (s *Store) ConfirmedSeriesMissingVolumeName() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT sm.series_id
		  FROM series_match sm
		  LEFT JOIN series_enrichment se ON se.series_id = sm.series_id
		 WHERE sm.state = ? AND sm.volume_id != '' AND COALESCE(se.volume_name, '') = ''
		 ORDER BY sm.series_id`, MatchStateConfirmed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ConfirmedSeriesMissingCreatorIDs lists confirmed series matched to a
// ComicVine volume whose stored creators still carry no ComicVine person
// id — confirmed (or re-matched) before Apply started backfilling ids, so
// re-running the matcher alone never fixes them (match/runner.go skips
// already-confirmed series). Excludes:
//   - series with no comicvine_volume_id: Metron/GCD-sourced series never
//     get a CV id, so re-applying them here would be a no-op every run.
//   - series with no creators (nothing to backfill) or whose creators were
//     hand-edited (manual_fields), which Apply must never overwrite.
//   - series whose creators already carry an id (already backfilled).
func (s *Store) ConfirmedSeriesMissingCreatorIDs() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT sm.series_id
		  FROM series_match sm
		  JOIN series_enrichment se ON se.series_id = sm.series_id
		 WHERE sm.state = ?
		   AND sm.volume_id != ''
		   AND se.comicvine_volume_id != 0
		   AND se.creators NOT IN ('', '[]')
		   AND se.creators NOT LIKE '%"id":%'
		   AND se.manual_fields NOT LIKE '%"creators"%'
		 ORDER BY sm.series_id`, MatchStateConfirmed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) NextWritebackJob() (string, bool, error) {
	var id string
	err := s.db.QueryRow(`SELECT comic_id FROM metadata_writeback
		WHERE status='pending' AND attempts < 3
		ORDER BY updated_at ASC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (s *Store) MarkWritebackDone(comicID string) error {
	_, err := s.db.Exec(`UPDATE metadata_writeback SET status='done', error=NULL, updated_at=? WHERE comic_id=?`,
		time.Now().UnixMilli(), comicID)
	return err
}

func (s *Store) MarkWritebackError(comicID, msg string) error {
	_, err := s.db.Exec(`UPDATE metadata_writeback
		SET attempts = attempts + 1,
		    error = ?,
		    status = CASE WHEN attempts + 1 >= 3 THEN 'error' ELSE 'pending' END,
		    updated_at = ?
		WHERE comic_id = ?`, msg, time.Now().UnixMilli(), comicID)
	return err
}

// WritebackStatusForSeries returns comicID -> [status, error] for issues
// of a series that have a writeback row.
func (s *Store) WritebackStatusForSeries(seriesID string) (map[string][2]string, error) {
	rows, err := s.db.Query(`SELECT w.comic_id, w.status, COALESCE(w.error, '')
		FROM metadata_writeback w JOIN issues i ON i.id = w.comic_id
		WHERE i.series_id = ?`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]string{}
	for rows.Next() {
		var id, status, errMsg string
		if err := rows.Scan(&id, &status, &errMsg); err != nil {
			return nil, err
		}
		out[id] = [2]string{status, errMsg}
	}
	return out, rows.Err()
}

// CountWritebackByStatus returns the number of writeback rows in each status
// (pending / done / error), for the match console summary and /healthz. Only
// rows whose issue still exists count: a file renamed outside the id
// migration leaves its old row behind, and that row's error is moot.
func (s *Store) CountWritebackByStatus() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT w.status, COUNT(*) FROM metadata_writeback w
		JOIN issues i ON i.id = w.comic_id GROUP BY w.status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

func (s *Store) MigrateWritebackID(oldID, newID string) error {
	_, err := s.db.Exec(`UPDATE OR REPLACE metadata_writeback SET comic_id=? WHERE comic_id=?`, newID, oldID)
	return err
}
