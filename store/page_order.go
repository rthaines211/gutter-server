package store

import (
	"time"
)

// pageTables lists every table that stores a page index, with its column.
var pageTables = []struct{ table, col string }{
	{"bookmarks", "page_num"},
	{"chapters", "page_num"},
	{"page_flags", "page"},
}

// PageDataComicIDs returns every comic id that has a stored page number.
func (s *Store) PageDataComicIDs() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT comic_id FROM reading_progress
		UNION SELECT comic_id FROM bookmarks
		UNION SELECT comic_id FROM chapters
		UNION SELECT comic_id FROM page_flags`)
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

// RemapPages moves a comic's stored page numbers to new positions, m[old] = new,
// in one transaction. Values outside 0..len(m)-1 are left alone. Progress on
// the last page or beyond stays put (finished stays finished), and progress
// written at or after before (already in the new order) is skipped; remapped
// progress gets a fresh updated_at so /sync sends it. doneKey is recorded in
// migrations in the same transaction; a comic whose doneKey exists is skipped,
// so a retried pass never remaps a comic twice.
func (s *Store) RemapPages(comicID string, m []int, before time.Time, doneKey string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM migrations WHERE name=?`, doneKey).Scan(&done); err != nil || done > 0 {
		return err
	}

	var cur int
	err = tx.QueryRow(`SELECT current_page FROM reading_progress WHERE comic_id=? AND updated_at < ?`,
		comicID, before.UTC().Format("2006-01-02 15:04:05")).Scan(&cur)
	if err == nil && cur >= 0 && cur < len(m)-1 {
		if _, err := tx.Exec(`UPDATE reading_progress SET current_page=?, updated_at=CURRENT_TIMESTAMP WHERE comic_id=?`,
			m[cur], comicID); err != nil {
			return err
		}
	}

	for _, t := range pageTables {
		rows, err := tx.Query(`SELECT rowid, `+t.col+` FROM `+t.table+` WHERE comic_id=?`, comicID)
		if err != nil {
			return err
		}
		type row struct{ id, v int64 }
		var rs []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.v); err != nil {
				rows.Close()
				return err
			}
			rs = append(rs, r)
		}
		rows.Close()
		// Two phases so a permutation never trips UNIQUE(comic_id, page):
		// park each new value as -1-new, then flip all parked values back.
		for _, r := range rs {
			if r.v < 0 || r.v >= int64(len(m)) {
				continue
			}
			if _, err := tx.Exec(`UPDATE `+t.table+` SET `+t.col+`=? WHERE rowid=?`, -1-m[r.v], r.id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE `+t.table+` SET `+t.col+`=-1-`+t.col+` WHERE comic_id=? AND `+t.col+`<0`, comicID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO migrations (name, done_at) VALUES (?, ?)`, doneKey, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// MigrationDone reports whether the named one-time migration has finished.
func (s *Store) MigrationDone(name string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM migrations WHERE name=?`, name).Scan(&n)
	return n > 0, err
}

// MarkMigrationDone records that the named one-time migration has finished.
func (s *Store) MarkMigrationDone(name string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO migrations (name, done_at) VALUES (?, ?)`, name, time.Now().Unix())
	return err
}
