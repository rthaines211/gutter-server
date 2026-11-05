package admin

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

// clampCursor is where the "negative or garbage i" requirement lives — the
// review handler and the skip handler both go through it.
func TestClampCursor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"0", 0},
		{"5", 5},
		{"-1", 0},
		{"-100", 0},
		{"", 0},
		{"not-a-number", 0},
		{"3.5", 0},
	}
	for _, c := range cases {
		if got := clampCursor(c.in); got != c.want {
			t.Errorf("clampCursor(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// newReviewTestAdmin builds an Admin with parsed templates — required by any
// test that calls a.render (handleMatchReview always does, even for the
// queue-clear state). Handlers that only redirect (confirm, dont-match, skip)
// don't need this and can use a bare &Admin{Store: st}.
func newReviewTestAdmin(t *testing.T, st *store.Store) *Admin {
	t.Helper()
	a, err := New(st, nil, nil, "", "", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func seedReviewSeries(t *testing.T, st *store.Store, id, name, volumeID string) {
	t.Helper()
	if err := st.UpsertSeries(store.Series{
		ID: id, Name: name, FolderPath: t.TempDir(), IssueCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: id, Source: store.SourceComicVine, VolumeID: volumeID,
		State: store.MatchStateHighConfidence, Method: "first_page_md5", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

// An out-of-range cursor — including on an empty queue — must render the
// "Queue clear" state, not error. This is the normal way a review session
// ends: confirm/don't-match shrink the queue until the cursor runs past it.
func TestHandleMatchReviewOutOfRangeIsQueueClear(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedReviewSeries(t, st, "series-a", "Series A", "111")

	a := newReviewTestAdmin(t, st)

	// Cursor past the single item in the queue.
	req := httptest.NewRequest("GET", "/_admin/match/review?state=high_confidence&i=5", nil)
	w := httptest.NewRecorder()
	a.handleMatchReview(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Queue clear") {
		t.Fatalf("expected queue-clear state in body, got: %s", w.Body.String())
	}

	// An entirely empty queue at i=0 must behave the same way, not error.
	req2 := httptest.NewRequest("GET", "/_admin/match/review?state=no_match&i=0", nil)
	w2 := httptest.NewRecorder()
	a.handleMatchReview(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("expected 200 for empty queue, got %d: %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "Queue clear") {
		t.Fatalf("expected queue-clear state for empty queue, got: %s", w2.Body.String())
	}
}

// A negative or garbage i must clamp to 0 and render the first series, not error.
func TestHandleMatchReviewClampsBadCursor(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedReviewSeries(t, st, "series-a", "Series A", "111")

	a := newReviewTestAdmin(t, st)
	for _, i := range []string{"-3", "garbage"} {
		req := httptest.NewRequest("GET", "/_admin/match/review?state=high_confidence&i="+i, nil)
		w := httptest.NewRecorder()
		a.handleMatchReview(w, req)
		if w.Code != 200 {
			t.Fatalf("i=%q: expected 200, got %d: %s", i, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "Series A") {
			t.Fatalf("i=%q: expected first series rendered, got: %s", i, w.Body.String())
		}
	}
}

// Skip leaves the queue untouched, so it must redirect to i+1.
func TestHandleMatchReviewSkipIncrementsCursor(t *testing.T) {
	a := &Admin{}
	form := url.Values{"state": {"ambiguous"}, "i": {"2"}}
	req := httptest.NewRequest("POST", "/_admin/match/review/skip", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	a.handleMatchReviewSkip(w, req)

	if w.Code != 303 {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	want := "/_admin/match/review?state=ambiguous&i=3"
	if got := w.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// Confirming at cursor i removes the series from the queue, so the redirect
// must send the browser back to the SAME i — the next unreviewed series
// slides into that slot. It must not increment.
func TestHandleMatchConfirmFromReviewKeepsSameCursor(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedReviewSeries(t, st, "series-a", "Series A", "111")

	a := &Admin{Store: st}
	form := url.Values{
		"series_id": {"series-a"},
		"redirect":  {"/_admin/match/review?state=high_confidence&i=0"},
	}
	req := httptest.NewRequest("POST", "/_admin/match/confirm", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	a.handleMatchConfirm(w, req)

	if w.Code != 303 {
		t.Fatalf("expected 303, got %d: %s", w.Code, w.Body.String())
	}
	want := "/_admin/match/review?state=high_confidence&i=0"
	if got := w.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q (same cursor)", got, want)
	}

	m, err := st.GetSeriesMatch("series-a")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.MatchStateConfirmed {
		t.Fatalf("expected confirmed, got %s", m.State)
	}
}

// Don't match also removes the series from the queue and must likewise
// redirect to the same cursor.
func TestHandleMatchDontMatchFromReviewKeepsSameCursor(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedReviewSeries(t, st, "series-a", "Series A", "")

	a := &Admin{Store: st}
	form := url.Values{
		"series_id": {"series-a"},
		"redirect":  {"/_admin/match/review?state=no_match&i=4"},
	}
	req := httptest.NewRequest("POST", "/_admin/match/dont-match", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	a.handleMatchDontMatch(w, req)

	if w.Code != 303 {
		t.Fatalf("expected 303, got %d: %s", w.Code, w.Body.String())
	}
	want := "/_admin/match/review?state=no_match&i=4"
	if got := w.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q (same cursor)", got, want)
	}
}

// End-to-end: confirming the series at cursor 0 shrinks the queue, so
// re-requesting the SAME cursor must show the next series that slid into
// that slot — not error, not the confirmed series again.
func TestHandleMatchReviewAutoAdvanceAfterConfirm(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedReviewSeries(t, st, "series-a", "Series A", "111")
	seedReviewSeries(t, st, "series-b", "Series B", "222")

	a := newReviewTestAdmin(t, st)

	// Sanity: i=0 shows series-a (alphabetically first).
	req := httptest.NewRequest("GET", "/_admin/match/review?state=high_confidence&i=0", nil)
	w := httptest.NewRecorder()
	a.handleMatchReview(w, req)
	if !strings.Contains(w.Body.String(), "Series A") {
		t.Fatalf("expected Series A at i=0 before confirm, got: %s", w.Body.String())
	}

	if err := a.confirm("series-a"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// Same cursor, series-a is gone, series-b has slid into slot 0.
	req2 := httptest.NewRequest("GET", "/_admin/match/review?state=high_confidence&i=0", nil)
	w2 := httptest.NewRecorder()
	a.handleMatchReview(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "Series B") {
		t.Fatalf("expected Series B to have slid into i=0, got: %s", w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "Series A") {
		t.Fatalf("confirmed series must not still appear in the queue: %s", w2.Body.String())
	}
}

func TestReviewCountMatchesQueue(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedReviewSeries(t, st, "a", "A", "1")
	seedReviewSeries(t, st, "b", "B", "2")
	seedReviewSeries(t, st, "c", "C", "3")
	for id, state := range map[string]string{"b": store.MatchStateAmbiguous, "c": store.MatchStateCollection} {
		if err := st.UpsertSeriesMatch(store.SeriesMatch{
			SeriesID: id, Source: store.SourceComicVine, State: state, UpdatedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetDontMatch("b", true); err != nil {
		t.Fatal(err)
	}
	if got := newReviewTestAdmin(t, st).reviewCount(); got != 2 {
		t.Fatalf("reviewCount = %d, want 2", got)
	}
}

func TestHandleMatchReviewSkipsDontMatch(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedReviewSeries(t, st, "a", "A", "1")
	if err := st.SetDontMatch("a", true); err != nil {
		t.Fatal(err)
	}
	a := newReviewTestAdmin(t, st)
	req := httptest.NewRequest("GET", "/_admin/match/review?state=high_confidence&i=0", nil)
	w := httptest.NewRecorder()
	a.handleMatchReview(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Queue clear") {
		t.Fatalf("expected queue-clear, got %d: %s", w.Code, w.Body.String())
	}
}
