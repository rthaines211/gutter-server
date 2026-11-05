package server

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryanhaines/comic-reader/server/admin"
)

// The server-wide WriteTimeout (5m in prod) must not kill an SSE stream: a
// command sent after it elapses still has to reach the reader. Wrapped in the
// request-log middleware exactly as prod is, since the recorder sits between
// the handler and the connection.
func TestRemoteStreamOutlivesServerWriteTimeout(t *testing.T) {
	srv := &Server{}
	ts := httptest.NewUnstartedServer(admin.NewRequestLog(10).Middleware(srv.NewRouter()))
	ts.Config.WriteTimeout = 300 * time.Millisecond
	ts.Start()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/remote/events?role=reader")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for srv.hub().readerCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(600 * time.Millisecond) // well past WriteTimeout

	go func() {
		http.Post(ts.URL+"/remote/command", "application/json", strings.NewReader(`{"type":"next"}`))
	}()

	done := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				done <- sc.Text()
				return
			}
		}
	}()
	select {
	case line := <-done:
		if !strings.Contains(line, `"next"`) {
			t.Fatalf("streamed %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream died at WriteTimeout: command never arrived")
	}
}

// failingWriter accepts headers, then errors on every body write — a dead
// connection the request context hasn't noticed yet.
type failingWriter struct {
	h      http.Header
	writes int
}

func (f *failingWriter) Header() http.Header { return f.h }
func (f *failingWriter) WriteHeader(int)     {}
func (f *failingWriter) Flush()              {}
func (f *failingWriter) Write([]byte) (int, error) {
	f.writes++
	return 0, http.ErrHandlerTimeout
}

// A stream whose writes fail must unsubscribe, so commands get 409 instead of
// a 204 into a dead channel.
func TestRemoteStreamExitsOnWriteError(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/remote/events?role=reader", nil)
	exited := make(chan struct{})
	go func() {
		srv.HandleRemoteEvents(&failingWriter{h: http.Header{}}, req)
		close(exited)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for srv.hub().readerCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	srv.hub().sendCommand([]byte(`{"type":"next"}`))

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("handler kept a dead stream subscribed")
	}
	if n := srv.hub().readerCount(); n != 0 {
		t.Fatalf("readerCount = %d after dead stream, want 0", n)
	}
}
