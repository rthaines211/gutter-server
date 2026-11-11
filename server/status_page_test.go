package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/server/admin"
)

func statusBody(t *testing.T, srv *Server, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.HandleStatusPage(rec, httptest.NewRequest("GET", target, nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("want 200 text/html, got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	return rec.Body.String()
}

func TestStatusPageScanning(t *testing.T) {
	srv := newTestServer(t)
	ss := admin.NewScanState()
	ss.StartScan()
	ss.SetTotal(583)
	for range 120 {
		ss.IncrScanned()
	}
	srv.Admin = &admin.Admin{ScanState: ss}
	body := statusBody(t, srv, "/")
	for _, want := range []string{"Scanning", "120", "583"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestStatusPageReady(t *testing.T) {
	srv := newTestServer(t)
	srv.Admin = &admin.Admin{ScanState: admin.NewScanState()}
	for _, id := range []string{"a", "b", "c"} {
		srv.Store.DB().Exec(`INSERT INTO series(id,name,folder_path) VALUES(?,?,?)`, id, id, id)
	}
	body := statusBody(t, srv, "/")
	if !strings.Contains(body, "Ready") || !strings.Contains(body, "3 series") {
		t.Errorf("want Ready and 3 series, got %s", body)
	}
}

func TestStatusPageProviders(t *testing.T) {
	srv := newTestServer(t)
	srv.Admin = &admin.Admin{ScanState: admin.NewScanState()}
	if body := statusBody(t, srv, "/"); !strings.Contains(body, "Matching: off") {
		t.Error("want Matching: off")
	}
}

func TestStatusPageServerURL(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "http://h.example:8081/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	srv.HandleStatusPage(rec, req)
	if !strings.Contains(rec.Body.String(), "https://h.example:8081") {
		t.Errorf("want https url, got %s", rec.Body.String())
	}
}

func TestStatusPageEmptyLibraryExplainsLayout(t *testing.T) {
	srv := newTestServer(t)
	srv.Admin = &admin.Admin{ScanState: admin.NewScanState(), ComicDirs: []string{"/comics", "/<b>x"}}
	body := statusBody(t, srv, "/")
	if !strings.Contains(body, "No series found in /comics, /&lt;b&gt;x.") ||
		!strings.Contains(body, "one folder per series with CBZ/CBR files inside") {
		t.Errorf("want empty-library hint with escaped dirs, got %s", body)
	}
	if strings.Contains(body, "Ready") {
		t.Error("empty library must not say Ready")
	}
}

func TestStatusPageScanningBeforeTotal(t *testing.T) {
	srv := newTestServer(t)
	ss := admin.NewScanState()
	ss.StartScan()
	srv.Admin = &admin.Admin{ScanState: ss}
	body := statusBody(t, srv, "/")
	if !strings.Contains(body, `<p id="scan">Scanning…</p>`) || strings.Contains(body, "0 of 0") {
		t.Errorf("want bare Scanning… while total is 0, got %s", body)
	}
	if !strings.Contains(body, "s.total ?") {
		t.Error("poller must also skip counts while total is 0")
	}
}

func TestStatusPageGetTheApp(t *testing.T) {
	body := statusBody(t, newTestServer(t), "/")
	for _, want := range []string{
		"Get the app",
		`href="https://apps.apple.com/us/app/gutter-comic-reader/id6831270255"`,
		`href="https://getgutter.app"`,
		"<svg ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// Self-contained: nothing on the page loads from the network.
	for _, bad := range []string{"<img", "<link", "src=", "url(", "@import"} {
		if strings.Contains(body, bad) {
			t.Errorf("body must not contain %q", bad)
		}
	}
}
