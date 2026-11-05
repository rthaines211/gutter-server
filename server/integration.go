package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// relToComicDir maps an absolute path (as the enrichment app sees it) to a
// path relative to one of ComicDirs, by textual prefix — no stat, because the
// old path of a move no longer exists when we're told about it.
func (s *Server) relToComicDir(abs string) (string, bool) {
	for _, root := range s.ComicDirs {
		if strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return abs[len(root)+1:], true
		}
	}
	return "", false
}

// HandleFileMove implements the enrichment app's move reconciliation: remap
// the old path-derived comic ID to the new one so reading progress survives
// a rename/move/convert performed outside this server.
func (s *Server) HandleFileMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPath     string `json:"old_path"`
		NewPath     string `json:"new_path"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OldPath == "" || req.NewPath == "" {
		writeJSONError(w, http.StatusBadRequest, "old_path and new_path required")
		return
	}
	oldRel, ok := s.relToComicDir(req.OldPath)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "old_path outside comic dirs")
		return
	}
	newRel, ok := s.relToComicDir(req.NewPath)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "new_path outside comic dirs")
		return
	}
	oldID, newID := pathToID(oldRel), pathToID(newRel)
	if !s.Store.ComicExists(oldID) {
		writeJSONError(w, http.StatusNotFound, "no record for old_path")
		return
	}
	if s.Store.ComicExists(newID) {
		writeJSONError(w, http.StatusConflict, "a record already exists for new_path")
		return
	}
	if err := s.Store.MigrateComicIDFull(oldID, newID, filepath.Base(newRel)); err != nil {
		writeInternalError(w, err)
		return
	}
	log.Printf("file-move: %s -> %s (fingerprint %s)", oldRel, newRel, req.Fingerprint)
	if s.CoverDir != "" {
		os.Remove(filepath.Join(s.CoverDir, "comic_"+oldID+".jpg"))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": newID})
}

// noteComicServed records per-comic serve time for the enrichment app's
// active-file check. Called alongside notePageServed.
func (s *Server) noteComicServed(id string) { s.servedComics.Store(id, time.Now().UnixNano()) }

// HandleFileState implements the enrichment app's pre-write check: is a
// comic actively being read right now? Active = served (page or download)
// within the last 5 minutes. Unknown/out-of-library paths report inactive
// rather than erroring, since the enrichment client treats any non-200
// response as inactive anyway.
func (s *Server) HandleFileState(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"active": false, "last_accessed_at": 0}
	if rel, ok := s.relToComicDir(r.URL.Query().Get("path")); ok {
		if v, ok := s.servedComics.Load(pathToID(rel)); ok {
			last := v.(int64)
			resp["last_accessed_at"] = last / 1e9
			resp["active"] = time.Since(time.Unix(0, last)) < 5*time.Minute
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
