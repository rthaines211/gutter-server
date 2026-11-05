package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStepSequence(t *testing.T) {
	var s State
	var titles []string
	for _, healthy := range []bool{true, false, false, false, true, true} {
		next, msg := Step(s, healthy, "boom")
		s = next
		if msg == nil {
			titles = append(titles, "")
		} else {
			titles = append(titles, msg.Title)
		}
	}
	want := []string{"", "", "comic-reader is down", "", "comic-reader is back", ""}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", titles)
	}
}

func TestStepSingleBlipSilent(t *testing.T) {
	s, msg := Step(State{}, false, "x")
	if msg != nil {
		t.Fatal("alert after one failure")
	}
	if _, msg = Step(s, true, ""); msg != nil {
		t.Fatal("'back' without a prior 'down'")
	}
}

func TestStepDownBodyTruncated(t *testing.T) {
	s, _ := Step(State{}, false, "")
	_, msg := Step(s, false, strings.Repeat("x", 500))
	if msg == nil || len([]rune(msg.Body)) != 200 {
		t.Fatalf("msg = %+v", msg)
	}
}

func TestCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	healthy, detail := Check(ctx, srv.URL)
	if healthy || detail == "" {
		t.Fatalf("healthy=%v detail=%q", healthy, detail)
	}
}

func TestCheck503Detail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("media root unreadable: /media/Comics: stale"))
	}))
	defer srv.Close()
	healthy, detail := Check(context.Background(), srv.URL)
	if healthy || !strings.Contains(detail, "media root unreadable") {
		t.Fatalf("healthy=%v detail=%q", healthy, detail)
	}
}

func TestCheckOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	if healthy, _ := Check(context.Background(), srv.URL); !healthy {
		t.Fatal("200 reported unhealthy")
	}
}
