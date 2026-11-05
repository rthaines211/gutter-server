package enrichment

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/comicvine"
	"github.com/rthaines211/gutter-server/store"
)

type fakeVine struct {
	searchResults map[string][]comicvine.Volume
	volumes       map[int]*comicvine.VolumeDetail
	issues        map[int][]comicvine.Issue
	searchErr     error
}

func (f *fakeVine) SearchVolumes(name string) ([]comicvine.Volume, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.searchResults[name], nil
}
func (f *fakeVine) GetVolume(id int) (*comicvine.VolumeDetail, error) {
	v, ok := f.volumes[id]
	if !ok {
		return nil, errors.New("no such volume")
	}
	return v, nil
}
func (f *fakeVine) ListVolumeIssues(id int) ([]comicvine.Issue, error) { return f.issues[id], nil }
func (f *fakeVine) Available() int                                     { return 999 }

func newWorkerTest(t *testing.T) (*store.Store, *fakeVine, *Worker) {
	t.Helper()
	st, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake := &fakeVine{
		searchResults: map[string][]comicvine.Volume{},
		volumes:       map[int]*comicvine.VolumeDetail{},
		issues:        map[int][]comicvine.Issue{},
	}
	return st, fake, NewWorker(st, fake)
}

func seed(t *testing.T, st *store.Store, id, name string, year, count int) {
	t.Helper()
	if err := st.UpsertSeries(store.Series{ID: id, Name: name, FolderPath: "/" + id, IssueCount: count, Year: year}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues(id, []store.Issue{
		{ID: id + "-001", SeriesID: id, Filename: name + " 001.cbz", Number: "001", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueMissingEnrichment(); err != nil {
		t.Fatal(err)
	}
}

func sagaVolume() comicvine.Volume {
	return comicvine.Volume{ID: 49019, Name: "Saga", StartYear: 2012, Publisher: "Image", CountOfIssues: 66, Description: "Epic."}
}

func TestProcessOneAutoEnriches(t *testing.T) {
	st, fake, w := newWorkerTest(t)
	seed(t, st, "saga", "Saga", 2012, 1)
	fake.searchResults["Saga"] = []comicvine.Volume{sagaVolume()}
	fake.volumes[49019] = &comicvine.VolumeDetail{
		Volume:   sagaVolume(),
		Creators: []comicvine.Creator{{Name: "Brian K. Vaughan", Role: "writer"}},
	}
	fake.issues[49019] = []comicvine.Issue{
		{ID: 1000, Number: "1", Title: "Chapter One", Summary: "It begins.", CoverDate: "2012-03-14"},
	}

	did, err := w.processOne()
	if err != nil || !did {
		t.Fatalf("processOne = %v, %v", did, err)
	}

	state, err := st.GetEnrichmentState("saga")
	if err != nil || state.Status != "enriched" {
		t.Fatalf("expected enriched, got %+v err %v", state, err)
	}
	e, err := st.GetSeriesEnrichment("saga")
	if err != nil {
		t.Fatal(err)
	}
	if e.VolumeID != 49019 || e.Status != "auto" || e.Publisher != "Image" {
		t.Fatalf("unexpected enrichment %+v", e)
	}
	var creators []store.Creator
	if err := json.Unmarshal([]byte(e.Creators), &creators); err != nil || len(creators) != 1 {
		t.Fatalf("bad creators JSON %q", e.Creators)
	}
	// Issue "001" matched ComicVine "1" via number normalization.
	im, err := st.ListIssueEnrichmentForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if im["saga-001"].Title != "Chapter One" {
		t.Fatalf("expected issue enrichment, got %+v", im)
	}
}

func TestWorkerStoresCreatorID(t *testing.T) {
	st, fake, w := newWorkerTest(t)
	seed(t, st, "saga", "Saga", 2012, 1)
	fake.searchResults["Saga"] = []comicvine.Volume{sagaVolume()}
	fake.volumes[49019] = &comicvine.VolumeDetail{
		Volume:   sagaVolume(),
		Creators: []comicvine.Creator{{ID: 7, Name: "A", Role: "writer"}},
	}
	fake.issues[49019] = []comicvine.Issue{}

	if _, err := w.processOne(); err != nil {
		t.Fatal(err)
	}
	e, err := st.GetSeriesEnrichment("saga")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Creators, `"id":7`) {
		t.Fatalf("expected creators JSON to contain id:7, got %q", e.Creators)
	}
}

func TestProcessOneAmbiguousStoresCandidates(t *testing.T) {
	st, fake, w := newWorkerTest(t)
	seed(t, st, "batman", "Batman", 2011, 1)
	fake.searchResults["Batman"] = []comicvine.Volume{
		{ID: 1, Name: "Batman", StartYear: 2011, CountOfIssues: 57},
		{ID: 2, Name: "Batman", StartYear: 2011, CountOfIssues: 60},
	}

	if _, err := w.processOne(); err != nil {
		t.Fatal(err)
	}
	state, err := st.GetEnrichmentState("batman")
	if err != nil || state.Status != "pending_review" {
		t.Fatalf("expected pending_review, got %+v err %v", state, err)
	}
	var cands []Candidate
	if err := json.Unmarshal([]byte(state.Candidates), &cands); err != nil || len(cands) != 2 {
		t.Fatalf("bad candidates %q", state.Candidates)
	}
	if cands[0].VolumeID == 0 || cands[0].Name != "Batman" {
		t.Fatalf("unexpected candidate %+v", cands[0])
	}
}

func TestProcessOneNoResultsUnmatched(t *testing.T) {
	st, fake, w := newWorkerTest(t)
	seed(t, st, "zzz", "Zzyzx Quarterly", 0, 1)
	fake.searchResults["Zzyzx Quarterly"] = nil

	if _, err := w.processOne(); err != nil {
		t.Fatal(err)
	}
	state, _ := st.GetEnrichmentState("zzz")
	if state.Status != "unmatched" {
		t.Fatalf("expected unmatched, got %+v", state)
	}
}

func TestProcessOneSearchErrorLeavesQueuedWithAttempt(t *testing.T) {
	st, fake, w := newWorkerTest(t)
	seed(t, st, "saga", "Saga", 2012, 1)
	fake.searchErr = errors.New("boom")

	if _, err := w.processOne(); err == nil {
		t.Fatal("expected error to propagate")
	}
	state, _ := st.GetEnrichmentState("saga")
	if state.Status != "queued" || state.Attempts != 1 {
		t.Fatalf("expected queued attempts=1, got %+v", state)
	}
}

func TestProcessOneAppliesChosenVolume(t *testing.T) {
	st, fake, w := newWorkerTest(t)
	seed(t, st, "batman", "Batman", 2011, 1)
	fake.volumes[2] = &comicvine.VolumeDetail{
		Volume: comicvine.Volume{ID: 2, Name: "Batman", StartYear: 2011, Publisher: "DC Comics", CountOfIssues: 60},
	}
	if err := st.SetEnrichmentChosen("batman", 2); err != nil {
		t.Fatal(err)
	}

	if _, err := w.processOne(); err != nil {
		t.Fatal(err)
	}
	e, err := st.GetSeriesEnrichment("batman")
	if err != nil {
		t.Fatal(err)
	}
	if e.VolumeID != 2 || e.Status != "confirmed" {
		t.Fatalf("expected confirmed volume 2, got %+v", e)
	}
}

func TestProcessOneIdleEnqueuesNewSeries(t *testing.T) {
	st, fake, w := newWorkerTest(t)
	_ = fake
	// Series exists but was never enqueued (simulates post-scan growth).
	if err := st.UpsertSeries(store.Series{ID: "new", Name: "New Series", FolderPath: "/new", IssueCount: 1}); err != nil {
		t.Fatal(err)
	}
	did, err := w.processOne()
	if err != nil || !did {
		t.Fatalf("expected idle pass to enqueue, got %v %v", did, err)
	}
	state, err := st.GetEnrichmentState("new")
	if err != nil || state.Status != "queued" {
		t.Fatalf("expected queued, got %+v err %v", state, err)
	}
}

func TestSnippetRuneSafe(t *testing.T) {
	// Build a 300-rune string from multi-byte runes ('é' = 2 bytes each).
	s := string([]rune(func() []rune {
		r := make([]rune, 300)
		for i := range r {
			r[i] = 'é'
		}
		return r
	}()))
	got := snippet(s, 200)
	runes := []rune(got)
	// Should be 200 runes + the ellipsis character (1 rune).
	if len(runes) != 201 {
		t.Fatalf("expected 201 runes (200 + ellipsis), got %d", len(runes))
	}
	for i, r := range runes[:200] {
		if r == '�' {
			t.Fatalf("replacement char at rune position %d", i)
		}
	}
}

func TestStopTwiceDoesNotPanic(t *testing.T) {
	_, _, w := newWorkerTest(t)
	w.Stop()
	w.Stop() // must not panic
}

// TestApplySkipsManualEnrichment verifies I2: when a series_enrichment row with
// status='manual' exists (set by UpdateSeriesMetadata), the worker's apply path
// must not overwrite it. The enrichment_state must be set to 'skipped_manual'
// so the series does not loop back into the queue.
func TestApplySkipsManualEnrichment(t *testing.T) {
	st, fake, w := newWorkerTest(t)
	seed(t, st, "manual-series", "Manual Series", 2020, 1)

	// Insert a manual enrichment row (as UpdateSeriesMetadata would create).
	if _, err := st.DB().Exec(`
		INSERT INTO series_enrichment(series_id, comicvine_volume_id, description, publisher, start_year, creators, status, enriched_at)
		VALUES('manual-series', 0, 'user desc', 'User Publisher', 2020, '[]', 'manual', 0)
	`); err != nil {
		t.Fatal(err)
	}

	// Provide a matching ComicVine result so the worker would normally auto-enrich.
	vol := comicvine.Volume{ID: 9999, Name: "Manual Series", StartYear: 2020, Publisher: "CV Publisher", CountOfIssues: 1}
	fake.searchResults["Manual Series"] = []comicvine.Volume{vol}
	fake.volumes[9999] = &comicvine.VolumeDetail{
		Volume:   vol,
		Creators: []comicvine.Creator{{Name: "CV Author", Role: "writer"}},
	}
	fake.issues[9999] = []comicvine.Issue{}

	// Run the worker once.
	worked, err := w.processOne()
	if err != nil {
		t.Fatalf("processOne error: %v", err)
	}
	if !worked {
		t.Fatal("expected processOne to return true (item was processed)")
	}

	// Manual enrichment row must be untouched.
	var desc, publisher, status string
	st.DB().QueryRow(`SELECT description, publisher, status FROM series_enrichment WHERE series_id='manual-series'`).
		Scan(&desc, &publisher, &status)
	if desc != "user desc" {
		t.Errorf("description overwritten: got %q, want 'user desc'", desc)
	}
	if publisher != "User Publisher" {
		t.Errorf("publisher overwritten: got %q, want 'User Publisher'", publisher)
	}
	if status != "manual" {
		t.Errorf("enrichment status changed: got %q, want 'manual'", status)
	}

	// enrichment_state must be 'skipped_manual' so the worker stops retrying.
	var stateStatus string
	st.DB().QueryRow(`SELECT status FROM enrichment_state WHERE series_id='manual-series'`).Scan(&stateStatus)
	if stateStatus != "skipped_manual" {
		t.Errorf("enrichment_state status: got %q, want 'skipped_manual'", stateStatus)
	}
}

// A series confirmed from GCD has no ComicVine volume. The worker must not
// go looking for one live — it would write an 'auto' row over the confirmed
// GCD enrichment. searchErr proves no search happened.
func TestProcessOneSkipsConfirmedNonComicVineSeries(t *testing.T) {
	st, fake, w := newWorkerTest(t)
	seed(t, st, "jurgens", "Action Comics by Dan Jurgens Omnibus", 2025, 1)
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "jurgens", Source: store.SourceGCD, VolumeID: "226636",
		State: store.MatchStateConfirmed, ConfirmedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	fake.searchErr = errors.New("must not search ComicVine for a GCD series")

	if _, err := w.processOne(); err != nil {
		t.Fatal(err)
	}
	e, err := st.GetEnrichmentState("jurgens")
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "skipped" {
		t.Fatalf("expected skipped, got %+v", e)
	}
}
