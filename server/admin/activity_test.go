package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/store"
)

func TestActivityPageShowsRunEvents(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.InsertPipelineRun(store.PipelineRun{StartedAt: 1, FinishedAt: 2, SeriesCount: 1,
		Events: []store.PipelineEvent{{SeriesID: "saga", SeriesName: "Saga (2012)", Kind: "auto_confirmed", Detail: "volume 49019"}}}); err != nil {
		t.Fatal(err)
	}
	a := newReviewTestAdmin(t, st)
	rec := httptest.NewRecorder()
	newTestMux(a).ServeHTTP(rec, httptest.NewRequest("GET", "/_admin/activity", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Saga (2012)", "auto_confirmed", "volume 49019", "/_admin/match/series/saga"} {
		if !strings.Contains(body, want) {
			t.Errorf("activity page missing %q", want)
		}
	}
}
