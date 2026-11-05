package store

import "time"

type Collection struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Icon      string    `json:"icon"`
	Color     string    `json:"color"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) CreateCollection(name, icon, color string) (int64, error) {
	var maxPos int
	s.db.QueryRow("SELECT COALESCE(MAX(position), -1) FROM collections").Scan(&maxPos)
	result, err := s.db.Exec(`
		INSERT INTO collections (name, icon, color, position) VALUES (?, ?, ?, ?)
	`, name, icon, color, maxPos+1)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) ListCollections() ([]Collection, error) {
	rows, err := s.db.Query(`
		SELECT id, name, icon, color, position, created_at, updated_at
		FROM collections ORDER BY position ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Collection
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Icon, &c.Color, &c.Position, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

func (s *Store) GetCollection(id int) (*Collection, error) {
	var c Collection
	err := s.db.QueryRow(`
		SELECT id, name, icon, color, position, created_at, updated_at
		FROM collections WHERE id = ?
	`, id).Scan(&c.ID, &c.Name, &c.Icon, &c.Color, &c.Position, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) UpdateCollection(id int, name, icon, color string) error {
	_, err := s.db.Exec(`
		UPDATE collections SET name = ?, icon = ?, color = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, name, icon, color, id)
	return err
}

func (s *Store) DeleteCollection(id int) error {
	_, err := s.db.Exec("DELETE FROM collections WHERE id = ?", id)
	return err
}

func (s *Store) AddSeriesToCollection(collectionID int, seriesID string) error {
	var maxPos int
	s.db.QueryRow(
		"SELECT COALESCE(MAX(position), -1) FROM collection_series WHERE collection_id = ?",
		collectionID,
	).Scan(&maxPos)
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO collection_series (collection_id, series_id, position)
		VALUES (?, ?, ?)
	`, collectionID, seriesID, maxPos+1)
	return err
}

func (s *Store) RemoveSeriesFromCollection(collectionID int, seriesID string) error {
	_, err := s.db.Exec(
		"DELETE FROM collection_series WHERE collection_id = ? AND series_id = ?",
		collectionID, seriesID,
	)
	return err
}

func (s *Store) ListCollectionSeriesIDs(collectionID int) ([]string, error) {
	rows, err := s.db.Query(
		"SELECT series_id FROM collection_series WHERE collection_id = ? ORDER BY position ASC",
		collectionID,
	)
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

func (s *Store) ReorderCollectionSeries(collectionID int, seriesIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, sid := range seriesIDs {
		if _, err := tx.Exec(
			"UPDATE collection_series SET position = ? WHERE collection_id = ? AND series_id = ?",
			i, collectionID, sid,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
