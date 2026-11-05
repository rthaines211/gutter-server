package match

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhaines/comic-reader/match/gcd/gcdtest"
	"github.com/ryanhaines/comic-reader/store"
)

// A hash resolving to an international reprint (Panini España's Saga '2013')
// must be ignored, and the file bound by number into the volume the folder's
// other issues pinned — not left splitting the series into two volumes.
func TestRunnerIgnoresReprintPublisherHash(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Saga (2012)")
	pageOne, pageTwo := []byte("saga-page-one"), []byte("reprint-page-two")
	writeCBZ(t, filepath.Join(folder, "Saga 001 (2012).cbz"), pageOne)
	writeCBZ(t, filepath.Join(folder, "Saga 002 (2012).cbz"), pageTwo)

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

	// pageTwo's hash maps to the Panini reprint issue 600001.
	cv := newCVFixture(t, md5hex(pageOne), cvFile{md5: md5hex(pageTwo), issueID: 600001})
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateHighConfidence || m.VolumeID != "49019" {
		t.Fatalf("want high_confidence in 49019, got %s in %q (%s)", m.State, m.VolumeID, m.Penalties)
	}

	ims, err := st.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 2 {
		t.Fatalf("want 2 issue matches, got %+v", ims)
	}
	for _, im := range ims {
		if im.IssueID == "saga-002" {
			if im.UpstreamID != "317967" || im.Method != MethodVolumeNumber {
				t.Fatalf("reprint hash not rebound by number: %+v", im)
			}
		}
	}
}

// Re-pinning the stray issue to the chosen volume's same-number issue must
// clear the volume/number penalties and land the series in high_confidence.
func TestRepinAndReverify(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: "/lib/Saga (2012)", IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001 (2012).cbz", Number: "1", Position: 0},
		{ID: "saga-002", SeriesID: "saga", Filename: "Saga 002 (2012).cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}
	// saga-002 is mis-pinned into Other Book (volume 51234) as its '3'.
	for _, im := range []store.IssueMatch{
		{IssueID: "saga-001", Source: store.SourceComicVine, UpstreamID: "317966", State: store.MatchStateHighConfidence, Method: MethodFirstPageMD5},
		{IssueID: "saga-002", Source: store.SourceComicVine, UpstreamID: "400001", State: store.MatchStateAmbiguous, Method: MethodFirstPageMD5},
	} {
		if err := st.UpsertIssueMatch(im); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "saga", Source: store.SourceComicVine, VolumeID: "49019",
		State: store.MatchStateAmbiguous, Method: MethodFirstPageMD5, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, "unused-md5")
	r := NewRunner(st, cv, nil, func() int64 { return 1000 })

	// Before the re-pin, reverify must report the split and keep it ambiguous.
	if err := r.Reverify("saga"); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("saga")
	if m.State != store.MatchStateAmbiguous || m.Penalties == "{}" {
		t.Fatalf("want ambiguous with penalties before repin, got %s (%s)", m.State, m.Penalties)
	}

	// The console offers Saga's own '2' as the target.
	target, err := r.LookupIssueInVolume(store.SourceComicVine, "49019", "2")
	if err != nil || target.IssueID != 317967 {
		t.Fatalf("want repin target 317967, got %+v (%v)", target, err)
	}

	if err := r.RepinIssue("saga-002", store.SourceComicVine, "317967"); err != nil {
		t.Fatal(err)
	}
	if err := r.Reverify("saga"); err != nil {
		t.Fatal(err)
	}

	m, _ = st.GetSeriesMatch("saga")
	if m.State != store.MatchStateHighConfidence || m.Penalties != "{}" || m.VolumeID != "49019" {
		t.Fatalf("want clean high_confidence in 49019 after repin, got %s in %q (%s)", m.State, m.VolumeID, m.Penalties)
	}

	ims, _ := st.ListIssueMatchesForSeries("saga")
	if len(ims) != 2 {
		t.Fatalf("repin duplicated rows: %+v", ims)
	}
	for _, im := range ims {
		if im.IssueID == "saga-002" && (im.UpstreamID != "317967" || im.Method != MethodHumanRepin) {
			t.Fatalf("repin not recorded: %+v", im)
		}
	}
}

func TestLookupVolume(t *testing.T) {
	cv := newCVFixture(t, "unused-md5")
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := NewRunner(st, cv, nil, func() int64 { return 1 })

	v, err := r.LookupVolume(store.SourceComicVine, "49019")
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "Saga" || v.StartYear != "2012" || v.Publisher != "Image" {
		t.Fatalf("unexpected volume %+v", v)
	}
	if _, err := r.LookupVolume(store.SourceComicVine, "99999999"); err == nil {
		t.Fatal("want error for unknown volume")
	}
}

// A "Review" click on a collection folder (every file a collection, each
// resolved to its own one-issue volume) must keep it a collection folder —
// Reverify must not reverse the runner's three guarantees: state stays
// collection, VolumeID stays empty, and no split penalty appears. Mirrors
// TestRunnerTradeFolderPerFileVolumes in runner_test.go.
func TestReverifyKeepsCollectionFolder(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	folder := filepath.Join(t.TempDir(), "Criminal (2015)")
	if err := st.UpsertSeries(store.Series{
		ID: "criminal", Name: "Criminal (2015)", FolderPath: folder, IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("criminal", []store.Issue{
		{ID: "c1", SeriesID: "criminal", Filename: "Criminal v01 - Coward (2015).cbz", Number: "1", Position: 0},
		{ID: "c2", SeriesID: "criminal", Filename: "Criminal v02 - Lawless (2015).cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}
	for _, im := range []store.IssueMatch{
		{IssueID: "c1", Source: store.SourceComicVine, UpstreamID: "900001", State: store.MatchStateCollection, Method: MethodFirstPageMD5},
		{IssueID: "c2", Source: store.SourceComicVine, UpstreamID: "900002", State: store.MatchStateCollection, Method: MethodFirstPageMD5},
	} {
		if err := st.UpsertIssueMatch(im); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "criminal", Source: store.SourceComicVine, VolumeID: "",
		State: store.MatchStateCollection, Method: MethodFirstPageMD5, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 80001, name: "Criminal: Coward", year: "2015", count: 1},
			{id: 80002, name: "Criminal: Lawless", year: "2015", count: 1},
		},
		[]cvIssue{{id: 900001, volumeID: 80001, number: "1"}, {id: 900002, volumeID: 80002, number: "1"}})
	r := NewRunner(st, cv, nil, func() int64 { return 2000 })

	if err := r.Reverify("criminal"); err != nil {
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
		t.Fatalf("collection folder must have no volume after reverify, got %q", m.VolumeID)
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

// handleMatchSetVolume writes a human-chosen volume onto series_match and
// then calls Reverify. The collection-folder early return must not run when
// a volume is already pinned — otherwise the pin a human just made gets
// silently reset to "" on the very next reverify.
func TestReverifyKeepsManualVolumeOnCollectionFolder(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	folder := filepath.Join(t.TempDir(), "Criminal (2015)")
	if err := st.UpsertSeries(store.Series{
		ID: "criminal", Name: "Criminal (2015)", FolderPath: folder, IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("criminal", []store.Issue{
		{ID: "c1", SeriesID: "criminal", Filename: "Criminal v01 - Coward (2015).cbz", Number: "1", Position: 0},
		{ID: "c2", SeriesID: "criminal", Filename: "Criminal v02 - Lawless (2015).cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}
	for _, im := range []store.IssueMatch{
		{IssueID: "c1", Source: store.SourceComicVine, UpstreamID: "900001", State: store.MatchStateCollection, Method: MethodFirstPageMD5},
		{IssueID: "c2", Source: store.SourceComicVine, UpstreamID: "900002", State: store.MatchStateCollection, Method: MethodFirstPageMD5},
	} {
		if err := st.UpsertIssueMatch(im); err != nil {
			t.Fatal(err)
		}
	}
	// A human already pinned this folder to volume 80001 (e.g. via
	// handleMatchSetVolume), unlike TestReverifyKeepsCollectionFolder which
	// seeds VolumeID: "".
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "criminal", Source: store.SourceComicVine, VolumeID: "80001",
		State: store.MatchStateCollection, Method: MethodFirstPageMD5, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 80001, name: "Criminal: Coward", year: "2015", count: 1},
			{id: 80002, name: "Criminal: Lawless", year: "2015", count: 1},
		},
		[]cvIssue{{id: 900001, volumeID: 80001, number: "1"}, {id: 900002, volumeID: 80002, number: "1"}})
	r := NewRunner(st, cv, nil, func() int64 { return 2000 })

	if err := r.Reverify("criminal"); err != nil {
		t.Fatal(err)
	}

	m, err := st.GetSeriesMatch("criminal")
	if err != nil {
		t.Fatal(err)
	}
	if m.VolumeID != "80001" {
		t.Fatalf("manual volume pin must survive reverify, got %q", m.VolumeID)
	}
	if m.State == store.MatchStateCollection {
		t.Fatalf("a pinned volume must not be treated as a collection folder, got %s (%s)", m.State, m.Penalties)
	}
}

// A repin is validated against, and stored under, the dump the human picked
// it from. The id spaces overlap, so filing a GCD id under comicvine would
// point the app at whatever unrelated ComicVine issue shares that number.
func TestRepinIssueIsSourceScoped(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "jurgens", "Action Comics by Dan Jurgens Omnibus", map[string]string{
		"Action Comics by Dan Jurgens Omnibus Vol. 1.cbz": "1",
	})
	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdOmnibus))

	const issueID = "jurgens-Action Comics by Dan Jurgens Omnibus Vol. 1.cbz"
	if err := r.RepinIssue(issueID, store.SourceGCD, "2851449"); err != nil {
		t.Fatal(err)
	}
	ims, _ := st.ListIssueMatchesForSeries("jurgens")
	if len(ims) != 1 || ims[0].Source != store.SourceGCD || ims[0].UpstreamID != "2851449" {
		t.Fatalf("want one gcd-sourced repin row on 2851449, got %+v", ims)
	}

	// The same id is not in the ComicVine dump: the repin must be refused
	// rather than stored under the wrong source.
	if err := r.RepinIssue(issueID, store.SourceComicVine, "2851449"); err == nil {
		t.Fatal("a gcd id must not repin against ComicVine")
	}
}

// ComicVine files each collected edition as its own one-issue volume whose
// issue is numbered 1, regardless of where the trade sits in the reading
// order: "Black Science: True Atonement" is volume 96273, one issue, number
// "1", named "Volume 5". A local file numbered 5 can never be matched into it
// by number, so re-pinning a trade needs the sole-issue path.
func TestSoleIssueInVolume(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "bs", "Black Science", map[string]string{
		"Black Science Vol. 5.cbz": "5",
	})
	cv := newCVFixtureVolumes(t, "deadbeefdeadbeefdeadbeefdeadbeef",
		[]cvVolume{
			{id: 96273, name: "Black Science: True Atonement", year: "2016", count: 1},
			{id: 69537, name: "Black Science", year: "2013", count: 43},
		},
		[]cvIssue{
			{id: 563740, volumeID: 96273, number: "1"},
			{id: 900001, volumeID: 69537, number: "1"},
			{id: 900005, volumeID: 69537, number: "5"},
		})
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })

	// The trade's volume has no "#5" — number matching cannot reach it.
	if _, err := r.LookupIssueInVolume(store.SourceComicVine, "96273", "5"); err == nil {
		t.Fatal("a one-issue trade volume must not resolve by the local number 5")
	}
	// The sole-issue path does, without needing a number at all.
	got, err := r.SoleIssueInVolume(store.SourceComicVine, "96273")
	if err != nil {
		t.Fatalf("SoleIssueInVolume: %v", err)
	}
	if got.IssueID != 563740 {
		t.Errorf("IssueID = %d, want 563740", got.IssueID)
	}
	// A multi-issue volume must refuse: the number is the only thing that
	// could pick between its issues, so guessing would pin the wrong one.
	if _, err := r.SoleIssueInVolume(store.SourceComicVine, "69537"); err == nil {
		t.Error("a multi-issue volume must not resolve without a number")
	}
}

func TestVolumeCoverURLPicksLowestIssue(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cv := newCVFixtureVolumes(t, "x", []cvVolume{{id: 80001, name: "Two Issues", year: "2020", count: 2}},
		[]cvIssue{
			{id: 800002, volumeID: 80001, number: "2", imageURL: "http://x/two.jpg"},
			{id: 800001, volumeID: 80001, number: "01", imageURL: "http://x/original/one.jpg"},
		})
	r := NewRunner(st, cv, nil, func() int64 { return 1 })

	if got := r.VolumeCoverURL(store.SourceComicVine, "80001"); got != "http://x/scale_medium/one.jpg" {
		t.Errorf("got %q, want issue 1's cover", got)
	}
	if got := r.VolumeCoverURL(store.SourceGCD, "80001"); got != "" {
		t.Errorf("gcd: got %q, want empty", got)
	}
	if got := r.VolumeCoverURL(store.SourceComicVine, "abc"); got != "" {
		t.Errorf("bad id: got %q, want empty", got)
	}
}
