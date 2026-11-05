package server

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// authorizePush gates the device endpoints. The API is otherwise open and
// publicly reachable, so registration must not be.
func (s *Server) authorizePush(w http.ResponseWriter, r *http.Request) bool {
	if s.PushSecret == "" {
		http.NotFound(w, r)
		return false
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.PushSecret)) != 1 {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

func (s *Server) syncDevices() {
	if err := s.Notifier.SyncDevices(); err != nil {
		log.Printf("push: syncing device mirror: %v", err)
	}
}

func (s *Server) HandleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if !s.authorizePush(w, r) {
		return
	}
	var body struct{ Token, Env string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil ||
		body.Token == "" || (body.Env != "sandbox" && body.Env != "production") {
		writeJSONError(w, http.StatusBadRequest, `want {"token": "...", "env": "sandbox"|"production"}`)
		return
	}
	if err := s.Store.UpsertDeviceToken(body.Token, body.Env); err != nil {
		writeInternalError(w, err)
		return
	}
	s.syncDevices()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) HandleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	if !s.authorizePush(w, r) {
		return
	}
	if err := s.Store.DeleteDeviceToken(r.PathValue("token")); err != nil {
		writeInternalError(w, err)
		return
	}
	s.syncDevices()
	w.WriteHeader(http.StatusNoContent)
}
