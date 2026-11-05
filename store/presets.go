package store

import "time"

type ReaderPreset struct {
	ID               int       `json:"id"`
	DeviceID         string    `json:"device_id"`
	Name             string    `json:"name"`
	Builtin          bool      `json:"builtin"`
	ReadingMode      string    `json:"reading_mode"`
	ReadingDirection string    `json:"reading_direction"`
	Theme            string    `json:"theme"`
	PageFit          string    `json:"page_fit"`
	PageAnimation    string    `json:"page_animation"`
	TapZones         bool      `json:"tap_zones"`
	VolumeKeys       bool      `json:"volume_keys"`
	ZoomLock         bool      `json:"zoom_lock"`
	CreatedAt        time.Time `json:"created_at"`
}

var defaultPresets = []ReaderPreset{
	{Name: "Western", Builtin: true, ReadingMode: "page", ReadingDirection: "ltr", Theme: "black", PageFit: "fitScreen", PageAnimation: "slide", TapZones: true, VolumeKeys: true, ZoomLock: false},
	{Name: "Manga", Builtin: true, ReadingMode: "page", ReadingDirection: "rtl", Theme: "black", PageFit: "fitWidth", PageAnimation: "slide", TapZones: true, VolumeKeys: true, ZoomLock: false},
	{Name: "Webtoon", Builtin: true, ReadingMode: "scroll", ReadingDirection: "ltr", Theme: "black", PageFit: "fitWidth", PageAnimation: "none", TapZones: true, VolumeKeys: false, ZoomLock: false},
}

func (s *Store) ListPresets(deviceID string) ([]ReaderPreset, error) {
	rows, err := s.db.Query(`
		SELECT id, device_id, name, builtin, reading_mode, reading_direction, theme,
		       page_fit, page_animation, tap_zones, volume_keys, zoom_lock, created_at
		FROM reader_presets WHERE device_id = ? ORDER BY id ASC
	`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ReaderPreset
	for rows.Next() {
		var p ReaderPreset
		if err := rows.Scan(&p.ID, &p.DeviceID, &p.Name, &p.Builtin,
			&p.ReadingMode, &p.ReadingDirection, &p.Theme,
			&p.PageFit, &p.PageAnimation, &p.TapZones, &p.VolumeKeys, &p.ZoomLock,
			&p.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (s *Store) SeedDefaultPresets(deviceID string) error {
	for _, p := range defaultPresets {
		_, err := s.db.Exec(`
			INSERT INTO reader_presets (device_id, name, builtin, reading_mode, reading_direction,
			                            theme, page_fit, page_animation, tap_zones, volume_keys, zoom_lock)
			VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
		`, deviceID, p.Name, p.ReadingMode, p.ReadingDirection, p.Theme,
			p.PageFit, p.PageAnimation, p.TapZones, p.VolumeKeys, p.ZoomLock)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreatePreset(p ReaderPreset) (int64, error) {
	result, err := s.db.Exec(`
		INSERT INTO reader_presets (device_id, name, builtin, reading_mode, reading_direction,
		                            theme, page_fit, page_animation, tap_zones, volume_keys, zoom_lock)
		VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)
	`, p.DeviceID, p.Name, p.ReadingMode, p.ReadingDirection, p.Theme,
		p.PageFit, p.PageAnimation, p.TapZones, p.VolumeKeys, p.ZoomLock)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) UpdatePreset(id int, p ReaderPreset) error {
	_, err := s.db.Exec(`
		UPDATE reader_presets SET name = ?, reading_mode = ?, reading_direction = ?,
		       theme = ?, page_fit = ?, page_animation = ?, tap_zones = ?, volume_keys = ?, zoom_lock = ?
		WHERE id = ?
	`, p.Name, p.ReadingMode, p.ReadingDirection, p.Theme,
		p.PageFit, p.PageAnimation, p.TapZones, p.VolumeKeys, p.ZoomLock, id)
	return err
}

func (s *Store) DeletePreset(id int) error {
	var builtin bool
	err := s.db.QueryRow("SELECT builtin FROM reader_presets WHERE id = ?", id).Scan(&builtin)
	if err != nil {
		return err
	}
	if builtin {
		return ErrBuiltinPreset
	}
	_, err = s.db.Exec("DELETE FROM reader_presets WHERE id = ?", id)
	return err
}

func (s *Store) ResetPreset(id int) error {
	var name string
	var builtin bool
	err := s.db.QueryRow("SELECT name, builtin FROM reader_presets WHERE id = ?", id).Scan(&name, &builtin)
	if err != nil {
		return err
	}
	if !builtin {
		return ErrNotBuiltinPreset
	}
	for _, dp := range defaultPresets {
		if dp.Name == name {
			_, err := s.db.Exec(`
				UPDATE reader_presets SET reading_mode = ?, reading_direction = ?,
				       theme = ?, page_fit = ?, page_animation = ?, tap_zones = ?, volume_keys = ?, zoom_lock = ?
				WHERE id = ?
			`, dp.ReadingMode, dp.ReadingDirection, dp.Theme,
				dp.PageFit, dp.PageAnimation, dp.TapZones, dp.VolumeKeys, dp.ZoomLock, id)
			return err
		}
	}
	return ErrNotFound
}

var ErrBuiltinPreset = errorString("cannot delete builtin preset")
var ErrNotBuiltinPreset = errorString("preset is not builtin")

type errorString string

func (e errorString) Error() string { return string(e) }
