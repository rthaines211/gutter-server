package server

import "sync"

// remoteChanBuffer is how many messages a subscriber may fall behind before
// its messages are dropped. Dropping beats blocking: a stalled phone must
// never wedge the iPad's command stream.
const remoteChanBuffer = 8

// readerGoneFrame tells connected phones the iPad stopped reading. Clearing
// the stored snapshot only covers a phone that connects *afterwards*; one
// already streaming gets no bytes at all and renders a frozen page count
// until app restart. Typed rather than a bare `null` payload, because the
// phone decodes every frame as a JSON object and a null would throw inside
// the .map() feeding its StreamProvider — killing that stream for good.
const readerGoneFrame = `{"type":"reader_gone"}`

// remoteHub fans commands out to connected readers (iPads) and state
// snapshots out to connected remotes (phones). Entirely in memory; the
// session is simply whoever currently holds a subscription.
type remoteHub struct {
	mu      sync.Mutex
	readers map[chan []byte]struct{}
	remotes map[chan []byte]struct{}
	state   []byte
}

func newRemoteHub() *remoteHub {
	return &remoteHub{
		readers: map[chan []byte]struct{}{},
		remotes: map[chan []byte]struct{}{},
	}
}

func (h *remoteHub) set(role string) map[chan []byte]struct{} {
	switch role {
	case "reader":
		return h.readers
	case "remote":
		return h.remotes
	}
	return nil
}

func (h *remoteHub) subscribe(role string) (chan []byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.set(role)
	if m == nil {
		return nil, false
	}
	ch := make(chan []byte, remoteChanBuffer)
	m[ch] = struct{}{}
	return ch, true
}

func (h *remoteHub) unsubscribe(role string, ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.set(role)
	if m == nil {
		return
	}
	delete(m, ch)
	// The session is whoever holds a reader stream, so with the last reader
	// gone the stored snapshot describes nobody. Drop it, or a phone that
	// connects afterwards replays a frozen page count from an iPad that has
	// closed the reader. A brief flap to the empty state during a Wi-Fi blip
	// is honest; a stale scrubber is not.
	if role == "reader" && len(m) == 0 {
		h.state = nil
		fanout(h.remotes, []byte(readerGoneFrame))
	}
}

// fanout sends to every channel without blocking; a subscriber that is
// behind by more than remoteChanBuffer simply misses the message.
func fanout(m map[chan []byte]struct{}, msg []byte) {
	for ch := range m {
		select {
		case ch <- msg:
		default:
		}
	}
}

// sendCommand delivers to every connected reader. Reports false when no
// reader is connected so the handler can answer 409 instead of pretending.
func (h *remoteHub) sendCommand(msg []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.readers) == 0 {
		return false
	}
	fanout(h.readers, msg)
	return true
}

func (h *remoteHub) publishState(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = msg
	fanout(h.remotes, msg)
}

// snapshot returns the last published state, replayed to a phone on connect
// so its scrubber is correct immediately rather than blank until the next
// page turn.
func (h *remoteHub) snapshot() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

func (h *remoteHub) readerCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.readers)
}
