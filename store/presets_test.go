package store

import "testing"

func TestSeedAndListPresets(t *testing.T) {
	s := newTestStore(t)
	if err := s.SeedDefaultPresets("device-1"); err != nil {
		t.Fatalf("SeedDefaultPresets failed: %v", err)
	}
	list, err := s.ListPresets("device-1")
	if err != nil {
		t.Fatalf("ListPresets failed: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 presets, got %d", len(list))
	}
	if list[0].Name != "Western" || list[1].Name != "Manga" || list[2].Name != "Webtoon" {
		t.Errorf("unexpected names: %s, %s, %s", list[0].Name, list[1].Name, list[2].Name)
	}
	if !list[0].Builtin {
		t.Error("expected Western to be builtin")
	}
	if list[1].ReadingDirection != "rtl" {
		t.Errorf("expected Manga direction rtl, got %s", list[1].ReadingDirection)
	}
}

func TestCreateAndDeletePreset(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreatePreset(ReaderPreset{
		DeviceID: "device-1", Name: "Custom", ReadingMode: "page",
		ReadingDirection: "ltr", Theme: "black", PageFit: "fitScreen",
		PageAnimation: "fade", TapZones: true, VolumeKeys: true, ZoomLock: true,
	})
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}
	list, _ := s.ListPresets("device-1")
	if len(list) != 1 || list[0].Name != "Custom" || list[0].PageAnimation != "fade" {
		t.Errorf("unexpected preset: %+v", list)
	}
	if err := s.DeletePreset(int(id)); err != nil {
		t.Fatalf("DeletePreset failed: %v", err)
	}
	list, _ = s.ListPresets("device-1")
	if len(list) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(list))
	}
}

func TestDeleteBuiltinPresetRejected(t *testing.T) {
	s := newTestStore(t)
	s.SeedDefaultPresets("device-1")
	list, _ := s.ListPresets("device-1")
	err := s.DeletePreset(list[0].ID)
	if err != ErrBuiltinPreset {
		t.Errorf("expected ErrBuiltinPreset, got %v", err)
	}
}

func TestUpdatePreset(t *testing.T) {
	s := newTestStore(t)
	s.SeedDefaultPresets("device-1")
	list, _ := s.ListPresets("device-1")
	western := list[0]
	western.Theme = "sepia"
	if err := s.UpdatePreset(western.ID, western); err != nil {
		t.Fatalf("UpdatePreset failed: %v", err)
	}
	list, _ = s.ListPresets("device-1")
	if list[0].Theme != "sepia" {
		t.Errorf("expected sepia, got %s", list[0].Theme)
	}
}

func TestResetBuiltinPreset(t *testing.T) {
	s := newTestStore(t)
	s.SeedDefaultPresets("device-1")
	list, _ := s.ListPresets("device-1")
	western := list[0]
	western.Theme = "sepia"
	s.UpdatePreset(western.ID, western)
	if err := s.ResetPreset(western.ID); err != nil {
		t.Fatalf("ResetPreset failed: %v", err)
	}
	list, _ = s.ListPresets("device-1")
	if list[0].Theme != "black" {
		t.Errorf("expected reset to black, got %s", list[0].Theme)
	}
}

func TestPresetsDeviceIsolation(t *testing.T) {
	s := newTestStore(t)
	s.SeedDefaultPresets("device-1")
	s.CreatePreset(ReaderPreset{DeviceID: "device-2", Name: "Other"})
	list1, _ := s.ListPresets("device-1")
	list2, _ := s.ListPresets("device-2")
	if len(list1) != 3 {
		t.Errorf("device-1 expected 3, got %d", len(list1))
	}
	if len(list2) != 1 {
		t.Errorf("device-2 expected 1, got %d", len(list2))
	}
}
