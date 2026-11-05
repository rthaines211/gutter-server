package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rthaines211/gutter-server/store"
)

type ReadingProgressResponse struct {
	ComicID     string          `json:"comic_id"`
	CurrentPage int             `json:"current_page"`
	TotalPages  int             `json:"total_pages"`
	UpdatedAt   string          `json:"updated_at"`
	SeriesID    string          `json:"series_id,omitempty"`
	Chapters    []store.Chapter `json:"chapters,omitempty"`
}

type ReadingListResponse struct {
	InProgress []ReadingProgressResponse `json:"in_progress"`
}

type PutReadingRequest struct {
	CurrentPage int              `json:"current_page"`
	TotalPages  int              `json:"total_pages"`
	Bookmarks   []store.Bookmark `json:"bookmarks,omitempty"`
	// Pointer so we can tell "field omitted" (nil → leave chapters untouched)
	// from "field present, empty" ([] → clear all). A routine progress save
	// omits chapters; only an explicit chapter write replaces the set.
	Chapters *[]store.Chapter `json:"chapters"`
	// Optional client content-time (unix ms). Stale writes (≤ stored) get 409.
	ReadAt *int64 `json:"read_at"`
}

func (s *Server) HandleGetReading(w http.ResponseWriter, r *http.Request) {
	inProgress, err := s.Store.ListInProgress()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	result := make([]ReadingProgressResponse, 0, len(inProgress))
	for _, p := range inProgress {
		resp := ReadingProgressResponse{
			ComicID:     p.ComicID,
			CurrentPage: p.CurrentPage,
			TotalPages:  p.TotalPages,
			UpdatedAt:   p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		// The issue row knows its series; a folder-name match would miss
		// issues in a twin folder (ScanLibrary).
		if id, err := s.Store.SeriesIDForComic(p.ComicID); err == nil {
			resp.SeriesID = id
		}
		result = append(result, resp)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ReadingListResponse{InProgress: result})
}

type BatchReadingItem struct {
	ComicID     string `json:"comic_id"`
	CurrentPage int    `json:"current_page"`
	TotalPages  int    `json:"total_pages"`
}

type BatchReadingRequest struct {
	Items []BatchReadingItem `json:"items"`
}

func (s *Server) HandleBatchReading(w http.ResponseWriter, r *http.Request) {
	var req BatchReadingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	items := make([]store.Progress, len(req.Items))
	for i, item := range req.Items {
		items[i] = store.Progress{
			ComicID:     item.ComicID,
			CurrentPage: item.CurrentPage,
			TotalPages:  item.TotalPages,
		}
	}
	if err := s.Store.BatchUpsertProgress(items); err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type BatchDeleteReadingRequest struct {
	ComicIDs []string `json:"comic_ids"`
}

// HandleBatchDeleteReading deletes progress records (mark-series-unread).
// Explicit user intent: bypasses the read_at staleness check by design.
func (s *Server) HandleBatchDeleteReading(w http.ResponseWriter, r *http.Request) {
	var req BatchDeleteReadingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.Store.BatchDeleteProgress(req.ComicIDs); err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) HandlePutReading(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req PutReadingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.Store.UpsertProgress(store.Progress{
		ComicID: id, CurrentPage: req.CurrentPage, TotalPages: req.TotalPages, ReadAt: req.ReadAt,
	}); err != nil {
		if errors.Is(err, store.ErrStaleWrite) {
			writeJSONError(w, http.StatusConflict, "stale progress write")
			return
		}
		writeInternalError(w, err)
		return
	}
	for _, bm := range req.Bookmarks {
		bm.ComicID = id
		if err := s.Store.AddBookmark(bm); err != nil {
			writeInternalError(w, err)
			return
		}
	}
	// Only replace chapters when the client explicitly sent the field. A
	// chapters-less progress save (page turn) must not wipe the chapter set.
	if req.Chapters != nil {
		if err := s.Store.ReplaceChapters(id, *req.Chapters); err != nil {
			writeInternalError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
