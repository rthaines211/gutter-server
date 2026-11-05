package metron

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New("user", "pass", t.TempDir())
	c.BaseURL = srv.URL
	return c
}

const seriesDetailJSON = `{"id":123,"name":"Saga","sort_name":"Saga","volume":1,
	"year_began":2012,"desc":"Space opera.","issue_count":66,
	"publisher":{"id":7,"name":"Image"},"cv_id":49019}`

const issueDetailJSON = `{"id":456,"series":{"id":123,"name":"Saga"},"number":"1",
	"name":["Chapter One"],"cover_date":"2012-03-14","desc":"First issue.",
	"cv_id":317966,
	"credits":[{"id":1,"creator":"Brian K. Vaughan","role":[{"id":1,"name":"Writer"}]},
	           {"id":2,"creator":"Fiona Staples","role":[{"id":2,"name":"Artist"},{"id":3,"name":"Cover"}]}]}`

func TestGetSeries(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "user" || p != "pass" {
			t.Errorf("missing/wrong basic auth: %q %q", u, p)
		}
		if r.URL.Path != "/series/123/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, seriesDetailJSON)
	}))
	s, err := c.GetSeries(123)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Saga" || s.YearBegan != 2012 || s.Publisher != "Image" ||
		s.CVID != 49019 || s.Desc != "Space opera." {
		t.Errorf("bad parse: %+v", s)
	}
}

func TestSeriesByCVID(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/series/" && r.URL.Query().Get("cv_id") == "49019":
			fmt.Fprint(w, `{"count":1,"next":null,"previous":null,
				"results":[{"id":123,"series":"Saga (2012)","year_began":2012,"issue_count":66}]}`)
		case r.URL.Path == "/series/123/":
			fmt.Fprint(w, seriesDetailJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	s, err := c.SeriesByCVID(49019)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 123 || s.CVID != 49019 {
		t.Errorf("bad resolve: %+v", s)
	}
}

func TestSeriesByCVIDNotFound(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"count":0,"next":null,"previous":null,"results":[]}`)
	}))
	if _, err := c.SeriesByCVID(1); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestListSeriesIssuesPaginates(t *testing.T) {
	page := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			fmt.Fprint(w, `{"count":2,"next":"http://x/issue/?page=2","previous":null,
				"results":[{"id":456,"issue":"Saga (2012) #1","number":"1","cover_date":"2012-03-14"}]}`)
			return
		}
		fmt.Fprint(w, `{"count":2,"next":null,"previous":null,
			"results":[{"id":457,"issue":"Saga (2012) #2","number":"2","cover_date":"2012-04-11"}]}`)
	}))
	issues, err := c.ListSeriesIssues(123)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].Number != "1" || issues[1].Number != "2" {
		t.Errorf("pagination broke: %+v", issues)
	}
}

func TestGetIssueCredits(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, issueDetailJSON)
	}))
	iss, err := c.GetIssue(456)
	if err != nil {
		t.Fatal(err)
	}
	if iss.CVID != 317966 || iss.Title != "Chapter One" || iss.CoverDate != "2012-03-14" {
		t.Errorf("bad parse: %+v", iss)
	}
	if len(iss.Credits) != 2 || iss.Credits[0].Creator != "Brian K. Vaughan" ||
		iss.Credits[0].Roles[0] != "writer" || iss.Credits[1].Roles[1] != "cover" {
		t.Errorf("credits: %+v", iss.Credits)
	}
}

func TestDiskCache(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, seriesDetailJSON)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	c := New("user", "pass", dir)
	c.BaseURL = srv.URL
	if _, err := c.GetSeries(123); err != nil {
		t.Fatal(err)
	}
	c2 := New("user", "pass", dir)
	c2.BaseURL = srv.URL
	if _, err := c2.GetSeries(123); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("second call must come from disk cache, got %d HTTP hits", hits)
	}
}

// TestRecordLiveFixtures hits the real Metron API and writes testdata/*.json.
// It runs only with real creds in the env and asserts the parsed fields are
// non-empty — if Metron's field names drift from our json tags, this fails
// loudly. Run once during implementation: the recorded files document the
// real shapes.
func TestRecordLiveFixtures(t *testing.T) {
	user, pass := os.Getenv("GUTTER_METRON_USER"), os.Getenv("GUTTER_METRON_PASS")
	if user == "" || pass == "" {
		t.Skip("GUTTER_METRON_USER/GUTTER_METRON_PASS not set")
	}
	c := New(user, pass, "testdata/livecache")
	s, err := c.SeriesByCVID(49019) // Saga (2012)
	if err != nil {
		t.Fatalf("SeriesByCVID: %v", err)
	}
	if s.Name == "" || s.YearBegan == 0 || s.Publisher == "" || s.CVID != 49019 {
		t.Errorf("series fields empty — fix json tags to match testdata/livecache/*.json: %+v", s)
	}
	issues, err := c.ListSeriesIssues(s.ID)
	if err != nil || len(issues) == 0 {
		t.Fatalf("ListSeriesIssues: %v (%d)", err, len(issues))
	}
	iss, err := c.GetIssue(issues[0].ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if iss.Number == "" || iss.CoverDate == "" || len(iss.Credits) == 0 || iss.CVID == 0 {
		t.Errorf("issue fields empty — fix json tags to match testdata/livecache/*.json: %+v", iss)
	}
}
