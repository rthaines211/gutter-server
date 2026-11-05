package store

import "time"

// PageFlag is a page the reader flagged as having wrong panels or balloons;
// the panel labelling set imports these first.
type PageFlag struct {
	ComicID   string `json:"comic_id"`
	Page      int    `json:"page"`
	Reason    string `json:"reason"` // order | panel_missing | panel_cut | balloon_missing | balloon_cut; "" before reasons existed
	Zoom      string `json:"zoom"`   // the reader's zoom mode when flagged: panels | bubbles
	FlaggedAt int64  `json:"flagged_at"`
}

// FlagPage records a flag; flagging the same page again replaces its reason and time.
func (s *Store) FlagPage(comicID string, page int, reason, zoom string) error {
	_, err := s.db.Exec(`
		INSERT INTO page_flags (comic_id, page, reason, zoom, flagged_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(comic_id, page) DO UPDATE SET
			reason = excluded.reason, zoom = excluded.zoom, flagged_at = excluded.flagged_at
	`, comicID, page, reason, zoom, time.Now().Unix())
	return err
}

// ListPageFlags returns every flag, newest first.
func (s *Store) ListPageFlags() ([]PageFlag, error) {
	rows, err := s.db.Query(`SELECT comic_id, page, reason, zoom, flagged_at FROM page_flags ORDER BY flagged_at DESC, comic_id, page`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PageFlag{}
	for rows.Next() {
		var f PageFlag
		if err := rows.Scan(&f.ComicID, &f.Page, &f.Reason, &f.Zoom, &f.FlaggedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
