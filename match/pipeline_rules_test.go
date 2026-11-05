package match

import (
	"context"
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

func TestAutoConfirmableRequiresAllFour(t *testing.T) {
	ok := store.SeriesMatch{State: store.MatchStateHighConfidence, Method: MethodFirstPageMD5,
		Penalties: "{}", VolumeID: "49019"}
	if !AutoConfirmable(&ok) {
		t.Fatal("clean hash match must auto-confirm")
	}
	for name, mut := range map[string]func(m *store.SeriesMatch){
		"ambiguous":  func(m *store.SeriesMatch) { m.State = store.MatchStateAmbiguous },
		"name_year":  func(m *store.SeriesMatch) { m.Method = MethodNameYear },
		"penalties":  func(m *store.SeriesMatch) { m.Penalties = `{"year":"x"}` },
		"collection": func(m *store.SeriesMatch) { m.VolumeID = "" },
	} {
		m := ok
		mut(&m)
		if AutoConfirmable(&m) {
			t.Errorf("%s: must not auto-confirm", name)
		}
	}
	if AutoConfirmable(nil) {
		t.Error("nil must not auto-confirm")
	}
}

// Singles get format 'single' so organize uses the template with a year.
func TestMatchSeriesRecordsSingleFormat(t *testing.T) {
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
	iss, _ := st.ListIssues("saga")
	if iss[0].Format != "single" {
		t.Fatalf("format = %q, want single", iss[0].Format)
	}
}

// An annual hash hit records its own match but casts no volume vote, so a
// folder holding only an annual can't auto-confirm onto the annual volume.
func TestMatchSeriesAnnualOnlyDoesNotAutoConfirm(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	page := []byte("saga-annual")
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga Annual 001 (2012).cbz", page)
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{"Saga Annual 001 (2012).cbz": "1"})
	r := NewRunner(st, newCVFixture(t, md5hex(page)), []string{lib}, func() int64 { return 1 })

	if _, _, err := r.MatchSeries(context.Background(), mustGetSeries(t, st, "saga")); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("saga")
	if AutoConfirmable(m) {
		t.Fatalf("annual-only folder auto-confirmable: %+v", m)
	}
	iss, _ := st.ListIssues("saga")
	if iss[0].Format != "annual" {
		t.Fatalf("format = %q, want annual", iss[0].Format)
	}
}

// One annual beside regular issues must not split the vote into ambiguous.
func TestMatchSeriesAnnualDoesNotSplitVolume(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	p1, pa := []byte("saga-1"), []byte("other-annual")
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga 001 (2012).cbz", p1)
	writeCBZ(t, lib+"/Saga (2012)/Saga Annual 001 (2012).cbz", pa)
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{
		"Saga 001 (2012).cbz": "1", "Saga Annual 001 (2012).cbz": "1"})
	// pa hashes to issue 400001, which is in volume 51234, a different volume.
	cv := newCVFixture(t, md5hex(p1), cvFile{md5: md5hex(pa), issueID: 400001})
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1 })

	if _, _, err := r.MatchSeries(context.Background(), mustGetSeries(t, st, "saga")); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch("saga")
	if m.VolumeID != "49019" || m.State == store.MatchStateAmbiguous {
		t.Fatalf("annual split the vote: vol=%s state=%s pen=%s", m.VolumeID, m.State, m.Penalties)
	}
}

func mustSeries(t *testing.T, st *store.Store, id, name, folder string, files map[string]string) {
	t.Helper()
	var issues []store.Issue
	i := 0
	for fn, num := range files {
		issues = append(issues, store.Issue{ID: id + "-" + fn, SeriesID: id, Filename: fn, Number: num, Position: i})
		i++
	}
	if err := st.UpsertSeries(store.Series{ID: id, Name: name, FolderPath: folder, IssueCount: len(files)}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues(id, issues); err != nil {
		t.Fatal(err)
	}
}

func mustGetSeries(t *testing.T, st *store.Store, id string) store.Series {
	t.Helper()
	s, err := st.GetSeries(id)
	if err != nil {
		t.Fatal(err)
	}
	return *s
}

// F1: an annual with no hash hit stays unbound — never bound by number into
// the main volume (spec §2).
func TestFillConfirmedAnnualWithoutHashStaysUnbound(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga Annual 002 (2012).cbz", []byte("no-hash"))
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{"Saga Annual 002 (2012).cbz": "2"})
	confirmSeries(t, st, "saga", store.SourceComicVine, "49019")
	r := NewRunner(st, newCVFixture(t, "unused"), []string{lib}, func() int64 { return 1 })

	res, err := r.FillConfirmed(context.Background(), mustGetSeries(t, st, "saga"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unbound) != 1 || res.Unbound[0] != "Saga Annual 002 (2012).cbz" || len(res.Bound) != 0 {
		t.Fatalf("res = %+v", res)
	}
	if ims, _ := st.ListIssueMatchesForSeries("saga"); len(ims) != 0 {
		t.Fatalf("annual bound by number: %+v", ims)
	}
}

func TestMatchSeriesAnnualWithoutHashStaysUnbound(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	page := []byte("saga-1")
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga 001 (2012).cbz", page)
	writeCBZ(t, lib+"/Saga (2012)/Saga Annual 002 (2012).cbz", []byte("no-hash"))
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{
		"Saga 001 (2012).cbz": "1", "Saga Annual 002 (2012).cbz": "2"})
	r := NewRunner(st, newCVFixture(t, md5hex(page)), []string{lib}, func() int64 { return 1 })

	if _, _, err := r.MatchSeries(context.Background(), mustGetSeries(t, st, "saga")); err != nil {
		t.Fatal(err)
	}
	ims, _ := st.ListIssueMatchesForSeries("saga")
	for _, im := range ims {
		if im.IssueID == "saga-Saga Annual 002 (2012).cbz" {
			t.Fatalf("annual bound by number: %+v", im)
		}
	}
	if m, _ := st.GetSeriesMatch("saga"); !AutoConfirmable(m) {
		t.Fatalf("unbound annual blocked the folder: %+v", m)
	}
}

// F3 (Ruling E): a pull into a confirmed folder records the parsed format on
// every issue, so an old matched issue with a blank format renames alongside
// the new one instead of keeping the year-less default template.
func TestFillConfirmedRecordsFormatOnAllIssues(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga 001 (2012).cbz", []byte("old"))
	writeCBZ(t, lib+"/Saga (2012)/Saga 002 (2012).cbz", []byte("no-hash"))
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{
		"Saga 001 (2012).cbz": "1", "Saga 002 (2012).cbz": "2"})
	confirmSeries(t, st, "saga", store.SourceComicVine, "49019")
	if err := st.UpsertIssueMatch(store.IssueMatch{IssueID: "saga-Saga 001 (2012).cbz", Source: store.SourceComicVine,
		UpstreamID: "317966", State: store.MatchStateHighConfidence, Method: MethodFirstPageMD5}); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, newCVFixture(t, "unused"), []string{lib}, func() int64 { return 1 })

	if _, err := r.FillConfirmed(context.Background(), mustGetSeries(t, st, "saga")); err != nil {
		t.Fatal(err)
	}
	iss, _ := st.ListIssues("saga")
	for _, i := range iss {
		if i.Format != "single" {
			t.Errorf("%s format = %q, want single", i.Filename, i.Format)
		}
	}
}
