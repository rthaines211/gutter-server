package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
)

// flagReasons maps each zoom mode to the reasons the reader offers in it.
var flagReasons = map[string][]string{
	"panels":  {"order", "panel_missing", "panel_cut"},
	"bubbles": {"order", "balloon_missing", "balloon_cut"},
}

// HandleFlagPage records that a page's panels or balloons came out wrong, with
// why: body {"reason", "zoom"}, a reason the reader offers in that zoom mode.
// The page must exist; flagging it again replaces the reason.
func (s *Server) HandleFlagPage(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("num"))
	if err != nil || num < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid page number")
		return
	}
	id := r.PathValue("id")
	absPath, err := s.resolveComicPath(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	if _, err := s.pageSHA(absPath, num); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct{ Reason, Zoom string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil ||
		!slices.Contains(flagReasons[body.Zoom], body.Reason) {
		writeJSONError(w, http.StatusBadRequest, `want {"reason": ..., "zoom": "panels"|"bubbles"} with a reason offered in that zoom`)
		return
	}
	if err := s.Store.FlagPage(id, num, body.Reason, body.Zoom); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleListPageFlags lists flags with each comic's library-relative path, so
// the labelling set can fetch the page without decoding ids.
func (s *Server) HandleListPageFlags(w http.ResponseWriter, r *http.Request) {
	flags, err := s.Store.ListPageFlags()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	type item struct {
		ComicID   string `json:"comic_id"`
		Path      string `json:"path"`
		Page      int    `json:"page"`
		Reason    string `json:"reason"`
		Zoom      string `json:"zoom"`
		FlaggedAt int64  `json:"flagged_at"`
	}
	out := make([]item, 0, len(flags))
	for _, f := range flags {
		p, err := idToPath(f.ComicID)
		if err != nil {
			continue
		}
		out = append(out, item{f.ComicID, p, f.Page, f.Reason, f.Zoom, f.FlaggedAt})
	}
	writeJSON(w, out)
}
