package arcsync

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ryanhaines/comic-reader/comicvine"
	"github.com/ryanhaines/comic-reader/match/localcv"
	"github.com/ryanhaines/comic-reader/store"
)

// newLocalCVFixture builds a minimal localcv dump holding just what
// ArcCredits reads (id + story_arc_credits). localcv.Open only pings the
// file — it doesn't validate schema — so this doesn't need the full dump
// shape from match/localcv/fixture_test.go.
func newLocalCVFixture(t *testing.T) *localcv.DB {
	t.Helper()
	path := t.TempDir() + "/localcv.db"
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE cv_issue (id INTEGER PRIMARY KEY, story_arc_credits TEXT);
		INSERT INTO cv_issue (id, story_arc_credits) VALUES
			(10, '[{"id":57081,"name":"Dark Reign"}]'),
			(11, '[{"id":57081,"name":"Dark Reign"}]'),
			(12, '[{"id":57081,"name":"Dark Reign"}]');`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := localcv.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedEnrichedIssue mirrors store/cvarcs_test.go's seedCVIssue (unexported
// there) via the exported Store.DB() accessor, since arcsync is a different
// package.
func seedEnrichedIssue(t *testing.T, s *store.Store, issueID, seriesName, number string, cvID int64) {
	t.Helper()
	seriesID := "ser-" + seriesName
	db := s.DB()
	for _, q := range []struct {
		sql  string
		args []interface{}
	}{
		{`INSERT OR IGNORE INTO series (id, name, folder_path) VALUES (?, ?, ?)`,
			[]interface{}{seriesID, seriesName, "/p/" + seriesID}},
		{`INSERT INTO issues (id, series_id, filename, number) VALUES (?, ?, ?, ?)`,
			[]interface{}{issueID, seriesID, issueID + ".cbz", number}},
		{`INSERT INTO issue_enrichment (issue_id, comicvine_issue_id, enriched_at) VALUES (?, ?, 0)`,
			[]interface{}{issueID, cvID}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCVRebuilderRunOnce(t *testing.T) {
	s := newTestStore(t)
	seedEnrichedIssue(t, s, "i1", "A", "1", 10)
	seedEnrichedIssue(t, s, "i2", "A", "2", 11)
	seedEnrichedIssue(t, s, "i3", "A", "3", 12)
	cv := newLocalCVFixture(t)

	r := NewCVRebuilder(s, cv)
	if err := r.RunOnce(); err != nil {
		t.Fatal(err)
	}

	ids, err := s.ListArcIDs()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ids {
		if id == "cv-57081" {
			found = true
		}
	}
	if !found {
		t.Fatalf("got %v, want cv-57081 present", ids)
	}
}

func TestRunOnceFetchesArcDescriptions(t *testing.T) {
	s := newTestStore(t)
	seedEnrichedIssue(t, s, "i1", "A", "1", 10)
	seedEnrichedIssue(t, s, "i2", "A", "2", 11)
	seedEnrichedIssue(t, s, "i3", "A", "3", 12)
	cv := newLocalCVFixture(t)

	// Description text runs well past the ~80-char minimum so it isn't
	// swapped out for the deck (that fallback threshold is covered by
	// comicvine's own ArcDescriptionPlainText tests).
	const wantDescription = "Secret invasion aftermath engulfs the whole Marvel universe as heroes go underground."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"error":"OK","status_code":1,"number_of_total_results":1,
			"results":{"id":57081,"name":"Dark Reign","deck":"The deck.",
			"description":"<p>%s</p>"}}`, wantDescription)
	}))
	defer srv.Close()
	cvClient := comicvine.New("test-key", t.TempDir())
	cvClient.BaseURL = srv.URL

	r := NewCVRebuilder(s, cv)
	r.SetDescriptionSource(cvClient)
	if err := r.RunOnce(); err != nil {
		t.Fatal(err)
	}

	arcs, err := s.ListArcsUpdatedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	var got *store.Arc
	for i := range arcs {
		if arcs[i].ID == "cv-57081" {
			got = &arcs[i]
		}
	}
	if got == nil {
		t.Fatal("cv-57081 not found")
	}
	if got.Description != wantDescription {
		t.Fatalf("expected fetched description, got %q", got.Description)
	}

	// Already fetched at the current version: a second RunOnce must not
	// re-request it.
	remaining, err := s.ArcsNeedingDescription(10, arcDescVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no arcs still needing a description, got %+v", remaining)
	}
}

// A description fetched under an older arcDescVersion must be re-derived
// (re-fetched — free, since GetStoryArc hits the disk cache — and
// re-processed) even though description_fetched_at is already set, so a
// parsing-logic improvement reaches arcs ingested before it shipped.
func TestRunOnceRederivesStaleDescriptionVersion(t *testing.T) {
	s := newTestStore(t)
	seedEnrichedIssue(t, s, "i1", "A", "1", 10)
	seedEnrichedIssue(t, s, "i2", "A", "2", 11)
	seedEnrichedIssue(t, s, "i3", "A", "3", 12)
	cv := newLocalCVFixture(t)

	r := NewCVRebuilder(s, cv)
	if err := r.RunOnce(); err != nil {
		t.Fatal(err)
	}
	// Simulate a v1-era description that was mostly wiki boilerplate.
	if err := s.SetArcDescription("cv-57081",
		"Collected Editions / Related Issues boilerplate.", 1600000000000, 1); err != nil {
		t.Fatal(err)
	}

	// Synopsis text runs well past the ~80-char minimum so it isn't swapped
	// out for the deck.
	const wantDescription = "The real story text continues for several sentences describing what happens in the arc."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"error":"OK","status_code":1,"number_of_total_results":1,
			"results":{"id":57081,"name":"Dark Reign","deck":"The deck.",
			"description":"<h2>Creative Team</h2><p>Writer - Someone</p><h2>Synopsis</h2><p>%s</p>"}}`, wantDescription)
	}))
	defer srv.Close()
	cvClient := comicvine.New("test-key", t.TempDir())
	cvClient.BaseURL = srv.URL
	r.SetDescriptionSource(cvClient)

	if err := r.RunOnce(); err != nil {
		t.Fatal(err)
	}

	arcs, err := s.ListArcsUpdatedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	var got *store.Arc
	for i := range arcs {
		if arcs[i].ID == "cv-57081" {
			got = &arcs[i]
		}
	}
	if got == nil {
		t.Fatal("cv-57081 not found")
	}
	if got.Description != wantDescription {
		t.Fatalf("expected re-derived synopsis-only description, got %q", got.Description)
	}
}

func TestRunOnceLeavesFetchedAtZeroOnFetchError(t *testing.T) {
	s := newTestStore(t)
	seedEnrichedIssue(t, s, "i1", "A", "1", 10)
	seedEnrichedIssue(t, s, "i2", "A", "2", 11)
	seedEnrichedIssue(t, s, "i3", "A", "3", 12)
	cv := newLocalCVFixture(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cvClient := comicvine.New("test-key", t.TempDir())
	cvClient.BaseURL = srv.URL

	r := NewCVRebuilder(s, cv)
	r.SetDescriptionSource(cvClient)
	if err := r.RunOnce(); err != nil {
		t.Fatal(err)
	}

	remaining, err := s.ArcsNeedingDescription(10, arcDescVersion)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, target := range remaining {
		if target.ArcID == "cv-57081" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cv-57081 to still need a description after a fetch error, got %+v", remaining)
	}
}
