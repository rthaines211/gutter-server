package store

import "time"

type Bookmark struct {
	ID        int       `json:"id"`
	ComicID   string    `json:"comic_id"`
	PageNum   int       `json:"page_num"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) AddBookmark(b Bookmark) error {
	_, err := s.db.Exec(`
		INSERT INTO bookmarks (comic_id, page_num, note, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(comic_id, page_num) DO UPDATE SET
			note = excluded.note
	`, b.ComicID, b.PageNum, b.Note)
	return err
}

func (s *Store) ListBookmarks(comicID string) ([]Bookmark, error) {
	rows, err := s.db.Query(`
		SELECT id, comic_id, page_num, note, created_at
		FROM bookmarks WHERE comic_id = ?
		ORDER BY page_num ASC
	`, comicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Bookmark
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.ComicID, &b.PageNum, &b.Note, &b.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, rows.Err()
}

func (s *Store) DeleteBookmark(comicID string, pageNum int) error {
	_, err := s.db.Exec(`
		DELETE FROM bookmarks WHERE comic_id = ? AND page_num = ?
	`, comicID, pageNum)
	return err
}
