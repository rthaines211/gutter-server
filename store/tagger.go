package store

import (
	"database/sql"
	"errors"
)

// NextTaggableComic returns the next issue id that has not been processed by
// the tagger and has no chapters yet. ok=false when the queue is empty.
func (s *Store) NextTaggableComic() (string, bool, error) {
	var id string
	err := s.db.QueryRow(`
		SELECT i.id FROM issues i
		WHERE i.id NOT IN (SELECT comic_id FROM tagger_state)
		  AND i.id NOT IN (SELECT DISTINCT comic_id FROM chapters)
		ORDER BY i.id ASC
		LIMIT 1
	`).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

// MarkTagged records a terminal outcome (processed/skipped/failed) for a comic
// so the worker never re-reads it.
func (s *Store) MarkTagged(comicID, status string) error {
	_, err := s.db.Exec(`
		INSERT INTO tagger_state (comic_id, status, processed_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(comic_id) DO UPDATE SET status = excluded.status, processed_at = CURRENT_TIMESTAMP
	`, comicID, status)
	return err
}

// WriteAutoChapters replaces the comic's chapters with an auto-detected set
// (source='auto', confirmed=0), in one transaction.
func (s *Store) WriteAutoChapters(comicID string, chs []Chapter, confidence string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM chapters WHERE comic_id = ?`, comicID); err != nil {
		return err
	}
	for _, c := range chs {
		if _, err := tx.Exec(`
			INSERT INTO chapters (comic_id, page_num, title, source, confidence, confirmed, created_at)
			VALUES (?, ?, ?, 'auto', ?, 0, CURRENT_TIMESTAMP)
		`, comicID, c.PageNum, c.Title, confidence); err != nil {
			return err
		}
	}
	return tx.Commit()
}
