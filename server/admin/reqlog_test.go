package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestLogAddAndRecent(t *testing.T) {
	rl := NewRequestLog(5)
	if got := rl.Recent(10); len(got) != 0 {
		t.Fatalf("empty log returned %d entries", len(got))
	}
	for i := 0; i < 3; i++ {
		rl.Add(LogEntry{
			Timestamp:  time.Now(),
			Method:     "GET",
			Path:       "/test",
			StatusCode: 200,
			Duration:   time.Millisecond * time.Duration(i+1),
		})
	}
	got := rl.Recent(10)
	if len(got) != 3 {
		t.Fatalf("Recent(10) returned %d entries, want 3", len(got))
	}
	if got[0].Duration < got[1].Duration {
		t.Error("Recent should return entries in reverse chronological order")
	}
}

func TestRequestLogOverflow(t *testing.T) {
	rl := NewRequestLog(3)
	for i := 0; i < 5; i++ {
		rl.Add(LogEntry{
			Timestamp:  time.Now(),
			Method:     "GET",
			Path:       "/test",
			StatusCode: 200 + i,
		})
	}
	got := rl.Recent(10)
	if len(got) != 3 {
		t.Fatalf("overflow log returned %d entries, want 3", len(got))
	}
	if got[0].StatusCode != 204 {
		t.Errorf("most recent status = %d, want 204", got[0].StatusCode)
	}
	if got[2].StatusCode != 202 {
		t.Errorf("oldest status = %d, want 202", got[2].StatusCode)
	}
}

func TestRequestLogRecentLimit(t *testing.T) {
	rl := NewRequestLog(10)
	for i := 0; i < 8; i++ {
		rl.Add(LogEntry{StatusCode: 200 + i})
	}
	got := rl.Recent(3)
	if len(got) != 3 {
		t.Fatalf("Recent(3) returned %d entries, want 3", len(got))
	}
}

// flushableRecorder is an httptest.ResponseRecorder that reports flushes.
type flushableRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushableRecorder) Flush() { f.flushed = true }

func TestResponseRecorderForwardsFlush(t *testing.T) {
	inner := &flushableRecorder{ResponseRecorder: httptest.NewRecorder()}
	rec := &responseRecorder{ResponseWriter: inner, status: 200}

	flusher, ok := interface{}(rec).(http.Flusher)
	if !ok {
		t.Fatal("responseRecorder must implement http.Flusher or SSE silently buffers in production")
	}
	flusher.Flush()
	if !inner.flushed {
		t.Fatal("Flush was not forwarded to the underlying ResponseWriter")
	}
}
