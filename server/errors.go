package server

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
)

// writeJSONError sends {"error": msg} with the given status so clients can
// parse errors uniformly (study rec 5).
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeInternalError logs the real error server-side and returns a generic
// 500 — internal err.Error() text must not leak to clients.
func writeInternalError(w http.ResponseWriter, err error) {
	// Name the call site. A handler like HandleSync makes a dozen store calls,
	// and a bare error ("database disk image is malformed") gives no way to tell
	// which one failed — which cost hours on exactly that error.
	if _, file, line, ok := runtime.Caller(1); ok {
		log.Printf("internal error: %v (at %s:%d)", err, filepath.Base(file), line)
	} else {
		log.Printf("internal error: %v", err)
	}
	writeJSONError(w, http.StatusInternalServerError, "internal server error")
}
