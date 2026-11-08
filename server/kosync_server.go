package server

// KOReader sync server (the kosync protocol) under /kosync/. Point
// KOReader's "Custom sync server" at https://<host>/kosync and log in with
// any username and the Gutter password (or API key).
//
//	POST /kosync/users/create            "register": succeeds when the password is right
//	GET  /kosync/users/auth              login check (x-auth-user / x-auth-key)
//	PUT  /kosync/syncs/progress          push progress
//	GET  /kosync/syncs/progress/{doc}    pull progress
//	GET  /kosync/healthcheck             unauthenticated liveness
//
// Documents are matched to library comics by KOReader's digest (see the
// kosync package docs); matched progress is mirrored into reading_progress
// so the Gutter app sees it, and Gutter-side progress that is newer than
// KOReader's last push is returned to KOReader.

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rthaines211/gutter-server/kosync"
	"github.com/rthaines211/gutter-server/store"
)

const kosyncPrefix = "/kosync/"

// Reported as the device for progress that came from the Gutter app.
const (
	kosyncGutterDevice   = "Gutter"
	kosyncGutterDeviceID = "gutter-server"
)

// kosyncReindexInterval limits full-library digest scans triggered by
// unknown documents.
const kosyncReindexInterval = 5 * time.Minute

// kosyncReindexWait is how long a request waits for a digest scan before
// answering without a match; the scan keeps running and later requests use
// its results. Keeps a first scan of a big NAS library under KOReader's
// request timeout.
var kosyncReindexWait = 10 * time.Second

func writeKosync(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeKosyncError(w http.ResponseWriter, status, code int, msg string) {
	writeKosync(w, status, kosync.ErrorResponse{Code: code, Message: msg})
}

// kosyncKeyOK reports whether key (KOReader's x-auth-key, the MD5 hex of
// what the user typed as password) matches the Gutter password or API key.
// The plain secret is accepted too, for hand-rolled clients.
func kosyncKeyOK(key, password, apiKey string) bool {
	if key == "" {
		return false
	}
	eq := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	for _, secret := range []string{password, apiKey} {
		if secret == "" {
			continue
		}
		if eq(strings.ToLower(key), kosync.KeyFor(secret)) || eq(key, secret) {
			return true
		}
	}
	return false
}

// kosyncPublic lists the kosync endpoints KOReader calls without auth
// headers.
func kosyncPublic(r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == kosyncPrefix+"users/create":
		return true // the handler checks the password in the body
	case r.Method == http.MethodGet && r.URL.Path == kosyncPrefix+"healthcheck":
		return true
	}
	return false
}

// kosyncAuth gates /kosync/ requests. KOReader sends x-auth-user/x-auth-key
// instead of Authorization; Basic and Bearer are also accepted. Failures get
// kosync's JSON error shape rather than a Basic challenge.
func kosyncAuth(w http.ResponseWriter, r *http.Request, next http.Handler, password, apiKey string) {
	if kosyncPublic(r) {
		next.ServeHTTP(w, r)
		return
	}
	eq := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	ok := false
	if key := r.Header.Get("x-auth-key"); key != "" {
		ok = kosyncKeyOK(key, password, apiKey)
	} else if _, pass, has := r.BasicAuth(); has {
		ok = password != "" && eq(pass, password)
	} else if tok, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found {
		ok = apiKey != "" && eq(tok, apiKey)
	}
	if !ok {
		writeKosyncError(w, http.StatusUnauthorized, kosync.CodeUnauthorized, "Unauthorized")
		return
	}
	// Same rule as RequireAuth: browsers resend Basic credentials, so refuse
	// cross-site writes.
	if r.Method != http.MethodGet && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeKosyncError(w, http.StatusForbidden, kosync.CodeUnauthorized, "cross-site request refused")
		return
	}
	next.ServeHTTP(w, r)
}

// HandleKosyncHealthcheck is kosync's liveness probe.
func (s *Server) HandleKosyncHealthcheck(w http.ResponseWriter, r *http.Request) {
	writeKosync(w, http.StatusOK, map[string]string{"state": "OK"})
}

// HandleKosyncCreateUser accepts KOReader's "Register" when the password is
// the Gutter password (or API key); there's only one user. Any username works.
func (s *Server) HandleKosyncCreateUser(w http.ResponseWriter, r *http.Request) {
	var req kosync.UserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		writeKosyncError(w, http.StatusForbidden, kosync.CodeInvalidFields, "Invalid request")
		return
	}
	if !s.AuthOff && !kosyncKeyOK(req.Password, s.Password, s.APIKey) {
		writeKosyncError(w, http.StatusPaymentRequired, kosync.CodeUserExists,
			"Registration is closed; log in with your Gutter password")
		return
	}
	writeKosync(w, http.StatusCreated, map[string]string{"username": req.Username})
}

// HandleKosyncAuth answers KOReader's login check. The auth gate has already
// validated the headers by the time this runs.
func (s *Server) HandleKosyncAuth(w http.ResponseWriter, r *http.Request) {
	writeKosync(w, http.StatusOK, map[string]string{"authorized": "OK"})
}

// HandleKosyncPutProgress stores KOReader's progress and mirrors it into
// reading_progress when the document is a library comic.
func (s *Server) HandleKosyncPutProgress(w http.ResponseWriter, r *http.Request) {
	var req kosync.ProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeKosyncError(w, http.StatusForbidden, kosync.CodeInvalidFields, "Invalid request")
		return
	}
	if err := req.Validate(); err != nil {
		writeKosyncError(w, http.StatusForbidden, kosync.CodeInvalidFields, "Invalid request: "+err.Error())
		return
	}
	comicID := s.kosyncComicFor(req.Document)
	if comicID != "" {
		if total := s.kosyncPageCount(comicID); total > 0 {
			if page, ok := kosync.ToGutterPage(req.Progress, req.Percentage, total); ok {
				readAt := time.Now().UnixMilli()
				err := s.Store.UpsertProgress(store.Progress{
					ComicID: comicID, CurrentPage: page, TotalPages: total, ReadAt: &readAt,
				})
				if err != nil && !errors.Is(err, store.ErrStaleWrite) {
					writeInternalError(w, err)
					return
				}
			}
		}
	}
	// Stamped after the reading_progress write, so a same-second Gutter
	// updated_at never looks newer than this push.
	now := time.Now().UTC()
	if err := s.Store.UpsertKosyncProgress(store.KosyncProgress{
		Document: req.Document, ComicID: comicID, Progress: req.Progress,
		Percentage: req.Percentage, Device: req.Device, DeviceID: req.DeviceID, Timestamp: now,
	}); err != nil {
		writeInternalError(w, err)
		return
	}
	writeKosync(w, http.StatusOK, kosync.UpdateResponse{Document: req.Document, Timestamp: now.Unix()})
}

// HandleKosyncGetProgress returns the newest progress for a document:
// KOReader's last push, or the Gutter app's progress when that is newer.
// Unknown documents get {} (KOReader reads that as "no progress").
func (s *Server) HandleKosyncGetProgress(w http.ResponseWriter, r *http.Request) {
	doc := r.PathValue("document")
	raw, err := s.Store.GetKosyncProgress(doc)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeInternalError(w, err)
		return
	}
	comicID := ""
	if raw != nil && raw.ComicID != "" {
		comicID = raw.ComicID
	} else {
		comicID = s.kosyncComicFor(doc)
	}

	if comicID != "" {
		gp, err := s.Store.GetProgress(comicID)
		if err == nil && gp.TotalPages > 0 && (raw == nil || gp.UpdatedAt.Unix() > raw.Timestamp.Unix()) {
			// Same page as KOReader's record: keep KOReader's own record.
			if raw != nil {
				if page, ok := kosync.ToGutterPage(raw.Progress, raw.Percentage, gp.TotalPages); ok && page == gp.CurrentPage {
					writeKosync(w, http.StatusOK, rawProgressResponse(raw))
					return
				}
			}
			prog, pct := kosync.FromGutterPage(gp.CurrentPage, gp.TotalPages)
			writeKosync(w, http.StatusOK, kosync.ProgressResponse{
				Document: doc, Progress: prog, Percentage: pct,
				Device: kosyncGutterDevice, DeviceID: kosyncGutterDeviceID,
				Timestamp: gp.UpdatedAt.Unix(),
			})
			return
		}
	}
	if raw != nil {
		writeKosync(w, http.StatusOK, rawProgressResponse(raw))
		return
	}
	writeKosync(w, http.StatusOK, struct{}{})
}

func rawProgressResponse(p *store.KosyncProgress) kosync.ProgressResponse {
	return kosync.ProgressResponse{
		Document: p.Document, Progress: p.Progress, Percentage: p.Percentage,
		Device: p.Device, DeviceID: p.DeviceID, Timestamp: p.Timestamp.Unix(),
	}
}

// kosyncPageCount returns a comic's real page count (the archive's image
// entries, which is what KOReader pages through), caching it.
func (s *Server) kosyncPageCount(comicID string) int {
	if s.Cache != nil {
		if v, ok := s.Cache.Get(opdsPageCountKey(comicID)); ok {
			if n, ok := v.(int); ok && n > 0 {
				return n
			}
		}
	}
	abs, err := s.resolveComicPath(comicID)
	if err != nil {
		return 0
	}
	cached, err := getArchive(abs)
	if err != nil {
		return 0
	}
	cached.mu.Lock()
	n := cached.reader.PageCount()
	cached.mu.Unlock()
	if s.Cache != nil && n > 0 {
		s.Cache.Set(opdsPageCountKey(comicID), n)
	}
	return n
}

// kosyncDocumentFor computes a comic file's KOReader digests.
func kosyncDocumentFor(comicID, absPath string) (store.KosyncDocument, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return store.KosyncDocument{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return store.KosyncDocument{}, err
	}
	digest, err := kosync.PartialMD5(f, fi.Size())
	if err != nil {
		return store.KosyncDocument{}, err
	}
	return store.KosyncDocument{
		ComicID:        comicID,
		BinaryDigest:   digest,
		FilenameDigest: kosync.FilenameDigest(absPath),
		Size:           fi.Size(),
		MTime:          fi.ModTime().Unix(),
	}, nil
}

// kosyncComicFor maps a KOReader document digest to a library comic id, or
// "" when none matches. An unknown digest triggers a (rate-limited) digest
// scan of the library.
func (s *Server) kosyncComicFor(digest string) string {
	if id := s.kosyncLookup(digest); id != "" {
		return id
	}
	done := make(chan bool, 1)
	go func() { done <- s.kosyncIndexer().reindex(s, false) }()
	select {
	case ran := <-done:
		if ran {
			return s.kosyncLookup(digest)
		}
	case <-time.After(kosyncReindexWait):
		log.Printf("kosync: digest scan still running; answering %s without a match", digest)
	}
	return ""
}

func (s *Server) kosyncLookup(digest string) string {
	ids, err := s.Store.ComicIDsForKosyncDigest(digest)
	if err != nil {
		log.Printf("kosync: digest lookup: %v", err)
		return ""
	}
	for _, id := range ids {
		if _, err := s.resolveComicPath(id); err == nil {
			return id
		}
	}
	return ""
}

type kosyncIndex struct {
	mu   sync.Mutex
	last time.Time
}

var kosyncIndexers sync.Map // *Server -> *kosyncIndex

func (s *Server) kosyncIndexer() *kosyncIndex {
	v, _ := kosyncIndexers.LoadOrStore(s, &kosyncIndex{})
	return v.(*kosyncIndex)
}

// reindex computes digests for library comics that are new or changed
// (size/mtime) since they were last indexed, and drops comics that are gone.
// Unless force is set it runs at most once per kosyncReindexInterval; it
// reports whether a scan ran.
func (ix *kosyncIndex) reindex(s *Server, force bool) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if !force && !ix.last.IsZero() && time.Since(ix.last) < kosyncReindexInterval {
		return false
	}
	ix.last = time.Now()

	known, err := s.Store.ListKosyncDocuments()
	if err != nil {
		log.Printf("kosync: reindex: %v", err)
		return false
	}
	seriesList, err := s.Store.ListSeries()
	if err != nil {
		log.Printf("kosync: reindex: %v", err)
		return false
	}
	seen := map[string]bool{}
	added := 0
	for _, sr := range seriesList {
		issues, err := s.Store.ListIssues(sr.ID)
		if err != nil {
			continue
		}
		for _, iss := range issues {
			if ext := strings.ToLower(filepath.Ext(iss.Filename)); ext != ".cbz" && ext != ".cbr" {
				continue
			}
			abs, err := s.resolveComicPath(iss.ID)
			if err != nil {
				continue
			}
			seen[iss.ID] = true
			fi, err := os.Stat(abs)
			if err != nil {
				continue
			}
			if d, ok := known[iss.ID]; ok && d.Size == fi.Size() && d.MTime == fi.ModTime().Unix() {
				continue
			}
			doc, err := kosyncDocumentFor(iss.ID, abs)
			if err != nil {
				continue
			}
			if err := s.Store.UpsertKosyncDocument(doc); err == nil {
				added++
			}
		}
	}
	// Nothing resolvable usually means the library volume is offline; keep
	// the index rather than wiping it.
	for id := range known {
		if len(seen) > 0 && !seen[id] {
			s.Store.DeleteKosyncDocument(id)
		}
	}
	if added > 0 {
		log.Printf("kosync: indexed %d comic(s) in %s", added, time.Since(ix.last).Round(time.Millisecond))
	}
	return true
}
