package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ryanhaines/comic-reader/comicvine"
	"github.com/ryanhaines/comic-reader/store"
)

func confirmSeries(t *testing.T, st *store.Store, id, source, vol string) {
	t.Helper()
	if err := st.UpsertSeriesMatch(store.SeriesMatch{SeriesID: id, Source: source, VolumeID: vol,
		State: store.MatchStateHighConfidence, Method: MethodFirstPageMD5}); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmSeriesMatch(id, 1); err != nil {
		t.Fatal(err)
	}
}

// A new issue in a confirmed CV folder binds from the offline dump by number,
// with no live client configured at all.
func TestFillConfirmedBindsOfflineByNumber(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga 002 (2012).cbz", []byte("no-hash"))
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{"Saga 002 (2012).cbz": "2"})
	confirmSeries(t, st, "saga", store.SourceComicVine, "49019")
	r := NewRunner(st, newCVFixture(t, "unused"), []string{lib}, func() int64 { return 1 })

	res, err := r.FillConfirmed(context.Background(), mustGetSeries(t, st, "saga"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Bound) != 1 || len(res.Unbound) != 0 {
		t.Fatalf("res = %+v", res)
	}
	ims, _ := st.ListIssueMatchesForSeries("saga")
	if len(ims) != 1 || ims[0].UpstreamID != "317967" {
		t.Fatalf("matches = %+v", ims)
	}
}

// Already-bound issues are not re-reported: a second fill over the same
// folder binds nothing, so the pipeline neither re-applies nor re-enqueues
// writeback nor emits "filled".
func TestFillConfirmedSecondCallBindsNothing(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga 002 (2012).cbz", []byte("no-hash"))
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{"Saga 002 (2012).cbz": "2"})
	confirmSeries(t, st, "saga", store.SourceComicVine, "49019")
	r := NewRunner(st, newCVFixture(t, "unused"), []string{lib}, func() int64 { return 1 })

	if res, err := r.FillConfirmed(context.Background(), mustGetSeries(t, st, "saga")); err != nil || len(res.Bound) != 1 {
		t.Fatalf("first fill = %+v, %v", res, err)
	}
	res, err := r.FillConfirmed(context.Background(), mustGetSeries(t, st, "saga"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Bound) != 0 || len(res.Unbound) != 0 {
		t.Fatalf("second fill = %+v, want nothing", res)
	}
}

// A hash hit in another (non-annual) volume is reported, not bound.
func TestFillConfirmedReportsMisfiled(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	page := []byte("elsewhere")
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga 003 (2012).cbz", page)
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{"Saga 003 (2012).cbz": "3"})
	confirmSeries(t, st, "saga", store.SourceComicVine, "49019")
	cv := newCVFixture(t, "unused", cvFile{md5: md5hex(page), issueID: 400001}) // vol 51234
	r := NewRunner(st, cv, []string{lib}, func() int64 { return 1 })

	res, err := r.FillConfirmed(context.Background(), mustGetSeries(t, st, "saga"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Misfiled) != 1 || res.Misfiled[0].VolumeID != "51234" || len(res.Bound) != 0 {
		t.Fatalf("res = %+v", res)
	}
	if ims, _ := st.ListIssueMatchesForSeries("saga"); len(ims) != 0 {
		t.Fatalf("misfiled issue was bound: %+v", ims)
	}
}

// A GCD-confirmed series never calls live ComicVine, even with live configured.
func TestFillConfirmedGCDNeverCallsLiveCV(t *testing.T) {
	st, _ := store.NewStore(t.TempDir() + "/app.db")
	defer st.Close()
	lib := t.TempDir()
	writeCBZ(t, lib+"/Saga (2012)/Saga 009 (2012).cbz", []byte("x"))
	mustSeries(t, st, "saga", "Saga (2012)", lib+"/Saga (2012)", map[string]string{"Saga 009 (2012).cbz": "9"})
	confirmSeries(t, st, "saga", store.SourceGCD, "777")
	r := NewRunner(st, newCVFixture(t, "unused"), []string{lib}, func() int64 { return 1 })
	r.SetLive(failingLiveCV(t)) // any request fails the test

	if _, err := r.FillConfirmed(context.Background(), mustGetSeries(t, st, "saga")); err != nil {
		t.Fatal(err)
	}
}

// failingLiveCV is a live ComicVine client whose every request fails the test.
func failingLiveCV(t *testing.T) *comicvine.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("live ComicVine called for a GCD series: %s", r.URL)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := comicvine.New("test-key", t.TempDir())
	c.BaseURL = srv.URL
	return c
}
