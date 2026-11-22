package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rthaines211/gutter-server/store"
)

type userArcPutRequest struct {
	Name     string   `json:"name"`
	IssueIDs []string `json:"issue_ids"`
}

// HandlePutUserArc creates or replaces a user-made reading order. It reaches
// the app through /sync like imported arcs.
func (s *Server) HandlePutUserArc(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body userArcPutRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if id == "" || body.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "id and name are required")
		return
	}
	if err := s.Store.UpsertUserArc(id, body.Name, body.IssueIDs); err != nil {
		if errors.Is(err, store.ErrNotUserArc) {
			writeJSONError(w, http.StatusConflict, "imported arcs cannot be edited")
			return
		}
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) HandleDeleteUserArc(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteUserArc(r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotUserArc) {
			writeJSONError(w, http.StatusConflict, "imported arcs cannot be deleted")
			return
		}
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
