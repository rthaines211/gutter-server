package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type BrowseItem struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Size     int64  `json:"size,omitempty"`
	CoverURL string `json:"cover_url,omitempty"`
	Path     string `json:"path"`
}

var comicExts = map[string]bool{
	".cbz": true,
	".cbr": true,
}

func (s *Server) HandleBrowse(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	cleaned := filepath.Clean(relPath)
	if strings.Contains(cleaned, "..") {
		writeJSONError(w, http.StatusBadRequest, "invalid path")
		return
	}

	var items []BrowseItem
	for _, root := range s.ComicDirs {
		absPath := filepath.Join(root, cleaned)
		absResolved, err := filepath.Abs(absPath)
		if err != nil || !strings.HasPrefix(absResolved, root) {
			writeJSONError(w, http.StatusBadRequest, "invalid path")
			return
		}
		entries, err := os.ReadDir(absPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if entry.IsDir() {
				items = append(items, BrowseItem{
					Name: name, Type: "dir",
					Path: filepath.Join(cleaned, name),
				})
			} else if comicExts[strings.ToLower(filepath.Ext(name))] {
				info, _ := entry.Info()
				var size int64
				if info != nil {
					size = info.Size()
				}
				comicPath := filepath.Join(cleaned, name)
				items = append(items, BrowseItem{
					Name: name, Type: "comic", Size: size,
					CoverURL: "/comics/" + pathToID(comicPath) + "/cover",
					Path:     comicPath,
				})
			}
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Type != items[j].Type {
			return items[i].Type == "dir"
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}
