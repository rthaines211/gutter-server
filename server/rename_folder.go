package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ryanhaines/comic-reader/rename"
	"github.com/ryanhaines/comic-reader/store"
)

// HandleRenameFolder handles PUT /series/{id}/rename-folder.
// It renames the series folder on disk, migrates all IDs in the DB,
// and removes stale cover thumbnails.
func (s *Server) HandleRenameFolder(w http.ResponseWriter, r *http.Request) {
	seriesID := r.PathValue("id")

	// 1. Decode and validate request body.
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	newName, err := rename.SanitizeFilename(body.Name)
	if err != nil || newName == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid folder name")
		return
	}

	// 2. Load issues to get the current folder name and all filenames.
	issues, err := s.Store.IssuesForTemplateRename(seriesID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "store error")
		return
	}
	if len(issues) == 0 {
		writeJSONError(w, http.StatusNotFound, "series not found")
		return
	}

	// 2a. A series that also reads a downloader's twin folder keeps its
	// folder: the rename could change the series id under the twin's issues.
	if twin, err := s.Store.HasTwinFolder(seriesID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "store error")
		return
	} else if twin {
		writeJSONError(w, http.StatusConflict, "series also reads from a twin folder; merge the folders first")
		return
	}

	// 3. Derive the old folder basename from the first issue.
	oldFolder := issues[0].FolderName // filepath.Base of folder_path

	// 4. No-op if name is unchanged.
	if newName == oldFolder {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"id":   seriesID,
			"name": newName,
		})
		return
	}

	// 5. Resolve the absolute path of the old folder from ComicDirs.
	oldAbsPath := ""
	for _, root := range s.ComicDirs {
		candidate := filepath.Join(root, oldFolder)
		if _, err := os.Stat(candidate); err == nil {
			oldAbsPath = candidate
			break
		}
	}
	if oldAbsPath == "" {
		writeJSONError(w, http.StatusNotFound, "series folder not found on disk")
		return
	}

	libRoot := filepath.Dir(oldAbsPath)
	newAbsPath := filepath.Join(libRoot, newName)

	// 6. Check for disk conflict.
	if _, err := os.Stat(newAbsPath); err == nil {
		writeJSONError(w, http.StatusConflict, "target folder already exists")
		return
	}

	// 7. Compute new IDs.
	newSeriesID := slugify(newName)
	newFolderPath := newAbsPath // folder_path is stored as absolute path

	// 7a. Guard against slug collision with a different existing series.
	if newSeriesID != seriesID {
		exists, err := s.Store.SeriesExists(newSeriesID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "store error")
			return
		}
		if exists {
			writeJSONError(w, http.StatusConflict, "a series with that name already exists")
			return
		}
	}

	pairs := make([]store.IDPair, 0, len(issues))
	for _, iss := range issues {
		newRelPath := filepath.Join(newName, iss.Filename)
		pairs = append(pairs, store.IDPair{
			Old: iss.ID,
			New: pathToID(newRelPath),
		})
	}

	// 8. Rename folder on disk.
	if err := os.Rename(oldAbsPath, newAbsPath); err != nil {
		log.Printf("rename-folder: os.Rename %s -> %s: %v", oldAbsPath, newAbsPath, err)
		writeJSONError(w, http.StatusInternalServerError, "failed to rename folder")
		return
	}

	// 9. Migrate all DB rows.
	if err := s.Store.MigrateSeriesFolder(seriesID, newSeriesID, newFolderPath, newName, pairs); err != nil {
		log.Printf("rename-folder: MigrateSeriesFolder %s -> %s: %v", seriesID, newSeriesID, err)
		// Rollback disk rename.
		if rerr := os.Rename(newAbsPath, oldAbsPath); rerr != nil {
			log.Printf("rename-folder: rollback rename failed: %v", rerr)
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to migrate database")
		return
	}

	// 10. Remove stale cover thumbnails (best-effort).
	if s.CoverDir != "" {
		os.Remove(filepath.Join(s.CoverDir, "series_"+seriesID+".jpg"))
		for _, iss := range issues {
			os.Remove(filepath.Join(s.CoverDir, "comic_"+iss.ID+".jpg"))
		}
	}

	// 11. Touch series so delta sync picks it up.
	if err := s.Store.TouchSeries(newSeriesID); err != nil {
		log.Printf("rename-folder: TouchSeries %s: %v", newSeriesID, err)
	}

	// 12. Return new id and name.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"id":   newSeriesID,
		"name": newName,
	})
}
