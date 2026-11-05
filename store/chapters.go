package store

import "time"

type Chapter struct {
	ComicID   string    `json:"comic_id"`
	PageNum   int       `json:"page_num"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) AddChapter(c Chapter) error {
	_, err := s.db.Exec(`
		INSERT INTO chapters (comic_id, page_num, title, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(comic_id, page_num) DO UPDATE SET title = excluded.title
	`, c.ComicID, c.PageNum, c.Title)
	return err
}

// ListChaptersForComics returns chapters for every given comic id in one
// batch (chunked at idChunkSize), grouped by comic id. A comic id with no
// chapters is simply absent from the map — same as ListChapters returning a
// nil slice for that id — so callers that follow ListChapters' nil-means-none
// convention don't need to change.
func (s *Store) ListChaptersForComics(comicIDs []string) (map[string][]Chapter, error) {
	result := make(map[string][]Chapter, len(comicIDs))
	for _, chunk := range chunkIDs(comicIDs) {
		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.Query(
			`SELECT comic_id, page_num, title, created_at
			 FROM chapters WHERE comic_id IN (`+placeholders(len(chunk))+`)
			 ORDER BY comic_id ASC, page_num ASC`,
			args...,
		)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c Chapter
			if err := rows.Scan(&c.ComicID, &c.PageNum, &c.Title, &c.CreatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			result[c.ComicID] = append(result[c.ComicID], c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) ListChapters(comicID string) ([]Chapter, error) {
	rows, err := s.db.Query(`
		SELECT comic_id, page_num, title, created_at
		FROM chapters WHERE comic_id = ?
		ORDER BY page_num ASC
	`, comicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Chapter
	for rows.Next() {
		var c Chapter
		if err := rows.Scan(&c.ComicID, &c.PageNum, &c.Title, &c.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// ReplaceChapters makes the provided set authoritative for the comic: removes
// any existing chapters not in the set and upserts the rest, in one transaction.
func (s *Store) ReplaceChapters(comicID string, chapters []Chapter) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM chapters WHERE comic_id = ?`, comicID); err != nil {
		return err
	}
	for _, c := range chapters {
		if _, err := tx.Exec(`
			INSERT INTO chapters (comic_id, page_num, title, created_at)
			VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		`, comicID, c.PageNum, c.Title); err != nil {
			return err
		}
	}
	return tx.Commit()
}
