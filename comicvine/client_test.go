package comicvine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const searchFixture = `{
	"error": "OK", "status_code": 1, "number_of_total_results": 2,
	"results": [
		{"id": 49019, "name": "Saga", "start_year": "2012",
		 "publisher": {"name": "Image"}, "count_of_issues": 66,
		 "image": {"medium_url": "https://cv.example/saga.jpg"},
		 "deck": "A space opera.", "description": "<p>Epic &amp; sweeping.</p>"},
		{"id": 99, "name": "Saga of the Swamp Thing", "start_year": "",
		 "publisher": null, "count_of_issues": 46, "image": null,
		 "deck": null, "description": null}
	]
}`

const volumeFixture = `{
	"error": "OK", "status_code": 1, "number_of_total_results": 1,
	"results": {
		"id": 49019, "name": "Saga", "start_year": "2012",
		"publisher": {"name": "Image"}, "count_of_issues": 66,
		"image": {"medium_url": "https://cv.example/saga.jpg"},
		"description": "<p>Epic.</p>",
		"person_credits": [
			{"name": "Brian K. Vaughan", "role": "writer"},
			{"name": "Fiona Staples", "role": "artist, cover"}
		]
	}
}`

func issuesFixture(offset, total int) string {
	return fmt.Sprintf(`{
		"error": "OK", "status_code": 1, "number_of_total_results": %d,
		"results": [
			{"id": %d, "issue_number": "%d", "name": "Chapter %d",
			 "description": "<p>Stuff happens.</p>", "cover_date": "2012-03-14"}
		]
	}`, total, 1000+offset, offset+1, offset+1)
}

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New("test-key", t.TempDir())
	c.BaseURL = srv.URL
	return c, srv
}

func TestSearchVolumesParsesAndStripsHTML(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "test-key" {
			t.Errorf("missing api_key, got query %s", r.URL.RawQuery)
		}
		if !strings.Contains(r.URL.Path, "/search/") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, searchFixture)
	}))

	vols, err := c.SearchVolumes("Saga")
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(vols))
	}
	v := vols[0]
	if v.ID != 49019 || v.Name != "Saga" || v.StartYear != 2012 ||
		v.Publisher != "Image" || v.CountOfIssues != 66 ||
		v.CoverURL != "https://cv.example/saga.jpg" {
		t.Fatalf("unexpected volume %+v", v)
	}
	if v.Description != "Epic & sweeping." {
		t.Fatalf("expected stripped HTML, got %q", v.Description)
	}
	// Null/empty fields don't explode.
	if vols[1].StartYear != 0 || vols[1].Publisher != "" || vols[1].CoverURL != "" {
		t.Fatalf("unexpected zero handling %+v", vols[1])
	}
}

func TestGetVolumeCreators(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, volumeFixture)
	}))
	vd, err := c.GetVolume(49019)
	if err != nil {
		t.Fatal(err)
	}
	if len(vd.Creators) != 2 || vd.Creators[0].Name != "Brian K. Vaughan" ||
		vd.Creators[1].Role != "artist, cover" {
		t.Fatalf("unexpected creators %+v", vd.Creators)
	}
}

func TestListVolumeIssuesPaginates(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		offset := 0
		fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
		// Pretend 101 total results so the client must page twice.
		fmt.Fprint(w, issuesFixture(offset, 101))
	}))
	issues, err := c.ListVolumeIssues(49019)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 pages fetched, got %d", calls)
	}
	if len(issues) != 2 || issues[0].Title != "Chapter 1" || issues[0].CoverDate != "2012-03-14" {
		t.Fatalf("unexpected issues %+v", issues)
	}
	if issues[0].Summary != "Stuff happens." {
		t.Fatalf("expected stripped summary, got %q", issues[0].Summary)
	}
}

func TestDiskCacheAvoidsSecondRequest(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, searchFixture)
	}))
	if _, err := c.SearchVolumes("Saga"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SearchVolumes("Saga"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected 1 HTTP call (second served from cache), got %d", calls)
	}
}

func TestAPIErrorEnvelopeNotCached(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, `{"error": "Invalid API Key", "status_code": 100, "results": []}`)
	}))
	if _, err := c.SearchVolumes("Saga"); err == nil {
		t.Fatal("expected error for status_code 100")
	}
	// Error responses must not be cached.
	c.SearchVolumes("Saga")
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("error response was cached; calls=%d", calls)
	}
}

func TestTokenBucketRefill(t *testing.T) {
	b := newTokenBucket(2, 1000) // 2 capacity, fast refill for the test
	b.wait()
	b.wait()
	b.wait() // would block forever without refill; perSec=1000 makes it instant
	if got := b.available(); got < 0 {
		t.Fatalf("available went negative: %d", got)
	}
}
