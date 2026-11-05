package match

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rthaines211/gutter-server/comicvine"
	"github.com/rthaines211/gutter-server/match/gcd/gcdtest"
	"github.com/rthaines211/gutter-server/match/localcv"
	"github.com/rthaines211/gutter-server/match/parse"
	"github.com/rthaines211/gutter-server/metron"
	"github.com/rthaines211/gutter-server/store"
)

// writeCBZ creates a CBZ whose single page holds the given bytes.
func writeCBZ(t *testing.T, path string, page []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create("001.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(page); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// cvFile is an extra first-page-MD5 -> issue id mapping for the fixture.
type cvFile struct {
	md5     string
	issueID int64
}

// cvIssue is a bare cv_issue row with no comic_files mapping, for exercising
// volume-number binding (an issue the dump has metadata for but whose page
// hash was never in the scene-release index).
type cvIssue struct {
	id       int64
	volumeID int64
	number   string
	imageURL string
}

// newCVFixture builds a localcv fixture mapping pageMD5 -> Saga #1, plus any
// extra mappings given. The dump carries a second volume so tests can exercise
// a folder whose issues resolve to different volumes.
func newCVFixture(t *testing.T, pageMD5 string, extra ...cvFile) *localcv.DB {
	return newCVFixtureFull(t, pageMD5, nil, extra...)
}

// newCVFixtureFull is newCVFixture with one more additive knob: extraIssues
// adds bare cv_issue rows (see cvIssue) alongside the usual md5 mappings.
func newCVFixtureFull(t *testing.T, pageMD5 string, extraIssues []cvIssue, extra ...cvFile) *localcv.DB {
	return newCVFixtureVolumes(t, pageMD5, nil, extraIssues, extra...)
}

// cvVolume is an extra cv_volume row for fixtures that need a volume with a
// specific count_of_issues (one-issue volumes are how the dump files trades).
type cvVolume struct {
	id    int64
	name  string
	year  string
	count int
}

// newCVFixtureVolumes is newCVFixtureFull with one more additive knob:
// extraVolumes adds bare cv_volume rows (see cvVolume) that extraIssues can
// reference.
func newCVFixtureVolumes(t *testing.T, pageMD5 string, extraVolumes []cvVolume, extraIssues []cvIssue, extra ...cvFile) *localcv.DB {
	t.Helper()
	path := t.TempDir() + "/localcv.db"
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := `
	CREATE TABLE cv_publisher (id INTEGER PRIMARY KEY, name TEXT, image_url TEXT, site_detail_url TEXT);
	CREATE TABLE cv_volume (id INTEGER PRIMARY KEY, name TEXT NOT NULL, aliases TEXT, start_year TEXT,
		publisher_id INTEGER, count_of_issues INTEGER, description TEXT, image_url TEXT, site_detail_url TEXT);
	CREATE TABLE cv_issue (id INTEGER PRIMARY KEY, volume_id INTEGER NOT NULL, name TEXT, issue_number TEXT,
		cover_date TEXT, store_date TEXT, description TEXT, image_url TEXT, site_detail_url TEXT,
		character_credits TEXT, person_credits TEXT, team_credits TEXT, location_credits TEXT,
		story_arc_credits TEXT, associated_images TEXT);
	CREATE TABLE comic_files (id INTEGER PRIMARY KEY, filename TEXT, file_size INTEGER, md5_hash TEXT,
		first_page_md5 TEXT, tth_hash TEXT, ct_phash TEXT, ct_ahash TEXT, cvid INTEGER);
	INSERT INTO cv_publisher(id, name) VALUES (10, 'Image');
	INSERT INTO cv_volume(id, name, start_year, publisher_id, count_of_issues) VALUES (49019, 'Saga', '2012', 10, 72);
	INSERT INTO cv_volume(id, name, start_year, publisher_id) VALUES (51234, 'Other Book', '2012', 10);
	INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url, description,
		person_credits, character_credits)
		VALUES (317966, 49019, 'Chapter One', '1', '2012-03-14', 'http://x/1.jpg', 'd', '[]', '[]'),
		       (317967, 49019, 'Chapter Two', '2', '2012-04-11', 'http://x/2.jpg', 'd', '[]', '[]'),
		       (400001, 51234, 'Elsewhere', '3', '2012-05-09', 'http://x/3.jpg', 'd', '[]', '[]');
	-- A third volume whose publisher, start year and description all differ from
	-- Saga's, so a test can tell which volume a series-level field came from.
	-- Purely additive: no existing test references volume 60001 or issue 500001.
	INSERT INTO cv_publisher(id, name) VALUES (11, 'Dark Horse');
	INSERT INTO cv_volume(id, name, start_year, publisher_id) VALUES (60001, 'Minority Book', '1999', 11);
	INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url, description,
		person_credits, character_credits)
		VALUES (500001, 60001, 'Minority One', '1', '1999-07-07', 'http://x/4.jpg', 'minority-desc', '[]', '[]');
	-- An international reprint volume: identical pages under a blocklisted
	-- publisher. Additive: nothing existing maps a hash to issue 600001.
	INSERT INTO cv_publisher(id, name) VALUES (12, 'Panini España');
	INSERT INTO cv_volume(id, name, start_year, publisher_id) VALUES (70001, 'Saga', '2013', 12);
	INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url, description,
		person_credits, character_credits)
		VALUES (600001, 70001, 'Reprint Two', '2', '2013-04-11', 'http://x/5.jpg', 'd', '[]', '[]');
	`
	if _, err := db.Exec(stmts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO comic_files(id, filename, first_page_md5, cvid) VALUES (1, 'Saga 001.cbz', ?, 317966)`,
		pageMD5); err != nil {
		t.Fatal(err)
	}
	for i, f := range extra {
		if _, err := db.Exec(
			`INSERT INTO comic_files(id, filename, first_page_md5, cvid) VALUES (?, ?, ?, ?)`,
			i+2, fmt.Sprintf("extra-%d.cbz", i), f.md5, f.issueID); err != nil {
			t.Fatal(err)
		}
	}
	for _, ev := range extraVolumes {
		if _, err := db.Exec(
			`INSERT INTO cv_volume(id, name, start_year, publisher_id, count_of_issues) VALUES (?, ?, ?, 10, ?)`,
			ev.id, ev.name, ev.year, ev.count); err != nil {
			t.Fatal(err)
		}
	}
	for _, ei := range extraIssues {
		if _, err := db.Exec(
			`INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url, description,
				person_credits, character_credits)
				VALUES (?, ?, '', ?, '', ?, '', '[]', '[]')`,
			ei.id, ei.volumeID, ei.number, ei.imageURL); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	cv, err := localcv.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cv.Close() })
	return cv
}

func TestRunnerMatchesByMD5(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	page := []byte("saga-page-one")
	sum := md5.Sum(page)
	pageMD5 := hex.EncodeToString(sum[:])

	folder := filepath.Join(lib, "Saga (2012)")
	const filename = "Saga 001 (2012).cbz"
	writeCBZ(t, filepath.Join(folder, filename), page)

	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: filename, Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, pageMD5)
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })

	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Series != 1 || res.Issues != 1 {
		t.Fatalf("unexpected result %+v", res)
	}

	m, err := st.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateHighConfidence {
		t.Fatalf("expected high_confidence, got %s (%s)", m.State, m.Penalties)
	}
	if m.VolumeID != "49019" || m.Method != MethodFirstPageMD5 {
		t.Fatalf("unexpected match %+v", m)
	}

	ims, err := st.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 1 || ims[0].UpstreamID != "317966" {
		t.Fatalf("unexpected issue matches %+v", ims)
	}
}

func TestRunnerNoMatch(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Unknown (2020)")
	const filename = "Unknown 001 (2020).cbz"
	writeCBZ(t, filepath.Join(folder, filename), []byte("not-in-the-dump"))

	if err := st.UpsertSeries(store.Series{
		ID: "unknown", Name: "Unknown (2020)", FolderPath: folder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("unknown", []store.Issue{
		{ID: "unknown-001", SeriesID: "unknown", Filename: filename, Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, _ := st.GetSeriesMatch("unknown")
	if m.State != store.MatchStateNoMatch {
		t.Fatalf("expected no_match, got %s", m.State)
	}
}

// md5hex is the first-page MD5 a single-page CBZ holding page will produce.
func md5hex(page []byte) string {
	sum := md5.Sum(page)
	return hex.EncodeToString(sum[:])
}

// A stale issue row (file gone, folder still there) must not stop the batch:
// a quarter of this library's rows name files that moved in a reorg.
func TestRunnerSkipsStaleIssueRow(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	page := []byte("saga-page-one")

	// Sorts first under ListSeries' alphabetical order, so if a stale row
	// aborted the run nothing after it would ever be matched.
	staleFolder := filepath.Join(lib, "AAA Stale (2020)")
	if err := os.MkdirAll(staleFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeries(store.Series{
		ID: "stale", Name: "AAA Stale (2020)", FolderPath: staleFolder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("stale", []store.Issue{
		// Never written to disk.
		{ID: "stale-001", SeriesID: "stale", Filename: "AAA Stale 001 (2020).cbz", Number: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	sagaFolder := filepath.Join(lib, "Saga (2012)")
	writeCBZ(t, filepath.Join(sagaFolder, "Saga 001 (2012).cbz"), page)
	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: sagaFolder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001 (2012).cbz", Number: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, md5hex(page))
	res, err := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 }).Run(context.Background())
	if err != nil {
		t.Fatalf("a stale row must not abort the run: %v", err)
	}
	if res.Series != 2 {
		t.Fatalf("expected both series processed, got %+v", res)
	}

	stale, err := st.GetSeriesMatch("stale")
	if err != nil {
		t.Fatal(err)
	}
	if stale.State != store.MatchStateNoMatch {
		t.Fatalf("expected no_match for the stale series, got %s", stale.State)
	}

	// The whole point: the series after the stale one still got matched.
	saga, err := st.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if saga.State != store.MatchStateHighConfidence || saga.VolumeID != "49019" {
		t.Fatalf("series after the stale one was not matched: %+v", saga)
	}
}

// A renamed series folder is filesystem-identical to an unmounted one, so the
// roots — not the folder — decide. A reorg renamed six of this library's
// folders while the drive stayed mounted; none of them may stop the batch.
func TestRunnerSkipsRenamedSeriesFolder(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	page := []byte("saga-page-one")

	// Sorts first, and its folder was "renamed away" — never created.
	if err := st.UpsertSeries(store.Series{
		ID: "renamed", Name: "AAA Renamed (2007)",
		FolderPath: filepath.Join(lib, "AAA Renamed Vol. 1 - 4 (2007)"), IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("renamed", []store.Issue{
		{ID: "renamed-001", SeriesID: "renamed", Filename: "AAA Renamed v01 (2007).cbz", Number: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	sagaFolder := filepath.Join(lib, "Saga (2012)")
	writeCBZ(t, filepath.Join(sagaFolder, "Saga 001 (2012).cbz"), page)
	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: sagaFolder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001 (2012).cbz", Number: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, md5hex(page))
	res, err := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 }).Run(context.Background())
	if err != nil {
		t.Fatalf("a renamed folder must not abort the run: %v", err)
	}
	if res.Series != 2 {
		t.Fatalf("expected both series processed, got %+v", res)
	}

	renamed, err := st.GetSeriesMatch("renamed")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.State != store.MatchStateNoMatch {
		t.Fatalf("expected no_match for the renamed series, got %s", renamed.State)
	}

	// The whole point: the series after the renamed one still got matched.
	saga, err := st.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if saga.State != store.MatchStateHighConfidence || saga.VolumeID != "49019" {
		t.Fatalf("series after the renamed one was not matched: %+v", saga)
	}
}

// A missing root means the external drive went away, so the batch must stop
// rather than rewrite every remaining series as no_match.
func TestRunnerAbortsOnMissingRoot(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Gone (2020)")
	if err := st.UpsertSeries(store.Series{
		ID: "gone", Name: "Gone (2020)", FolderPath: folder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("gone", []store.Issue{
		{ID: "gone-001", SeriesID: "gone", Filename: "Gone 001 (2020).cbz", Number: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	// The configured root does not exist: the drive is unmounted.
	unmounted := filepath.Join(t.TempDir(), "not-mounted")
	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	if _, err := NewRunner(st, cv, []string{unmounted}, func() int64 { return 1000 }).
		Run(context.Background()); err == nil {
		t.Fatal("expected Run to abort when a configured root is missing")
	}

	if _, err := st.GetSeriesMatch("gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no match row should have been written on abort, got err %v", err)
	}
}

// No roots means no unmount signal, so never abort — failing open beats
// silently stopping the batch.
func TestRunnerNoRootsNeverAborts(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	folder := filepath.Join(t.TempDir(), "Gone (2020)")
	if err := st.UpsertSeries(store.Series{
		ID: "gone", Name: "Gone (2020)", FolderPath: folder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("gone", []store.Issue{
		{ID: "gone-001", SeriesID: "gone", Filename: "Gone 001 (2020).cbz", Number: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	if _, err := NewRunner(st, cv, nil, func() int64 { return 1000 }).
		Run(context.Background()); err != nil {
		t.Fatalf("with no roots configured Run must never abort: %v", err)
	}

	m, err := st.GetSeriesMatch("gone")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateNoMatch {
		t.Fatalf("expected no_match, got %s", m.State)
	}
}

// One misfiled issue must not become the folder's recorded volume.
func TestRunnerMajorityVolumeWins(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Saga (2012)")
	p1 := []byte("saga-page-one")
	p2 := []byte("saga-page-two")
	p3 := []byte("saga-page-three")
	writeCBZ(t, filepath.Join(folder, "Saga 001 (2012).cbz"), p1)
	writeCBZ(t, filepath.Join(folder, "Saga 002 (2012).cbz"), p2)
	writeCBZ(t, filepath.Join(folder, "Saga 003 (2012).cbz"), p3)

	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001 (2012).cbz", Number: "1", Position: 0},
		{ID: "saga-002", SeriesID: "saga", Filename: "Saga 002 (2012).cbz", Number: "2", Position: 1},
		{ID: "saga-003", SeriesID: "saga", Filename: "Saga 003 (2012).cbz", Number: "3", Position: 2},
	}); err != nil {
		t.Fatal(err)
	}

	// Two issues resolve to volume 49019, one to 51234.
	cv := newCVFixture(t, md5hex(p1),
		cvFile{md5: md5hex(p2), issueID: 317967},
		cvFile{md5: md5hex(p3), issueID: 400001})

	if _, err := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 }).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.VolumeID != "49019" {
		t.Fatalf("expected the majority volume 49019, got %s", m.VolumeID)
	}
	if m.State != store.MatchStateAmbiguous {
		t.Fatalf("a split folder must be ambiguous, got %s", m.State)
	}
	if !strings.Contains(m.Penalties, `"volume"`) {
		t.Fatalf("expected a volume penalty, got %s", m.Penalties)
	}
}

// LookupIssue backs the review console's cover-vs-cover comparison: it must
// resolve a known upstream id and fail cleanly on one the dump doesn't have.
func TestLookupIssue(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	r := NewRunner(st, cv, nil, func() int64 { return 1000 })

	iss, err := r.LookupIssue(store.SourceComicVine, "317966")
	if err != nil {
		t.Fatal(err)
	}
	if iss.VolumeName != "Saga" || iss.IssueNumber != "1" {
		t.Fatalf("unexpected issue %+v", iss)
	}

	if _, err := r.LookupIssue(store.SourceComicVine, "999999999"); err == nil {
		t.Fatal("expected an error for an id the dump doesn't have")
	}

	if _, err := r.LookupIssue(store.SourceComicVine, "not-a-number"); err == nil {
		t.Fatal("expected an error for an unparseable id")
	}
}

// Issue 1's page hash is in the fixture; issue 2's is not, but both exist in
// cv_issue for the volume issue 1 pinned. Issue 2 must bind by number, not
// stay no_match.
func TestRunnerBindsByVolumeNumberWhenHashMissing(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Saga (2012)")
	p1 := []byte("saga-page-one")
	writeCBZ(t, filepath.Join(folder, "Saga 001 (2012).cbz"), p1)
	writeCBZ(t, filepath.Join(folder, "Saga 002 (2012).cbz"), []byte("not-in-the-dump"))

	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001 (2012).cbz", Number: "1", Position: 0},
		{ID: "saga-002", SeriesID: "saga", Filename: "Saga 002 (2012).cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	// Only issue 1's hash is in the fixture; issue 2 (317967, #2) exists in
	// cv_issue for the same volume (49019) but has no comic_files row.
	cv := newCVFixture(t, md5hex(p1))

	if _, err := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 }).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	ims, err := st.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 2 {
		t.Fatalf("expected 2 issue matches, got %d: %+v", len(ims), ims)
	}
	byIssue := map[string]store.IssueMatch{}
	for _, im := range ims {
		byIssue[im.IssueID] = im
	}
	m1, ok := byIssue["saga-001"]
	if !ok || m1.Method != MethodFirstPageMD5 || m1.UpstreamID != "317966" {
		t.Fatalf("unexpected match for issue 1: %+v", m1)
	}
	m2, ok := byIssue["saga-002"]
	if !ok || m2.Method != MethodVolumeNumber || m2.UpstreamID != "317967" {
		t.Fatalf("unexpected match for issue 2: %+v", m2)
	}
}

// With no hash hit and no dump volume of that name there is no anchor of any
// kind, and nothing may bind by number: number-binding into an unrelated
// volume would be guessing. (A zero-hit folder whose name and year the dump
// does know is the name_year pin, covered separately.)
func TestRunnerSkipsVolumeNumberWithoutAnyAnchor(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Nobody Home (2012)")
	writeCBZ(t, filepath.Join(folder, "Nobody Home 001 (2012).cbz"), []byte("not-in-the-dump-1"))
	writeCBZ(t, filepath.Join(folder, "Nobody Home 002 (2012).cbz"), []byte("not-in-the-dump-2"))

	if err := st.UpsertSeries(store.Series{
		ID: "nobody", Name: "Nobody Home (2012)", FolderPath: folder, IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("nobody", []store.Issue{
		{ID: "nb-001", SeriesID: "nobody", Filename: "Nobody Home 001 (2012).cbz", Number: "1", Position: 0},
		{ID: "nb-002", SeriesID: "nobody", Filename: "Nobody Home 002 (2012).cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	// Neither page hash is in comic_files, and no volume is called Nobody Home.
	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")

	if _, err := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 }).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	ims, err := st.ListIssueMatchesForSeries("nobody")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 0 {
		t.Fatalf("expected no issue matches without any anchor, got %+v", ims)
	}

	m, err := st.GetSeriesMatch("nobody")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateNoMatch {
		t.Fatalf("expected no_match, got %s", m.State)
	}
}

// Local number "008" must bind to upstream "8" via NormalizeNumber.
func TestRunnerVolumeNumberNormalizesPadding(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Saga (2012)")
	p1 := []byte("saga-page-one")
	writeCBZ(t, filepath.Join(folder, "Saga 001 (2012).cbz"), p1)
	writeCBZ(t, filepath.Join(folder, "Saga 008 (2012).cbz"), []byte("not-in-the-dump"))

	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001 (2012).cbz", Number: "1", Position: 0},
		{ID: "saga-008", SeriesID: "saga", Filename: "Saga 008 (2012).cbz", Number: "008", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	// Extra upstream issue #8 in the pinned volume 49019, with no comic_files
	// mapping — it can only be reached by number, not by hash.
	cv := newCVFixtureFull(t, md5hex(p1), []cvIssue{{id: 400008, volumeID: 49019, number: "8"}})

	if _, err := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 }).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	ims, err := st.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	byIssue := map[string]store.IssueMatch{}
	for _, im := range ims {
		byIssue[im.IssueID] = im
	}
	m8, ok := byIssue["saga-008"]
	if !ok {
		t.Fatalf("expected issue 008 to bind, got %+v", ims)
	}
	if m8.Method != MethodVolumeNumber || m8.UpstreamID != "400008" {
		t.Fatalf("expected volume_number binding to upstream 400008, got %+v", m8)
	}
}

// A one-file collection ("Oblivion Song Compendium Vol 001.cbz", Number="1"
// from the volume marker) against a one-issue upstream volume can only be
// that issue, even though the sole issue is GCD's numberless placeholder
// ("[nn]") and byNumber lookup misses. A numbered SINGLE (non-collection)
// against that same sole issue can only be that issue too — a one-file
// folder pinned to a one-issue volume binds regardless of a stray number
// token or the collection flag ("Fantastic Four Life Story 522 (2022).cbz").
func TestBindByNumberCollectionBindsToSoleNumberlessIssue(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "oblivion", Name: "Oblivion Song Compendium", FolderPath: "/lib/Oblivion Song Compendium Vol 1", IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("oblivion", []store.Issue{
		{ID: "os-1", SeriesID: "oblivion", Filename: "Oblivion Song Compendium Vol 001.cbz", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(st, nil, nil, func() int64 { return 1000 })
	folder := parse.Folder{Series: "Oblivion Song Compendium"}
	upIssues := []localcv.Issue{{IssueID: 999, IssueNumber: "[nn]", VolumeIssueCount: 1}}

	items := []unmatchedIssue{{
		iss:  store.Issue{ID: "os-1", SeriesID: "oblivion"},
		file: parse.File{Series: "Oblivion Song Compendium", Number: "1", IsCollection: true},
	}}
	_, bound, remaining, err := r.bindByNumber(context.Background(), folder, items, upIssues, store.SourceGCD, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if bound != 1 || len(remaining) != 0 {
		t.Fatalf("expected the collection to bind to the sole issue, got bound=%d remaining=%+v", bound, remaining)
	}
	ims, err := st.ListIssueMatchesForSeries("oblivion")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 1 || ims[0].UpstreamID != "999" {
		t.Fatalf("expected one issue_match row to upstream 999, got %+v", ims)
	}

	// A numbered SINGLE (non-collection) against the same one-issue volume
	// binds too: a one-file folder pinned to a one-issue volume can only be
	// that issue, stray number token or not. Fails without the fix (the file
	// falls through to byNumber, misses "[nn]", and lands in remaining).
	singleItems := []unmatchedIssue{{
		iss:  store.Issue{ID: "os-1", SeriesID: "oblivion"},
		file: parse.File{Series: "Oblivion Song Compendium", Number: "1", IsCollection: false},
	}}
	_, bound, remaining, err = r.bindByNumber(context.Background(), folder, singleItems, upIssues, store.SourceGCD, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if bound != 1 || len(remaining) != 0 {
		t.Fatalf("expected the numbered single to bind to the sole issue, got bound=%d remaining=%+v", bound, remaining)
	}
	ims, err = st.ListIssueMatchesForSeries("oblivion")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 1 || ims[0].UpstreamID != "999" {
		t.Fatalf("expected one issue_match row to upstream 999, got %+v", ims)
	}
}

// A numbered single against a MULTI-issue volume still misses on mismatch —
// soleFileSoleIssue never applies when len(upIssues) > 1.
func TestBindByNumberMultiIssueVolumeStillMissesOnMismatch(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	r := NewRunner(st, nil, nil, func() int64 { return 1000 })
	folder := parse.Folder{Series: "Fantastic Four"}
	upIssues := []localcv.Issue{{IssueID: 1, IssueNumber: "1"}, {IssueID: 2, IssueNumber: "2"}}

	items := []unmatchedIssue{{
		iss:  store.Issue{ID: "ff-5", SeriesID: "ff"},
		file: parse.File{Series: "Fantastic Four", Number: "5", IsCollection: false},
	}}
	_, bound, remaining, err := r.bindByNumber(context.Background(), folder, items, upIssues, store.SourceGCD, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if bound != 0 || len(remaining) != 1 {
		t.Fatalf("expected the mismatched number to miss against a multi-issue volume, got bound=%d remaining=%+v", bound, remaining)
	}
}

// A MULTI-file folder against a one-issue volume is not force-bound — each
// file still binds strictly by number. soleFileSoleIssue never applies when
// len(items) > 1.
func TestBindByNumberMultiFileFolderNotForceBound(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "ff", Name: "Fantastic Four", FolderPath: "/lib/Fantastic Four", IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("ff", []store.Issue{
		{ID: "ff-1", SeriesID: "ff", Filename: "Fantastic Four 001.cbz", Position: 0},
		{ID: "ff-2", SeriesID: "ff", Filename: "Fantastic Four 002.cbz", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(st, nil, nil, func() int64 { return 1000 })
	folder := parse.Folder{Series: "Fantastic Four"}
	upIssues := []localcv.Issue{{IssueID: 111, IssueNumber: "1"}}

	items := []unmatchedIssue{
		{iss: store.Issue{ID: "ff-1", SeriesID: "ff"}, file: parse.File{Series: "Fantastic Four", Number: "1", IsCollection: false}},
		{iss: store.Issue{ID: "ff-2", SeriesID: "ff"}, file: parse.File{Series: "Fantastic Four", Number: "2", IsCollection: false}},
	}
	_, bound, remaining, err := r.bindByNumber(context.Background(), folder, items, upIssues, store.SourceGCD, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if bound != 1 || len(remaining) != 1 {
		t.Fatalf("expected only the matching number to bind and the other to remain, got bound=%d remaining=%+v", bound, remaining)
	}
	if remaining[0].file.Number != "2" {
		t.Fatalf("expected issue 2 to be the one left over, got %+v", remaining[0])
	}
}

// A confirmed or dont_match series is never reprocessed.
func TestRunnerSkipsHumanDecisions(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Saga (2012)")
	writeCBZ(t, filepath.Join(folder, "Saga 001 (2012).cbz"), []byte("saga-page-one"))

	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "saga", Source: store.SourceComicVine, VolumeID: "999",
		State: store.MatchStateConfirmed, Method: "manual", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 2000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, _ := st.GetSeriesMatch("saga")
	if m.State != store.MatchStateConfirmed || m.VolumeID != "999" {
		t.Fatalf("confirmed match was overwritten: %+v", m)
	}
}

// fakeCVServer serves the live /issues/ endpoint with the given issues, as a
// stand-in for comicvine.gamespot.com in liveBind tests.
func fakeCVServer(t *testing.T, issues string) *comicvine.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/issues/") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"status_code":1,"number_of_total_results":%d,"results":[%s]}`,
			strings.Count(issues, `"id"`), issues)
	}))
	t.Cleanup(srv.Close)
	c := comicvine.New("test-key", t.TempDir())
	c.BaseURL = srv.URL
	return c
}

// A confirmed series is skipped by the offline passes forever, so an issue
// bought after confirmation never gets a match row. FillConfirmed must fill
// exactly that gap — by number, against the confirmed volume, falling back to
// live ComicVine once the offline dump's number pass misses — while leaving
// the series' confirmed state untouched.
func TestFillConfirmedLiveBindsNewIssues(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	folder := filepath.Join(t.TempDir(), "Saga (2012)")
	writeCBZ(t, filepath.Join(folder, "Saga 061 (2026).cbz"), []byte("saga-sixty-one"))
	writeCBZ(t, filepath.Join(folder, "Saga 999 (2026).cbz"), []byte("saga-nine-ninety-nine"))
	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001 (2012).cbz", Number: "1", Position: 0},
		{ID: "saga-061", SeriesID: "saga", Filename: "Saga 061 (2026).cbz", Number: "61", Position: 1},
		{ID: "saga-999", SeriesID: "saga", Filename: "Saga 999 (2026).cbz", Number: "999", Position: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "saga", Source: store.SourceComicVine, VolumeID: "49019",
		State: store.MatchStateConfirmed, Method: MethodFirstPageMD5, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssueMatch(store.IssueMatch{
		IssueID: "saga-001", Source: store.SourceComicVine, UpstreamID: "317966",
		State: store.MatchStateConfirmed, Method: MethodFirstPageMD5,
	}); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(st, newCVFixture(t, "0000"), []string{folder}, func() int64 { return 1000 })
	r.SetLive(fakeCVServer(t, `{"id":999001,"issue_number":"61","name":"Chapter 61","cover_date":"2026-08-01"}`))

	s, err := st.GetSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.FillConfirmed(context.Background(), *s); err != nil {
		t.Fatal(err)
	}

	ims, err := st.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	byIssue := map[string]store.IssueMatch{}
	for _, im := range ims {
		byIssue[im.IssueID] = im
	}
	got, ok := byIssue["saga-061"]
	if !ok {
		t.Fatalf("no live match row for saga-061, have %+v", ims)
	}
	if got.UpstreamID != "999001" || got.Method != MethodLiveVolumeNumber {
		t.Fatalf("unexpected live match %+v", got)
	}
	if _, ok := byIssue["saga-999"]; ok {
		t.Fatal("issue 999 is not in the live volume, must stay unmatched")
	}

	m, err := st.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateConfirmed {
		t.Fatalf("series state changed to %s, must stay confirmed", m.State)
	}
}

// Pass 3: an unconfirmed series whose volume was pinned by MD5 still has
// issues the offline dump has never heard of (post-Feb-2026). They must bind
// by number against the live volume list.
func TestLivePassBindsWhatTheDumpCannot(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	page := []byte("saga-page-one")
	sum := md5.Sum(page)
	pageMD5 := hex.EncodeToString(sum[:])

	folder := filepath.Join(lib, "Saga (2012)")
	writeCBZ(t, filepath.Join(folder, "Saga 001 (2012).cbz"), page)
	writeCBZ(t, filepath.Join(folder, "Saga 061 (2026).cbz"), []byte("unknown-to-dump"))

	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001 (2012).cbz", Number: "1", Position: 0},
		{ID: "saga-061", SeriesID: "saga", Filename: "Saga 061 (2026).cbz", Number: "61", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(st, newCVFixture(t, pageMD5), []string{lib}, func() int64 { return 1000 })
	r.SetLive(fakeCVServer(t, `{"id":999001,"issue_number":"61","name":"Chapter 61","cover_date":"2026-08-01"}`))

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	ims, err := st.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	byIssue := map[string]store.IssueMatch{}
	for _, im := range ims {
		byIssue[im.IssueID] = im
	}
	if got := byIssue["saga-061"]; got.UpstreamID != "999001" || got.Method != MethodLiveVolumeNumber {
		t.Fatalf("live pass did not bind saga-061: %+v", ims)
	}
}

// fakeMetronServer serves series-by-cv_id, series detail, and the issue list
// for one series, as a stand-in for metron.cloud.
func fakeMetronServer(t *testing.T, cvVolumeID int, issuesJSON string) *metron.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/series/" && r.URL.Query().Get("cv_id") == strconv.Itoa(cvVolumeID):
			fmt.Fprint(w, `{"count":1,"next":null,"previous":null,
				"results":[{"id":900,"series":"Saga (2012)","year_began":2012,"issue_count":70}]}`)
		case r.URL.Path == "/series/900/":
			fmt.Fprintf(w, `{"id":900,"name":"Saga","year_began":2012,"desc":"d",
				"issue_count":70,"publisher":{"name":"Image"},"cv_id":%d}`, cvVolumeID)
		case r.URL.Path == "/issue/":
			fmt.Fprintf(w, `{"count":9,"next":null,"previous":null,"results":[%s]}`, issuesJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := metron.New("u", "p", t.TempDir())
	c.BaseURL = srv.URL
	return c
}

// An issue the live CV pass can't bind (not in the CV volume list) must fall
// through to Metron and get a metron-source match row; an issue neither
// provider knows stays unmatched.
func TestMetronBindsLiveLeftovers(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	folder := filepath.Join(t.TempDir(), "Saga (2012)")
	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-070", SeriesID: "saga", Filename: "Saga 070 (2026).cbz", Number: "70", Position: 0},
		{ID: "saga-999", SeriesID: "saga", Filename: "Saga 999 (2026).cbz", Number: "999", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "saga", Source: store.SourceComicVine, VolumeID: "49019",
		State: store.MatchStateConfirmed, Method: MethodFirstPageMD5, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, "0000000000000000000000000000dead")
	r := NewRunner(st, cv, []string{filepath.Dir(folder)}, func() int64 { return 2000 })
	// Live CV knows nothing (empty issue list) -> both issues are leftovers.
	r.SetLive(fakeCVServer(t, ``))
	// Metron knows #70 but not #999.
	r.SetMetron(fakeMetronServer(t, 49019,
		`{"id":9070,"issue":"Saga (2012) #70","number":"70","cover_date":"2026-03-01"}`))

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	ims, err := st.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	byIssue := map[string]store.IssueMatch{}
	for _, im := range ims {
		byIssue[im.IssueID] = im
	}
	m70, ok := byIssue["saga-070"]
	if !ok {
		t.Fatal("saga-070 got no match row")
	}
	if m70.Source != store.SourceMetron || m70.UpstreamID != "9070" ||
		m70.Method != MethodMetronVolumeNumber {
		t.Errorf("metron bind wrong: %+v", m70)
	}
	if _, ok := byIssue["saga-999"]; ok {
		t.Error("saga-999 must stay unmatched (neither provider knows it)")
	}
}

// With no live CV client at all, the confirmed-series fill must still reach
// Metron: liveBind reports every item as a leftover when r.live is nil.
func TestMetronFillsConfirmedWithoutLiveCV(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	folder := filepath.Join(t.TempDir(), "Saga (2012)")
	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-070", SeriesID: "saga", Filename: "Saga 070 (2026).cbz", Number: "70", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "saga", Source: store.SourceComicVine, VolumeID: "49019",
		State: store.MatchStateConfirmed, Method: MethodFirstPageMD5, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, "0000000000000000000000000000dead")
	r := NewRunner(st, cv, []string{filepath.Dir(folder)}, func() int64 { return 2000 })
	// No SetLive: Metron alone must fill the gap.
	r.SetMetron(fakeMetronServer(t, 49019,
		`{"id":9070,"issue":"Saga (2012) #70","number":"70","cover_date":"2026-03-01"}`))

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	ims, err := st.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 1 || ims[0].IssueID != "saga-070" {
		t.Fatalf("expected one match row for saga-070, got %+v", ims)
	}
	if ims[0].Source != store.SourceMetron || ims[0].UpstreamID != "9070" ||
		ims[0].Method != MethodMetronVolumeNumber {
		t.Errorf("metron fill wrong: %+v", ims[0])
	}
}

// matchSeries pass 4: an UNCONFIRMED series whose volume was pinned by an MD5
// hit still has issues neither the dump (pass 2) nor live ComicVine (pass 3)
// can bind. Those live-pass leftovers must fall through to Metron.
func TestMatchSeriesMetronPassBindsLiveLeftovers(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	page := []byte("saga-page-one")
	sum := md5.Sum(page)
	pageMD5 := hex.EncodeToString(sum[:])

	folder := filepath.Join(lib, "Saga (2012)")
	writeCBZ(t, filepath.Join(folder, "Saga 001 (2012).cbz"), page)
	writeCBZ(t, filepath.Join(folder, "Saga 070 (2026).cbz"), []byte("unknown-to-dump-070"))
	writeCBZ(t, filepath.Join(folder, "Saga 999 (2026).cbz"), []byte("unknown-to-dump-999"))

	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: folder, IssueCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001 (2012).cbz", Number: "1", Position: 0},
		{ID: "saga-070", SeriesID: "saga", Filename: "Saga 070 (2026).cbz", Number: "70", Position: 1},
		{ID: "saga-999", SeriesID: "saga", Filename: "Saga 999 (2026).cbz", Number: "999", Position: 2},
	}); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(st, newCVFixture(t, pageMD5), []string{lib}, func() int64 { return 1000 })
	// Live CV knows nothing beyond the dump -> #70 and #999 are leftovers.
	r.SetLive(fakeCVServer(t, ``))
	// Metron knows #70 but not #999.
	r.SetMetron(fakeMetronServer(t, 49019,
		`{"id":9070,"issue":"Saga (2012) #70","number":"70","cover_date":"2026-03-01"}`))

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	ims, err := st.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	byIssue := map[string]store.IssueMatch{}
	for _, im := range ims {
		byIssue[im.IssueID] = im
	}
	if got := byIssue["saga-001"]; got.Method != MethodFirstPageMD5 {
		t.Fatalf("MD5 anchor did not bind saga-001: %+v", ims)
	}
	m70, ok := byIssue["saga-070"]
	if !ok {
		t.Fatal("saga-070 got no match row from the metron pass")
	}
	if m70.Source != store.SourceMetron || m70.UpstreamID != "9070" ||
		m70.Method != MethodMetronVolumeNumber {
		t.Errorf("metron pass-4 bind wrong: %+v", m70)
	}
	if _, ok := byIssue["saga-999"]; ok {
		t.Error("saga-999 must stay unmatched (neither provider knows it)")
	}
}

// A series with no MD5 hits anywhere (all issues post-date the dump) must be
// matched through Metron series search: series_match gets the ComicVine
// cross-reference id as its volume_id, and issues bind by number.
func TestMetronSeriesFallbackMatchesUnhashedSeries(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "New Book (2026)")
	writeCBZ(t, filepath.Join(folder, "New Book 001 (2026).cbz"), []byte("unknown-page"))
	if err := st.UpsertSeries(store.Series{
		ID: "newbook", Name: "New Book (2026)", FolderPath: folder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("newbook", []store.Issue{
		{ID: "nb-001", SeriesID: "newbook", Filename: "New Book 001 (2026).cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/series/" && r.URL.Query().Get("name") != "":
			fmt.Fprint(w, `{"count":1,"next":null,"previous":null,
				"results":[{"id":800,"series":"New Book (2026)","year_began":2026,"issue_count":3}]}`)
		case r.URL.Path == "/series/" && r.URL.Query().Get("cv_id") == "77001":
			fmt.Fprint(w, `{"count":1,"next":null,"previous":null,
				"results":[{"id":800,"series":"New Book (2026)","year_began":2026,"issue_count":3}]}`)
		case r.URL.Path == "/series/800/":
			fmt.Fprint(w, `{"id":800,"name":"New Book","year_began":2026,"desc":"d",
				"issue_count":3,"publisher":{"name":"Image"},"cv_id":77001}`)
		case r.URL.Path == "/issue/":
			fmt.Fprint(w, `{"count":1,"next":null,"previous":null,
				"results":[{"id":8001,"issue":"New Book (2026) #1","number":"1","cover_date":"2026-01-01"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	mc := metron.New("u", "p", t.TempDir())
	mc.BaseURL = srv.URL

	cv := newCVFixture(t, "00000000000000000000000000000001") // dump knows nothing of this book
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 2000 })
	r.SetMetron(mc)

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	sm, err := st.GetSeriesMatch("newbook")
	if err != nil {
		t.Fatal(err)
	}
	if sm.VolumeID != "77001" {
		t.Errorf("series volume_id must be the CV cross-reference id, got %q (state %s)", sm.VolumeID, sm.State)
	}
	ims, _ := st.ListIssueMatchesForSeries("newbook")
	if len(ims) != 1 || ims[0].Source != store.SourceMetron || ims[0].UpstreamID != "8001" {
		t.Errorf("issue not metron-bound: %+v", ims)
	}
}

// A Metron series without a cv_id cannot feed the CV-keyed tables — the
// fallback must skip it and leave the series no_match.
func TestMetronSeriesFallbackSkipsWithoutCVID(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Indie Book (2026)")
	writeCBZ(t, filepath.Join(folder, "Indie Book 001 (2026).cbz"), []byte("indie-page"))
	if err := st.UpsertSeries(store.Series{
		ID: "indie", Name: "Indie Book (2026)", FolderPath: folder, IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("indie", []store.Issue{
		{ID: "in-001", SeriesID: "indie", Filename: "Indie Book 001 (2026).cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/series/":
			fmt.Fprint(w, `{"count":1,"next":null,"previous":null,
				"results":[{"id":801,"series":"Indie Book (2026)","year_began":2026,"issue_count":1}]}`)
		case r.URL.Path == "/series/801/":
			fmt.Fprint(w, `{"id":801,"name":"Indie Book","year_began":2026,"desc":"d",
				"issue_count":1,"publisher":{"name":"Small"},"cv_id":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	mc := metron.New("u", "p", t.TempDir())
	mc.BaseURL = srv.URL

	cv := newCVFixture(t, "00000000000000000000000000000002")
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 2000 })
	r.SetMetron(mc)

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	sm, err := st.GetSeriesMatch("indie")
	if err != nil {
		t.Fatal(err)
	}
	if sm.VolumeID != "" || sm.State != store.MatchStateNoMatch {
		t.Errorf("cv_id-less metron series must be skipped: %+v", sm)
	}
}

// A partial Metron bind (some issues bound, some not) must not promote the
// whole series: only the bound issues' pass-1 no_match votes are superseded,
// the rest survive and keep the series out of bulk-confirmable states.
// One of THREE issues binds so the unbound votes hold a strict majority —
// dominantState breaks a 50/50 tie toward high_confidence, so a 1-of-2 shape
// cannot pin this behavior.
func TestMetronSeriesFallbackPartialBindDoesNotPromote(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "New Book (2026)")
	writeCBZ(t, filepath.Join(folder, "New Book 001 (2026).cbz"), []byte("unknown-page-1"))
	writeCBZ(t, filepath.Join(folder, "New Book 002 (2026).cbz"), []byte("unknown-page-2"))
	writeCBZ(t, filepath.Join(folder, "New Book 003 (2026).cbz"), []byte("unknown-page-3"))
	if err := st.UpsertSeries(store.Series{
		ID: "newbook", Name: "New Book (2026)", FolderPath: folder, IssueCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("newbook", []store.Issue{
		{ID: "nb-001", SeriesID: "newbook", Filename: "New Book 001 (2026).cbz", Number: "1", Position: 0},
		{ID: "nb-002", SeriesID: "newbook", Filename: "New Book 002 (2026).cbz", Number: "2", Position: 1},
		{ID: "nb-003", SeriesID: "newbook", Filename: "New Book 003 (2026).cbz", Number: "3", Position: 2},
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/series/":
			fmt.Fprint(w, `{"count":1,"next":null,"previous":null,
				"results":[{"id":800,"series":"New Book (2026)","year_began":2026,"issue_count":3}]}`)
		case r.URL.Path == "/series/800/":
			fmt.Fprint(w, `{"id":800,"name":"New Book","year_began":2026,"desc":"d",
				"issue_count":3,"publisher":{"name":"Image"},"cv_id":77001}`)
		case r.URL.Path == "/issue/":
			// Metron only knows issue #1; #2 and #3 stay unbound.
			fmt.Fprint(w, `{"count":1,"next":null,"previous":null,
				"results":[{"id":8001,"issue":"New Book (2026) #1","number":"1","cover_date":"2026-01-01"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	mc := metron.New("u", "p", t.TempDir())
	mc.BaseURL = srv.URL

	cv := newCVFixture(t, "00000000000000000000000000000003")
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 2000 })
	r.SetMetron(mc)

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	sm, err := st.GetSeriesMatch("newbook")
	if err != nil {
		t.Fatal(err)
	}
	if sm.VolumeID != "77001" {
		t.Errorf("volume_id must still be the CV cross-reference id, got %q", sm.VolumeID)
	}
	// {high_confidence:1, no_match:2} — the surviving unbound votes dominate.
	if sm.State != store.MatchStateNoMatch {
		t.Errorf("partial bind must not promote the series, got state %s", sm.State)
	}
	ims, _ := st.ListIssueMatchesForSeries("newbook")
	if len(ims) != 1 || ims[0].IssueID != "nb-001" || ims[0].Source != store.SourceMetron {
		t.Errorf("expected exactly one metron row for nb-001, got %+v", ims)
	}
}

// Shape A: a folder of trades whose hashes all land in ONE multi-issue trade
// volume is an ordinary high-confidence series pinned to that volume. The
// parsed format is written onto each issue row.
func TestRunnerTradeFolderSingleVolume(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Saga (2012-2022)")
	p1, p2 := []byte("saga-tpb-1"), []byte("saga-tpb-2")
	writeCBZ(t, filepath.Join(folder, "Saga v01 (2012).cbz"), p1)
	writeCBZ(t, filepath.Join(folder, "Saga v02 (2013).cbz"), p2)

	if err := st.UpsertSeries(store.Series{ID: "saga-tpb", Name: "Saga (2012-2022)", FolderPath: folder, IssueCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga-tpb", []store.Issue{
		{ID: "t1", SeriesID: "saga-tpb", Filename: "Saga v01 (2012).cbz", Number: "1", Position: 0},
		{ID: "t2", SeriesID: "saga-tpb", Filename: "Saga v02 (2013).cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	// Volume 52845 = the dump's 12-issue "Saga" TPB volume. The fixture's
	// default pageMD5 slot is given a dummy hash so only the explicit
	// cvFile mappings below resolve.
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{{id: 52845, name: "Saga", year: "2012", count: 12}},
		[]cvIssue{{id: 360829, volumeID: 52845, number: "1"}, {id: 411844, volumeID: 52845, number: "2"}},
		cvFile{md5: md5hex(p1), issueID: 360829}, cvFile{md5: md5hex(p2), issueID: 411844})

	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetSeriesMatch("saga-tpb")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateHighConfidence || m.VolumeID != "52845" {
		t.Fatalf("expected high_confidence on 52845, got %s / %q (%s)", m.State, m.VolumeID, m.Penalties)
	}
	ims, err := st.ListIssueMatchesForSeries("saga-tpb")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 2 {
		t.Fatalf("expected 2 issue matches, got %+v", ims)
	}
	issues, err := st.ListIssues("saga-tpb")
	if err != nil {
		t.Fatal(err)
	}
	for _, iss := range issues {
		if iss.Format != "tpb" {
			t.Fatalf("expected format tpb on %s, got %q", iss.ID, iss.Format)
		}
	}
}

// Shape B: every trade in the folder resolves to its OWN one-issue volume
// ("Criminal: Coward", "Criminal: Lawless"). No single volume describes the
// folder, so it becomes a collection folder: state collection, empty volume,
// per-file matches kept, no split penalty.
func TestRunnerTradeFolderPerFileVolumes(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Criminal (2015)")
	p1, p2 := []byte("criminal-1"), []byte("criminal-2")
	writeCBZ(t, filepath.Join(folder, "Criminal v01 - Coward (2015).cbz"), p1)
	writeCBZ(t, filepath.Join(folder, "Criminal v02 - Lawless (2015).cbz"), p2)

	if err := st.UpsertSeries(store.Series{ID: "criminal", Name: "Criminal (2015)", FolderPath: folder, IssueCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("criminal", []store.Issue{
		{ID: "c1", SeriesID: "criminal", Filename: "Criminal v01 - Coward (2015).cbz", Number: "1", Position: 0},
		{ID: "c2", SeriesID: "criminal", Filename: "Criminal v02 - Lawless (2015).cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 80001, name: "Criminal: Coward", year: "2015", count: 1},
			{id: 80002, name: "Criminal: Lawless", year: "2015", count: 1},
		},
		[]cvIssue{{id: 900001, volumeID: 80001, number: "1"}, {id: 900002, volumeID: 80002, number: "1"}},
		cvFile{md5: md5hex(p1), issueID: 900001}, cvFile{md5: md5hex(p2), issueID: 900002})

	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetSeriesMatch("criminal")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateCollection {
		t.Fatalf("expected collection, got %s (%s)", m.State, m.Penalties)
	}
	if m.VolumeID != "" {
		t.Fatalf("collection folder must have no folder volume, got %q", m.VolumeID)
	}
	if strings.Contains(m.Penalties, "resolve to") {
		t.Fatalf("collection folder must not carry a split penalty: %s", m.Penalties)
	}
	ims, err := st.ListIssueMatchesForSeries("criminal")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 2 {
		t.Fatalf("expected per-file matches kept, got %+v", ims)
	}
}

// Shape B with a multi-part trade set: "Sinestro Corps War" is ONE ComicVine
// volume of two, and only its first part is in the folder. The old rule wanted
// every voted volume to have VolumeIssueCount <= 1, so that single trade left
// the Blackest Night omnibus ambiguous. One file per voted volume is the test.
func TestRunnerTradeFolderMultiPartTradeSet(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Blackest Night Omnibus (2019)")
	p1, p2 := []byte("bn-sinestro"), []byte("bn-agent-orange")
	writeCBZ(t, filepath.Join(folder, "Green Lantern v01 - The Sinestro Corps War (2008).cbz"), p1)
	writeCBZ(t, filepath.Join(folder, "Green Lantern v02 - Agent Orange (2009).cbz"), p2)

	if err := st.UpsertSeries(store.Series{ID: "bn", Name: "Blackest Night Omnibus (2019)", FolderPath: folder, IssueCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("bn", []store.Issue{
		{ID: "b1", SeriesID: "bn", Filename: "Green Lantern v01 - The Sinestro Corps War (2008).cbz", Number: "1", Position: 0},
		{ID: "b2", SeriesID: "bn", Filename: "Green Lantern v02 - Agent Orange (2009).cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			// count 2: the trade set has a second part that is NOT in this folder.
			{id: 32780, name: "Green Lantern: The Sinestro Corps War", year: "2008", count: 2},
			{id: 32710, name: "Green Lantern: Agent Orange", year: "2009", count: 1},
		},
		[]cvIssue{{id: 910001, volumeID: 32780, number: "1"}, {id: 910002, volumeID: 32710, number: "1"}},
		cvFile{md5: md5hex(p1), issueID: 910001}, cvFile{md5: md5hex(p2), issueID: 910002})

	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetSeriesMatch("bn")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateCollection {
		t.Fatalf("expected collection, got %s (%s)", m.State, m.Penalties)
	}
	if m.VolumeID != "" {
		t.Fatalf("collection folder must have no folder volume, got %q", m.VolumeID)
	}
	if strings.Contains(m.Penalties, "resolve to") {
		t.Fatalf("collection folder must not carry a split penalty: %s", m.Penalties)
	}
}

// A folder with zero hash hits is not a dead end when the dump knows the
// volume by exact name and year: pin it, bind by number. Iron Man (2026) is
// the real case — the July 2026 dump has the volume and all seven issues, but
// the scene-release hash index covers 3% of 2026. The 2024 volume of the same
// name is a decoy the year filter must reject.
func TestRunnerZeroHashHitsPinsVolumeByNameYear(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Iron Man (2026)")
	writeCBZ(t, filepath.Join(folder, "Iron Man 001 (2026).cbz"), []byte("im-1"))
	writeCBZ(t, filepath.Join(folder, "Iron Man 002 (2026).cbz"), []byte("im-2"))

	if err := st.UpsertSeries(store.Series{ID: "ironman", Name: "Iron Man (2026)", FolderPath: folder, IssueCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("ironman", []store.Issue{
		{ID: "im1", SeriesID: "ironman", Filename: "Iron Man 001 (2026).cbz", Number: "1", Position: 0},
		{ID: "im2", SeriesID: "ironman", Filename: "Iron Man 002 (2026).cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}

	// No comic_files rows for either page: the hash index has nothing.
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 160522, name: "Iron Man", year: "2024", count: 10},
			{id: 170313, name: "Iron Man", year: "2026", count: 7},
		},
		[]cvIssue{
			{id: 1, volumeID: 160522, number: "1"}, {id: 2, volumeID: 160522, number: "2"},
			{id: 3, volumeID: 170313, number: "1"}, {id: 4, volumeID: 170313, number: "2"},
		})

	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetSeriesMatch("ironman")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateHighConfidence || m.VolumeID != "170313" {
		t.Fatalf("expected high_confidence on 170313, got %s on %q (%s)", m.State, m.VolumeID, m.Penalties)
	}
	if m.Method != MethodNameYear {
		t.Fatalf("series method should record the name pin, got %q", m.Method)
	}
	ims, err := st.ListIssueMatchesForSeries("ironman")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 2 {
		t.Fatalf("expected both issues bound by number, got %+v", ims)
	}
	for _, im := range ims {
		if im.UpstreamID != "3" && im.UpstreamID != "4" {
			t.Fatalf("issue bound into the wrong volume: %+v", im)
		}
	}
}

// Iron Man has a 2025 volume (1 issue) and a 2026 volume (7). Both sit within
// yearSlack of a 2026 folder, so the slack alone can't pick — but an exact
// year match can, and must win over the neighbor.
func TestRunnerNamePinPrefersExactYear(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Iron Man (2026)")
	writeCBZ(t, filepath.Join(folder, "Iron Man 001 (2026).cbz"), []byte("im-1"))
	if err := st.UpsertSeries(store.Series{ID: "ironman", Name: "Iron Man (2026)", FolderPath: folder, IssueCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("ironman", []store.Issue{
		{ID: "im1", SeriesID: "ironman", Filename: "Iron Man 001 (2026).cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 165197, name: "Iron Man", year: "2025", count: 1},
			{id: 170313, name: "Iron Man", year: "2026", count: 7},
		},
		[]cvIssue{{id: 1, volumeID: 165197, number: "1"}, {id: 3, volumeID: 170313, number: "1"}})

	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, err := st.GetSeriesMatch("ironman")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateHighConfidence || m.VolumeID != "170313" {
		t.Fatalf("expected the exact-year volume 170313, got %s on %q (%s)", m.State, m.VolumeID, m.Penalties)
	}
}

// mkSeries writes a folder with the given files, the series row and one issue
// row per file (number = the file's parsed number), returning the library root.
func mkSeries(t *testing.T, st *store.Store, id, name string, files map[string]string) string {
	t.Helper()
	lib := t.TempDir()
	folder := filepath.Join(lib, name)
	var issues []store.Issue
	i := 0
	for fn, num := range files {
		writeCBZ(t, filepath.Join(folder, fn), []byte(fn))
		issues = append(issues, store.Issue{ID: id + "-" + fn, SeriesID: id, Filename: fn, Number: num, Position: i})
		i++
	}
	if err := st.UpsertSeries(store.Series{ID: id, Name: name, FolderPath: folder, IssueCount: len(files)}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues(id, issues); err != nil {
		t.Fatal(err)
	}
	return lib
}

func runAndGet(t *testing.T, st *store.Store, cv *localcv.DB, lib, id string) *store.SeriesMatch {
	t.Helper()
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, err := st.GetSeriesMatch(id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Folder names never carry ComicVine's punctuation: "Barbara Gordon Breakout"
// is "Barbara Gordon: Breakout". The pin compares canonical keys.
func TestRunnerNamePinIsCanonical(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "bgb", "Barbara Gordon Breakout (2026)", map[string]string{"Barbara Gordon Breakout 001 (2026).cbz": "1"})
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{{id: 171903, name: "Barbara Gordon: Breakout", year: "2026", count: 3}},
		[]cvIssue{{id: 1, volumeID: 171903, number: "1"}})
	if m := runAndGet(t, st, cv, lib, "bgb"); m.VolumeID != "171903" || m.Method != MethodNameYear {
		t.Fatalf("expected canonical pin on 171903, got %s on %q via %s", m.State, m.VolumeID, m.Method)
	}
}

// Ice Cream Man has two 2018 volumes (45 issues and 11). A folder holding #45
// can only be the first — the numbers decide.
func TestRunnerNamePinTieBreakByNumber(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "icm", "Ice Cream Man (2018)", map[string]string{"Ice Cream Man 45 (2026).cbz": "45"})
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 107948, name: "Ice Cream Man", year: "2018", count: 45},
			{id: 111675, name: "Ice Cream Man", year: "2018", count: 11},
		},
		[]cvIssue{{id: 1, volumeID: 107948, number: "45"}, {id: 2, volumeID: 111675, number: "1"}})
	if m := runAndGet(t, st, cv, lib, "icm"); m.VolumeID != "107948" {
		t.Fatalf("expected the volume that has #45, got %s on %q (%s)", m.State, m.VolumeID, m.Penalties)
	}
}

// Devil's Reign: a 6-issue ongoing and a one-issue trade, same name, same
// year. A folder holding only the TPB is the trade.
func TestRunnerNamePinTieBreakTradePrefersOneIssueVolume(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "dr", "Devil's Reign (2022)", map[string]string{"Devil's Reign TPB 2022.cbz": ""})
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 140354, name: "Devil's Reign", year: "2022", count: 6},
			{id: 146133, name: "Devil's Reign", year: "2022", count: 1},
		},
		[]cvIssue{{id: 1, volumeID: 140354, number: "1"}, {id: 2, volumeID: 146133, number: "1"}})
	if m := runAndGet(t, st, cv, lib, "dr"); m.VolumeID != "146133" {
		t.Fatalf("expected the one-issue trade volume, got %s on %q (%s)", m.State, m.VolumeID, m.Penalties)
	}
}

// A human's volume choice for a folder nothing matched binds by number into
// that volume and records the method — the console's only way in.
func TestRunnerRematchWithChosenVolume(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "cap", "Captain America Ed Brubaker Omnibus", map[string]string{"Captain America by Ed Brubaker Omnibus 349.cbz": "349"})
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{{id: 133486, name: "Captain America By Ed Brubaker Omnibus", year: "2021", count: 1}},
		[]cvIssue{{id: 9, volumeID: 133486, number: "349"}})
	if m := runAndGet(t, st, cv, lib, "cap"); m.State != store.MatchStateNoMatch {
		t.Fatalf("precondition: the folder name has no canonical hit, got %s", m.State)
	}
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if err := r.Rematch(context.Background(), "cap", store.SourceComicVine, "133486"); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("cap")
	if m.VolumeID != "133486" || m.Method != MethodHumanVolume || m.State == store.MatchStateNoMatch {
		t.Fatalf("expected human_volume bind on 133486, got %s on %q via %s", m.State, m.VolumeID, m.Method)
	}
	if ims, _ := st.ListIssueMatchesForSeries("cap"); len(ims) != 1 || ims[0].UpstreamID != "9" {
		t.Fatalf("expected the issue bound by number into the chosen volume, got %+v", ims)
	}
}

func TestRunnerSearchVolumesAllWords(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 133486, name: "Captain America By Ed Brubaker Omnibus", year: "2021", count: 1},
			{id: 113248, name: "Moon Knight by Jeff Lemire & Greg Smallwood", year: "2018", count: 1},
		}, nil)
	r := NewRunner(st, cv, nil, func() int64 { return 1000 })
	got, err := r.SearchVolumes("brubaker omnibus", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].VolumeID != 133486 {
		t.Fatalf("expected the Brubaker omnibus only, got %+v", got)
	}
}

// The name pin is only as good as the numbers that line up behind it. A
// folder whose issue numbers exist nowhere in the name-matched volume stays
// no_match with no volume — the same acceptance rule metronSeriesFallback uses.
func TestRunnerNamePinRejectedWhenNumbersDoNotBind(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Iron Man (2026)")
	writeCBZ(t, filepath.Join(folder, "Iron Man 050 (2026).cbz"), []byte("im-50"))

	if err := st.UpsertSeries(store.Series{ID: "ironman", Name: "Iron Man (2026)", FolderPath: folder, IssueCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("ironman", []store.Issue{
		{ID: "im50", SeriesID: "ironman", Filename: "Iron Man 050 (2026).cbz", Number: "50", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{{id: 170313, name: "Iron Man", year: "2026", count: 7}},
		[]cvIssue{{id: 3, volumeID: 170313, number: "1"}})

	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetSeriesMatch("ironman")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateNoMatch || m.VolumeID != "" {
		t.Fatalf("a name pin with no number agreement must not stick: got %s on %q", m.State, m.VolumeID)
	}
	if m.Method == MethodNameYear {
		t.Fatalf("a rejected pin must not be recorded as the series method")
	}
}

// A folder that mixes two standalone trades (each its own one-issue volume,
// like TestRunnerTradeFolderPerFileVolumes) with one non-collection single
// issue bound to a real multi-issue volume is not a "collection folder" —
// the folder-level rule must compare against every bound issue (matched),
// not just pass 1's md5Hits, or the single's volume gets silently dropped
// and the folder wrongly declared a collection.
func TestRunnerMixedTradesAndSingleIsAmbiguous(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Criminal (2015)")
	p1, p2 := []byte("criminal-1"), []byte("criminal-2")
	pSingle := []byte("saga-single")
	writeCBZ(t, filepath.Join(folder, "Criminal v01 - Coward (2015).cbz"), p1)
	writeCBZ(t, filepath.Join(folder, "Criminal v02 - Lawless (2015).cbz"), p2)
	writeCBZ(t, filepath.Join(folder, "Saga 001 (2012).cbz"), pSingle)

	if err := st.UpsertSeries(store.Series{ID: "criminal", Name: "Criminal (2015)", FolderPath: folder, IssueCount: 3}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("criminal", []store.Issue{
		{ID: "c1", SeriesID: "criminal", Filename: "Criminal v01 - Coward (2015).cbz", Number: "1", Position: 0},
		{ID: "c2", SeriesID: "criminal", Filename: "Criminal v02 - Lawless (2015).cbz", Number: "2", Position: 1},
		{ID: "c3", SeriesID: "criminal", Filename: "Saga 001 (2012).cbz", Number: "1", Position: 2},
	}); err != nil {
		t.Fatal(err)
	}

	// pSingle's hash hits the fixture's default row (Saga #1, volume 49019) —
	// a real multi-issue volume, unlike the two one-issue trade volumes.
	cv := newCVFixtureVolumes(t, md5hex(pSingle),
		[]cvVolume{
			{id: 80001, name: "Criminal: Coward", year: "2015", count: 1},
			{id: 80002, name: "Criminal: Lawless", year: "2015", count: 1},
		},
		[]cvIssue{{id: 900001, volumeID: 80001, number: "1"}, {id: 900002, volumeID: 80002, number: "1"}},
		cvFile{md5: md5hex(p1), issueID: 900001}, cvFile{md5: md5hex(p2), issueID: 900002})

	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetSeriesMatch("criminal")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateAmbiguous {
		t.Fatalf("expected ambiguous, got %s (%s)", m.State, m.Penalties)
	}
	if !strings.Contains(m.Penalties, "resolve to") {
		t.Fatalf("expected a split penalty naming the conflicting volumes, got %s", m.Penalties)
	}
}

// The Jurgens-shaped case, made pinnable: a folder ComicVine has never heard
// of, which GCD has under the same canonical name. Every row is tagged gcd.
const gcdOmnibus = `
INSERT INTO gcd_publisher(id, name) VALUES (54, 'DC');
INSERT INTO gcd_series(id, name, year_began, publisher_id, country_id, language_id, issue_count)
	VALUES (226636, 'Action Comics by Dan Jurgens Omnibus', 2025, 54, 225, 25, 2);
INSERT INTO gcd_issue(id, series_id, number, title, key_date, variant_of_id, sort_code)
	VALUES (2762266, 226636, '1', '', '2025-08-05', NULL, 1),
	       (2851449, 226636, '2', '', '2026-07-14', NULL, 2);
`

func TestRunnerGCDPinsWhatComicVineCannot(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "jurgens", "Action Comics by Dan Jurgens Omnibus", map[string]string{
		"Action Comics by Dan Jurgens Omnibus Vol. 1.cbz": "1",
	})
	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef") // nothing by that name
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdOmnibus))
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("jurgens")
	if m.Source != store.SourceGCD || m.VolumeID != "226636" || m.Method != MethodNameYear || m.State != store.MatchStateHighConfidence {
		t.Fatalf("expected a gcd name_year pin on 226636, got %+v", m)
	}
	ims, _ := st.ListIssueMatchesForSeries("jurgens")
	if len(ims) != 1 || ims[0].Source != store.SourceGCD || ims[0].UpstreamID != "2762266" || ims[0].Method != MethodVolumeNumber {
		t.Fatalf("expected one gcd issue match on 2762266, got %+v", ims)
	}
}

// ComicVine pinned the folder, so GCD — which also knows the name — is
// never consulted. The rows stay comicvine.
func TestRunnerGCDNotConsultedWhenComicVinePins(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "ironman", "Iron Man (2026)", map[string]string{"Iron Man 001 (2026).cbz": "1"})
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{{id: 170313, name: "Iron Man", year: "2026", count: 7}},
		[]cvIssue{{id: 3, volumeID: 170313, number: "1"}})
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, `
		INSERT INTO gcd_series(id, name, year_began, country_id, language_id, issue_count) VALUES (232591, 'Iron Man', 2026, 225, 25, 8);
		INSERT INTO gcd_issue(id, series_id, number, sort_code) VALUES (1, 232591, '1 (681)', 1);`))
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("ironman")
	if m.Source != store.SourceComicVine || m.VolumeID != "170313" {
		t.Fatalf("ComicVine's pin must win, got %+v", m)
	}
}

// A GCD name hit whose numbers don't line up is not a match.
func TestRunnerGCDPinRejectedWhenNumbersDoNotBind(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "jurgens", "Action Comics by Dan Jurgens Omnibus", map[string]string{
		"Action Comics by Dan Jurgens Omnibus Vol. 9.cbz": "9",
	})
	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdOmnibus))
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("jurgens")
	if m.State != store.MatchStateNoMatch || m.VolumeID != "" || m.Method == MethodNameYear || m.Source == store.SourceGCD {
		t.Fatalf("a rejected gcd pin must leave no trace, got %+v", m)
	}
}

// Avengers vs. X-Men Omnibus (2012): the folder names the story's year, the
// dump the print's (2022). For an all-collection folder with exactly one
// canonical hit at any year, the year is not a disagreement.
func TestRunnerCollectionFolderIgnoresYearWhenNameIsUnique(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "avx", "Avengers vs. X-Men Omnibus (2012)", map[string]string{
		"Avengers vs. X-Men Omnibus (in reading order).cbz": "",
	})
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{{id: 165709, name: "Avengers Vs. X-Men Omnibus", year: "2022", count: 1}},
		[]cvIssue{{id: 1121754, volumeID: 165709, number: "1"}})
	if m := runAndGet(t, st, cv, lib, "avx"); m.VolumeID != "165709" || m.Method != MethodNameYear {
		t.Fatalf("expected the omnibus pinned across the year gap, got %+v", m)
	}
}

// …but not when the name is shared: two "X-Men Omnibus" volumes a decade
// apart stay unpinned, year gap or not.
func TestRunnerCollectionFolderYearRuleNeedsUniqueName(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "xo", "X-Men Omnibus (2001)", map[string]string{"X-Men Omnibus HC.cbz": ""})
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 1, name: "X-Men Omnibus", year: "2011", count: 1},
			{id: 2, name: "X-Men Omnibus", year: "2021", count: 1},
		},
		[]cvIssue{{id: 11, volumeID: 1, number: "1"}, {id: 12, volumeID: 2, number: "1"}})
	if m := runAndGet(t, st, cv, lib, "xo"); m.State != store.MatchStateNoMatch {
		t.Fatalf("two namesakes must not pin, got %+v", m)
	}
}

// The console's pin into GCD: bind by number, tag every row gcd, record
// human_volume.
func TestRunnerRematchIntoGCD(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "jurgens", "Action Comics Jurgens", map[string]string{
		"Action Comics by Dan Jurgens Omnibus Vol. 1.cbz": "1",
	})
	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdOmnibus))
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Rematch(context.Background(), "jurgens", store.SourceGCD, "226636"); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("jurgens")
	if m.Source != store.SourceGCD || m.VolumeID != "226636" || m.Method != MethodHumanVolume || m.State == store.MatchStateNoMatch {
		t.Fatalf("expected human gcd pin, got %+v", m)
	}
	iss, err := r.LookupIssue(store.SourceGCD, "2762266")
	if err != nil || iss.VolumeName != "Action Comics by Dan Jurgens Omnibus" {
		t.Fatalf("LookupIssue(gcd) = %+v, %v", iss, err)
	}
	if _, err := r.LookupIssue(store.SourceComicVine, "2762266"); err == nil {
		t.Fatal("a gcd id must not resolve against ComicVine")
	}
}

// A human's GCD pick must win over whatever pass 1 hashed. This folder's
// first file hashes to a ComicVine issue (Saga #1), so every ComicVine pass
// has an opinion and the folder is nowhere near zero votes — the console
// reaching for GCD is exactly how a human overrules that. The pass-1
// ComicVine row stays where it is; the new gcd row sits beside it.
func TestRunnerRematchIntoGCDOverridesComicVineHashHit(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	const hashed = "Action Comics by Dan Jurgens Omnibus Vol. 1.cbz"
	lib := mkSeries(t, st, "jurgens", "Action Comics by Dan Jurgens Omnibus", map[string]string{
		hashed: "1",
		"Action Comics by Dan Jurgens Omnibus Vol. 2.cbz": "2",
	})
	// mkSeries writes each file's own name as its single page, so this maps
	// Vol. 1's first page onto the fixture's Saga #1 (volume 49019).
	cv := newCVFixture(t, md5hex([]byte(hashed)))
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdOmnibus))
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetSeriesMatch("jurgens"); m.VolumeID != "49019" || m.Source != store.SourceComicVine {
		t.Fatalf("precondition: pass 1 must pin the ComicVine volume, got %+v", m)
	}

	if err := r.Rematch(context.Background(), "jurgens", store.SourceGCD, "226636"); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("jurgens")
	if m.Source != store.SourceGCD || m.VolumeID != "226636" || m.Method != MethodHumanVolume {
		t.Fatalf("the human's gcd pick must replace the hash-hit volume, got %+v", m)
	}

	if m.State != store.MatchStateHighConfidence {
		t.Fatalf("the rebuilt folder must be scored on gcd alone, got %s (%s)", m.State, m.Penalties)
	}

	// The pick replaces the machine's answer: pass 1's ComicVine row for the
	// hashed file is gone, and both files now hang off the GCD series.
	ims, _ := st.ListIssueMatchesForSeries("jurgens")
	if len(ims) != 2 {
		t.Fatalf("want exactly the two gcd rows, got %+v", ims)
	}
	for _, im := range ims {
		if im.Source != store.SourceGCD {
			t.Fatalf("a comicvine row survived the gcd re-pin: %+v", ims)
		}
	}
}

// Nothing is left over when every file already hash-matched ComicVine: the
// human's GCD pick still rebinds the whole folder, rather than recording a
// series pin with no issue rows behind it.
func TestRunnerRematchIntoGCDWhenEveryFileHashMatched(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	const f1 = "Action Comics by Dan Jurgens Omnibus Vol. 1.cbz"
	const f2 = "Action Comics by Dan Jurgens Omnibus Vol. 2.cbz"
	lib := mkSeries(t, st, "jurgens", "Action Comics by Dan Jurgens Omnibus", map[string]string{
		f1: "1",
		f2: "2",
	})
	// Both files hash: f1 onto the fixture's Saga #1, f2 onto Saga #2.
	cv := newCVFixture(t, md5hex([]byte(f1)), cvFile{md5: md5hex([]byte(f2)), issueID: 317967})
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdOmnibus))
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ims, _ := st.ListIssueMatchesForSeries("jurgens"); len(ims) != 2 {
		t.Fatalf("precondition: both files must hash-match ComicVine, got %+v", ims)
	}

	if err := r.Rematch(context.Background(), "jurgens", store.SourceGCD, "226636"); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("jurgens")
	if m.Source != store.SourceGCD || m.Method != MethodHumanVolume || m.State != store.MatchStateHighConfidence {
		t.Fatalf("expected a clean gcd human pin, got %+v", m)
	}
	ims, _ := st.ListIssueMatchesForSeries("jurgens")
	if len(ims) != 2 {
		t.Fatalf("want both files rebound into gcd, got %+v", ims)
	}
	for _, im := range ims {
		if im.Source != store.SourceGCD {
			t.Fatalf("a comicvine row survived the gcd re-pin: %+v", ims)
		}
	}
}

func TestRunnerSearchVolumesSpansBothDumps(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{{id: 138151, name: "Captain America by Dan Jurgens Omnibus", year: "2021", count: 1}}, nil)
	r := NewRunner(st, cv, nil, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdOmnibus))
	got, err := r.SearchVolumes("jurgens omnibus", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Source != store.SourceComicVine || got[0].VolumeID != 138151 || got[1].Source != store.SourceGCD || got[1].VolumeID != 226636 {
		t.Fatalf("expected the CV hit then the GCD hit, got %+v", got)
	}
}
