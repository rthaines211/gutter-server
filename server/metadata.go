package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rthaines211/gutter-server/metadata"
	"github.com/rthaines211/gutter-server/store"
)

var validFormats = map[string]bool{
	"": true, "single": true, "annual": true, "one-shot": true,
	"tpb": true, "hc": true, "omnibus": true,
}

func (s *Server) HandlePatchComicMetadata(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Title     *string `json:"title"`
		Summary   *string `json:"summary"`
		Number    *string `json:"number"`
		Format    *string `json:"format"`
		Writer    *string `json:"writer"`
		Penciller *string `json:"penciller"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Reject all-nil patches at the HTTP layer so we don't bump updated_at pointlessly.
	if req.Title == nil && req.Summary == nil && req.Number == nil &&
		req.Format == nil && req.Writer == nil && req.Penciller == nil {
		writeJSONError(w, http.StatusBadRequest, "empty patch")
		return
	}
	if req.Format != nil && !validFormats[strings.ToLower(*req.Format)] {
		writeJSONError(w, http.StatusBadRequest, "invalid format")
		return
	}
	seriesID, err := s.Store.SeriesIDForComic(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "comic not found")
		return
	}
	patch := store.IssueMetadataPatch{
		Title:     req.Title,
		Summary:   req.Summary,
		Number:    req.Number,
		Format:    req.Format,
		Writer:    req.Writer,
		Penciller: req.Penciller,
	}
	if err := s.Store.UpdateIssueMetadata(id, patch); err != nil {
		writeInternalError(w, err)
		return
	}
	if err := s.Store.EnqueueWriteback(id); err != nil {
		writeInternalError(w, err)
		return
	}
	s.Store.TouchSeries(seriesID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) HandlePatchSeriesMetadata(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name        *string   `json:"name"`
		Publisher   *string   `json:"publisher"`
		Writer      *string   `json:"writer"`
		Penciller   *string   `json:"penciller"`
		Description *string   `json:"description"`
		Year        *int      `json:"year"`
		Genres      *[]string `json:"genres"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Reject all-nil patches at the HTTP layer.
	if req.Name == nil && req.Publisher == nil && req.Writer == nil &&
		req.Penciller == nil && req.Description == nil && req.Year == nil && req.Genres == nil {
		writeJSONError(w, http.StatusBadRequest, "empty patch")
		return
	}
	issueIDs, err := s.Store.IssueIDsForSeries(id)
	if err != nil || len(issueIDs) == 0 {
		writeJSONError(w, http.StatusNotFound, "series not found")
		return
	}
	// Canonicalize a supplied publisher; nil (field absent) stays nil so the
	// patch semantics are unchanged.
	if req.Publisher != nil {
		canon := metadata.CanonicalPublisher(*req.Publisher)
		req.Publisher = &canon
	}
	patch := store.SeriesMetadataPatch{
		Name:        req.Name,
		Publisher:   req.Publisher,
		Writer:      req.Writer,
		Penciller:   req.Penciller,
		Description: req.Description,
		Year:        req.Year,
		Genres:      req.Genres,
	}
	if err := s.Store.UpdateSeriesMetadata(id, patch); err != nil {
		writeInternalError(w, err)
		return
	}
	if err := s.Store.EnqueueWriteback(issueIDs...); err != nil {
		writeInternalError(w, err)
		return
	}
	s.Store.TouchSeries(id)
	w.WriteHeader(http.StatusNoContent)
}
