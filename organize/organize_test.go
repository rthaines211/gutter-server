package organize

import (
	"database/sql"
	"github.com/ryanhaines/comic-reader/rename"
	"strconv"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/ryanhaines/comic-reader/match"
	"github.com/ryanhaines/comic-reader/match/localcv"
	"github.com/ryanhaines/comic-reader/store"
)

// newCVFixture builds the offline ComicVine dump used by every test here:
// volumes 49019 Saga/2012/Image and 51234 Other Book/2012/Image, with issues
// 317966 '1' and 317967 '2' in 49019 and 400001 '3' in 51234. Same SQL shape
// as match/runner_test.go's newCVFixtureFull, trimmed to what this package's
// Volume/Issue lookups actually touch.
func newCVFixture(t *testing.T) *localcv.DB {
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
	INSERT INTO cv_publisher(id, name) VALUES (10, 'Image');
	INSERT INTO cv_volume(id, name, start_year, publisher_id) VALUES (49019, 'Saga', '2012', 10);
	INSERT INTO cv_volume(id, name, start_year, publisher_id) VALUES (51234, 'Other Book', '2012', 10);
	INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url, description,
		person_credits, character_credits)
		VALUES (317966, 49019, 'Chapter One', '1', '2012-03-14', 'http://x/1.jpg', 'd', '[]', '[]'),
		       (317967, 49019, 'Chapter Two', '2', '2012-04-11', 'http://x/2.jpg', 'd', '[]', '[]'),
		       (400001, 51234, 'Elsewhere', '3', '2012-05-09', 'http://x/3.jpg', 'd', '[]', '[]');
	-- A volume whose name survives slash-stripping but then starts with a dot,
	-- so SanitizeFilename fails on the canonical folder name.
	INSERT INTO cv_volume(id, name, start_year, publisher_id) VALUES (66666, './hack//Sign', '2012', 10);
	INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url, description,
		person_credits, character_credits)
		VALUES (500001, 66666, 'Login', '1', '2012-06-01', 'http://x/6.jpg', 'd', '[]', '[]');
	`
	if _, err := db.Exec(stmts); err != nil {
		t.Fatal(err)
	}
	db.Close()

	cv, err := localcv.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cv.Close() })
	return cv
}

// newTestFixture wires a fresh app store to a Runner backed by newCVFixture.
func newTestFixture(t *testing.T) (*store.Store, *match.Runner) {
	t.Helper()
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	r := match.NewRunner(st, newCVFixture(t), nil, func() int64 { return 1000 })
	return st, r
}

// seed writes a series, its issues, a confirmed series_match to volumeID, and
// confirmed issue_match rows for issueMatches (issue id -> upstream id).
func seed(t *testing.T, st *store.Store, series store.Series, issues []store.Issue,
	volumeID string, issueMatches map[string]string) {
	t.Helper()
	if err := st.UpsertSeries(series); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues(series.ID, issues); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: series.ID, Source: store.SourceComicVine, VolumeID: volumeID,
		State: store.MatchStateConfirmed, Method: "manual", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	for issueID, upstreamID := range issueMatches {
		if err := st.UpsertIssueMatch(store.IssueMatch{
			IssueID: issueID, Source: store.SourceComicVine, UpstreamID: upstreamID,
			State: store.MatchStateConfirmed, Method: "manual",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func saveDefaultTemplate(t *testing.T, st *store.Store, tpl string) {
	t.Helper()
	if err := st.SaveRenameTemplates(map[string]string{"default": tpl}); err != nil {
		t.Fatal(err)
	}
}

func flatten(p *Plan) []Op {
	var out []Op
	for _, sp := range p.Series {
		out = append(out, sp.Ops...)
	}
	return out
}

func wantOps(t *testing.T, ops []Op, want Op) {
	t.Helper()
	for _, op := range ops {
		if op == want {
			return
		}
	}
	t.Fatalf("expected op %+v not found in %+v", want, ops)
}

func TestPlanCanonicalFolderAndFilenames(t *testing.T) {
	st, m := newTestFixture(t)
	// Series confirmed to Saga (49019) but folder named "saga v1" and file
	// named oddly.
	seed(t, st, store.Series{ID: "saga-v1", Name: "saga v1", FolderPath: "/lib/saga v1", IssueCount: 1},
		[]store.Issue{{ID: "i1", SeriesID: "saga-v1", Filename: "saga_01.cbz", Number: "1", Position: 0}},
		"49019", map[string]string{"i1": "317966"})
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	ops := flatten(p)
	wantOps(t, ops, Op{Kind: OpRenameFolder, SeriesID: "saga-v1", Old: "saga v1", New: "Saga (2012)"})
	wantOps(t, ops, Op{Kind: OpRenameFile, SeriesID: "saga-v1", IssueID: "i1",
		Old: "saga_01.cbz", New: "Saga #001 (2012).cbz"})
}

// A stray is an issue whose confirmed home series (49019, "series-a") doesn't
// match the volume its own issue_match resolves to (51234). Whether that
// becomes a move+retitle or a flag depends only on whether some OTHER series
// is confirmed to 51234.
func TestPlanStrayWithConfirmedHomeMovesElseFlags(t *testing.T) {
	t.Run("moves when target confirmed", func(t *testing.T) {
		st, m := newTestFixture(t)
		saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

		// series-a confirmed to Saga (49019); folder and issue #1 already
		// canonical so the only ops come from the stray, issue #2.
		seed(t, st, store.Series{ID: "series-a", Name: "Saga", FolderPath: "/lib/Saga (2012)", IssueCount: 2},
			[]store.Issue{
				{ID: "i1", SeriesID: "series-a", Filename: "Saga #001 (2012).cbz", Number: "1", Position: 0},
				{ID: "i2", SeriesID: "series-a", Filename: "strayfile.cbz", Number: "3", Position: 1},
			},
			"49019", map[string]string{"i1": "317966", "i2": "400001"})

		// series-b confirmed to Other Book (51234): the stray's real home.
		seed(t, st, store.Series{ID: "series-b", Name: "Other Book", FolderPath: "/lib/Other Book (2012)", IssueCount: 0},
			nil, "51234", nil)

		p, err := New(st, m, nil, "").Plan()
		if err != nil {
			t.Fatal(err)
		}
		ops := flatten(p)
		wantOps(t, ops, Op{Kind: OpMoveFile, SeriesID: "series-a", IssueID: "i2",
			Old: "Saga (2012)/strayfile.cbz", New: "Other Book (2012)/strayfile.cbz"})
		wantOps(t, ops, Op{Kind: OpRenameFile, SeriesID: "series-b", IssueID: "i2",
			Old: "strayfile.cbz", New: "Other Book #003 (2012).cbz"})
	})

	t.Run("flags when nothing confirmed to the stray's volume", func(t *testing.T) {
		st, m := newTestFixture(t)
		saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

		// Only series-a is confirmed; nothing is confirmed to 51234.
		seed(t, st, store.Series{ID: "series-a", Name: "Saga", FolderPath: "/lib/Saga (2012)", IssueCount: 2},
			[]store.Issue{
				{ID: "i1", SeriesID: "series-a", Filename: "Saga #001 (2012).cbz", Number: "1", Position: 0},
				{ID: "i2", SeriesID: "series-a", Filename: "strayfile.cbz", Number: "3", Position: 1},
			},
			"49019", map[string]string{"i1": "317966", "i2": "400001"})

		p, err := New(st, m, nil, "").Plan()
		if err != nil {
			t.Fatal(err)
		}
		ops := flatten(p)
		wantOps(t, ops, Op{Kind: OpFlag, SeriesID: "series-a", IssueID: "i2",
			Old: "Saga (2012)/strayfile.cbz", Reason: "no confirmed series for volume 51234 (Other Book 2012)"})
		// The flag must be the ONLY op for the homeless stray: no move, and no
		// home-template rename fabricating a "Saga #003" name for non-Saga content.
		for _, op := range ops {
			if op.Kind == OpMoveFile {
				t.Fatalf("expected no move without a confirmed target, got %+v", op)
			}
			if op.IssueID == "i2" && op.Kind != OpFlag {
				t.Fatalf("flagged stray must get no other ops, got %+v", op)
			}
		}
	})
}

// ComicVine and GCD volume ids overlap, so a stray's home is only a home if
// it was confirmed against the same dump the stray resolved in. Here the only
// series confirmed to "51234" is confirmed to GCD's 51234, which is a
// different book from ComicVine's — the stray must be flagged homeless, not
// moved into that folder.
func TestPlanStrayDoesNotCrossDumps(t *testing.T) {
	st, m := newTestFixture(t)
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	// series-a confirmed to ComicVine Saga (49019); i2 resolves to ComicVine
	// volume 51234 via issue 400001.
	seed(t, st, store.Series{ID: "series-a", Name: "Saga", FolderPath: "/lib/Saga (2012)", IssueCount: 2},
		[]store.Issue{
			{ID: "i1", SeriesID: "series-a", Filename: "Saga #001 (2012).cbz", Number: "1", Position: 0},
			{ID: "i2", SeriesID: "series-a", Filename: "strayfile.cbz", Number: "3", Position: 1},
		},
		"49019", map[string]string{"i1": "317966", "i2": "400001"})

	// series-b is confirmed to GCD 51234 — same number, other dump, other book.
	if err := st.UpsertSeries(store.Series{ID: "series-b", Name: "Gcd Book", FolderPath: "/lib/Gcd Book (2012)"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "series-b", Source: store.SourceGCD, VolumeID: "51234",
		State: store.MatchStateConfirmed, Method: "manual", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	ops := flatten(p)
	wantOps(t, ops, Op{Kind: OpFlag, SeriesID: "series-a", IssueID: "i2",
		Old: "Saga (2012)/strayfile.cbz", Reason: "no confirmed series for volume 51234 (Other Book 2012)"})
	for _, op := range ops {
		if op.Kind == OpMoveFile {
			t.Fatalf("a ComicVine stray must not move into a GCD-confirmed folder, got %+v", op)
		}
	}
}

// A canonical folder name that fails SanitizeFilename ('./hack//Sign' keeps
// its leading dot after slash-stripping) must flag only the folder rename;
// rule-(d) file renames for the series still get planned.
func TestPlanFolderSanitizeFailureStillPlansRenames(t *testing.T) {
	st, m := newTestFixture(t)
	// Number-first template so the resolved filename doesn't itself start
	// with the dot and fail sanitize too.
	saveDefaultTemplate(t, st, "{number:03} - {series} ({year})")

	seed(t, st, store.Series{ID: "hack", Name: "hack", FolderPath: "/lib/hack", IssueCount: 1},
		[]store.Issue{{ID: "i1", SeriesID: "hack", Filename: "hack_01.cbz", Number: "1", Position: 0}},
		"66666", map[string]string{"i1": "500001"})

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	ops := flatten(p)
	wantOps(t, ops, Op{Kind: OpFlag, SeriesID: "hack", Old: "hack", Reason: "cannot compute canonical folder name"})
	wantOps(t, ops, Op{Kind: OpRenameFile, SeriesID: "hack", IssueID: "i1",
		Old: "hack_01.cbz", New: "001 - .-hack--Sign (2012).cbz"})
	for _, op := range ops {
		if op.Kind == OpRenameFolder {
			t.Fatalf("expected no folder rename when canonical name fails sanitize, got %+v", op)
		}
	}
}

func TestPlanCleanSeriesProducesNoOps(t *testing.T) {
	st, m := newTestFixture(t)
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	// Folder already canonical, filename already template-perfect.
	seed(t, st, store.Series{ID: "clean", Name: "Saga", FolderPath: "/lib/Saga (2012)", IssueCount: 1},
		[]store.Issue{{ID: "i1", SeriesID: "clean", Filename: "Saga #001 (2012).cbz", Number: "1", Position: 0}},
		"49019", map[string]string{"i1": "317966"})

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Series) != 0 {
		t.Fatalf("expected no ops for a clean series, got %+v", flatten(p))
	}
}

func TestPlanDuplicateTargetsFlagBoth(t *testing.T) {
	st, m := newTestFixture(t)
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	// Two issues that both resolve to "Saga #001 (2012).cbz" under the
	// template — same number, both bound to the series' own confirmed
	// volume so neither is a stray.
	seed(t, st, store.Series{ID: "dup", Name: "Saga", FolderPath: "/lib/Saga (2012)", IssueCount: 2},
		[]store.Issue{
			{ID: "i1", SeriesID: "dup", Filename: "a.cbz", Number: "1", Position: 0},
			{ID: "i2", SeriesID: "dup", Filename: "b.cbz", Number: "1", Position: 1},
		},
		"49019", map[string]string{"i1": "317966", "i2": "317967"})

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	ops := flatten(p)
	wantOps(t, ops, Op{Kind: OpFlag, SeriesID: "dup", IssueID: "i1", Old: "a.cbz", Reason: `duplicate target name "Saga #001 (2012).cbz"`})
	wantOps(t, ops, Op{Kind: OpFlag, SeriesID: "dup", IssueID: "i2", Old: "b.cbz", Reason: `duplicate target name "Saga #001 (2012).cbz"`})
	for _, op := range ops {
		if op.Kind == OpRenameFile {
			t.Fatalf("expected the colliding rename to become a flag, got %+v", op)
		}
	}
}

// An issue without a parsed number must be left alone, not renamed to the
// bare series name (which collides with every other numberless issue).
func TestPlanSkipsNumberlessIssues(t *testing.T) {
	st, m := newTestFixture(t)
	seed(t, st, store.Series{ID: "saga-v1", Name: "saga v1", FolderPath: "/lib/saga v1", IssueCount: 2},
		[]store.Issue{
			{ID: "i1", SeriesID: "saga-v1", Filename: "Book 001.cbz", Number: "1", Position: 0},
			{ID: "i2", SeriesID: "saga-v1", Filename: "Book 002.cbz", Number: "", Position: 1},
		},
		"49019", map[string]string{"i1": "317966", "i2": "317967"})
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range flatten(p) {
		if op.IssueID == "i2" {
			t.Fatalf("numberless issue must produce no op, got %+v", op)
		}
	}
}

// A confirmed collection folder (no folder volume) never produces strays or
// folder renames; each file is renamed with the folder's series name, its
// own number, and its own cover year, through the tpb template.
func TestPlanCollectionFolderRenamesPerFile(t *testing.T) {
	st, m := newTestFixture(t)
	if err := st.SaveRenameTemplates(map[string]string{
		"default": "{series} #{number:03} ({year})",
		"tpb":     "{series} Vol. {number} ({year})",
	}); err != nil {
		t.Fatal(err)
	}

	// Confirmed with VolumeID "" = collection folder. Issue matches point at
	// two different volumes; that must NOT be treated as strays.
	seed(t, st, store.Series{ID: "criminal", Name: "Criminal", Year: 2015, FolderPath: "/lib/Criminal (2015)", IssueCount: 2},
		[]store.Issue{
			{ID: "c1", SeriesID: "criminal", Filename: "Criminal v01 - Coward (2015) (Digital).cbz", Number: "1", Position: 0, Format: "tpb"},
			{ID: "c2", SeriesID: "criminal", Filename: "Criminal v02 - Lawless (2015) (Digital).cbz", Number: "2", Position: 1, Format: "tpb"},
		},
		"", map[string]string{"c1": "317966", "c2": "400001"})
	for id, date := range map[string]string{"c1": "2015-03-01", "c2": "2016-01-01"} {
		if err := st.UpsertIssueEnrichment(store.IssueEnrichment{IssueID: id, Title: "x", CoverDate: date, EnrichedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	ops := flatten(p)
	wantOps(t, ops, Op{Kind: OpRenameFile, SeriesID: "criminal", IssueID: "c1",
		Old: "Criminal v01 - Coward (2015) (Digital).cbz", New: "Criminal Vol. 1 (2015).cbz"})
	wantOps(t, ops, Op{Kind: OpRenameFile, SeriesID: "criminal", IssueID: "c2",
		Old: "Criminal v02 - Lawless (2015) (Digital).cbz", New: "Criminal Vol. 2 (2016).cbz"})
	for _, op := range ops {
		if op.Kind == OpFlag || op.Kind == OpMoveFile || op.Kind == OpRenameFolder {
			t.Fatalf("collection folder must only rename files, got %+v", op)
		}
	}
}

// parentVote is the whole safety story for collection folder naming: it is
// what separates a real parent series from an incidental mention in a trade's
// description. The threshold (votes >= len(volumes)) was derived from the 14
// collection folders in the live library, where it kept 12 correct results and
// abstained on the 2 that a bare plurality got wrong.
func TestParentVote(t *testing.T) {
	// ref builds a description carrying ComicVine volume back-links.
	ref := func(ids ...int64) string {
		s := "<p>Collects issues of "
		for _, id := range ids {
			s += `<a href="/x/" data-ref-id="4050-` + strconv.FormatInt(id, 10) + `">x</a> `
		}
		return s + "</p>"
	}
	vol := func(id int64, desc string) *localcv.Volume {
		return &localcv.Volume{VolumeID: id, Description: desc}
	}

	for _, c := range []struct {
		name string
		vols []*localcv.Volume
		want int64
		ok   bool
	}{
		{
			// Black Science: every trade links volume 69537.
			name: "unanimous parent wins",
			vols: []*localcv.Volume{
				vol(74320, ref(69537)), vol(79704, ref(69537)), vol(83802, ref(69537)),
			},
			want: 69537, ok: true,
		},
		{
			// Countdown to Infinite Crisis: five one-shots, one stray
			// mention. 1 vote < 5 volumes, so abstain rather than name the
			// folder after a passing reference.
			name: "thin evidence abstains",
			vols: []*localcv.Volume{
				vol(1, ref(999)), vol(2, ""), vol(3, ""), vol(4, ""), vol(5, ""),
			},
			ok: false,
		},
		{
			name: "exactly at the threshold wins",
			vols: []*localcv.Volume{vol(1, ref(500)), vol(2, ref(500))},
			want: 500, ok: true,
		},
		{
			name: "one short of the threshold abstains",
			vols: []*localcv.Volume{vol(1, ref(500)), vol(2, ""), vol(3, "")},
			ok:   false,
		},
		{
			// Two parents equally referenced is not one parent.
			name: "a tie abstains",
			vols: []*localcv.Volume{vol(1, ref(500, 600)), vol(2, ref(500, 600))},
			ok:   false,
		},
		{
			// A trade linking a sibling trade must not win: siblings are in
			// the folder's own volume set.
			name: "self-references are ignored",
			vols: []*localcv.Volume{vol(1, ref(2, 2, 2)), vol(2, ref(1, 1, 1))},
			ok:   false,
		},
		{
			name: "a single volume is not a collection",
			vols: []*localcv.Volume{vol(1, ref(500, 500, 500))},
			ok:   false,
		},
		{
			name: "no back-links at all abstains",
			vols: []*localcv.Volume{vol(1, ""), vol(2, "")},
			ok:   false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			own := map[int64]bool{}
			for _, v := range c.vols {
				own[v.VolumeID] = true
			}
			got, ok := parentVote(c.vols, own)
			if ok != c.ok || (c.ok && got != c.want) {
				t.Errorf("parentVote() = (%d, %v), want (%d, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

// TestCollectionSubtitle: the enrichment title repeats what the template
// already emits - the series name, the volume designator, and edition noise -
// so all three have to come off before it is worth appending.
func TestCollectionSubtitle(t *testing.T) {
	for _, tc := range []struct{ title, series, want string }{
		// series prefix
		{"Birthright: Homecoming", "Birthright", "Homecoming"},
		{"Black Science - How to Fall Forever", "Black Science", "How to Fall Forever"},
		{"birthright: homecoming", "Birthright", "homecoming"},
		// volume designator repeated in the title
		{"Volume 1: The Court of Owls", "Batman", "The Court of Owls"},
		{"Volume 4: Zero Year - Secret City", "Batman", "Zero Year - Secret City"},
		{"Volume Four: Disunity", "Undiscovered Country", "Disunity"},
		{"Book 2 - The Return", "Saga", "The Return"},
		// nothing left but the designator -> no subtitle at all
		{"Volume One", "Wolverine and the X-Men by Jason Aaron", ""},
		{"Volume 1", "Daredevil by Mark Waid", ""},
		{"Vol. 3", "Saga", ""},
		// edition noise is not a subtitle
		{"HC", "Fantastic Four - Doom 2099 Omnibus", ""},
		{"tpb", "Saga", ""},
		// series prefix AND designator together
		{"Batman: Volume 7: Endgame", "Batman", "Endgame"},
		// genuinely informative titles survive untouched
		{"Belly of the Beast", "Birthright", "Belly of the Beast"},
		{"Saga: Volume One", "Birthright", "Saga: Volume One"},
		{"Birthright", "Birthright", ""},
		{"", "Birthright", ""},
		{"Homecoming", "", "Homecoming"},
	} {
		if got := collectionSubtitle(tc.title, tc.series); got != tc.want {
			t.Errorf("collectionSubtitle(%q, %q) = %q, want %q", tc.title, tc.series, got, tc.want)
		}
	}
}

// TestTrimTrailingOmnibus: the omnibus template emits "Omnibus Vol. {number}",
// so a series already ending in "Omnibus" rendered it twice.
func TestTrimTrailingFormatWord(t *testing.T) {
	for _, tc := range []struct{ series, format, want string }{
		{"Superman by Phillip Kennedy Johnson Omnibus", "omnibus", "Superman by Phillip Kennedy Johnson"},
		{"Grayson: The Superspy Omnibus", "omnibus", "Grayson: The Superspy"},
		{"Batman", "omnibus", "Batman"},
		// only the omnibus template doubles up; leave other formats alone
		{"Wolverine Omnibus", "tpb", "Wolverine Omnibus"},
		{"Omnibus", "omnibus", "Omnibus"}, // nothing left over -> keep as-is
		// annuals double the same way: "Nightwing 2024 Annual Annual #001"
		{"Nightwing 2024 Annual", "annual", "Nightwing 2024"},
		{"Green Lantern Corps", "annual", "Green Lantern Corps"},
		{"Annual", "annual", "Annual"},
		{"Nightwing 2024 Annual", "", "Nightwing 2024 Annual"},
	} {
		if got := trimTrailingFormatWord(tc.series, tc.format); got != tc.want {
			t.Errorf("trimTrailingFormatWord(%q, %q) = %q, want %q", tc.series, tc.format, got, tc.want)
		}
	}
}

// TestCollectionTemplateRendersSubtitle is the end-to-end check on the actual
// tpb template: a trade with a subtitle gains it, and one without must not be
// left with a dangling " - " separator.
func TestCollectionTemplateRendersSubtitle(t *testing.T) {
	const tpl = "{series} Vol. {number} - {title} ({year})"
	for _, tc := range []struct {
		name, title, want string
	}{
		{"with subtitle", "Birthright: Homecoming", "Birthright Vol. 01 - Homecoming (2015)"},
		{"no subtitle", "", "Birthright Vol. 01 (2015)"},
		{"title is series", "Birthright", "Birthright Vol. 01 (2015)"},
	} {
		got := rename.Resolve(tpl, map[string]string{
			"series": "Birthright",
			"number": "01",
			"year":   "2015",
			"title":  collectionSubtitle(tc.title, "Birthright"),
		})
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCollectionFolderHint: a folder where most files are homeless is a run of
// trades confirmed against one volume, not a series with strays. The per-file
// flags name only the symptom, and the remedy lives on a different page, so the
// plan has to say it outright.
func TestCollectionFolderHint(t *testing.T) {
	hint := func(strays, issues int) bool { return issues > 1 && strays*2 > issues }

	for _, tc := range []struct {
		name           string
		strays, issues int
		want           bool
	}{
		{"run of trades (Green Lantern: No Fear)", 10, 13, true},
		{"all files homeless", 5, 5, true},
		{"ongoing series, few strays (Nightwing 2016)", 5, 143, false},
		{"exactly half is not a majority", 2, 4, false},
		{"single-file folder never hints", 1, 1, false},
		{"no strays", 0, 12, false},
	} {
		if got := hint(tc.strays, tc.issues); got != tc.want {
			t.Errorf("%s: %d/%d -> hint=%v, want %v", tc.name, tc.strays, tc.issues, got, tc.want)
		}
	}
}

// {year} is the issue's cover year, not the volume start year.
func TestPlanUsesCoverYear(t *testing.T) {
	st, m := newTestFixture(t)
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")
	seed(t, st, store.Series{ID: "saga", Name: "Saga (2012)", FolderPath: "/lib/Saga (2012)", IssueCount: 1},
		[]store.Issue{{ID: "i1", SeriesID: "saga", Filename: "saga_01.cbz", Number: "1", Position: 0}},
		"49019", map[string]string{"i1": "317966"})
	if err := st.UpsertIssueEnrichment(store.IssueEnrichment{IssueID: "i1", VineID: 317966, CoverDate: "2014-01-01"}); err != nil {
		t.Fatal(err)
	}

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	wantOps(t, flatten(p), Op{Kind: OpRenameFile, SeriesID: "saga", IssueID: "i1",
		Old: "saga_01.cbz", New: "Saga #001 (2014).cbz"})
}

// An annual whose own volume (51234) differs from the folder's (49019)
// stays home even when another series is confirmed to 51234, which used
// to produce a move.
func TestPlanKeepsAnnualInParentFolder(t *testing.T) {
	st, m := newTestFixture(t)
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")
	seed(t, st, store.Series{ID: "saga", Name: "Saga (2012)", FolderPath: "/lib/Saga (2012)", IssueCount: 2},
		[]store.Issue{
			{ID: "i1", SeriesID: "saga", Filename: "Saga #001 (2012).cbz", Number: "1", Position: 0},
			{ID: "ann", SeriesID: "saga", Filename: "Saga Annual 001.cbz", Number: "1", Position: 1},
		},
		"49019", map[string]string{"i1": "317966", "ann": "400001"})
	if err := st.SetIssueFormatIfBlank("ann", "annual"); err != nil {
		t.Fatal(err)
	}
	seed(t, st, store.Series{ID: "other", Name: "Other Book (2012)", FolderPath: "/lib/Other Book (2012)", IssueCount: 0},
		nil, "51234", nil)

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range flatten(p) {
		if op.IssueID == "ann" && (op.Kind == OpMoveFile || op.Kind == OpFlag) {
			t.Fatalf("annual treated as a stray: %+v", op)
		}
	}
}
