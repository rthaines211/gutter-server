package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

var errTestSentinel = errors.New("secret detail")

func TestWriteJSONError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusBadRequest, "invalid JSON")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["error"] != "invalid JSON" {
		t.Fatalf("error = %q", body["error"])
	}
}

func TestWriteInternalErrorHidesDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	writeInternalError(rec, errTestSentinel)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] != "internal server error" {
		t.Fatalf("error = %q, must be generic (no err.Error() leak)", body["error"])
	}
}

// The bare "internal error: <err>" line gave no way to tell which of a
// handler's dozen store calls failed — which cost hours on a recurring
// "database disk image is malformed" from /sync.
func TestWriteInternalErrorNamesCallSite(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	w := httptest.NewRecorder()
	writeInternalError(w, errors.New("database disk image is malformed"))

	out := buf.String()
	if !strings.Contains(out, "errors_test.go:") {
		t.Errorf("log does not name the caller's file:line, got %q", out)
	}
	if !strings.Contains(out, "malformed") {
		t.Errorf("log lost the underlying error, got %q", out)
	}
	if strings.Contains(w.Body.String(), "malformed") {
		t.Errorf("internal detail leaked to the client: %s", w.Body.String())
	}
}
