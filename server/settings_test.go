package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/store"
)

func TestGetSettingsDefault(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/settings", nil)
	req.Header.Set("X-Device-ID", "ipad-1")
	w := httptest.NewRecorder()
	srv.HandleGetSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var settings store.ReaderSettings
	json.NewDecoder(w.Body).Decode(&settings)
	if settings.ReadingMode != "page" {
		t.Errorf("expected 'page', got %q", settings.ReadingMode)
	}
}

func TestPutAndGetSettings(t *testing.T) {
	srv := newTestServer(t)
	body := `{"reading_mode":"scroll","reading_direction":"rtl","theme":"sepia","tap_zones":false,"bubble_zoom":true,"sleep_timer_min":15}`
	req := httptest.NewRequest("PUT", "/settings", bytes.NewBufferString(body))
	req.Header.Set("X-Device-ID", "ipad-1")
	w := httptest.NewRecorder()
	srv.HandlePutSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/settings", nil)
	req.Header.Set("X-Device-ID", "ipad-1")
	w = httptest.NewRecorder()
	srv.HandleGetSettings(w, req)
	var settings store.ReaderSettings
	json.NewDecoder(w.Body).Decode(&settings)
	if settings.ReadingMode != "scroll" {
		t.Errorf("expected 'scroll', got %q", settings.ReadingMode)
	}
	if settings.Theme != "sepia" {
		t.Errorf("expected 'sepia', got %q", settings.Theme)
	}
	if !settings.BubbleZoom {
		t.Error("expected bubble_zoom true")
	}
}

func TestGetSettingsNoDeviceID(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/settings", nil)
	w := httptest.NewRecorder()
	srv.HandleGetSettings(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPutSettingsRejectsBadZoomMode(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("PUT", "/settings", bytes.NewBufferString(`{"zoom_mode":"sideways"}`))
	req.Header.Set("X-Device-ID", "ipad-1")
	w := httptest.NewRecorder()
	srv.HandlePutSettings(w, req)
	if w.Code != 400 {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetSettingsEmitsZoomModeAndDerivedBubbleZoom(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("PUT", "/settings", bytes.NewBufferString(`{"zoom_mode":"bubbles","panel_dim":true}`))
	req.Header.Set("X-Device-ID", "ipad-1")
	srv.HandlePutSettings(httptest.NewRecorder(), req)
	req = httptest.NewRequest("GET", "/settings", nil)
	req.Header.Set("X-Device-ID", "ipad-1")
	w := httptest.NewRecorder()
	srv.HandleGetSettings(w, req)
	body := w.Body.String()
	for _, want := range []string{`"zoom_mode":"bubbles"`, `"panel_dim":true`, `"bubble_zoom":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}

func TestSleepWarningSecRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("PUT", "/settings", bytes.NewBufferString(`{"sleep_warning_sec":120}`))
	req.Header.Set("X-Device-ID", "ipad-1")
	w := httptest.NewRecorder()
	srv.HandlePutSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/settings", nil)
	req.Header.Set("X-Device-ID", "ipad-1")
	w = httptest.NewRecorder()
	srv.HandleGetSettings(w, req)
	var settings store.ReaderSettings
	json.NewDecoder(w.Body).Decode(&settings)
	if settings.SleepWarningSec != 120 {
		t.Errorf("expected 120, got %d", settings.SleepWarningSec)
	}
}

func TestSleepWarningSecDefaultsTo60(t *testing.T) {
	srv := newTestServer(t)
	body := `{"reading_mode":"scroll","reading_direction":"rtl","theme":"sepia","tap_zones":false,"bubble_zoom":true,"sleep_timer_min":15}`
	req := httptest.NewRequest("PUT", "/settings", bytes.NewBufferString(body))
	req.Header.Set("X-Device-ID", "ipad-1")
	w := httptest.NewRecorder()
	srv.HandlePutSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/settings", nil)
	req.Header.Set("X-Device-ID", "ipad-1")
	w = httptest.NewRecorder()
	srv.HandleGetSettings(w, req)
	var settings store.ReaderSettings
	json.NewDecoder(w.Body).Decode(&settings)
	if settings.SleepWarningSec != 60 {
		t.Errorf("expected 60, got %d", settings.SleepWarningSec)
	}

	// Fresh device, no row at all.
	req = httptest.NewRequest("GET", "/settings", nil)
	req.Header.Set("X-Device-ID", "ipad-never-seen")
	w = httptest.NewRecorder()
	srv.HandleGetSettings(w, req)
	var fresh store.ReaderSettings
	json.NewDecoder(w.Body).Decode(&fresh)
	if fresh.SleepWarningSec != 60 {
		t.Errorf("fresh device: expected 60, got %d", fresh.SleepWarningSec)
	}
}
