package store

import (
	"time"

	"github.com/rthaines211/gutter-server/push"
)

// UpsertDeviceToken records a token, or refreshes its env and last-seen time.
func (s *Store) UpsertDeviceToken(token, env string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO device_tokens(token, env, created_at, last_seen_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(token) DO UPDATE SET env = excluded.env, last_seen_at = excluded.last_seen_at`,
		token, env, now, now)
	return err
}

func (s *Store) DeleteDeviceToken(token string) error {
	_, err := s.db.Exec(`DELETE FROM device_tokens WHERE token = ?`, token)
	return err
}

func (s *Store) ListDeviceTokens() ([]push.Device, error) {
	rows, err := s.db.Query(`SELECT token, env FROM device_tokens ORDER BY created_at, token`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []push.Device
	for rows.Next() {
		var d push.Device
		if err := rows.Scan(&d.Token, &d.Env); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
