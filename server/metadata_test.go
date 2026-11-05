package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

// seedTestComic inserts a series (if missing) + a single issue row.
// Cannot reuse store/migrate_test.go:seedComic (internal package), so we
// replicate the minimal setup using the public store API.
// NOTE: replaces ALL issues for the series — call once per series or use
// seedTestComics for multi-issue series.
func seedTestComic(t *testing.T, s *store.Store, seriesID, comicID, filename string) {
	t.Helper()
	seedTestComics(t, s, seriesID, []store.Issue{
		{ID: comicID, SeriesID: seriesID, Filename: filename, Number: "1", Position: 0},
	})
}

// seedTestComics inserts a series + a list of issues in one call.
func seedTestComics(t *testing.T, s *store.Store, seriesID string, issues []store.Issue) {
	t.Helper()
	if err := s.UpsertSeries(store.Series{
		ID:         seriesID,
		Name:       seriesID,
		FolderPath: "/lib/" + seriesID,
		IssueCount: len(issues),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssues(seriesID, issues); err != nil {
		t.Fatal(err)
	}
}

func TestHandlePatchComicMetadata(t *testing.T) {
	srv := newTestServer(t)
	seedTestComic(t, srv.Store, "srs", "comic1", "a.cbz")

	// Valid patch → 204
	req := httptest.NewRequest("PATCH", "/comics/comic1/metadata",
		strings.NewReader(`{"title":"T","number":"7","format":"tpb"}`))
	rec := httptest.NewRecorder()
	srv.NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	// Writeback enqueued.
	id, ok, _ := srv.Store.NextWritebackJob()
	if !ok || id != "comic1" {
		t.Fatalf("writeback not enqueued: %q %v", id, ok)
	}

	// Unknown comic → 404.
	req = httptest.NewRequest("PATCH", "/comics/nope/metadata", strings.NewReader(`{"title":"x"}`))
	rec = httptest.NewRecorder()
	srv.NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}

	// All-nil patch → 400 empty patch.
	req = httptest.NewRequest("PATCH", "/comics/comic1/metadata", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	srv.NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for empty patch, got %d", rec.Code)
	}

	// Invalid format → 400.
	req = httptest.NewRequest("PATCH", "/comics/comic1/metadata", strings.NewReader(`{"format":"magazine"}`))
	rec = httptest.NewRecorder()
	srv.NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for invalid format, got %d", rec.Code)
	}
}

func TestHandlePatchSeriesMetadata(t *testing.T) {
	srv := newTestServer(t)
	seedTestComics(t, srv.Store, "srs", []store.Issue{
		{ID: "comic1", SeriesID: "srs", Filename: "a.cbz", Number: "1", Position: 0},
		{ID: "comic2", SeriesID: "srs", Filename: "b.cbz", Number: "2", Position: 1},
	})

	req := httptest.NewRequest("PATCH", "/series/srs/metadata",
		strings.NewReader(`{"publisher":"P","year":1999,"genres":["Action"]}`))
	rec := httptest.NewRecorder()
	srv.NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	// Both issues enqueued for writeback.
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		id, ok, _ := srv.Store.NextWritebackJob()
		if !ok {
			t.Fatalf("expected 2 writeback jobs, got %d", i)
		}
		got[id] = true
		srv.Store.MarkWritebackDone(id)
	}
	if !got["comic1"] || !got["comic2"] {
		t.Fatalf("wrong jobs: %v", got)
	}

	// Unknown series → 404.
	req = httptest.NewRequest("PATCH", "/series/nope/metadata", strings.NewReader(`{"name":"x"}`))
	rec = httptest.NewRecorder()
	srv.NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}

	// All-nil patch → 400 empty patch.
	req = httptest.NewRequest("PATCH", "/series/srs/metadata", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	srv.NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for empty series patch, got %d", rec.Code)
	}
}
