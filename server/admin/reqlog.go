package admin

import (
	"log"
	"net/http"
	"sync"
	"time"
)

type LogEntry struct {
	Timestamp  time.Time
	Method     string
	Path       string
	StatusCode int
	Duration   time.Duration
	Size       int64
}

type RequestLog struct {
	mu      sync.Mutex
	entries []LogEntry
	pos     int
	full    bool
	cap     int
}

func NewRequestLog(capacity int) *RequestLog {
	return &RequestLog{
		entries: make([]LogEntry, capacity),
		cap:     capacity,
	}
}

func (rl *RequestLog) Add(entry LogEntry) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.entries[rl.pos] = entry
	rl.pos = (rl.pos + 1) % rl.cap
	if rl.pos == 0 {
		rl.full = true
	}
}

func (rl *RequestLog) Recent(n int) []LogEntry {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	count := rl.pos
	if rl.full {
		count = rl.cap
	}
	if n > count {
		n = count
	}
	if n == 0 {
		return nil
	}
	result := make([]LogEntry, n)
	for i := 0; i < n; i++ {
		idx := (rl.pos - 1 - i + rl.cap) % rl.cap
		result[i] = rl.entries[idx]
	}
	return result
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	size   int64
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.size += int64(n)
	return n, err
}

// Flush forwards to the underlying writer so streaming responses (SSE) are
// not swallowed by this middleware. Embedding the ResponseWriter interface
// promotes only Header/Write/WriteHeader, so without this the recorder is
// not an http.Flusher and SSE buffers forever behind the request log.
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection through the
// recorder (SSE clears its write deadline that way).
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (rl *RequestLog) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		dur := time.Since(start)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, dur.Round(time.Millisecond))
		rl.Add(LogEntry{
			Timestamp:  start,
			Method:     r.Method,
			Path:       r.URL.Path,
			StatusCode: rec.status,
			Duration:   dur,
			Size:       rec.size,
		})
	})
}
