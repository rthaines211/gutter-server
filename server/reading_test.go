package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/ryanhaines/comic-reader/cache"
	"github.com/ryanhaines/comic-reader/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Server{ComicDirs: []string{t.TempDir()}, Store: s, Cache: cache.NewLRU(10)}
}

func TestBatchReading(t *testing.T) {
	srv := newTestServer(t)
	id1 := pathToID("series/issue1.cbz")
	id2 := pathToID("series/issue2.cbz")
	body := `{"items": [
		{"comic_id": "` + id1 + `", "current_page": 10, "total_pages": 10},
		{"comic_id": "` + id2 + `", "current_page": 20, "total_pages": 20}
	]}`
	req := httptest.NewRequest("PUT", "/reading/batch", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleBatchReading(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT batch expected 200, got %d: %s", w.Code, w.Body.String())
	}
	p1, err := srv.Store.GetProgress(id1)
	if err != nil {
		t.Fatalf("GetProgress for id1 failed: %v", err)
	}
	if p1.CurrentPage != 10 {
		t.Errorf("expected page 10, got %d", p1.CurrentPage)
	}
	p2, _ := srv.Store.GetProgress(id2)
	if p2.CurrentPage != 20 {
		t.Errorf("expected page 20, got %d", p2.CurrentPage)
	}
}

func TestPutAndGetReading(t *testing.T) {
	srv := newTestServer(t)
	body := `{"current_page": 5, "total_pages": 20, "bookmarks": [{"page_num": 3, "note": "cool"}]}`
	id := pathToID("test.cbz")
	req := httptest.NewRequest("PUT", "/reading/"+id, bytes.NewBufferString(body))
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.HandlePutReading(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/reading", nil)
	w = httptest.NewRecorder()
	srv.HandleGetReading(w, req)
	if w.Code != 200 {
		t.Fatalf("GET expected 200, got %d", w.Code)
	}
	var result ReadingListResponse
	json.NewDecoder(w.Body).Decode(&result)
	if len(result.InProgress) != 1 {
		t.Errorf("expected 1 in-progress, got %d", len(result.InProgress))
	}
}

func TestHandleBatchDeleteReading(t *testing.T) {
	srv := newTestServer(t)
	idA := pathToID("series/aaa.cbz")
	idB := pathToID("series/bbb.cbz")
	if err := srv.Store.UpsertProgress(store.Progress{ComicID: idA, CurrentPage: 5, TotalPages: 10}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertProgress(store.Progress{ComicID: idB, CurrentPage: 5, TotalPages: 10}); err != nil {
		t.Fatal(err)
	}

	mux := srv.NewRouter()
	body := `{"comic_ids":["` + idA + `","zzz"]}`
	req := httptest.NewRequest("DELETE", "/reading/batch", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("DELETE batch expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if _, err := srv.Store.GetProgress(idA); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected idA progress deleted (ErrNotFound), got %v", err)
	}
	if p, err := srv.Store.GetProgress(idB); err != nil {
		t.Errorf("expected idB progress still present, got err %v", err)
	} else if p.CurrentPage != 5 {
		t.Errorf("idB progress modified, got page %d", p.CurrentPage)
	}

	// invalid JSON body -> 400
	req = httptest.NewRequest("DELETE", "/reading/batch", bytes.NewBufferString(`not json`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400 for invalid JSON, got %d", w.Code)
	}
	var errResp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatal(err)
	}
	if errResp["error"] != "invalid JSON" {
		t.Errorf("expected error 'invalid JSON', got %q", errResp["error"])
	}
}

// PUT with a read_at that is not newer than the stored value must be rejected
// (409, JSON error body); a subsequent PUT with no read_at (legacy write)
// must still be accepted.
func TestPutReadingStaleWriteRejected(t *testing.T) {
	srv := newTestServer(t)
	id := pathToID("series/stale.cbz")
	put := func(body string) (int, map[string]string) {
		req := httptest.NewRequest("PUT", "/reading/"+id, bytes.NewBufferString(body))
		req.SetPathValue("id", id)
		w := httptest.NewRecorder()
		srv.HandlePutReading(w, req)
		var resp map[string]string
		json.NewDecoder(w.Body).Decode(&resp)
		return w.Code, resp
	}

	if c, _ := put(`{"current_page":3,"total_pages":20,"read_at":2000}`); c != 200 {
		t.Fatalf("initial write: expected 200, got %d", c)
	}

	c, resp := put(`{"current_page":10,"total_pages":20,"read_at":1000}`)
	if c != 409 {
		t.Fatalf("stale write: expected 409, got %d", c)
	}
	if resp["error"] != "stale progress write" {
		t.Errorf("expected error 'stale progress write', got %q", resp["error"])
	}
	if p, err := srv.Store.GetProgress(id); err != nil || p.CurrentPage != 3 {
		t.Errorf("stale write must not apply: expected CurrentPage=3, got %+v (err %v)", p, err)
	}

	if c, _ := put(`{"current_page":15,"total_pages":20}`); c != 200 {
		t.Fatalf("legacy write (no read_at): expected 200, got %d", c)
	}
	if p, err := srv.Store.GetProgress(id); err != nil || p.CurrentPage != 15 {
		t.Errorf("expected legacy write to apply: got %+v (err %v)", p, err)
	}
}

// A chapters-less progress save must NOT wipe previously-set chapters; only an
// explicit chapters field replaces the set (empty [] clears).
func TestPutReadingChaptersPresencePreserves(t *testing.T) {
	srv := newTestServer(t)
	id := pathToID("series/omnibus.cbz")
	put := func(body string) int {
		req := httptest.NewRequest("PUT", "/reading/"+id, bytes.NewBufferString(body))
		req.SetPathValue("id", id)
		w := httptest.NewRecorder()
		srv.HandlePutReading(w, req)
		return w.Code
	}
	count := func() int {
		ch, err := srv.Store.ListChapters(id)
		if err != nil {
			t.Fatal(err)
		}
		return len(ch)
	}

	// 1) Write 2 chapters.
	if c := put(`{"current_page":3,"total_pages":100,"chapters":[{"comic_id":"` + id + `","page_num":5,"title":"A","created_at":"2026-06-23T20:00:00Z"},{"comic_id":"` + id + `","page_num":40,"title":"B","created_at":"2026-06-23T20:00:00Z"}]}`); c != 200 {
		t.Fatalf("write chapters: status %d", c)
	}
	if count() != 2 {
		t.Fatalf("expected 2 chapters, got %d", count())
	}

	// 2) Progress-only save (no chapters field) must preserve them.
	if c := put(`{"current_page":7,"total_pages":100}`); c != 200 {
		t.Fatalf("progress save: status %d", c)
	}
	if count() != 2 {
		t.Fatalf("progress-only save wiped chapters: got %d, want 2", count())
	}

	// 3) Explicit empty chapters clears the set.
	if c := put(`{"current_page":9,"total_pages":100,"chapters":[]}`); c != 200 {
		t.Fatalf("clear chapters: status %d", c)
	}
	if count() != 0 {
		t.Fatalf("explicit [] should clear, got %d", count())
	}
}
