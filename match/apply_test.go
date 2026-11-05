package match

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rthaines211/gutter-server/match/gcd/gcdtest"
	"github.com/rthaines211/gutter-server/metron"
	"github.com/rthaines211/gutter-server/store"
)

func TestApplyPopulatesEnrichment(t *testing.T) {
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
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := r.Apply("saga"); err != nil {
		t.Fatal(err)
	}

	se, err := st.GetSeriesEnrichment("saga")
	if err != nil {
		t.Fatal(err)
	}
	if se.VolumeID != 49019 || se.Publisher != "Image" || se.StartYear != 2012 {
		t.Fatalf("unexpected series enrichment %+v", se)
	}

	ie, err := st.ListIssueEnrichmentForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if ie["saga-001"].Title != "Chapter One" {
		t.Fatalf("unexpected issue enrichment %+v", ie)
	}
	if ie["saga-001"].CoverDate != "2012-03-14" {
		t.Fatalf("expected cover date, got %+v", ie["saga-001"])
	}
}

// The series match records the MAJORITY volume, but the series-level fields
// (publisher, description, start year) come from a resolved issue. If Apply took
// them from whichever issue sorted first it could pair the majority volume's id
// with a minority volume's publisher. The console offers Confirm on ambiguous
// rows, which is exactly the volume-split case, so this has to hold.
func TestApplyUsesMajorityVolumeForSeriesFields(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lib := t.TempDir()
	folder := filepath.Join(lib, "Saga (2012)")
	// Position 0 is the MINORITY volume, so "first resolved issue" and
	// "majority volume issue" are different rows.
	minority := []byte("minority-page")
	maj1 := []byte("saga-page-one")
	maj2 := []byte("saga-page-two")
	writeCBZ(t, filepath.Join(folder, "Saga 001 (2012).cbz"), minority)
	writeCBZ(t, filepath.Join(folder, "Saga 002 (2012).cbz"), maj1)
	writeCBZ(t, filepath.Join(folder, "Saga 003 (2012).cbz"), maj2)

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

	// Two issues resolve to 49019 (Image/2012/"d"), one to 60001 (Dark Horse/1999).
	cv := newCVFixture(t, md5hex(maj1),
		cvFile{md5: md5hex(minority), issueID: 500001},
		cvFile{md5: md5hex(maj2), issueID: 317967})

	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply("saga"); err != nil {
		t.Fatal(err)
	}

	se, err := st.GetSeriesEnrichment("saga")
	if err != nil {
		t.Fatal(err)
	}
	if se.VolumeID != 49019 {
		t.Fatalf("expected the majority volume 49019, got %d", se.VolumeID)
	}
	// These are the fields that would come from the minority volume if Apply
	// used the first resolved issue instead of a majority-volume one.
	if se.Publisher != "Image" {
		t.Fatalf("publisher came from the minority volume: %q, want %q", se.Publisher, "Image")
	}
	if se.StartYear != 2012 {
		t.Fatalf("start year came from the minority volume: %d, want 2012", se.StartYear)
	}
	if se.Description != "d" {
		t.Fatalf("description came from the minority volume: %q, want %q", se.Description, "d")
	}
}

// A match with no resolved volume must write nothing. The admin confirm guard
// refuses these, but Apply is reachable directly and must not write a series row
// with a zero volume id.
func TestApplyNoVolumeIsNoOp(t *testing.T) {
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
		State: store.MatchStateNoMatch, Method: MethodFirstPageMD5, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	r := NewRunner(st, cv, nil, func() int64 { return 1000 })
	if err := r.Apply("nomatch"); err != nil {
		t.Fatalf("Apply on a volumeless match must be a no-op, got %v", err)
	}

	if _, err := st.GetSeriesEnrichment("nomatch"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected no series_enrichment row, got err %v", err)
	}
}

// A confirmed collection folder has no folder volume. Apply writes each file's
// own trade-volume name as its title and a series row carrying only the
// publisher; the folder keeps its local name and year.
func TestApplyCollectionFolderWritesPerFileTitles(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "criminal", Name: "Criminal (2015)", Year: 2015, FolderPath: t.TempDir(), IssueCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("criminal", []store.Issue{
		{ID: "c1", SeriesID: "criminal", Filename: "Criminal v01 - Coward (2015).cbz", Number: "1", Position: 0, Format: "tpb"},
		{ID: "c2", SeriesID: "criminal", Filename: "Criminal v02 - Lawless (2015).cbz", Number: "2", Position: 1, Format: "tpb"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "criminal", Source: store.SourceComicVine, VolumeID: "",
		State: store.MatchStateCollection, Method: MethodFirstPageMD5, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	for id, up := range map[string]string{"c1": "900001", "c2": "900002"} {
		if err := st.UpsertIssueMatch(store.IssueMatch{
			IssueID: id, Source: store.SourceComicVine, UpstreamID: up,
			State: store.MatchStateHighConfidence, Method: MethodFirstPageMD5,
		}); err != nil {
			t.Fatal(err)
		}
	}

	cv := newCVFixtureVolumes(t, "unused-md5",
		[]cvVolume{
			{id: 80001, name: "Criminal: Coward", year: "2015", count: 1},
			{id: 80002, name: "Criminal: Lawless", year: "2015", count: 1},
		},
		[]cvIssue{{id: 900001, volumeID: 80001, number: "1"}, {id: 900002, volumeID: 80002, number: "1"}})
	r := NewRunner(st, cv, nil, func() int64 { return 1000 })
	if err := r.Apply("criminal"); err != nil {
		t.Fatal(err)
	}

	ie, err := st.ListIssueEnrichmentForSeries("criminal")
	if err != nil {
		t.Fatal(err)
	}
	if ie["c1"].Title != "Criminal: Coward" || ie["c2"].Title != "Criminal: Lawless" {
		t.Fatalf("expected trade names as titles, got %+v", ie)
	}
	se, err := st.GetSeriesEnrichment("criminal")
	if err != nil {
		t.Fatal(err)
	}
	if se.VolumeID != 0 || se.Publisher != "Image" || se.StartYear != 2015 {
		t.Fatalf("unexpected series enrichment %+v", se)
	}
}

// A confirmed series whose issues were bound through Metron must still get
// enrichment rows on Apply — resolved via the Metron API, not the dump, with
// the ComicVine cross-reference id in comicvine_issue_id and the per-issue
// credits collapsed into the series roster.
func TestApplyResolvesMetronMatches(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertSeries(store.Series{
		ID: "newbook", Name: "New Book (2026)", FolderPath: "/lib/New Book (2026)", IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("newbook", []store.Issue{
		{ID: "nb-001", SeriesID: "newbook", Filename: "New Book 001 (2026).cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "newbook", Source: store.SourceComicVine, VolumeID: "77001",
		State: store.MatchStateConfirmed, Method: MethodMetronVolumeNumber, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssueMatch(store.IssueMatch{
		IssueID: "nb-001", Source: store.SourceMetron, UpstreamID: "8001",
		State: store.MatchStateConfirmed, Method: MethodMetronVolumeNumber,
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/issue/8001/":
			fmt.Fprint(w, `{"id":8001,"series":{"id":800},"number":"1","name":["Debut"],
				"desc":"First.","cover_date":"2026-01-01","cv_id":990001,
				"credits":[{"creator":"Ann Author","role":[{"name":"Writer"}]},
				           {"creator":"Pat Painter","role":[{"name":"Artist"},{"name":"Cover"}]}]}`)
		case r.URL.Path == "/series/" && r.URL.Query().Get("cv_id") == "77001":
			fmt.Fprint(w, `{"count":1,"next":null,"previous":null,
				"results":[{"id":800,"series":"New Book (2026)","year_began":2026,"issue_count":3}]}`)
		case r.URL.Path == "/series/800/":
			fmt.Fprint(w, `{"id":800,"name":"New Book","year_began":2026,"desc":"A new book.",
				"issue_count":3,"publisher":{"name":"Image"},"cv_id":77001}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	mc := metron.New("u", "p", t.TempDir())
	mc.BaseURL = srv.URL

	cv := newCVFixture(t, "00000000000000000000000000000003") // dump knows nothing
	r := NewRunner(st, cv, []string{"/lib"}, func() int64 { return 3000 })
	r.SetMetron(mc)

	if err := r.Apply("newbook"); err != nil {
		t.Fatal(err)
	}

	ie, err := st.ListIssueEnrichmentForSeries("newbook")
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ie["nb-001"]
	if !ok {
		t.Fatal("no issue enrichment written for metron-sourced match")
	}
	if e.VineID != 990001 || e.Title != "Debut" || e.CoverDate != "2026-01-01" || e.Summary != "First." {
		t.Errorf("issue enrichment wrong: %+v", e)
	}

	se, err := st.GetSeriesEnrichment("newbook")
	if err != nil {
		t.Fatal(err)
	}
	if se.VolumeID != 77001 || se.Publisher != "Image" || se.StartYear != 2026 || se.Description != "A new book." {
		t.Errorf("series enrichment wrong: %+v", se)
	}
	// Roles are exploded: one creator entry per (name, role), lowercased.
	want := `[{"name":"Ann Author","role":"writer"},{"name":"Pat Painter","role":"artist"},{"name":"Pat Painter","role":"cover"}]`
	if se.Creators != want {
		t.Errorf("creators = %s, want %s", se.Creators, want)
	}
}

// A confirmed GCD match feeds the same enrichment tables the app reads, with
// zero ComicVine ids — the value Metron already writes without a cross-ref.
func TestApplyGCDMatchWritesEnrichmentWithZeroVineIDs(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := mkSeries(t, st, "jurgens", "Action Comics by Dan Jurgens Omnibus", map[string]string{
		"Action Comics by Dan Jurgens Omnibus Vol. 1.cbz": "1",
	})
	cv := newCVFixture(t, "deadbeefdeadbeefdeadbeefdeadbeef")
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1000 })
	r.SetGCD(gcdtest.Open(t, gcdOmnibus+`
		INSERT INTO gcd_story(id, issue_id, title, type_id, sequence_number, genre, characters, synopsis)
			VALUES (50, 2762266, 'Superman Reborn', 19, 1, 'superhero', 'Superman; Lois Lane (reporter)', 'Clark returns.');
		UPDATE gcd_issue SET page_count = 22 WHERE id = 2762266;
		INSERT INTO gcd_creator_name_detail(id, name) VALUES (200, 'Dan Jurgens'), (201, 'Brett Breeding');
		INSERT INTO gcd_story_credit(id, story_id, credit_type_id, creator_id) VALUES (1, 50, 1, 200), (2, 50, 2, 200), (3, 50, 3, 201);`))
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply("jurgens"); err != nil {
		t.Fatal(err)
	}
	ie, err := st.GetIssueEnrichment("jurgens-Action Comics by Dan Jurgens Omnibus Vol. 1.cbz")
	if err != nil {
		t.Fatal(err)
	}
	if ie.VineID != 0 || ie.Title != "Superman Reborn" || ie.CoverDate != "2025-08-05" || ie.Summary != "Clark returns." ||
		ie.PageCount != 22 || ie.Characters != `[{"name":"Superman"},{"name":"Lois Lane"}]` {
		t.Fatalf("issue enrichment wrong: %+v", ie)
	}
	se, err := st.GetSeriesEnrichment("jurgens")
	if err != nil {
		t.Fatal(err)
	}
	if se.VolumeID != 0 || se.Publisher != "DC" || se.StartYear != 2025 || se.Status != "confirmed" || se.Description != "" ||
		se.Genre != "superhero" || se.GCDSeriesID != 226636 {
		t.Fatalf("series enrichment wrong: %+v", se)
	}
	if se.Creators != `[{"name":"Dan Jurgens","role":"script"},{"name":"Dan Jurgens","role":"pencils"},{"name":"Brett Breeding","role":"inks"}]` {
		t.Fatalf("creators wrong: %s", se.Creators)
	}
}

func TestCharactersJSON(t *testing.T) {
	if got := charactersJSON(nil); got != "[]" {
		t.Fatalf("nil: got %q", got)
	}
	if got := charactersJSON([]string{"Batman", "Robin"}); got != `[{"name":"Batman"},{"name":"Robin"}]` {
		t.Fatalf("got %q", got)
	}
}

func TestApplyRecordsVolumeName(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	page := []byte("saga-1")
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga 001 (2012).cbz", page)
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{"Saga 001 (2012).cbz": "1"})
	r := NewRunner(st, newCVFixture(t, md5hex(page)), []string{lib}, func() int64 { return 1 })
	if _, _, err := r.MatchSeries(context.Background(), mustGetSeries(t, st, "saga")); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply("saga"); err != nil {
		t.Fatal(err)
	}
	e, err := st.GetSeriesEnrichment("saga")
	if err != nil {
		t.Fatal(err)
	}
	if e.VolumeName != "Saga" {
		t.Fatalf("VolumeName = %q, want Saga", e.VolumeName)
	}
}

func TestConvertCreditsKeepsID(t *testing.T) {
	// Local shape independent of store.Creator so the test still catches a
	// missing ID field even before store.Creator grows one.
	type creatorWithID struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	}
	got := convertCredits(`[{"id":40724,"name":"Mark Waid","role":"writer"}]`)
	var gotCreators []creatorWithID
	if err := json.Unmarshal([]byte(got), &gotCreators); err != nil {
		t.Fatalf("unmarshal got %q: %v", got, err)
	}
	want := []creatorWithID{{ID: 40724, Name: "Mark Waid", Role: "writer"}}
	if !reflect.DeepEqual(gotCreators, want) {
		t.Fatalf("convertCredits = %+v, want %+v", gotCreators, want)
	}
}
