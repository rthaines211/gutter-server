package store

// Delta-sync queries. `since` is Unix seconds; updated_at columns are SQLite
// DATETIME text in UTC, so compare via strftime('%s', ...). The CAST is
// required: strftime returns TEXT and a bare TEXT > INTEGER comparison in
// SQLite is decided by storage class, not value.

func (s *Store) ListSeriesUpdatedSince(sinceUnixSec int64) ([]Series, error) {
	// Enrichment writes series_enrichment without touching series.updated_at, so
	// a newly matched publisher/year would never reach a client that already
	// synced the row. enriched_at is UnixMilli (both writers use it) against a
	// seconds watermark, hence the *1000. That widens the handler's 1s overlap to
	// the whole second; re-sending is harmless since client upserts are idempotent.
	rows, err := s.db.Query(seriesSelect+`
		WHERE CAST(strftime('%s', s.updated_at) AS INTEGER) > ?
		   OR COALESCE(se.enriched_at, 0) > ? * 1000
		ORDER BY s.name COLLATE NOCASE ASC
	`, sinceUnixSec, sinceUnixSec)
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

func (s *Store) ListProgressUpdatedSince(sinceUnixSec int64) ([]Progress, error) {
	rows, err := s.db.Query(`
		SELECT comic_id, current_page, total_pages, updated_at
		FROM reading_progress
		WHERE CAST(strftime('%s', updated_at) AS INTEGER) > ?
	`, sinceUnixSec)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Progress
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.ComicID, &p.CurrentPage, &p.TotalPages, &p.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (s *Store) ListSeriesIDs() ([]string, error) {
	return s.listIDs(`SELECT id FROM series`)
}

func (s *Store) ListProgressComicIDs() ([]string, error) {
	return s.listIDs(`SELECT comic_id FROM reading_progress`)
}

func (s *Store) listIDs(query string) ([]string, error) {
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
