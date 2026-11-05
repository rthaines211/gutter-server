package store

import (
	"errors"
	"testing"
)

func TestSettingsTapZoneLayoutDeriveFromBool(t *testing.T) {
	s := newTestStore(t)

	// Pre-migration row: tap_zones=false, then clear tap_zone_layout to simulate
	// a row written before the column existed (NULL).
	if err := s.UpsertSettings(ReaderSettings{DeviceID: "dev1", TapZones: false}); err != nil {
		t.Fatalf("UpsertSettings failed: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE reader_settings SET tap_zone_layout = NULL WHERE device_id = ?`, "dev1"); err != nil {
		t.Fatalf("failed to clear tap_zone_layout: %v", err)
	}
	got, err := s.GetSettings("dev1")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got.TapZoneLayout != "disabled" {
		t.Errorf("expected derived tap_zone_layout %q for tap_zones=false, got %q", "disabled", got.TapZoneLayout)
	}

	// Repeat with tap_zones=true -> "standard".
	if err := s.UpsertSettings(ReaderSettings{DeviceID: "dev1b", TapZones: true}); err != nil {
		t.Fatalf("UpsertSettings failed: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE reader_settings SET tap_zone_layout = NULL WHERE device_id = ?`, "dev1b"); err != nil {
		t.Fatalf("failed to clear tap_zone_layout: %v", err)
	}
	got, err = s.GetSettings("dev1b")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got.TapZoneLayout != "standard" {
		t.Errorf("expected derived tap_zone_layout %q for tap_zones=true, got %q", "standard", got.TapZoneLayout)
	}
}

// TestSettingsUpsertDerivesTapZoneLayoutFromBool exercises the actual old-client
// write path: an UpsertSettings call with TapZones set but TapZoneLayout empty
// (as an old app PUTing {tap_zones:false} with no tap_zone_layout would produce),
// with no NULL-column trick. UpsertSettings must derive the layout from the bool
// instead of hardcoding "standard", or GetSettings never gets a chance to re-derive.
func TestSettingsUpsertDerivesTapZoneLayoutFromBool(t *testing.T) {
	s := newTestStore(t)

	if err := s.UpsertSettings(ReaderSettings{DeviceID: "dev3", TapZones: false}); err != nil {
		t.Fatalf("UpsertSettings failed: %v", err)
	}
	got, err := s.GetSettings("dev3")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got.TapZoneLayout != "disabled" {
		t.Errorf("expected tap_zone_layout %q for tap_zones=false, got %q", "disabled", got.TapZoneLayout)
	}

	if err := s.UpsertSettings(ReaderSettings{DeviceID: "dev3b", TapZones: true}); err != nil {
		t.Fatalf("UpsertSettings failed: %v", err)
	}
	got, err = s.GetSettings("dev3b")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got.TapZoneLayout != "standard" {
		t.Errorf("expected tap_zone_layout %q for tap_zones=true, got %q", "standard", got.TapZoneLayout)
	}
}

func TestSettingsTapZoneLayoutRoundTrip(t *testing.T) {
	s := newTestStore(t)

	if err := s.UpsertSettings(ReaderSettings{DeviceID: "dev2", TapZoneLayout: "edge"}); err != nil {
		t.Fatalf("UpsertSettings failed: %v", err)
	}
	got, err := s.GetSettings("dev2")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got.TapZoneLayout != "edge" {
		t.Errorf("expected tap_zone_layout %q, got %q", "edge", got.TapZoneLayout)
	}
}

// The app PUTs page_fit, keep_screen_on and volume_keys; before these columns
// existed the server dropped them and every launch reset them to defaults.
func TestSettingsPageFitKeepScreenOnVolumeKeysRoundTrip(t *testing.T) {
	s := newTestStore(t)

	def, err := s.GetSettings("fresh")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if def.PageFit != "fitScreen" || !def.KeepScreenOn || !def.VolumeKeys {
		t.Errorf("defaults: got page_fit=%q keep_screen_on=%v volume_keys=%v", def.PageFit, def.KeepScreenOn, def.VolumeKeys)
	}

	in := DefaultSettings("dev9")
	in.PageFit = "fitWidth"
	in.KeepScreenOn = false
	in.VolumeKeys = false
	if err := s.UpsertSettings(in); err != nil {
		t.Fatalf("UpsertSettings failed: %v", err)
	}
	got, err := s.GetSettings("dev9")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got.PageFit != "fitWidth" || got.KeepScreenOn || got.VolumeKeys {
		t.Errorf("round trip: got page_fit=%q keep_screen_on=%v volume_keys=%v", got.PageFit, got.KeepScreenOn, got.VolumeKeys)
	}
}

func TestSettingsZoomModeRoundTripAndDerivedBubbleZoom(t *testing.T) {
	s := newTestStore(t)
	def, _ := s.GetSettings("fresh")
	if def.ZoomMode != "off" || def.PanelDim || def.BubbleZoom {
		t.Errorf("defaults: %+v", def)
	}
	in := DefaultSettings("d1")
	in.ZoomMode = "panels"
	in.PanelDim = true
	if err := s.UpsertSettings(in); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSettings("d1")
	if got.ZoomMode != "panels" || !got.PanelDim || got.BubbleZoom {
		t.Errorf("round trip: %+v", got)
	}
	in.ZoomMode = "bubbles"
	s.UpsertSettings(in)
	got, _ = s.GetSettings("d1")
	if got.ZoomMode != "bubbles" || !got.BubbleZoom {
		t.Errorf("bubbles: %+v", got)
	}
}

func TestSettingsZoomModeDerivedFromOldBubbleZoomFlag(t *testing.T) {
	s := newTestStore(t)
	in := DefaultSettings("old")
	in.ZoomMode = "" // an older app build sends only bubble_zoom: no zoom_mode in its JSON
	in.BubbleZoom = true
	s.UpsertSettings(in)
	if got, _ := s.GetSettings("old"); got.ZoomMode != "bubbles" {
		t.Errorf("want bubbles, got %q", got.ZoomMode)
	}
	in.BubbleZoom = false
	s.UpsertSettings(in)
	if got, _ := s.GetSettings("old"); got.ZoomMode != "off" {
		t.Errorf("want off, got %q", got.ZoomMode)
	}
}

func TestSettingsRejectsUnknownZoomMode(t *testing.T) {
	s := newTestStore(t)
	in := DefaultSettings("bad")
	in.ZoomMode = "sideways"
	if err := s.UpsertSettings(in); !errors.Is(err, ErrBadSettings) {
		t.Errorf("want ErrBadSettings, got %v", err)
	}
}

func TestSettingsMigrationLiftsBubbleZoomIntoZoomMode(t *testing.T) {
	path := t.TempDir() + "/m.db"
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	in := DefaultSettings("mig")
	in.ZoomMode = "bubbles"
	s.UpsertSettings(in)
	// a row written before zoom_mode existed: the flag set, the mode at its default
	if _, err := s.db.Exec("UPDATE reader_settings SET zoom_mode = 'off' WHERE device_id = 'mig'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = NewStore(path) // the migration runs on open
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, _ := s.GetSettings("mig"); got.ZoomMode != "bubbles" {
		t.Errorf("want bubbles after migration, got %q", got.ZoomMode)
	}
}
