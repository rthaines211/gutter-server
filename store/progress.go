package store

import (
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type Progress struct {
	ComicID     string    `json:"comic_id"`
	CurrentPage int       `json:"current_page"`
	TotalPages  int       `json:"total_pages"`
	UpdatedAt   time.Time `json:"updated_at"`
	// ReadAt is the client-supplied content time (unix ms) of this progress
	// state. nil = legacy write (no staleness check). updated_at remains
	// server-receipt time.
	ReadAt *int64 `json:"read_at,omitempty"`
}

var ErrStaleWrite = errors.New("stale progress write")

// UpsertProgress writes p unless it is stale: when both the request and the
// stored row carry read_at and the request's is not newer, the write is
// rejected with ErrStaleWrite (study rec 4). Writes without read_at always
// apply (legacy clients).
func (s *Store) UpsertProgress(p Progress) error {
	res, err := s.db.Exec(`
		INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at, read_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?)
		ON CONFLICT(comic_id) DO UPDATE SET
			current_page = excluded.current_page,
			total_pages = excluded.total_pages,
			updated_at = CURRENT_TIMESTAMP,
			read_at = excluded.read_at
		WHERE excluded.read_at IS NULL
		   OR reading_progress.read_at IS NULL
		   OR excluded.read_at > reading_progress.read_at
	`, p.ComicID, p.CurrentPage, p.TotalPages, p.ReadAt)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrStaleWrite
	}
	return nil
}

// GetProgressForComics returns progress for every given comic id in one
// batch (chunked at idChunkSize), keyed by comic id. A comic id with no
// progress row is absent from the map — same as GetProgress returning
// ErrNotFound for that id.
func (s *Store) GetProgressForComics(comicIDs []string) (map[string]Progress, error) {
	result := make(map[string]Progress, len(comicIDs))
	for _, chunk := range chunkIDs(comicIDs) {
		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.Query(
			`SELECT comic_id, current_page, total_pages, updated_at
			 FROM reading_progress WHERE comic_id IN (`+placeholders(len(chunk))+`)`,
			args...,
		)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p Progress
			if err := rows.Scan(&p.ComicID, &p.CurrentPage, &p.TotalPages, &p.UpdatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			result[p.ComicID] = p
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) GetProgress(comicID string) (*Progress, error) {
	var p Progress
	err := s.db.QueryRow(`
		SELECT comic_id, current_page, total_pages, updated_at
		FROM reading_progress WHERE comic_id = ?
	`, comicID).Scan(&p.ComicID, &p.CurrentPage, &p.TotalPages, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) ListInProgress() ([]Progress, error) {
	rows, err := s.db.Query(`
		SELECT comic_id, current_page, total_pages, updated_at
		FROM reading_progress
		WHERE current_page < total_pages
		ORDER BY updated_at DESC
	`)
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

func (s *Store) BatchUpsertProgress(items []Progress) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at, read_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?)
		ON CONFLICT(comic_id) DO UPDATE SET
			current_page = excluded.current_page,
			total_pages = excluded.total_pages,
			updated_at = CURRENT_TIMESTAMP,
			read_at = excluded.read_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	// Batch writes are explicit user actions (mark read/unread) happening now:
	// stamp read_at so an older queued offline write can't undo them.
	now := time.Now().UnixMilli()
	for _, p := range items {
		if _, err := stmt.Exec(p.ComicID, p.CurrentPage, p.TotalPages, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) BatchDeleteProgress(comicIDs []string) error {
	if len(comicIDs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("DELETE FROM reading_progress WHERE comic_id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range comicIDs {
		if _, err := stmt.Exec(id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecentFinishedReadings returns the most-recently-updated reading_progress
// rows for issues in seriesID where the comic is finished
// (current_page >= total_pages AND total_pages > 0), ordered newest-first,
// up to limit rows. Returns empty slice (not nil) when there are none.
func (s *Store) RecentFinishedReadings(seriesID string, limit int) ([]Progress, error) {
	rows, err := s.db.Query(`
		SELECT rp.comic_id, rp.current_page, rp.total_pages, rp.updated_at
		FROM reading_progress rp
		WHERE rp.comic_id IN (SELECT id FROM issues WHERE series_id = ?)
		  AND rp.current_page >= rp.total_pages
		  AND rp.total_pages > 0
		ORDER BY rp.updated_at DESC
		LIMIT ?
	`, seriesID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Progress{}
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.ComicID, &p.CurrentPage, &p.TotalPages, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

