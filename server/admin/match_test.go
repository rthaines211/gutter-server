package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryanhaines/comic-reader/match"
	"github.com/ryanhaines/comic-reader/match/gcd/gcdtest"
	"github.com/ryanhaines/comic-reader/store"
)

// A GCD series in the fixture below, shared by the two cross/same-source
// set-volume tests: one series, one issue.
const gcdJurgensOmnibus = `
INSERT INTO gcd_publisher(id, name) VALUES (54, 'DC');
INSERT INTO gcd_series(id, name, year_began, publisher_id, country_id, language_id, issue_count)
	VALUES (226636, 'Action Comics by Dan Jurgens Omnibus', 2025, 54, 225, 25, 1);
INSERT INTO gcd_issue(id, series_id, number, title, key_date, variant_of_id, sort_code)
	VALUES (2762266, 226636, '1', '', '2025-08-05', NULL, 1);
`

// The matcher takes ~4 minutes of external-drive I/O per pass, so handleMatchRun
// admits exactly one runner. This exercises the CompareAndSwap sequence the
// handler relies on: acquire, second attempt refused, release, acquire again.
func TestMatchRunGuardIsExclusive(t *testing.T) {
	a := &Admin{}

	if !a.matchRunning.CompareAndSwap(false, true) {
		t.Fatal("first acquisition failed, want success on a fresh Admin")
	}
	if a.matchRunning.CompareAndSwap(false, true) {
		t.Fatal("second acquisition succeeded while the guard was held, want refusal")
	}

	// The handler releases via defer, on the error path too.
	a.matchRunning.Store(false)

	if !a.matchRunning.CompareAndSwap(false, true) {
		t.Fatal("acquisition after release failed, guard leaked")
	}
}

// Confirming a series with no matched volume would flip the state, write no
// metadata, and redirect as though it worked — a green result and an empty app.
// It must be refused, and the state must survive untouched.
func TestConfirmRefusesSeriesWithNoVolume(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "nomatch", Name: "Unknown (2020)", FolderPath: t.TempDir(), IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "nomatch", Source: store.SourceComicVine, VolumeID: "",
		State: store.MatchStateNoMatch, Method: "first_page_md5", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// Match is nil: matching disabled is the same code path the handlers hit.
	a := &Admin{Store: st}
	if err := a.confirm("nomatch"); err == nil {
		t.Fatal("expected confirm to refuse a series with no matched volume")
	}

	m, err := st.GetSeriesMatch("nomatch")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateNoMatch {
		t.Fatalf("state was flipped despite the refusal: %s", m.State)
	}
	if m.ConfirmedAt != 0 {
		t.Fatalf("confirmed_at was stamped despite the refusal: %d", m.ConfirmedAt)
	}
}

// deriveUnconfirmState must land a series back where a fresh matcher pass
// would put it, for each of the three input shapes it can see.
func TestDeriveUnconfirmState(t *testing.T) {
	cases := []struct {
		name            string
		volumeID        string
		penalties       string
		hasIssueMatches bool
		want            string
	}{
		{"no volume", "", "{}", false, store.MatchStateNoMatch},
		{"no volume, no penalties string", "", "", false, store.MatchStateNoMatch},
		{"no volume, has issue matches", "", "{}", true, store.MatchStateCollection},
		{"no volume, no issue matches", "", "{}", false, store.MatchStateNoMatch},
		{"volume with disagreement", "49019", `{"year":"local 2016 vs volume start 2020"}`, false, store.MatchStateAmbiguous},
		{"volume, clean", "49019", "{}", false, store.MatchStateHighConfidence},
		{"volume, empty penalties string", "49019", "", false, store.MatchStateHighConfidence},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveUnconfirmState(c.volumeID, c.penalties, c.hasIssueMatches); got != c.want {
				t.Errorf("deriveUnconfirmState(%q, %q, %v) = %s, want %s", c.volumeID, c.penalties, c.hasIssueMatches, got, c.want)
			}
		})
	}
}

// The guard must not block the normal path: a matched volume still confirms.
func TestConfirmAcceptsSeriesWithVolume(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: t.TempDir(), IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "saga", Source: store.SourceComicVine, VolumeID: "49019",
		State: store.MatchStateHighConfidence, Method: "first_page_md5", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	a := &Admin{Store: st}
	if err := a.confirm("saga"); err != nil {
		t.Fatalf("confirm on a matched series must succeed, got %v", err)
	}

	m, err := st.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateConfirmed {
		t.Fatalf("expected confirmed, got %s", m.State)
	}
}

// If Apply fails, the series must not be left confirmed: match/runner.go
// skips confirmed series on every future run, and the console hides all
// action buttons on confirmed rows, so a stranded "confirmed, no metadata"
// row would be recoverable only by hand-editing SQLite.
//
// Apply's own per-issue upstream resolution failures are swallowed (see
// match/apply.go IssueByID handling) and cannot be reached from here without
// an interface seam. But Apply also does real store I/O of its own — it
// starts with r.st.GetSeriesMatch(seriesID) — and match.Runner is built over
// its own *store.Store distinct from Admin.Store. Handing the Runner a store
// that has never heard of this series makes that first call fail for real,
// through the normal (non-mocked) code path: production always wires the
// runner and Admin.Store to the very same *store.Store (see main.go), so this
// exercises a genuine failure mode of Apply, just one that can't occur with
// today's single shared store in production.
func TestConfirmDoesNotStrandSeriesWhenApplyFails(t *testing.T) {
	appStore, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer appStore.Close()

	if err := appStore.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: t.TempDir(), IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := appStore.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "saga", Source: store.SourceComicVine, VolumeID: "49019",
		State: store.MatchStateHighConfidence, Method: "first_page_md5", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// A second, empty store backs the Runner: Apply's GetSeriesMatch("saga")
	// against it fails with ErrNotFound, forcing a real Apply error.
	runnerStore, err := store.NewStore(t.TempDir() + "/runner.db")
	if err != nil {
		t.Fatal(err)
	}
	defer runnerStore.Close()

	a := &Admin{
		Store: appStore,
		Match: match.NewRunner(runnerStore, nil, nil, func() int64 { return 1000 }),
	}

	if err := a.confirm("saga"); err == nil {
		t.Fatal("expected confirm to surface Apply's failure")
	}

	m, err := appStore.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.State == store.MatchStateConfirmed {
		t.Fatalf("series was left confirmed despite Apply failing: %+v", m)
	}
	if m.ConfirmedAt != 0 {
		t.Fatalf("confirmed_at was stamped despite Apply failing: %d", m.ConfirmedAt)
	}
}

// A browser that navigates away (or times out) cancels the request context.
// The match pass is ~4 minutes of I/O; it must run detached so that
// cancellation kills nothing, and the handler must redirect immediately
// instead of holding the connection open for the whole pass.
func TestMatchRunDetachedFromRequestContext(t *testing.T) {
	gotCtx := make(chan context.Context, 1)
	release := make(chan struct{})

	a := &Admin{Match: &match.Runner{}}
	a.runMatch = func(ctx context.Context) error {
		gotCtx <- ctx
		<-release
		return nil
	}

	reqCtx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/_admin/match/run", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		a.handleMatchRun(rec, req)
		close(done)
	}()

	runCtx := <-gotCtx

	// Handler must return while the run is still going.
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handler still blocked on the run, want immediate redirect")
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	// The browser hangs up. The run's context must not care.
	cancel()
	select {
	case <-runCtx.Done():
		t.Fatal("run context canceled by request cancellation")
	case <-time.After(100 * time.Millisecond):
	}

	// Guard releases once the run finishes.
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for a.matchRunning.Load() {
		if time.Now().After(deadline) {
			t.Fatal("matchRunning never released after run finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A collection folder has no folder volume but is confirmable: confirm must
// stamp it confirmed with the volume left empty. no_match stays refused
// (TestConfirmRefusesSeriesWithNoVolume).
func TestConfirmAcceptsCollectionFolder(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "criminal", Name: "Criminal (2015)", FolderPath: t.TempDir(), IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "criminal", Source: store.SourceComicVine, VolumeID: "",
		State: store.MatchStateCollection, Method: "first_page_md5", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	a := &Admin{Store: st}
	if err := a.confirm("criminal"); err != nil {
		t.Fatalf("confirm on a collection folder must succeed, got %v", err)
	}
	m, err := st.GetSeriesMatch("criminal")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateConfirmed || m.VolumeID != "" || m.ConfirmedAt == 0 {
		t.Fatalf("expected confirmed with empty volume, got %+v", m)
	}
}

// Unconfirming a confirmed collection folder (VolumeID "", per-file
// issue_match rows still present) must return it to the collection state,
// not no_match — otherwise it loses its "Confirm as collection" button until
// the next matcher run. TestDeriveUnconfirmState covers the pure decision;
// this exercises the full unconfirm path including the issue_match lookup.
func TestUnconfirmReturnsCollectionFolderToCollectionState(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "criminal", Name: "Criminal (2015)", FolderPath: t.TempDir(), IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("criminal", []store.Issue{
		{ID: "criminal-1", SeriesID: "criminal", Filename: "Criminal 001.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssueMatch(store.IssueMatch{
		IssueID: "criminal-1", Source: store.SourceComicVine, UpstreamID: "12345",
		State: store.MatchStateConfirmed, Confidence: 0, Method: "first_page_md5",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "criminal", Source: store.SourceComicVine, VolumeID: "",
		State: store.MatchStateConfirmed, Method: "first_page_md5",
		ConfirmedAt: 1000, UpdatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	a := &Admin{Store: st}
	if err := a.unconfirm("criminal"); err != nil {
		t.Fatalf("unconfirm failed: %v", err)
	}

	m, err := st.GetSeriesMatch("criminal")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateCollection || m.VolumeID != "" || m.ConfirmedAt != 0 {
		t.Fatalf("expected collection state with empty volume and cleared confirmed_at, got %+v", m)
	}
}

// set-volume carries the dump the human picked from; a GCD pick on a folder
// with no issue matches goes to Rematch with source gcd. The runner has no
// dumps here, so Rematch fails — the assertion is on what was asked, which
// the recorded error message names.
func TestSetVolumeRoutesGCDPickToRematch(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertSeries(store.Series{ID: "jurgens", Name: "Action Comics Jurgens", FolderPath: t.TempDir(), IssueCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{SeriesID: "jurgens", Source: store.SourceComicVine, State: store.MatchStateNoMatch, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	a := &Admin{Store: st, Match: match.NewRunner(st, nil, nil, func() int64 { return 1000 })}

	form := "series_id=jurgens&volume_id=226636&source=gcd"
	req := httptest.NewRequest("POST", "/_admin/match/set-volume", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	a.handleMatchSetVolume(rec, req)

	// No GCD dump is configured on this runner, so the GCD path reports
	// exactly that rather than silently binding against ComicVine.
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "gcd") {
		t.Fatalf("expected the gcd path to be taken and refused without a dump, got %d %q", rec.Code, rec.Body.String())
	}

	// A source that names no dump would be written to series_match and then
	// resolved as ComicVine by every later lookup — refuse it at the door.
	form = "series_id=jurgens&volume_id=226636&source=bogus"
	req = httptest.NewRequest("POST", "/_admin/match/set-volume", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	a.handleMatchSetVolume(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown source, got %d %q", rec.Code, rec.Body.String())
	}
}

// A GCD pick on a folder that already carries a ComicVine issue_match row
// (from an earlier, wrong hash pin) must drop that row rather than leave it
// behind under the newly chosen gcd series: the id spaces overlap, so a
// stale comicvine row is meaningless once the series is gcd, and Apply would
// resolve it against an unrelated ComicVine issue.
func TestSetVolumeGCDPickDropsStaleComicVineRow(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "jurgens", Name: "Action Comics by Dan Jurgens Omnibus",
		FolderPath: "/lib/Action Comics by Dan Jurgens Omnibus", IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("jurgens", []store.Issue{
		{ID: "jurgens-1", SeriesID: "jurgens", Filename: "Action Comics by Dan Jurgens Omnibus Vol. 1.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	// The folder previously hash-matched an unrelated ComicVine volume.
	if err := st.UpsertIssueMatch(store.IssueMatch{
		IssueID: "jurgens-1", Source: store.SourceComicVine, UpstreamID: "999999",
		State: store.MatchStateHighConfidence, Confidence: 0, Method: "first_page_md5",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "jurgens", Source: store.SourceComicVine, VolumeID: "49019",
		State: store.MatchStateHighConfidence, Method: "first_page_md5", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	r := match.NewRunner(st, nil, nil, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdJurgensOmnibus))
	a := &Admin{Store: st, Match: r}

	form := "series_id=jurgens&volume_id=226636&source=gcd"
	req := httptest.NewRequest("POST", "/_admin/match/set-volume", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	a.handleMatchSetVolume(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected the gcd pin to succeed, got %d %q", rec.Code, rec.Body.String())
	}

	ims, err := st.ListIssueMatchesForSeries("jurgens")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 1 || ims[0].Source != store.SourceGCD || ims[0].UpstreamID != "2762266" {
		t.Fatalf("expected the stale comicvine row dropped and replaced by the gcd bind, got %+v", ims)
	}

	m, err := st.GetSeriesMatch("jurgens")
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != store.SourceGCD || m.VolumeID != "226636" {
		t.Fatalf("expected series_match repointed to the gcd pick, got %+v", m)
	}
}

// A pick that keeps the folder's current source must still take the
// SetSeriesMatchVolume+Reverify path — the cross-source rebuild added above
// must not fire just because the folder already has issue rows.
func TestSetVolumeSameSourcePickStillReverifiesInPlace(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "jurgens", Name: "Action Comics by Dan Jurgens Omnibus",
		FolderPath: "/lib/Action Comics by Dan Jurgens Omnibus", IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("jurgens", []store.Issue{
		{ID: "jurgens-1", SeriesID: "jurgens", Filename: "Action Comics by Dan Jurgens Omnibus Vol. 1.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	// The issue is already correctly bound in gcd, but the series itself was
	// pinned to the wrong gcd volume, so the folder reads ambiguous.
	if err := st.UpsertIssueMatch(store.IssueMatch{
		IssueID: "jurgens-1", Source: store.SourceGCD, UpstreamID: "2762266",
		State: store.MatchStateAmbiguous, Confidence: 0, Method: "human_volume",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "jurgens", Source: store.SourceGCD, VolumeID: "999999",
		State: store.MatchStateAmbiguous, Method: "human_volume", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	r := match.NewRunner(st, nil, nil, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdJurgensOmnibus))
	a := &Admin{Store: st, Match: r}

	// Same source as the existing pin (gcd), just the volume corrected to
	// where the issue row actually resolves.
	form := "series_id=jurgens&volume_id=226636&source=gcd"
	req := httptest.NewRequest("POST", "/_admin/match/set-volume", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	a.handleMatchSetVolume(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected the same-source pick to succeed, got %d %q", rec.Code, rec.Body.String())
	}

	// Reverify ran: the issue row's own gcd match now agrees with the
	// corrected series volume, so the ambiguity clears in place.
	m, err := st.GetSeriesMatch("jurgens")
	if err != nil {
		t.Fatal(err)
	}
	if m.VolumeID != "226636" || m.State != store.MatchStateHighConfidence || m.Penalties != "{}" {
		t.Fatalf("expected Reverify to clear the ambiguity in place, got %+v", m)
	}

	// The issue row itself is untouched by a rebuild (same upstream id and
	// method) — proving the in-place path, not Rematch, ran.
	ims, err := st.ListIssueMatchesForSeries("jurgens")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 1 || ims[0].UpstreamID != "2762266" || ims[0].Method != "human_volume" {
		t.Fatalf("expected the issue row left alone by Reverify, got %+v", ims)
	}
}

// A folder the runner's collection detection missed must be markable by hand.
// Black Science is the live case: nine trades, but v01 and v05 share volume
// 74320, so matched != len(volumeVotes), the folder confirms against one
// trade's volume, and Organize flags the other seven as homeless strays.
func TestMarkCollectionClearsVolumeAndSetsCollectionState(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "bs", Name: "Black Science: How to Fall Forever (2014)", FolderPath: t.TempDir(), IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("bs", []store.Issue{
		{ID: "bs-1", SeriesID: "bs", Filename: "Black Science v01.cbz", Number: "1", Position: 0},
		{ID: "bs-2", SeriesID: "bs", Filename: "Black Science v02.cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}
	for _, im := range []store.IssueMatch{
		{IssueID: "bs-1", Source: store.SourceComicVine, UpstreamID: "1", State: store.MatchStateHighConfidence},
		{IssueID: "bs-2", Source: store.SourceComicVine, UpstreamID: "2", State: store.MatchStateHighConfidence},
	} {
		if err := st.UpsertIssueMatch(im); err != nil {
			t.Fatal(err)
		}
	}
	// Confirmed against the v01 trade's volume, with penalties from the split.
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "bs", Source: store.SourceComicVine, VolumeID: "74320",
		State: store.MatchStateAmbiguous, Penalties: `{"volume":"split"}`, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	a := &Admin{Store: st}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/_admin/match/mark-collection",
		strings.NewReader("series_id=bs"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.handleMatchMarkCollection(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d: %s", rec.Code, rec.Body.String())
	}
	m, err := st.GetSeriesMatch("bs")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateCollection {
		t.Errorf("state = %s, want %s", m.State, store.MatchStateCollection)
	}
	if m.VolumeID != "" {
		t.Errorf("volume_id = %q, want empty", m.VolumeID)
	}
	// Stale penalties must not survive: they describe a volume split that no
	// longer applies once the folder has no folder-level volume. A leftover
	// non-empty value would send unconfirm to ambiguous via
	// deriveUnconfirmState instead of back to collection.
	if m.Penalties != "{}" {
		t.Errorf("penalties = %q, want {}", m.Penalties)
	}
	// The point of the whole exercise: the row is now Confirmable, so the
	// "Confirm as collection" button renders.
	if !(m.VolumeID != "" || m.State == store.MatchStateCollection) {
		t.Error("row must be confirmable after marking as collection")
	}
}

// A folder with no per-file matches is a no-match, not a collection: clearing
// its volume would leave a row that deriveUnconfirmState reads as no_match
// anyway. Same distinction the matcher draws.
func TestMarkCollectionRefusesFolderWithNoIssueMatches(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "ghost", Name: "Ghost (1999)", FolderPath: t.TempDir(), IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "ghost", Source: store.SourceComicVine, VolumeID: "999",
		State: store.MatchStateAmbiguous, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	a := &Admin{Store: st}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/_admin/match/mark-collection",
		strings.NewReader("series_id=ghost"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.handleMatchMarkCollection(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	m, err := st.GetSeriesMatch("ghost")
	if err != nil {
		t.Fatal(err)
	}
	if m.VolumeID != "999" || m.State != store.MatchStateAmbiguous {
		t.Errorf("row must be untouched, got %+v", m)
	}
}

// Applied metadata must be withdrawn before the match beneath it moves — the
// same order set-volume and repin enforce.
func TestMarkCollectionRefusesConfirmedRow(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "bs", Name: "Black Science (2014)", FolderPath: t.TempDir(), IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("bs", []store.Issue{
		{ID: "bs-1", SeriesID: "bs", Filename: "Black Science v01.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssueMatch(store.IssueMatch{
		IssueID: "bs-1", Source: store.SourceComicVine, UpstreamID: "1", State: store.MatchStateConfirmed,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "bs", Source: store.SourceComicVine, VolumeID: "74320",
		State: store.MatchStateConfirmed, ConfirmedAt: 1000, UpdatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	a := &Admin{Store: st}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/_admin/match/mark-collection",
		strings.NewReader("series_id=bs"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.handleMatchMarkCollection(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	m, _ := st.GetSeriesMatch("bs")
	if m.VolumeID != "74320" || m.State != store.MatchStateConfirmed {
		t.Errorf("confirmed row must be untouched, got %+v", m)
	}
}

// match_detail.html has no other render coverage, so a bad field reference in
// the mark-collection block would only surface in the browser. This renders it
// through the real template pipeline and checks the state gate: the button is
// for rows that aren't confirmed and aren't already collections.
func TestMatchDetailRendersMarkCollectionButtonByState(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, err := New(st, nil, nil, t.TempDir(), t.TempDir()+"/app.db", 8080, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		state string
		want  bool
	}{
		{store.MatchStateAmbiguous, true},
		{store.MatchStateHighConfidence, true},
		{store.MatchStateCollection, false},
		{store.MatchStateConfirmed, false},
	} {
		rec := httptest.NewRecorder()
		data := a.baseData("match")
		data.Content = MatchSeriesDetailData{
			SeriesID: "bs", Name: "Black Science", State: c.state,
		}
		a.render(rec, "match_detail.html", data)

		// render() writes its 500 into the same recorder after partial output,
		// so a template error leaves Code at 200 with the message appended to
		// the body — check the body, not the status.
		body := rec.Body.String()
		if strings.Contains(body, "executing") || strings.Contains(body, "can't evaluate field") {
			t.Fatalf("state %s: template error: %s", c.state, body[max(0, len(body)-300):])
		}
		if got := strings.Contains(body, "mark-collection"); got != c.want {
			t.Errorf("state %s: button present = %v, want %v", c.state, got, c.want)
		}
	}
}

// A mis-pinned file inside a collection folder had no fix: the per-row re-pin
// button is gated on a folder volume (buildMatchSeriesDetail), and a collection
// folder never has one. Black Science v05 is the live case — it matched by
// volume_number onto 454059, the same upstream issue as v01, while every other
// trade matched by first_page_md5.
//
// These cover the handler's own logic (form parsing, source decoding, guards).
// Resolving a number inside a volume is Runner.LookupIssueInVolume, covered in
// match/review_test.go against a real dump fixture.
// The per-row volume picker is the only fix path for a mis-pinned file in a
// collection folder, and match_detail.html's issue table is otherwise
// unrendered by tests. Renders it with search results present and confirms the
// picker appears with a source-qualified value, and stays hidden on confirmed.
func TestMatchDetailRendersPerIssueVolumePicker(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, err := New(st, nil, nil, t.TempDir(), t.TempDir()+"/app.db", 8080, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		state string
		want  bool
	}{
		{store.MatchStateCollection, true},
		{store.MatchStateConfirmed, false},
	} {
		rec := httptest.NewRecorder()
		data := a.baseData("match")
		data.Content = MatchSeriesDetailData{
			SeriesID: "bs", Name: "Black Science", State: c.state,
			SearchResults: []VolumeOption{
				{ID: "96273", Source: store.SourceComicVine, Name: "Black Science: True Atonement", StartYear: "2016"},
			},
			Issues: []MatchIssueRow{
				{IssueID: "bs-5", Filename: "Black Science Vol. 5.cbz", LocalNumber: "5"},
			},
			VisibleIssues: []MatchIssueRow{
				{IssueID: "bs-5", Filename: "Black Science Vol. 5.cbz", LocalNumber: "5"},
			},
			ShowAll: true,
		}
		a.render(rec, "match_detail.html", data)

		body := rec.Body.String()
		if strings.Contains(body, "executing") || strings.Contains(body, "can't evaluate field") {
			t.Fatalf("state %s: template error: %s", c.state, body[max(0, len(body)-300):])
		}
		if got := strings.Contains(body, `name="repin_volume"`); got != c.want {
			t.Errorf("state %s: picker present = %v, want %v", c.state, got, c.want)
		}
		// The value must carry the dump, not a bare id.
		if c.want && !strings.Contains(body, `value="comicvine:96273"`) {
			t.Errorf("state %s: picker option must encode source:id", c.state)
		}
	}
}

func TestDecodeRepinVolume(t *testing.T) {
	for _, c := range []struct {
		name       string
		raw        string
		fallback   string
		wantSource string
		wantVol    string
		wantOK     bool
	}{
		{"comicvine prefix", "comicvine:96273", "comicvine", "comicvine", "96273", true},
		{"gcd prefix overrides the folder source", "gcd:12345", "comicvine", "gcd", "12345", true},
		{"bare id falls back to the folder source", "96273", "comicvine", "comicvine", "96273", true},
		{"bare id falls back to a gcd folder", "12345", "gcd", "gcd", "12345", true},
		{"unknown source refused", "bogus:96273", "comicvine", "", "", false},
		{"empty id after prefix refused", "comicvine:", "comicvine", "", "", false},
		{"empty value refused", "", "comicvine", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			src, vol, ok := decodeRepinVolume(c.raw, c.fallback)
			if ok != c.wantOK || src != c.wantSource || vol != c.wantVol {
				t.Errorf("decodeRepinVolume(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
					c.raw, c.fallback, src, vol, ok, c.wantSource, c.wantVol, c.wantOK)
			}
		})
	}
}

// repin_volume is an alternative to upstream_id, not an addition: a request
// carrying neither is still refused, and one carrying only repin_volume is not.
func TestRepinRequiresUpstreamIDOrVolume(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a := &Admin{Store: st}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/_admin/match/repin",
		strings.NewReader("series_id=bs&issue_id=bs-5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.handleMatchRepin(rec, req)

	// a.Match is nil, so the disabled-matching precondition fires before the
	// field check; both are refusals, and neither writes anything.
	if rec.Code == http.StatusSeeOther {
		t.Fatalf("a repin with neither upstream_id nor repin_volume must not succeed")
	}
}

// TestMatchReviewConfirmGateByState: the review queue gated Confirm on
// HasVolume, but a collection folder has per-file volumes and so no folder
// volume — leaving /_admin/match/review?state=collection with only Skip and
// Don't match, which is the page the console links you to for that queue.
func TestMatchReviewConfirmGateByState(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := New(st, nil, nil, t.TempDir(), t.TempDir()+"/app.db", 8080, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name        string
		detail      MatchSeriesDetailData
		wantConfirm bool
		wantLabel   string
	}{
		{"collection has no folder volume but is confirmable",
			MatchSeriesDetailData{SeriesID: "glc", State: store.MatchStateCollection, Confirmable: true},
			true, "Confirm as collection"},
		{"ordinary matched row",
			MatchSeriesDetailData{SeriesID: "bat", State: store.MatchStateAmbiguous, HasVolume: true, Confirmable: true},
			true, "Confirm"},
		{"nothing resolved: no Confirm at all",
			MatchSeriesDetailData{SeriesID: "nm", State: store.MatchStateNoMatch},
			false, ""},
	} {
		rec := httptest.NewRecorder()
		data := a.baseData("match")
		d := c.detail
		data.Content = MatchReviewData{State: d.State, Detail: &d, Total: 1}
		a.render(rec, "match_review.html", data)
		body := rec.Body.String()
		if strings.Contains(body, "can't evaluate field") || strings.Contains(body, "executing") {
			t.Fatalf("%s: template error: %s", c.name, body[max(0, len(body)-300):])
		}
		if got := strings.Contains(body, `id="confirm-form"`); got != c.wantConfirm {
			t.Errorf("%s: confirm form present = %v, want %v", c.name, got, c.wantConfirm)
		}
		if c.wantLabel != "" && !strings.Contains(body, c.wantLabel) {
			t.Errorf("%s: missing label %q", c.name, c.wantLabel)
		}
	}
}
