package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrBadSettings: a value outside its allowed set; the handler answers 400.
var ErrBadSettings = errors.New("bad settings")

type ReaderSettings struct {
	DeviceID         string `json:"device_id"`
	ReadingMode      string `json:"reading_mode"`
	ReadingDirection string `json:"reading_direction"`
	Theme            string `json:"theme"`
	TapZones         bool   `json:"tap_zones"`
	TapZoneLayout    string `json:"tap_zone_layout"`
	// ZoomMode is off | panels | bubbles. BubbleZoom is derived from it on read
	// and, when an older app sends only the bool, sets it on write. It is still
	// stored in the old panel_detection column.
	ZoomMode        string `json:"zoom_mode"`
	BubbleZoom      bool   `json:"bubble_zoom"`
	PanelDim        bool   `json:"panel_dim"`
	SleepTimerMin   int    `json:"sleep_timer_min"`
	SleepWarningSec int    `json:"sleep_warning_sec"`
	PageAnimation   string `json:"page_animation"`
	ZoomLock        bool   `json:"zoom_lock"`
	PageFit         string `json:"page_fit"`
	KeepScreenOn    bool   `json:"keep_screen_on"`
	VolumeKeys      bool   `json:"volume_keys"`
}

func DefaultSettings(deviceID string) ReaderSettings {
	return ReaderSettings{
		DeviceID:         deviceID,
		ReadingMode:      "page",
		ReadingDirection: "ltr",
		Theme:            "black",
		TapZones:         true,
		TapZoneLayout:    "standard",
		ZoomMode:         "off",
		BubbleZoom:       false,
		PanelDim:         false,
		SleepTimerMin:    0,
		SleepWarningSec:  60,
		PageAnimation:    "slide",
		ZoomLock:         false,
		PageFit:          "fitScreen",
		KeepScreenOn:     true,
		VolumeKeys:       true,
	}
}

func (s *Store) GetSettings(deviceID string) (*ReaderSettings, error) {
	var rs ReaderSettings
	err := s.db.QueryRow(`
		SELECT device_id, reading_mode, reading_direction, theme,
		       tap_zones, panel_detection, sleep_timer_min,
		       page_animation, zoom_lock, COALESCE(tap_zone_layout, ''),
		       page_fit, keep_screen_on, volume_keys, zoom_mode, panel_dim,
		       sleep_warning_sec
		FROM reader_settings WHERE device_id = ?
	`, deviceID).Scan(
		&rs.DeviceID, &rs.ReadingMode, &rs.ReadingDirection, &rs.Theme,
		&rs.TapZones, &rs.BubbleZoom, &rs.SleepTimerMin,
		&rs.PageAnimation, &rs.ZoomLock, &rs.TapZoneLayout,
		&rs.PageFit, &rs.KeepScreenOn, &rs.VolumeKeys, &rs.ZoomMode, &rs.PanelDim,
		&rs.SleepWarningSec,
	)
	if err == sql.ErrNoRows {
		defaults := DefaultSettings(deviceID)
		return &defaults, nil
	}
	if err != nil {
		return nil, err
	}
	if rs.TapZoneLayout == "" {
		if rs.TapZones {
			rs.TapZoneLayout = "standard"
		} else {
			rs.TapZoneLayout = "disabled"
		}
	}
	rs.BubbleZoom = rs.ZoomMode == "bubbles"
	return &rs, nil
}

func (s *Store) UpsertSettings(rs ReaderSettings) error {
	if rs.ZoomMode == "" { // an older app build sends only bubble_zoom
		if rs.BubbleZoom {
			rs.ZoomMode = "bubbles"
		} else {
			rs.ZoomMode = "off"
		}
	}
	switch rs.ZoomMode {
	case "off", "panels", "bubbles":
	default:
		return fmt.Errorf("%w: zoom_mode %q", ErrBadSettings, rs.ZoomMode)
	}
	rs.BubbleZoom = rs.ZoomMode == "bubbles"
	if rs.PageAnimation == "" {
		rs.PageAnimation = "slide"
	}
	if rs.SleepWarningSec <= 0 {
		rs.SleepWarningSec = 60
	}
	if rs.PageFit == "" {
		rs.PageFit = "fitScreen"
	}
	if rs.TapZoneLayout == "" {
		if rs.TapZones {
			rs.TapZoneLayout = "standard"
		} else {
			rs.TapZoneLayout = "disabled"
		}
	}
	_, err := s.db.Exec(`
		INSERT INTO reader_settings (device_id, reading_mode, reading_direction, theme,
		                             tap_zones, panel_detection, sleep_timer_min,
		                             page_animation, zoom_lock, tap_zone_layout,
		                             page_fit, keep_screen_on, volume_keys, zoom_mode, panel_dim,
		                             sleep_warning_sec)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			reading_mode = excluded.reading_mode,
			reading_direction = excluded.reading_direction,
			theme = excluded.theme,
			tap_zones = excluded.tap_zones,
			panel_detection = excluded.panel_detection,
			sleep_timer_min = excluded.sleep_timer_min,
			page_animation = excluded.page_animation,
			zoom_lock = excluded.zoom_lock,
			tap_zone_layout = excluded.tap_zone_layout,
			page_fit = excluded.page_fit,
			keep_screen_on = excluded.keep_screen_on,
			volume_keys = excluded.volume_keys,
			zoom_mode = excluded.zoom_mode,
			panel_dim = excluded.panel_dim,
			sleep_warning_sec = excluded.sleep_warning_sec
	`, rs.DeviceID, rs.ReadingMode, rs.ReadingDirection, rs.Theme,
		rs.TapZones, rs.BubbleZoom, rs.SleepTimerMin,
		rs.PageAnimation, rs.ZoomLock, rs.TapZoneLayout,
		rs.PageFit, rs.KeepScreenOn, rs.VolumeKeys, rs.ZoomMode, rs.PanelDim,
		rs.SleepWarningSec)
	return err
}
