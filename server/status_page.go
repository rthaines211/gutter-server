package server

import (
	_ "embed"
	"html/template"
	"net/http"

	"github.com/rthaines211/gutter-server/server/admin"
)

//go:embed status_page.html
var statusPageHTML string

var statusPageTmpl = template.Must(template.New("status").Parse(statusPageHTML))

// HandleStatusPage serves GET /: version, scan progress, and the URL to
// enter in Gutter. Behind the global auth middleware like everything else.
func (s *Server) HandleStatusPage(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	var snap admin.ScanSnapshot
	match := false
	if s.Admin != nil {
		if s.Admin.ScanState != nil {
			snap = s.Admin.ScanState.Snapshot()
		}
		match = s.Admin.Match != nil
	}
	count := 0
	if s.Store != nil {
		count, _ = s.Store.SeriesCount()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	statusPageTmpl.Execute(w, map[string]any{
		"Version":  admin.Version,
		"Scanning": snap.IsScanning,
		"Scanned":  snap.Scanned,
		"Total":    snap.Total,
		"Count":    count,
		"Match":    match,
		"URL":      scheme + "://" + r.Host,
	})
}
