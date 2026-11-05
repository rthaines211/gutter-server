package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// GetBubbleOrder returns the saved label order for a page, or nil when none.
func (s *Store) GetBubbleOrder(pageSHA string) ([]int, error) {
	var raw string
	err := s.db.QueryRow(`SELECT labels FROM bubble_order WHERE page_sha = ?`, pageSHA).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var labels []int
	return labels, json.Unmarshal([]byte(raw), &labels)
}

func (s *Store) SetBubbleOrder(pageSHA string, labels []int) error {
	raw, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO bubble_order (page_sha, labels, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(page_sha) DO UPDATE SET labels = excluded.labels, updated_at = excluded.updated_at
	`, pageSHA, string(raw), time.Now().Unix())
	return err
}

func (s *Store) DeleteBubbleOrder(pageSHA string) error {
	_, err := s.db.Exec(`DELETE FROM bubble_order WHERE page_sha = ?`, pageSHA)
	return err
}
