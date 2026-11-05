package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenameTemplatesRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	router := srv.NewRouter()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/rename-templates", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d", rec.Code)
	}
	var got struct {
		Templates map[string]string `json:"templates"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Templates["tpb"] != "{series} Vol. {number} - {title} ({year})" {
		t.Fatalf("seed missing: %v", got.Templates)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("PUT", "/rename-templates",
		strings.NewReader(`{"templates":{"tpb":"{series} TP {number}"}}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/rename-templates", nil))
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Templates["tpb"] != "{series} TP {number}" {
		t.Fatalf("update not persisted: %v", got.Templates)
	}
}

func TestRenameTemplatesRejectsUnresolvable(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.NewRouter().ServeHTTP(rec, httptest.NewRequest("PUT", "/rename-templates",
		strings.NewReader(`{"templates":{"tpb":"{nonexistent_key}"}}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}
