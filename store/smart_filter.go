package store

import (
	"database/sql"
	"errors"
	"time"
)

type SmartFilter struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`
	Icon       string    `json:"icon"`
	Color      string    `json:"color"`
	Conditions string    `json:"conditions"`
	Position   int       `json:"position"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s *Store) CreateSmartFilter(name, icon, color, conditions string) (int64, error) {
	var maxPos int
	s.db.QueryRow("SELECT COALESCE(MAX(position), -1) FROM smart_filters").Scan(&maxPos)
	result, err := s.db.Exec(`
		INSERT INTO smart_filters (name, icon, color, conditions, position)
		VALUES (?, ?, ?, ?, ?)
	`, name, icon, color, conditions, maxPos+1)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) GetSmartFilter(id int) (*SmartFilter, error) {
	var sf SmartFilter
	err := s.db.QueryRow(`
		SELECT id, name, icon, color, conditions, position, created_at, updated_at
		FROM smart_filters WHERE id = ?
	`, id).Scan(&sf.ID, &sf.Name, &sf.Icon, &sf.Color, &sf.Conditions, &sf.Position, &sf.CreatedAt, &sf.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sf, nil
}

func (s *Store) ListSmartFilters() ([]SmartFilter, error) {
	rows, err := s.db.Query(`
		SELECT id, name, icon, color, conditions, position, created_at, updated_at
		FROM smart_filters ORDER BY position ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []SmartFilter
	for rows.Next() {
		var sf SmartFilter
		if err := rows.Scan(&sf.ID, &sf.Name, &sf.Icon, &sf.Color, &sf.Conditions,
			&sf.Position, &sf.CreatedAt, &sf.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, sf)
	}
	return list, rows.Err()
}

func (s *Store) UpdateSmartFilter(id int, name, icon, color, conditions string) error {
	_, err := s.db.Exec(`
		UPDATE smart_filters SET name = ?, icon = ?, color = ?, conditions = ?,
		       updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, name, icon, color, conditions, id)
	return err
}

func (s *Store) DeleteSmartFilter(id int) error {
	_, err := s.db.Exec("DELETE FROM smart_filters WHERE id = ?", id)
	return err
}
