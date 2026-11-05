package server

// Bubble zoom: sidecars are written by hostjob/bubbles on the macOS host and
// keyed by the SHA-1 of a page's bytes, so they survive organize renames (new
// comic IDs) and writeback rewrites (page bytes are copied raw). Reader order
// fixes live in the bubble_order table under the same key.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type bubbleSidecar struct {
	V       int             `json:"v"`
	W       int             `json:"w"`
	H       int             `json:"h"`
	Bubbles []sidecarBubble `json:"bubbles"`
	Mask    string          `json:"mask,omitempty"`
	// Panel zoom: the page's panels [x0, y0, x1, y1] in reading order (sidecars from 2026-10-14).
	Panels [][4]int `json:"panels,omitempty"`
}

type sidecarBubble struct {
	R     [4]int `json:"r"`
	Label int    `json:"label"`
	T     string `json:"t,omitempty"` // the balloon's text as Vision read it (sidecars from 2026-10-12)
}

var errNoSidecar = errors.New("page not processed")

// pageSHA finds page num's sha1: from the host job's book index when it is
// current, else by hashing the page.
func (s *Server) pageSHA(absPath string, num int) (string, error) {
	if idx := s.bookIndex(absPath); idx != nil {
		if names, err := pageNames(absPath); err == nil && num >= 0 && num < len(names) {
			if sha, ok := idx[names[num]]; ok {
				return sha, nil
			}
		}
	}
	return hashPage(absPath, num)
}

// bookIndex is the host job's {entry name: page sha1} for the archive at absPath,
// or nil when there is none or the file changed since (size or mtime differ).
// It lets a 1,500-page book's balloons be found without reading 800 MB of pages.
func (s *Server) bookIndex(absPath string) map[string]string {
	if s.BubblesDir == "" {
		return nil
	}
	rel, err := filepath.Rel(filepath.Dir(s.BubblesDir), absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil
	}
	sum := sha1.Sum([]byte(rel))
	data, err := os.ReadFile(filepath.Join(s.BubblesDir, "books", hex.EncodeToString(sum[:])+".json"))
	if err != nil {
		return nil
	}
	var idx struct {
		Size  int64             `json:"size"`
		Mtime int64             `json:"mtime"`
		Pages map[string]string `json:"pages"`
	}
	if json.Unmarshal(data, &idx) != nil {
		return nil
	}
	fi, err := os.Stat(absPath)
	if err != nil || fi.Size() != idx.Size || fi.ModTime().Unix() != idx.Mtime {
		return nil
	}
	return idx.Pages
}

func pageNames(absPath string) ([]string, error) {
	cached, err := getArchive(absPath)
	if err != nil {
		return nil, err
	}
	cached.mu.Lock()
	defer cached.mu.Unlock()
	return cached.reader.PageNames(), nil
}

// hashPage hashes page num of the archive at absPath, the same bytes HandleComicPage serves.
func hashPage(absPath string, num int) (string, error) {
	cached, err := getArchive(absPath)
	if err != nil {
		return "", err
	}
	cached.mu.Lock()
	rc, _, err := cached.reader.PageAt(num)
	cached.mu.Unlock()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	h := sha1.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readSidecar returns the sidecar as the host job wrote it (stored order).
func (s *Server) readSidecar(sha string) (*bubbleSidecar, error) {
	if s.BubblesDir == "" || !isSHA1(sha) { // a sha from a book index is file data: never a path
		return nil, errNoSidecar
	}
	data, err := os.ReadFile(filepath.Join(s.BubblesDir, sha[:2], sha+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoSidecar
	}
	if err != nil {
		return nil, err
	}
	var sc bubbleSidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		// A truncated sidecar reads as "not processed": the reader falls back to chunk pan.
		log.Printf("bubbles: unreadable sidecar %s: %v", sha, err)
		return nil, errNoSidecar
	}
	if sc.Bubbles == nil {
		sc.Bubbles = []sidecarBubble{}
	}
	return &sc, nil
}

// loadBubbles is readSidecar with the reader's saved order applied.
func (s *Server) loadBubbles(sha string) (*bubbleSidecar, error) {
	sc, err := s.readSidecar(sha)
	if err != nil || s.Store == nil {
		return sc, err
	}
	labels, err := s.Store.GetBubbleOrder(sha)
	if err != nil {
		return nil, err
	}
	if labels != nil && isLabelPerm(sc.Bubbles, labels) { // a stale order (pipeline re-run) is ignored
		byLabel := map[int]sidecarBubble{}
		for _, b := range sc.Bubbles {
			byLabel[b.Label] = b
		}
		for i, l := range labels {
			sc.Bubbles[i] = byLabel[l]
		}
	}
	return sc, nil
}

func isSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func storedLabels(bs []sidecarBubble) []int {
	out := make([]int, len(bs))
	for i, b := range bs {
		out[i] = b.Label
	}
	return out
}

func isLabelPerm(bs []sidecarBubble, labels []int) bool {
	if len(bs) != len(labels) {
		return false
	}
	want := map[int]bool{}
	for _, b := range bs {
		want[b.Label] = true
	}
	for _, l := range labels {
		if !want[l] {
			return false
		}
		delete(want, l)
	}
	return true
}

// pageSHAFromRequest resolves {id}/{num} to the page hash, writing the error response itself.
func (s *Server) pageSHAFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	num, err := strconv.Atoi(r.PathValue("num"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid page number")
		return "", false
	}
	absPath, err := s.resolveComicPath(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return "", false
	}
	sha, err := s.pageSHA(absPath, num)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return "", false
	}
	return sha, true
}

func (s *Server) HandlePageBubbles(w http.ResponseWriter, r *http.Request) {
	sha, ok := s.pageSHAFromRequest(w, r)
	if !ok {
		return
	}
	sc, err := s.loadBubbles(sha)
	if errors.Is(err, errNoSidecar) {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, sc)
}

func (s *Server) HandleComicBubbles(w http.ResponseWriter, r *http.Request) {
	absPath, err := s.resolveComicPath(r.PathValue("id"))
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
	names := cached.reader.PageNames()
	cached.mu.Unlock()
	idx := s.bookIndex(absPath) // read once; nil means hash every page
	pages := map[string]*bubbleSidecar{}
	for i := range names {
		// One bad page must not fail a whole collection's download; skip it (chunk pan offline).
		sha, ok := idx[names[i]]
		var err error
		if !ok {
			sha, err = hashPage(absPath, i)
		}
		if err != nil {
			log.Printf("bubbles: %s page %d: %v", absPath, i, err)
			continue
		}
		sc, err := s.loadBubbles(sha)
		if err != nil {
			if !errors.Is(err, errNoSidecar) {
				log.Printf("bubbles: %s page %d: %v", absPath, i, err)
			}
			continue
		}
		pages[strconv.Itoa(i)] = sc
	}
	writeJSON(w, map[string]any{"pages": pages})
}

func (s *Server) HandlePutBubbleOrder(w http.ResponseWriter, r *http.Request) {
	sha, ok := s.pageSHAFromRequest(w, r)
	if !ok {
		return
	}
	sc, err := s.readSidecar(sha)
	if errors.Is(err, errNoSidecar) {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	var body struct {
		Labels []int `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !isLabelPerm(sc.Bubbles, body.Labels) {
		writeJSONError(w, http.StatusBadRequest, "labels must be a permutation of the page's bubble labels")
		return
	}
	if slices.Equal(body.Labels, storedLabels(sc.Bubbles)) {
		err = s.Store.DeleteBubbleOrder(sha)
	} else {
		err = s.Store.SetBubbleOrder(sha, body.Labels)
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
