package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

func TestPresetsAutoSeed(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/presets", nil)
	req.Header.Set("X-Device-ID", "test-device")
	w := httptest.NewRecorder()
	srv.HandleListPresets(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var presets []store.ReaderPreset
	json.NewDecoder(w.Body).Decode(&presets)
	if len(presets) != 3 {
		t.Fatalf("expected 3 auto-seeded presets, got %d", len(presets))
	}
	if presets[0].Name != "Western" {
		t.Errorf("expected Western, got %s", presets[0].Name)
	}
}

func TestPresetsCreateAndDelete(t *testing.T) {
	srv := newTestServer(t)
	body := `{"name":"Custom","reading_mode":"page","reading_direction":"ltr","theme":"sepia","page_fit":"fitWidth","page_animation":"fade","tap_zones":true,"volume_keys":false,"zoom_lock":true}`
	req := httptest.NewRequest("POST", "/presets", bytes.NewBufferString(body))
	req.Header.Set("X-Device-ID", "test-device")
	w := httptest.NewRecorder()
	srv.HandleCreatePreset(w, req)
	if w.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created map[string]int64
	json.NewDecoder(w.Body).Decode(&created)
	presetID := strconv.FormatInt(created["id"], 10)

	req = httptest.NewRequest("DELETE", "/presets/"+presetID, nil)
	req.SetPathValue("id", presetID)
	w = httptest.NewRecorder()
	srv.HandleDeletePreset(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteBuiltinPresetForbidden(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/presets", nil)
	req.Header.Set("X-Device-ID", "test-device")
	w := httptest.NewRecorder()
	srv.HandleListPresets(w, req)
	var presets []store.ReaderPreset
	json.NewDecoder(w.Body).Decode(&presets)

	id := strconv.Itoa(presets[0].ID)
	req = httptest.NewRequest("DELETE", "/presets/"+id, nil)
	req.SetPathValue("id", id)
	w = httptest.NewRecorder()
	srv.HandleDeletePreset(w, req)
	if w.Code != 403 {
		t.Errorf("expected 403 for builtin delete, got %d", w.Code)
	}
}
