package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteCommandNoReaderIs409(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remote/command",
		strings.NewReader(`{"type":"next"}`))
	srv.HandleRemoteCommand(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] == "" {
		t.Fatal("409 must carry a JSON error body")
	}
}

func TestRemoteCommandRejectsUnknownType(t *testing.T) {
	srv := &Server{}
	srv.hub().subscribe("reader") // a reader exists, so 409 is not the reason
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remote/command",
		strings.NewReader(`{"type":"selfdestruct"}`))
	srv.HandleRemoteCommand(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown command type, got %d", rec.Code)
	}
}

func TestRemoteCommandWithReaderIs204(t *testing.T) {
	srv := &Server{}
	ch, _ := srv.hub().subscribe("reader")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remote/command",
		strings.NewReader(`{"type":"goto","page":11}`))
	srv.HandleRemoteCommand(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case msg := <-ch:
		if !strings.Contains(string(msg), `"page":11`) {
			t.Fatalf("reader got %q", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("reader never received the command")
	}
}

func TestRemoteStateIsStoredAndReplayed(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remote/state",
		strings.NewReader(`{"comicId":"abc","page":4,"totalPages":20,"readingMode":"page"}`))
	srv.HandleRemoteState(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if !strings.Contains(string(srv.hub().snapshot()), `"comicId":"abc"`) {
		t.Fatalf("snapshot not stored: %q", srv.hub().snapshot())
	}
}

// A pretty-printed body must not reach the SSE writer with newlines in it:
// `data: %s\n\n` would split one snapshot into several broken frames.
func TestRemoteStateIsCompactedBeforeStorage(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remote/state",
		strings.NewReader("{\n  \"comicId\": \"abc\",\n  \"page\": 4\n}"))
	srv.HandleRemoteState(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	snap := string(srv.hub().snapshot())
	if strings.ContainsAny(snap, "\n\r") {
		t.Fatalf("snapshot still contains newlines: %q", snap)
	}
	if snap != `{"comicId":"abc","page":4}` {
		t.Fatalf("snapshot = %q", snap)
	}
}

func TestRemoteEventsRejectsBadRole(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	srv.HandleRemoteEvents(rec, httptest.NewRequest("GET", "/remote/events?role=wizard", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a bad role, got %d", rec.Code)
	}
}

// Drives the real router so the SSE framing, headers and flushing are all
// exercised end to end over a live connection.
func TestRemoteEventsStreamsCommands(t *testing.T) {
	srv := &Server{}
	ts := httptest.NewServer(srv.NewRouter())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/remote/events?role=reader")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	// Wait for the subscription to land before sending.
	deadline := time.Now().Add(2 * time.Second)
	for srv.hub().readerCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	go func() {
		http.Post(ts.URL+"/remote/command", "application/json",
			strings.NewReader(`{"type":"next"}`))
	}()

	sc := bufio.NewScanner(resp.Body)
	done := make(chan string, 1)
	go func() {
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				done <- strings.TrimPrefix(sc.Text(), "data: ")
				return
			}
		}
	}()
	select {
	case line := <-done:
		if !strings.Contains(line, `"next"`) {
			t.Fatalf("streamed %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no SSE data frame arrived")
	}
}

func TestRemoteCommandRejectsNegativePage(t *testing.T) {
	srv := &Server{}
	srv.hub().subscribe("reader")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/remote/command",
		strings.NewReader(`{"type":"goto","page":-5}`))
	srv.HandleRemoteCommand(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a negative page, got %d", rec.Code)
	}
}

// The phone decodes every frame as a JSON object. A non-object body stored
// here is replayed verbatim and throws in its fromJson, which kills the
// phone's snapshot stream permanently. Reject at the door.
func TestRemoteStateRejectsNonObjectBodies(t *testing.T) {
	for _, body := range []string{`"x"`, `[1,2]`, `42`, `null`, `true`} {
		srv := &Server{}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/remote/state", strings.NewReader(body))
		srv.HandleRemoteState(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: want 400, got %d", body, rec.Code)
		}
		if srv.hub().snapshot() != nil {
			t.Fatalf("body %s was stored despite the 400", body)
		}
	}
}
