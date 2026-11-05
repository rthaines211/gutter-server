package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ryanhaines/comic-reader/archive"
	"github.com/ryanhaines/comic-reader/metadata"
	"github.com/ryanhaines/comic-reader/store"
)

// cachedArchive holds an open archive reader with an expiry.
type cachedArchive struct {
	reader  archive.Reader
	expires time.Time
	mu      sync.Mutex // per-archive lock for page reads
}

var (
	archiveMu    sync.Mutex
	archiveCache = make(map[string]*cachedArchive)
)

// getArchive returns a cached or freshly opened archive reader.
func getArchive(absPath string) (*cachedArchive, error) {
	archiveMu.Lock()

	// Clean expired entries
	now := time.Now()
	for k, v := range archiveCache {
		if now.After(v.expires) {
			v.reader.Close()
			delete(archiveCache, k)
		}
	}

	if cached, ok := archiveCache[absPath]; ok {
		cached.expires = now.Add(20 * time.Minute)
		archiveMu.Unlock()
		return cached, nil
	}

	// Release global lock before slow I/O
	archiveMu.Unlock()

	ar, err := archive.Open(absPath)
	if err != nil {
		return nil, err
	}

	entry := &cachedArchive{
		reader:  ar,
		expires: now.Add(20 * time.Minute),
	}

	archiveMu.Lock()
	// Check if another goroutine raced us
	if existing, ok := archiveCache[absPath]; ok {
		archiveMu.Unlock()
		ar.Close()
		return existing, nil
	}
	archiveCache[absPath] = entry
	archiveMu.Unlock()

	return entry, nil
}

func (s *Server) resolveComicPath(id string) (string, error) {
	relPath, err := idToPath(id)
	if err != nil {
		return "", fmt.Errorf("invalid comic id: %w", err)
	}
	cleaned := filepath.Clean(relPath)
	if strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("invalid path")
	}
	for _, root := range s.ComicDirs {
		abs := filepath.Join(root, cleaned)
		absResolved, err := filepath.Abs(abs)
		if err != nil || !strings.HasPrefix(absResolved, root) {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("comic not found")
}

// readComicInfo resolves the comic by id, opens (or reuses cached) the archive,
// parses ComicInfo.xml, and returns the resolved metadata.ComicInfo. The result
// is also cached under "info:<id>" so subsequent calls are cheap. Errors are
// returned without HTTP context so callers can decide how to handle them.
func (s *Server) readComicInfo(id string) (*metadata.ComicInfo, error) {
	cacheKey := "info:" + id
	if cached, ok := s.Cache.Get(cacheKey); ok {
		if info, ok := cached.(*metadata.ComicInfo); ok {
			return info, nil
		}
	}
	absPath, err := s.resolveComicPath(id)
	if err != nil {
		return nil, err
	}
	cached, err := getArchive(absPath)
	if err != nil {
		return nil, err
	}
	cached.mu.Lock()
	info, err := metadata.ParseComicInfo(cached.reader.ComicInfoXML())
	pageCount := cached.reader.PageCount()
	cached.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if info.PageCount == 0 {
		info.PageCount = pageCount
	}
	s.Cache.Set(cacheKey, info)
	return info, nil
}

func (s *Server) HandleComicInfo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Preserve the prior behavior: path resolution failures → 404, anything else → 500.
	if _, err := s.resolveComicPath(id); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	info, err := s.readComicInfo(id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (s *Server) HandleComicPage(w http.ResponseWriter, r *http.Request) {
	s.notePageServed() // let the tagger worker yield while the user is reading
	id := r.PathValue("id")
	s.noteComicServed(id)
	numStr := r.PathValue("num")
	absPath, err := s.resolveComicPath(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	pageNum, err := strconv.Atoi(numStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid page number")
		return
	}
	cached, err := getArchive(absPath)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	cached.mu.Lock()
	rc, contentType, err := cached.reader.PageAt(pageNum)
	cached.mu.Unlock()
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read page")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := w.Write(data); err != nil {
		log.Printf("write error serving page %d of %s: %v", pageNum, id, err)
	}
}

func (s *Server) HandleDownloadComic(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.noteComicServed(id)
	absPath, err := s.resolveComicPath(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "file not found")
		return
	}

	ext := strings.ToLower(filepath.Ext(absPath))
	contentType := "application/octet-stream"
	switch ext {
	case ".cbz":
		contentType = "application/x-cbz"
	case ".cbr":
		contentType = "application/x-cbr"
	case ".cb7":
		contentType = "application/x-cb7"
	case ".pdf":
		contentType = "application/pdf"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(absPath)))

	http.ServeFile(w, r, absPath)
}

func (s *Server) HandleComicCover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Check for cached thumbnail
	if s.CoverDir != "" {
		thumbPath := filepath.Join(s.CoverDir, "comic_"+id+".jpg")
		if _, err := os.Stat(thumbPath); err == nil {
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeFile(w, r, thumbPath)
			return
		}
	}

	absPath, err := s.resolveComicPath(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	cached, err := getArchive(absPath)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	cached.mu.Lock()
	rc, contentType, err := cached.reader.PageAt(0)
	cached.mu.Unlock()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rc.Close()

	// Lazily generate and cache thumbnail in background
	if s.CoverDir != "" {
		thumbPath := filepath.Join(s.CoverDir, "comic_"+id+".jpg")
		go func() {
			ar, err := getArchive(absPath)
			if err != nil {
				return
			}
			ar.mu.Lock()
			pageRC, _, err := ar.reader.PageAt(0)
			ar.mu.Unlock()
			if err != nil {
				return
			}
			defer pageRC.Close()
			GenerateThumbnail(pageRC, thumbPath)
		}()
	}

	data, err := io.ReadAll(rc)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read cover")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := w.Write(data); err != nil {
		log.Printf("write error serving cover for %s: %v", id, err)
	}
}

func (s *Server) HandleRenameComic(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Decode the comic path from base64 ID
	relPath, err := idToPath(id)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid comic id")
		return
	}

	// Parse request body
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate new name
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if strings.ContainsAny(req.Name, "/\\") {
		writeJSONError(w, http.StatusBadRequest, "name must not contain path separators")
		return
	}

	// A file in a downloader's twin folder keeps its name: Mylar tracks it
	// by path and would re-download it.
	if s.isTwinFolderComic(id, filepath.Base(relPath)) {
		writeJSONError(w, http.StatusConflict, "comic is in a downloader's twin folder; merge the folders first")
		return
	}

	// Resolve absolute path on disk
	absPath, err := s.resolveComicPath(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "comic not found")
		return
	}

	// Build new path: same directory, new name, same extension
	dir := filepath.Dir(absPath)
	ext := filepath.Ext(absPath)
	newAbsPath := filepath.Join(dir, req.Name+ext)

	// Check if target already exists
	if _, err := os.Stat(newAbsPath); err == nil {
		writeJSONError(w, http.StatusConflict, "a file with that name already exists")
		return
	}

	// Rename on disk
	if err := os.Rename(absPath, newAbsPath); err != nil {
		log.Printf("rename failed: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "rename failed")
		return
	}

	// Compute new relative path and ID
	newRelPath := filepath.Join(filepath.Dir(relPath), req.Name+ext)
	newID := pathToID(newRelPath)

	if err := s.Store.MigrateComicIDFull(id, newID, req.Name+ext); err != nil {
		log.Printf("rename migrate %s: %v", id, err)
		if rerr := os.Rename(newAbsPath, absPath); rerr != nil {
			log.Printf("rename rollback failed: %v", rerr)
		}
		writeJSONError(w, http.StatusInternalServerError, "rename failed")
		return
	}

	// Invalidate thumbnail cache for old ID
	if s.CoverDir != "" {
		thumbPath := filepath.Join(s.CoverDir, "comic_"+id+".jpg")
		os.Remove(thumbPath)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"id":   newID,
		"name": req.Name + ext,
	})
}

// isTwinFolderComic reports whether comic id lives in a twin folder of its
// series (store.InTwinFolder). An id with no issue row is not.
func (s *Server) isTwinFolderComic(id, filename string) bool {
	if s.Store == nil {
		return false
	}
	seriesID, err := s.Store.SeriesIDForComic(id)
	if err != nil {
		return false
	}
	series, err := s.Store.GetSeries(seriesID)
	return err == nil && store.InTwinFolder(series.FolderPath, id, filename)
}
