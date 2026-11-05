package notify

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/push"
	"github.com/rthaines211/gutter-server/store"
)

func ev(name, kind, detail string) store.PipelineEvent {
	return store.PipelineEvent{SeriesID: name, SeriesName: name, Kind: kind, Detail: detail}
}

func run(evs ...store.PipelineEvent) store.PipelineRun { return store.PipelineRun{Events: evs} }

func TestSummarizeReadyOnly(t *testing.T) {
	n, ok := Summarize(run(ev("Saga", "new_issues", ""), ev("Absolute Batman", "new_issues", ""), ev("Monstress", "new_issues", "")), true)
	want := push.Notification{Title: "New issues ready", Body: "New issues in Saga, Absolute Batman +1", Screen: "read"}
	if !ok || n != want {
		t.Fatalf("got %+v %v", n, ok)
	}
}

func TestSummarizeDedupesSeries(t *testing.T) {
	n, _ := Summarize(run(ev("Saga", "new_issues", ""), ev("Saga", "new_issues", ""), ev("Saga", "new_issues", "")), true)
	if n.Body != "New issues in Saga" {
		t.Fatalf("body = %q", n.Body)
	}
}

func TestSummarizeReviewAndReady(t *testing.T) {
	n, _ := Summarize(run(ev("A", "sent_to_review", ""), ev("B", "sent_to_review", ""), ev("A", "sent_to_review", ""), ev("Saga", "new_issues", "")), true)
	want := push.Notification{Title: "2 series need review", Body: "2 series need review · New issues in Saga", Screen: "review"}
	if n != want {
		t.Fatalf("got %+v", n)
	}
	one, _ := Summarize(run(ev("A", "sent_to_review", "")), true)
	if one.Title != "1 series needs review" {
		t.Fatalf("title = %q", one.Title)
	}
}

func TestSummarizeErrors(t *testing.T) {
	r := run(ev("Saga", "error", "organize failed"), ev("Saga", "new_issues", ""))
	n, _ := Summarize(r, true)
	want := push.Notification{Title: "Pipeline: 1 error", Body: "Saga: organize failed · New issues in Saga", Screen: "read"}
	if n != want {
		t.Fatalf("got %+v", n)
	}
	two, _ := Summarize(run(ev("A", "error", "x"), ev("B", "error", "y")), true)
	if two.Title != "Pipeline: 2 errors" || two.Body != "A: x" || two.Screen != "" {
		t.Fatalf("got %+v", two)
	}
}

func TestSummarizeErrorsExcluded(t *testing.T) {
	n, _ := Summarize(run(ev("Saga", "error", "organize failed"), ev("Saga", "new_issues", "")), false)
	if n.Title != "New issues ready" || n.Body != "New issues in Saga" {
		t.Fatalf("got %+v", n)
	}
	if _, ok := Summarize(run(ev("Saga", "error", "x")), false); ok {
		t.Fatal("error-only run with errors excluded should not send")
	}
}

// Re-fills, writebacks and renames of issues the library already had are
// not news: only "new_issues" (first sighting of a comic id) buzzes.
func TestSummarizeIgnoresRefillsAndRenames(t *testing.T) {
	if n, ok := Summarize(run(ev("Rook", "filled", "10 issue(s)"), ev("X-Men", "organized", "1 renamed/moved")), true); ok {
		t.Fatalf("re-fill/organize run sent %+v", n)
	}
}

func TestSummarizeNothing(t *testing.T) {
	if _, ok := Summarize(run(ev("A", "auto_confirmed", ""), ev("B", "unbound", ""), ev("C", "flagged", "")), true); ok {
		t.Fatal("uninteresting run should not send")
	}
	if _, ok := Summarize(run(), true); ok {
		t.Fatal("empty run should not send")
	}
}

type recorder struct {
	mu   sync.Mutex
	sent []push.Notification
	err  map[string]error
}

func (r *recorder) send(_ context.Context, d push.Device, n push.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, n)
	return r.err[d.Token]
}

func newNotifier(t *testing.T, now *time.Time) (*Notifier, *recorder) {
	t.Helper()
	st, err := store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.UpsertDeviceToken("dev1", "sandbox")
	rec := &recorder{}
	return &Notifier{Store: st, Send: rec.send, DevicesPath: t.TempDir() + "/push_devices.json",
		Now: func() time.Time { return *now }}, rec
}

func TestPipelineRunThrottlesErrors(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	n, rec := newNotifier(t, &now)
	errRun := run(ev("Saga", "error", "boom"))
	n.PipelineRun(errRun)
	n.PipelineRun(errRun)
	if len(rec.sent) != 1 {
		t.Fatalf("sent %d, want 1 within the window", len(rec.sent))
	}
	n.PipelineRun(run(ev("Saga", "error", "boom"), ev("Saga", "new_issues", "")))
	if len(rec.sent) != 2 || rec.sent[1].Title != "New issues ready" {
		t.Fatalf("ready inside window: %+v", rec.sent)
	}
	now = now.Add(6*time.Hour + time.Minute)
	n.PipelineRun(errRun)
	if len(rec.sent) != 3 || rec.sent[2].Title != "Pipeline: 1 error" {
		t.Fatalf("after window: %+v", rec.sent)
	}
}

func TestScanFailedThrottledAndTruncated(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	n, rec := newNotifier(t, &now)
	n.ScanFailed(errors.New(strings.Repeat("é", 300)))
	n.ScanFailed(errors.New("again"))
	if len(rec.sent) != 1 {
		t.Fatalf("sent %d", len(rec.sent))
	}
	got := rec.sent[0]
	if got.Title != "Scan failed" || len([]rune(got.Body)) != 200 || got.Screen != "" {
		t.Fatalf("got title %q body runes %d", got.Title, len([]rune(got.Body)))
	}
}

func TestFanOutDeletesUnregistered(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	n, rec := newNotifier(t, &now)
	n.Store.UpsertDeviceToken("dev2", "production")
	rec.err = map[string]error{"dev1": push.ErrUnregistered, "dev2": errors.New("apns: 400 BadDeviceToken")}
	n.PipelineRun(run(ev("Saga", "new_issues", "")))
	want := []push.Device{{Token: "dev2", Env: "production"}}
	got, _ := n.Store.ListDeviceTokens()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("store = %+v", got)
	}
	mirror, _ := push.ReadDevices(n.DevicesPath)
	if !reflect.DeepEqual(mirror, want) {
		t.Fatalf("mirror = %+v", mirror)
	}
}

func TestNilNotifierNoop(t *testing.T) {
	var n *Notifier
	n.PipelineRun(run(ev("Saga", "new_issues", "")))
	n.ScanFailed(errors.New("x"))
	if err := n.SyncDevices(); err != nil {
		t.Fatal(err)
	}
}
