package match

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/ryanhaines/comic-reader/match/gcd"
	"github.com/ryanhaines/comic-reader/match/gcd/gcdtest"
	"github.com/ryanhaines/comic-reader/match/localcv"
	"github.com/ryanhaines/comic-reader/store"
)

// cvDumpSchema is the same minimal ComicVine dump schema newCVFixtureVolumes
// uses, extracted here so the merge tests can seed their own rows (a
// description on the volume, distinct per-issue synopses) without disturbing
// the shared runner_test.go fixture data.
const cvDumpSchema = `
CREATE TABLE cv_publisher (id INTEGER PRIMARY KEY, name TEXT, image_url TEXT, site_detail_url TEXT);
CREATE TABLE cv_volume (id INTEGER PRIMARY KEY, name TEXT NOT NULL, aliases TEXT, start_year TEXT,
	publisher_id INTEGER, count_of_issues INTEGER, description TEXT, image_url TEXT, site_detail_url TEXT);
CREATE TABLE cv_issue (id INTEGER PRIMARY KEY, volume_id INTEGER NOT NULL, name TEXT, issue_number TEXT,
	cover_date TEXT, store_date TEXT, description TEXT, image_url TEXT, site_detail_url TEXT,
	character_credits TEXT, person_credits TEXT, team_credits TEXT, location_credits TEXT,
	story_arc_credits TEXT, associated_images TEXT);
CREATE TABLE comic_files (id INTEGER PRIMARY KEY, filename TEXT, file_size INTEGER, md5_hash TEXT,
	first_page_md5 TEXT, tth_hash TEXT, ct_phash TEXT, ct_ahash TEXT, cvid INTEGER);
`

// openCVDump builds a throwaway localcv.db from cvDumpSchema plus seed SQL,
// mirroring gcdtest.Open for the ComicVine side.
func openCVDump(t *testing.T, seed string) *localcv.DB {
	t.Helper()
	path := t.TempDir() + "/localcv.db"
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(cvDumpSchema + seed); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	cv, err := localcv.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cv.Close() })
	return cv
}

// gcdSagaSeed pins a GCD-side "Saga" (2012) series with two issues, a genre
// on each issue's primary comic story, and a page count — the primary side of
// the merge tests below.
const gcdSagaSeed = `
INSERT INTO gcd_publisher(id, name) VALUES (60, 'Image Comics');
INSERT INTO gcd_series(id, name, year_began, publisher_id, country_id, language_id, issue_count)
	VALUES (500001, 'Saga', 2012, 60, 225, 25, 2);
INSERT INTO gcd_issue(id, series_id, number, title, key_date, variant_of_id, sort_code, page_count)
	VALUES (700001, 500001, '1', '', '2012-03-14', NULL, 1, 32),
	       (700002, 500001, '2', '', '2012-04-11', NULL, 2, 32);
INSERT INTO gcd_story(id, issue_id, title, type_id, sequence_number, genre, characters, synopsis)
	VALUES (800001, 700001, '', 19, 1, 'science fiction', '', 'gcd synopsis one'),
	       (800002, 700002, '', 19, 1, 'science fiction', '', 'gcd synopsis two');
`

// uniqueCVSagaSeed is a single, unambiguous CV "Saga" (2012) volume: the
// exact-unique secondary hit the merge is supposed to find.
const uniqueCVSagaSeed = `
INSERT INTO cv_publisher(id, name) VALUES (10, 'Image');
INSERT INTO cv_volume(id, name, start_year, publisher_id, count_of_issues, description)
	VALUES (49019, 'Saga', '2012', 10, 72, 'cv volume description');
INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, description, person_credits, character_credits)
	VALUES (317966, 49019, 'Chapter One', '1', '2012-03-14', 'cv synopsis one', '[]', '[]'),
	       (317967, 49019, 'Chapter Two', '2', '2012-04-11', 'cv synopsis two', '[]', '[]');
`

// ambiguousCVSagaSeed is TWO CV volumes both named "Saga" and started in
// 2012: pinByName must refuse to pick either, so the merge must no-op.
const ambiguousCVSagaSeed = `
INSERT INTO cv_publisher(id, name) VALUES (10, 'Image');
INSERT INTO cv_volume(id, name, start_year, publisher_id, count_of_issues, description)
	VALUES (49019, 'Saga', '2012', 10, 72, 'cv volume description one'),
	       (49020, 'Saga', '2012', 10, 6, 'cv volume description two');
INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, description, person_credits, character_credits)
	VALUES (317966, 49019, 'Chapter One', '1', '2012-03-14', 'cv synopsis one', '[]', '[]'),
	       (317967, 49019, 'Chapter Two', '2', '2012-04-11', 'cv synopsis two', '[]', '[]'),
	       (317968, 49020, 'Other One', '1', '2012-03-14', 'other synopsis one', '[]', '[]');
`

// confirmedGCDSaga wires a store, a GCD-primary "Saga (2012)" series and a
// caller-supplied cv dump, then calls Apply and confirms — in that order,
// mirroring server/admin/match.go's confirm(), which runs Apply BEFORE
// ConfirmSeriesMatch (deliberately: see its doc comment). Apply, and so
// mergeSecondaryEnrichment, therefore always sees the series_match row in
// its PRE-confirm state; asserting on a row confirmed only after the fact
// would hide a bug where the merge is gated on state == confirmed and never
// fires in production. It returns the store for the test to assert against.
func confirmedGCDSaga(t *testing.T, cv *localcv.DB) *store.Store {
	t.Helper()
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	lib := mkSeries(t, st, "saga", "Saga (2012)", map[string]string{
		"Saga 001.cbz": "1",
		"Saga 002.cbz": "2",
	})

	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdSagaSeed))

	if err := r.Rematch(context.Background(), "saga", store.SourceGCD, "500001"); err != nil {
		t.Fatal(err)
	}
	m, err := st.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != store.SourceGCD || m.VolumeID != "500001" {
		t.Fatalf("precondition: expected a gcd pin, got %+v", m)
	}
	if m.State == store.MatchStateConfirmed {
		t.Fatalf("precondition: match must be pre-confirm when Apply runs, got %+v", m)
	}

	if err := r.Apply("saga"); err != nil {
		t.Fatal(err)
	}

	if err := st.ConfirmSeriesMatch("saga", 2000); err != nil {
		t.Fatal(err)
	}
	return st
}

// A GCD-confirmed "Saga (2012)" series with a UNIQUE matching CV "Saga" 2012
// volume: the opportunistic secondary lookup must find it and merge in CV's
// series description and per-issue summary, while keeping GCD's genre and
// page count.
func TestApplyMergeUniqueSecondaryHit(t *testing.T) {
	cv := openCVDump(t, uniqueCVSagaSeed)
	st := confirmedGCDSaga(t, cv)

	se, err := st.GetSeriesEnrichment("saga")
	if err != nil {
		t.Fatal(err)
	}
	if se.SecondarySource != store.SourceComicVine {
		t.Fatalf("expected comicvine secondary, got %+v", se)
	}
	if se.Description == "" {
		t.Fatalf("CV description not merged in: %+v", se)
	}
	if se.Genre == "" {
		t.Fatalf("GCD genre lost: %+v", se)
	}

	ie, err := st.ListIssueEnrichmentForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ie["saga-Saga 001.cbz"]
	if !ok {
		t.Fatalf("missing issue enrichment for issue 1: %+v", ie)
	}
	if got.Summary != "cv synopsis one" {
		t.Fatalf("expected CV summary, got %q", got.Summary)
	}
	if got.PageCount != 32 {
		t.Fatalf("expected GCD page count, got %v", got.PageCount)
	}
}

// confirmedCVSaga is confirmedGCDSaga's mirror image: CV is primary and the
// caller-supplied gcd dump supplies the secondary. Same pre-confirm-Apply
// ordering as confirmedGCDSaga, for the same reason (see its doc comment).
func confirmedCVSaga(t *testing.T, gcdDB *gcd.DB) *store.Store {
	t.Helper()
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	lib := mkSeries(t, st, "saga", "Saga (2012)", map[string]string{
		"Saga 001.cbz": "1",
		"Saga 002.cbz": "2",
	})

	cv := openCVDump(t, uniqueCVSagaSeed)
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdDB)

	if err := r.Rematch(context.Background(), "saga", store.SourceComicVine, "49019"); err != nil {
		t.Fatal(err)
	}
	m, err := st.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != store.SourceComicVine || m.VolumeID != "49019" {
		t.Fatalf("precondition: expected a cv pin, got %+v", m)
	}
	if m.State == store.MatchStateConfirmed {
		t.Fatalf("precondition: match must be pre-confirm when Apply runs, got %+v", m)
	}

	if err := r.Apply("saga"); err != nil {
		t.Fatal(err)
	}

	if err := st.ConfirmSeriesMatch("saga", 2000); err != nil {
		t.Fatal(err)
	}
	return st
}

// A CV-confirmed "Saga (2012)" series with a UNIQUE matching GCD "Saga" 2012
// series: the opportunistic secondary lookup must find it and merge in GCD's
// genre and page count (GCD-only fields the CV primary lacks), while a shared
// field the primary already populated (publisher: CV's dump has "Image",
// GCD's competing value is "Image Comics") is NOT clobbered by the secondary.
func TestApplyMergeCVPrimaryUniqueSecondaryHit(t *testing.T) {
	st := confirmedCVSaga(t, gcdtest.Open(t, gcdSagaSeed))

	se, err := st.GetSeriesEnrichment("saga")
	if err != nil {
		t.Fatal(err)
	}
	if se.SecondarySource != store.SourceGCD {
		t.Fatalf("expected gcd secondary, got %+v", se)
	}
	if se.Publisher != "Image" {
		t.Fatalf("expected CV publisher to survive the merge over GCD's competing value, got %+v", se)
	}
	if se.Genre != "science fiction" {
		t.Fatalf("expected GCD genre merged in, got %+v", se)
	}

	ie, err := st.ListIssueEnrichmentForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ie["saga-Saga 001.cbz"]
	if !ok {
		t.Fatalf("missing issue enrichment for issue 1: %+v", ie)
	}
	if got.Summary != "cv synopsis one" {
		t.Fatalf("expected CV summary to survive the merge, got %q", got.Summary)
	}
	if got.PageCount != 32 {
		t.Fatalf("expected GCD page count merged in, got %v", got.PageCount)
	}
}

// The same GCD-primary series, but with TWO same-name-year CV volumes:
// ambiguous, so the secondary merge must no-op and leave the series primary-only.
func TestApplyMergeAmbiguousSecondaryHit(t *testing.T) {
	cv := openCVDump(t, ambiguousCVSagaSeed)
	st := confirmedGCDSaga(t, cv)

	se, err := st.GetSeriesEnrichment("saga")
	if err != nil {
		t.Fatal(err)
	}
	if se.SecondarySource != "" {
		t.Fatalf("expected no secondary merge on an ambiguous cv hit, got %+v", se)
	}
	if se.Description != "" {
		t.Fatalf("expected primary-only (gcd has no description), got %+v", se)
	}
}
