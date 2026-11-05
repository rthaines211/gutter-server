package server

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// HandleHealthz reports liveness including media-mount health. A stale bind
// mount (macOS Docker + external drive) leaves HTTP and the DB fully working
// while every file read fails — the status-endpoint healthcheck never caught
// that, so autoheal never restarted the container.
func (s *Server) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	for _, dir := range s.ComicDirs {
		if _, err := os.ReadDir(dir); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "media root unreadable: %s: %v", dir, err)
			return
		}
	}
	if s.Store != nil {
		var one int
		if err := s.Store.DB().QueryRow("SELECT 1").Scan(&one); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "db unavailable: %v", err)
			return
		}
	}
	// Status lines are informational: a stale scan or a failed writeback
	// must not flip the status, or the watcher would page "server down".
	fmt.Fprintln(w, "ok")
	if s.Admin != nil && s.Admin.ScanState != nil {
		last := "none since start"
		if t := s.Admin.ScanState.Snapshot().LastScanAt; !t.IsZero() {
			last = t.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "last_scan: %s\n", last)
	}
	if s.Store != nil {
		if counts, err := s.Store.CountWritebackByStatus(); err == nil {
			fmt.Fprintf(w, "writeback_errors: %d\n", counts["error"])
		}
	}
}
