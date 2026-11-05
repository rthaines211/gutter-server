package server

import (
	"testing"
	"time"
)

func recv(t *testing.T, ch chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a message")
		return nil
	}
}

func TestSendCommandReachesReader(t *testing.T) {
	h := newRemoteHub()
	ch, ok := h.subscribe("reader")
	if !ok {
		t.Fatal("subscribe(reader) rejected")
	}
	if !h.sendCommand([]byte(`{"type":"next"}`)) {
		t.Fatal("sendCommand reported no reader")
	}
	if got := string(recv(t, ch)); got != `{"type":"next"}` {
		t.Fatalf("reader got %q", got)
	}
}

func TestSendCommandWithNoReader(t *testing.T) {
	h := newRemoteHub()
	h.subscribe("remote") // a phone alone is not a reader
	if h.sendCommand([]byte(`{"type":"next"}`)) {
		t.Fatal("sendCommand should report false with no reader connected")
	}
}

func TestLateRemoteGetsReplayedSnapshot(t *testing.T) {
	h := newRemoteHub()
	h.publishState([]byte(`{"page":7}`))
	if got := string(h.snapshot()); got != `{"page":7}` {
		t.Fatalf("snapshot = %q", got)
	}
}

func TestPublishStateReachesRemotesNotReaders(t *testing.T) {
	h := newRemoteHub()
	reader, _ := h.subscribe("reader")
	remote, _ := h.subscribe("remote")
	h.publishState([]byte(`{"page":3}`))
	if got := string(recv(t, remote)); got != `{"page":3}` {
		t.Fatalf("remote got %q", got)
	}
	select {
	case b := <-reader:
		t.Fatalf("reader should not receive state, got %q", b)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUnsubscribeRemovesChannel(t *testing.T) {
	h := newRemoteHub()
	ch, _ := h.subscribe("reader")
	h.unsubscribe("reader", ch)
	if h.sendCommand([]byte(`{"type":"next"}`)) {
		t.Fatal("unsubscribed reader still counted")
	}
}

// The iPad's SSE stream ending is the only signal that the reader closed, so
// the hub must go back to "no reader" — 204-forever is the bug this pins.
func TestLastReaderLeavingRestoresNoReaderAndClearsState(t *testing.T) {
	h := newRemoteHub()
	a, _ := h.subscribe("reader")
	b, _ := h.subscribe("reader")
	h.publishState([]byte(`{"page":7}`))

	h.unsubscribe("reader", a)
	if !h.sendCommand([]byte(`{"type":"next"}`)) {
		t.Fatal("one reader still connected should accept commands")
	}
	if h.snapshot() == nil {
		t.Fatal("snapshot cleared while a reader is still connected")
	}

	h.unsubscribe("reader", b)
	if h.sendCommand([]byte(`{"type":"next"}`)) {
		t.Fatal("sendCommand should report false once the last reader left")
	}
	if got := h.snapshot(); got != nil {
		t.Fatalf("snapshot should be cleared, got %q", got)
	}
	if n := h.readerCount(); n != 0 {
		t.Fatalf("readerCount = %d, want 0", n)
	}
}

// A phone leaving must not wipe the iPad's state.
func TestRemoteLeavingKeepsState(t *testing.T) {
	h := newRemoteHub()
	h.subscribe("reader")
	ch, _ := h.subscribe("remote")
	h.publishState([]byte(`{"page":7}`))
	h.unsubscribe("remote", ch)
	if h.snapshot() == nil {
		t.Fatal("a phone disconnecting cleared the iPad's snapshot")
	}
}

func TestSubscribeRejectsUnknownRole(t *testing.T) {
	h := newRemoteHub()
	if _, ok := h.subscribe("wizard"); ok {
		t.Fatal("unknown role accepted")
	}
}

func TestSlowSubscriberDoesNotBlockHub(t *testing.T) {
	h := newRemoteHub()
	slow, _ := h.subscribe("reader")
	for i := 0; i < remoteChanBuffer*3; i++ {
		done := make(chan bool, 1)
		go func() { h.sendCommand([]byte(`{"type":"next"}`)); done <- true }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("sendCommand blocked on a full subscriber channel")
		}
	}
	_ = slow
}

// A phone already streaming when the iPad closes the reader must be told.
// Clearing h.state only fixes the *next* connection; without a frame, a
// connected phone renders a frozen page count forever.
func TestLastReaderLeavingNotifiesConnectedRemotes(t *testing.T) {
	h := newRemoteHub()
	a, _ := h.subscribe("reader")
	b, _ := h.subscribe("reader")
	remote, _ := h.subscribe("remote")
	h.publishState([]byte(`{"page":7}`))
	if got := string(recv(t, remote)); got != `{"page":7}` {
		t.Fatalf("remote got %q", got)
	}

	// One reader left: still reading, so say nothing.
	h.unsubscribe("reader", a)
	select {
	case msg := <-remote:
		t.Fatalf("notified while a reader is still connected: %q", msg)
	case <-time.After(50 * time.Millisecond):
	}

	h.unsubscribe("reader", b)
	if got := string(recv(t, remote)); got != readerGoneFrame {
		t.Fatalf("remote got %q, want %q", got, readerGoneFrame)
	}
}

// A phone leaving is not a reader leaving; no frame, and no frame to the iPad.
func TestReaderGoneIsNotSentToReaders(t *testing.T) {
	h := newRemoteHub()
	reader, _ := h.subscribe("reader")
	other, _ := h.subscribe("reader")
	h.unsubscribe("reader", other)
	h.unsubscribe("reader", reader)
	select {
	case msg := <-reader:
		t.Fatalf("a reader received the reader_gone frame: %q", msg)
	case <-time.After(50 * time.Millisecond):
	}
}
