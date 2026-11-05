package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ryanhaines/comic-reader/push"
)

// State persists between launchd ticks.
type State struct {
	Fails   int  `json:"fails"`
	Alerted bool `json:"alerted"`
}

// Step advances the watcher: two failures in a row send one "down", and the
// first success after that sends one "back". A single blip stays silent.
func Step(s State, healthy bool, detail string) (State, *push.Notification) {
	if healthy {
		if s.Alerted {
			return State{}, &push.Notification{Title: "comic-reader is back"}
		}
		return State{}, nil
	}
	s.Fails++
	if s.Fails >= 2 && !s.Alerted {
		s.Alerted = true
		body := []rune(detail)
		if len(body) > 200 {
			body = body[:200]
		}
		return s, &push.Notification{Title: "comic-reader is down", Body: string(body)}
	}
	return s, nil
}

// Check GETs /healthz. Anything but a 200 inside ctx's deadline is unhealthy;
// a hung stale mount shows up as a timeout.
func Check(ctx context.Context, url string) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true, ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return false, strings.TrimSpace(fmt.Sprintf("%d %s", resp.StatusCode, b))
}
