package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ryanhaines/comic-reader/store"
)

func (s *Server) HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("X-Device-ID")
	if deviceID == "" {
		writeJSONError(w, http.StatusBadRequest, "X-Device-ID header required")
		return
	}
	settings, err := s.Store.GetSettings(deviceID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

func (s *Server) HandlePutSettings(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("X-Device-ID")
	if deviceID == "" {
		writeJSONError(w, http.StatusBadRequest, "X-Device-ID header required")
		return
	}
	var settings store.ReaderSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	settings.DeviceID = deviceID
	if err := s.Store.UpsertSettings(settings); err != nil {
		if errors.Is(err, store.ErrBadSettings) {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
