package store

import (
	"fmt"
	"time"
)

// Queue is a named, ordered list of series. Its ID is client-generated (a
// string), so the app can create queues offline and upsert them by ID.
type Queue struct {
	ID        string
	Name      string
	Icon      string
	Color     string
	Position  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UpsertQueue creates or replaces a queue and its ordered membership in one
// transaction. seriesIDs is stored in the given order (index == position).
func (s *Store) UpsertQueue(q Queue, seriesIDs []string) error {
	if q.ID == "" {
		return fmt.Errorf("store: UpsertQueue: queue ID must not be empty")
	}
	if q.Icon == "" {
		q.Icon = "bookmark"
	}
	if q.Color == "" {
		q.Color = "#FFB74D"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO queues (id, name, icon, color, position, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			icon = excluded.icon,
			color = excluded.color,
			position = excluded.position,
			updated_at = CURRENT_TIMESTAMP
	`, q.ID, q.Name, q.Icon, q.Color, q.Position); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM queue_series WHERE queue_id = ?`, q.ID); err != nil {
		return err
	}
	for i, sid := range seriesIDs {
		if _, err := tx.Exec(`
			INSERT INTO queue_series (queue_id, series_id, position) VALUES (?, ?, ?)
		`, q.ID, sid, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteQueue(id string) error {
	_, err := s.db.Exec(`DELETE FROM queues WHERE id = ?`, id)
	return err
}

func (s *Store) ListQueues() ([]Queue, error) {
	return s.queryQueues(`
		SELECT id, name, icon, color, position, created_at, updated_at
		FROM queues ORDER BY position ASC`)
}

// ListQueuesUpdatedSince returns queues whose updated_at is newer than the
// given unix-seconds watermark. Mirrors ListSeriesUpdatedSince in sync.go.
func (s *Store) ListQueuesUpdatedSince(sinceUnixSec int64) ([]Queue, error) {
	return s.queryQueues(`
		SELECT id, name, icon, color, position, created_at, updated_at
		FROM queues
		WHERE CAST(strftime('%s', updated_at) AS INTEGER) > ?
		ORDER BY position ASC`, sinceUnixSec)
}

func (s *Store) queryQueues(query string, args ...interface{}) ([]Queue, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Queue
	for rows.Next() {
		var q Queue
		if err := rows.Scan(&q.ID, &q.Name, &q.Icon, &q.Color, &q.Position, &q.CreatedAt, &q.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, q)
	}
	return list, rows.Err()
}

func (s *Store) ListQueueSeriesIDs(queueID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT series_id FROM queue_series WHERE queue_id = ? ORDER BY position ASC`, queueID)
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

func (s *Store) ListQueueIDs() ([]string, error) {
	return s.listIDs(`SELECT id FROM queues`)
}
