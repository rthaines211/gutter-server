package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/cache"
	"github.com/rthaines211/gutter-server/config"
	"github.com/rthaines211/gutter-server/match"
	"github.com/rthaines211/gutter-server/store"
)

// Every console screen must actually execute its template. html/template
// resolves field references at render time, not compile time, so a renamed
// field or a typo'd pipeline only ever surfaces as a 500 in the browser —
// which is exactly what this catches. One request per GET screen.
func TestAllScreensRender(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// One series carrying a match, so the queue/table paths render rows
	// rather than only their empty states.
	if err := st.UpsertSeries(store.Series{
		ID: "saga", Name: "Saga (2012)", FolderPath: t.TempDir(), IssueCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID: "saga", Source: store.SourceComicVine, VolumeID: "49240",
		State: store.MatchStateHighConfidence, Method: "first_page_md5", UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	a, err := New(st, cache.NewLRU(16), []string{t.TempDir()}, "", "", 8081, NewRequestLog(64), NewScanState())
	if err != nil {
		t.Fatal(err)
	}
	// main.go always sets these after New; the config screen dereferences
	// a.Config, so mirror production rather than leaving it nil.
	a.Config = &config.Config{Port: 8081, ComicDirs: []string{t.TempDir()}}

	screens := []struct{ name, path string }{
		{"overview", "/_admin/"},
		{"library", "/_admin/library"},
		{"libraryTable", "/_admin/library?view=table"},
		{"libraryFiltered", "/_admin/library?view=grid&q=saga&status=new"},
		{"series", "/_admin/library/saga"},
		{"seriesList", "/_admin/library/saga?issues=list"},
		{"match", "/_admin/match"},
		{"matchAmbiguous", "/_admin/match?state=ambiguous"},
		{"review", "/_admin/match/review"},
		{"matchDetail", "/_admin/match/series/saga"},
		{"organize", "/_admin/organize"},
		{"activity", "/_admin/activity"},
		{"scanner", "/_admin/scanner"},
		{"logs", "/_admin/logs"},
		{"endpoints", "/_admin/endpoints"},
		{"config", "/_admin/config"},
	}

	mux := newTestMux(a)
	for _, s := range screens {
		t.Run(s.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", s.path, nil))

			if rec.Code != 200 {
				t.Fatalf("GET %s = %d, want 200\n%s", s.path, rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			// A template that fails mid-execution still returns 200 with a
			// truncated body, so assert the shell actually closed out.
			if !strings.Contains(body, "</html>") {
				t.Errorf("GET %s: body not fully rendered (no </html>)", s.path)
			}
			// The redesign's shell markers — brand mark and stylesheet.
			if !strings.Contains(body, "GUTTER") {
				t.Errorf("GET %s: missing Gutter wordmark", s.path)
			}
			// html/template rewrites a value it can't prove safe in a CSS
			// context to ZgotmplZ, which silently renders the element
			// unstyled — colors vanish rather than erroring. Any helper
			// feeding a style attribute must return template.CSS.
			if strings.Contains(body, "ZgotmplZ") {
				t.Errorf("GET %s: ZgotmplZ in output — a style value is not template.CSS", s.path)
			}
		})
	}
}

func newTestMux(a *Admin) *http.ServeMux {
	mux := http.NewServeMux()
	a.Register(mux)
	return mux
}

// Every grid class a template uses on its own must carry display:grid.
// .cover-grid-8 originally only set grid-template-columns and relied on
// .cover-grid being present alongside it; used standalone in series.html that
// silently dropped the grid and blew each cover up to full width (1093px).
func TestStandaloneGridClassesDeclareDisplayGrid(t *testing.T) {
	cssBytes, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)

	tmpls, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}

	for _, class := range []string{"cover-grid", "cover-grid-8"} {
		usedAlone := false
		for _, f := range tmpls {
			b, err := templateFS.ReadFile("templates/" + f.Name())
			if err != nil {
				t.Fatal(err)
			}
			// class="x" exactly — not "cover-grid other-class".
			if strings.Contains(string(b), `class="`+class+`"`) {
				usedAlone = true
			}
		}
		if !usedAlone {
			continue
		}
		// Find the rule that sets display:grid and check this class is one of
		// its selectors. Both grids are declared in a single shared block.
		i := strings.Index(css, "display: grid")
		found := false
		for i >= 0 {
			// Walk back to the start of this rule's selector list.
			open := strings.LastIndex(css[:i], "{")
			prev := strings.LastIndexAny(css[:open], "}*/")
			sel := css[prev+1 : open]
			for _, part := range strings.Split(sel, ",") {
				if strings.TrimSpace(part) == "."+class {
					found = true
				}
			}
			next := strings.Index(css[i+1:], "display: grid")
			if next < 0 || found {
				break
			}
			i = i + 1 + next
		}
		if !found {
			t.Errorf(".%s is used standalone in a template but no display:grid rule lists it", class)
		}
	}
}

// With matching off the overview must not show match widgets; with a runner
// it must.
func TestOverviewMatchWidgets(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := New(st, cache.NewLRU(16), []string{t.TempDir()}, "", "", 8081, NewRequestLog(64), NewScanState())
	if err != nil {
		t.Fatal(err)
	}
	a.Config = &config.Config{Port: 8081, ComicDirs: []string{t.TempDir()}}
	get := func() string {
		rec := httptest.NewRecorder()
		newTestMux(a).ServeHTTP(rec, httptest.NewRequest("GET", "/_admin/", nil))
		if rec.Code != 200 {
			t.Fatalf("overview = %d", rec.Code)
		}
		return rec.Body.String()
	}
	body := get()
	for _, s := range []string{"Open queue", "SERIES MATCHED", "Match queue"} {
		if strings.Contains(body, s) {
			t.Errorf("matching off: overview contains %q", s)
		}
	}
	a.Match = &match.Runner{}
	body = get()
	for _, s := range []string{"Open queue", "SERIES MATCHED"} {
		if !strings.Contains(body, s) {
			t.Errorf("matching on: overview missing %q", s)
		}
	}
}

// CI stamps Version without a "v" (metadata-action's "0.1.0"); the templates
// add it, except for dev builds.
func TestVersionPrefix(t *testing.T) {
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := New(st, cache.NewLRU(16), []string{t.TempDir()}, "", "", 8081, NewRequestLog(64), NewScanState())
	if err != nil {
		t.Fatal(err)
	}
	a.Config = &config.Config{Port: 8081, ComicDirs: []string{t.TempDir()}}
	defer func(v string) { Version = v }(Version)
	for v, want := range map[string]string{"0.1.0": "v0.1.0", "dev": "8081 · dev"} {
		Version = v
		rec := httptest.NewRecorder()
		newTestMux(a).ServeHTTP(rec, httptest.NewRequest("GET", "/_admin/config", nil))
		body := rec.Body.String()
		if !strings.Contains(body, want) || strings.Contains(body, "vv") || strings.Contains(body, "vdev") {
			t.Errorf("Version %q: want %q, no vv/vdev", v, want)
		}
	}
}
