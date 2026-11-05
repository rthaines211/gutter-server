package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// remoteKeepalive is the SSE comment interval. Without it, Wi-Fi power save
// and NAT idle timeouts add 50-100ms to the first packet after a pause, and
// idle connections get reaped outright.
const remoteKeepalive = 15 * time.Second

var remoteCommandTypes = map[string]bool{
	"next": true, "prev": true, "goto": true,
	"bookmark": true, "nextIssue": true,
}

type remoteCommand struct {
	Type string `json:"type"`
	Page int    `json:"page"`
}

// HandleRemoteCommand accepts a command from the phone and fans it to the
// iPad. Commands are never queued: a page turn that lands 30s late is worse
// than one that never lands, so with no reader connected this is a 409.
func (s *Server) HandleRemoteCommand(w http.ResponseWriter, r *http.Request) {
	var cmd remoteCommand
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid command json")
		return
	}
	if !remoteCommandTypes[cmd.Type] {
		writeJSONError(w, http.StatusBadRequest, "unknown command type")
		return
	}
	// Only the lower bound is checkable here — the relay does not know page
	// counts. The upper clamp stays in the reader's _jumpToPage, which does.
	if cmd.Page < 0 {
		writeJSONError(w, http.StatusBadRequest, "page must not be negative")
		return
	}
	msg, err := json.Marshal(cmd)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !s.hub().sendCommand(msg) {
		writeJSONError(w, http.StatusConflict, "no reader connected")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoteState stores the iPad's latest snapshot and pushes it to any
// connected phones. Fire-and-forget: a lost snapshot is corrected by the
// next page turn, so there are no acks and no sequence numbers.
func (s *Server) HandleRemoteState(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid state json")
		return
	}
	// The snapshot is interpolated straight into an SSE `data:` line, so a
	// newline anywhere in it (a pretty-printing client) would split the frame.
	// Commands are re-marshalled through a struct and are safe; this is not.
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid state json")
		return
	}
	// Must be an object. The phone decodes every frame as one, so a stored
	// `"x"` or `[1,2]` is replayed and throws in its fromJson — which kills
	// the phone's snapshot stream until the app restarts.
	if b := compact.Bytes(); len(b) == 0 || b[0] != '{' {
		writeJSONError(w, http.StatusBadRequest, "state must be a json object")
		return
	}
	s.hub().publishState(compact.Bytes())
	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoteEvents is the SSE stream. role=reader receives commands,
// role=remote receives state snapshots.
func (s *Server) HandleRemoteEvents(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeInternalError(w, fmt.Errorf("response writer is not a flusher"))
		return
	}
	ch, ok := s.hub().subscribe(role)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "role must be reader or remote")
		return
	}
	defer s.hub().unsubscribe(role, ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Defeats proxy buffering, which would hold frames until the buffer fills.
	w.Header().Set("X-Accel-Buffering", "no")
	// The server-wide WriteTimeout (sized for page downloads) would otherwise
	// cut this long-lived stream off after a few minutes. Ignored error: only
	// writers that can't reach a real connection (tests) fail here.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// send writes one SSE frame; an error means the connection is dead, and
	// the caller returns so the deferred unsubscribe stops routing commands
	// into a channel nobody reads.
	send := func(frame string) error {
		if _, err := fmt.Fprint(w, frame); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	// Replay the last snapshot so a phone's scrubber is correct immediately
	// rather than blank until the next page turn.
	if role == "remote" {
		if snap := s.hub().snapshot(); snap != nil {
			if send("data: "+string(snap)+"\n\n") != nil {
				return
			}
		}
	}

	ticker := time.NewTicker(remoteKeepalive)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			err = send("data: " + string(msg) + "\n\n")
		case <-ticker.C:
			err = send(": ping\n\n")
		}
		if err != nil {
			return
		}
	}
}
