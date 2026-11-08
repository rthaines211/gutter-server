package server

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/cache"
	"github.com/rthaines211/gutter-server/kosync"
	"github.com/rthaines211/gutter-server/store"
)

const opdsTestPassword = "s3cret"

type opdsFixture struct {
	srv     *Server
	handler http.Handler
	dir     string
	cbzID   string
	cbrID   string
	cbzPath string
}

// newOPDSFixture builds a library with one series: a real 3-page CBZ and a
// placeholder CBR, behind the production router (auth on).
func newOPDSFixture(t *testing.T) *opdsFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Saga"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/test.cbz")
	if err != nil {
		t.Fatal(err)
	}
	cbzPath := filepath.Join(dir, "Saga", "Saga 001.cbz")
	if err := os.WriteFile(cbzPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Saga", "Saga 002.cbr"), []byte("Rar!placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := newIntegrationTestStore(t)
	cbzID := store.PathToID("Saga/Saga 001.cbz")
	cbrID := store.PathToID("Saga/Saga 002.cbr")
	created := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, sr := range []store.Series{
		{ID: "saga", Name: "Saga", FolderPath: filepath.Join(dir, "Saga"), IssueCount: 2, Writer: "Brian K. Vaughan", Publisher: "Image", Year: 2012, CreatedAt: created},
		{ID: "zebra", Name: "Zebra Tales", FolderPath: filepath.Join(dir, "Zebra"), IssueCount: 0, CreatedAt: created.Add(48 * time.Hour)},
	} {
		if err := st.UpsertSeries(sr); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceIssues("saga", []store.Issue{
		{ID: cbzID, SeriesID: "saga", Filename: "Saga 001.cbz", Number: "1", Position: 0},
		{ID: cbrID, SeriesID: "saga", Filename: "Saga 002.cbr", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{ComicDirs: []string{dir}, Store: st, Cache: cache.NewLRU(100), Password: opdsTestPassword}
	return &opdsFixture{srv: srv, handler: srv.NewRouter(), dir: dir, cbzID: cbzID, cbrID: cbrID, cbzPath: cbzPath}
}

func (f *opdsFixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.SetBasicAuth("koreader", opdsTestPassword)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// atomFeed is just enough of a feed to assert on structure.
type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID    string     `xml:"id"`
	Title string     `xml:"title"`
	Links []atomLink `xml:"link"`
}

type atomLink struct {
	Rel      string `xml:"rel,attr"`
	Href     string `xml:"href,attr"`
	Type     string `xml:"type,attr"`
	Count    string `xml:"http://vaemendis.net/opds-pse/ns count,attr"`
	LastRead string `xml:"http://vaemendis.net/opds-pse/ns lastRead,attr"`
}

func (e atomEntry) link(rel string) *atomLink {
	for i := range e.Links {
		if e.Links[i].Rel == rel {
			return &e.Links[i]
		}
	}
	return nil
}

func parseFeed(t *testing.T, body string) atomFeed {
	t.Helper()
	var f atomFeed
	if err := xml.Unmarshal([]byte(body), &f); err != nil {
		t.Fatalf("parse feed: %v\n%s", err, body)
	}
	return f
}

func TestOPDSFeeds(t *testing.T) {
	fx := newOPDSFixture(t)
	// Gutter progress on the CBZ: page index 1 of 3.
	if err := fx.srv.Store.UpsertProgress(store.Progress{ComicID: fx.cbzID, CurrentPage: 1, TotalPages: 3}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		path        string
		wantID      string
		wantEntries []string // entry titles, in order
		check       func(t *testing.T, f atomFeed)
	}{
		{
			name:        "root",
			path:        "/opds/",
			wantID:      "urn:gutter:root",
			wantEntries: []string{"Continue reading", "Recently added", "All series"},
			check: func(t *testing.T, f atomFeed) {
				var search bool
				for _, l := range f.Links {
					search = search || (l.Rel == "search" && l.Href == "/opds/opensearch.xml")
				}
				if !search {
					t.Error("root has no search link")
				}
			},
		},
		{name: "root without slash", path: "/opds", wantID: "urn:gutter:root"},
		{
			name:        "series list",
			path:        "/opds/series",
			wantID:      "urn:gutter:series",
			wantEntries: []string{"Saga", "Zebra Tales"},
			check: func(t *testing.T, f atomFeed) {
				l := f.Entries[0].link("subsection")
				if l == nil || l.Href != "/opds/series/saga" || !strings.Contains(l.Type, "kind=acquisition") {
					t.Errorf("series subsection link = %+v", l)
				}
				if th := f.Entries[0].link("http://opds-spec.org/image/thumbnail"); th == nil || th.Href != "/library/saga/cover" {
					t.Errorf("series thumbnail = %+v", th)
				}
			},
		},
		{name: "series list page 2 empty", path: "/opds/series?page=2", wantID: "urn:gutter:series", wantEntries: nil},
		{name: "recent newest first", path: "/opds/recent", wantID: "urn:gutter:recent", wantEntries: []string{"Zebra Tales", "Saga"}},
		{name: "search by name", path: "/opds/search?q=saga", wantEntries: []string{"Saga"}},
		{name: "search by writer", path: "/opds/search?q=vaughan", wantEntries: []string{"Saga"}},
		{name: "search multi-word", path: "/opds/search?q=zebra+tales", wantEntries: []string{"Zebra Tales"}},
		{name: "search no match", path: "/opds/search?q=nothing", wantEntries: nil},
		{name: "search empty", path: "/opds/search?q=", wantEntries: nil},
		{
			name:        "series issues",
			path:        "/opds/series/saga",
			wantID:      "urn:gutter:series:saga",
			wantEntries: []string{"Saga #1", "Saga #2"},
			check: func(t *testing.T, f atomFeed) {
				cbz, cbr := f.Entries[0], f.Entries[1]
				acq := cbz.link("http://opds-spec.org/acquisition")
				if acq == nil || acq.Href != "/opds/download/"+fx.cbzID || acq.Type != "application/vnd.comicbook+zip" {
					t.Errorf("cbz acquisition = %+v", acq)
				}
				pse := cbz.link("http://vaemendis.net/opds-pse/stream")
				if pse == nil {
					t.Fatal("cbz has no PSE stream link")
				}
				if pse.Href != "/comics/"+fx.cbzID+"/pages/{pageNumber}" || pse.Count != "3" || pse.LastRead != "1" {
					t.Errorf("pse link = %+v", pse)
				}
				if img := cbz.link("http://opds-spec.org/image"); img == nil || img.Href != "/comics/"+fx.cbzID+"/cover" {
					t.Errorf("cover link = %+v", img)
				}
				if a := cbr.link("http://opds-spec.org/acquisition"); a == nil || a.Type != "application/vnd.comicbook-rar" {
					t.Errorf("cbr acquisition = %+v", a)
				}
				if cbr.link("http://vaemendis.net/opds-pse/stream") != nil {
					t.Error("unopened CBR should not get a PSE link (page count unknown)")
				}
			},
		},
		{
			name:        "reading",
			path:        "/opds/reading",
			wantID:      "urn:gutter:reading",
			wantEntries: []string{"Saga #1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := fx.get(t, tc.path)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/atom+xml") {
				t.Errorf("content-type %q", ct)
			}
			f := parseFeed(t, rec.Body.String())
			if tc.wantID != "" && f.ID != tc.wantID {
				t.Errorf("id = %q, want %q", f.ID, tc.wantID)
			}
			if tc.wantEntries != nil || strings.Contains(tc.name, "empty") || strings.Contains(tc.name, "no match") {
				var got []string
				for _, e := range f.Entries {
					got = append(got, e.Title)
				}
				if strings.Join(got, "|") != strings.Join(tc.wantEntries, "|") {
					t.Errorf("entries = %q, want %q", got, tc.wantEntries)
				}
			}
			if tc.check != nil {
				tc.check(t, f)
			}
		})
	}
}

func TestOPDSErrorsAndAuth(t *testing.T) {
	fx := newOPDSFixture(t)
	tests := []struct {
		name  string
		path  string
		basic bool
		want  int
	}{
		{"unknown series", "/opds/series/nope", true, 404},
		{"no credentials", "/opds/", false, 401},
		{"download missing", "/opds/download/" + store.PathToID("Saga/missing.cbz"), true, 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			if tc.basic {
				req.SetBasicAuth("any-user", opdsTestPassword)
			}
			rec := httptest.NewRecorder()
			fx.handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d", rec.Code, tc.want)
			}
			if tc.want == 401 && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without a Basic challenge; OPDS clients need it to prompt")
			}
		})
	}
}

func TestOPDSOpenSearch(t *testing.T) {
	fx := newOPDSFixture(t)
	rec := fx.get(t, "/opds/opensearch.xml")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `template="/opds/search?q={searchTerms}"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestOPDSPSEPageStreams(t *testing.T) {
	fx := newOPDSFixture(t)
	// The PSE template resolves to the existing page endpoint, 0-based.
	for _, n := range []string{"0", "2"} {
		rec := fx.get(t, "/comics/"+fx.cbzID+"/pages/"+n)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
			t.Errorf("page %s: status %d type %q", n, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if rec := fx.get(t, "/comics/"+fx.cbzID+"/pages/3"); rec.Code != 404 {
		t.Errorf("page past end: status %d", rec.Code)
	}
}

func TestOPDSDownloadRecordsKosyncDigest(t *testing.T) {
	fx := newOPDSFixture(t)
	rec := fx.get(t, "/opds/download/"+fx.cbzID)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	want, _ := os.ReadFile(fx.cbzPath)
	if string(body) != string(want) {
		t.Fatal("download is not the raw archive")
	}
	f, _ := os.Open(fx.cbzPath)
	defer f.Close()
	digest, _ := kosync.PartialMD5(f, int64(len(want)))
	ids, err := fx.srv.Store.ComicIDsForKosyncDigest(digest)
	if err != nil || len(ids) != 1 || ids[0] != fx.cbzID {
		t.Errorf("digest %s → %v, %v", digest, ids, err)
	}
}
