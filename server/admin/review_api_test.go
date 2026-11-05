package admin

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/config"
	"github.com/rthaines211/gutter-server/match"
	"github.com/rthaines211/gutter-server/match/localcv"
	"github.com/rthaines211/gutter-server/store"
)

const (
	sagaOriginal = "https://x/uploads/original/1/saga1.jpg"
	sagaCover    = "https://x/uploads/scale_medium/1/saga1.jpg"
)

// reviewCV is a dump with Saga (volume 49019, issues 1 and 2) whose covers
// carry real-looking ComicVine URLs.
func reviewCV(t *testing.T) *localcv.DB {
	t.Helper()
	path := t.TempDir() + "/localcv.db"
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
	CREATE TABLE cv_publisher (id INTEGER PRIMARY KEY, name TEXT, image_url TEXT, site_detail_url TEXT);
	CREATE TABLE cv_volume (id INTEGER PRIMARY KEY, name TEXT NOT NULL, aliases TEXT, start_year TEXT,
		publisher_id INTEGER, count_of_issues INTEGER, description TEXT, image_url TEXT, site_detail_url TEXT);
	CREATE TABLE cv_issue (id INTEGER PRIMARY KEY, volume_id INTEGER NOT NULL, name TEXT, issue_number TEXT,
		cover_date TEXT, store_date TEXT, description TEXT, image_url TEXT, site_detail_url TEXT,
		character_credits TEXT, person_credits TEXT, team_credits TEXT, location_credits TEXT,
		story_arc_credits TEXT, associated_images TEXT);
	CREATE TABLE comic_files (id INTEGER PRIMARY KEY, filename TEXT, file_size INTEGER, md5_hash TEXT,
		first_page_md5 TEXT, tth_hash TEXT, ct_phash TEXT, ct_ahash TEXT, cvid INTEGER);
	INSERT INTO cv_publisher(id, name) VALUES (10, 'Image');
	INSERT INTO cv_volume(id, name, start_year, publisher_id, count_of_issues) VALUES (49019, 'Saga', '2012', 10, 72);
	INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url, description, person_credits, character_credits)
		VALUES (317966, 49019, 'Chapter One', '1', '2012-03-14', '` + sagaOriginal + `', 'd', '[]', '[]'),
		       (317967, 49019, 'Chapter Two', '2', '2012-04-11', 'https://x/uploads/original/1/saga2.jpg', 'd', '[]', '[]');
	`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	cv, err := localcv.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cv.Close() })
	return cv
}

type reviewEnv struct {
	a   *Admin
	st  *store.Store
	mux *http.ServeMux
}

func newReviewEnv(t *testing.T) *reviewEnv {
	t.Helper()
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lib := t.TempDir()
	r := match.NewRunner(st, reviewCV(t), []string{lib}, func() int64 { return time.Now().UnixMilli() })
	a := &Admin{Store: st, Match: r, ComicDirs: []string{lib}, CoverDir: t.TempDir(), ScanState: NewScanState(), Config: &config.Config{}}
	mux := http.NewServeMux()
	a.RegisterAPI(mux)
	return &reviewEnv{a: a, st: st, mux: mux}
}

// seed adds a one-issue series in the given state; cvIssue != "" also records
// an issue match to that ComicVine issue.
func (e *reviewEnv) seed(t *testing.T, name, state, volumeID, cvIssue string) (seriesID, issueID string) {
	t.Helper()
	seriesID = store.SlugifyName(name)
	issueID = store.PathToID(name + "/" + name + " 001.cbz")
	if err := e.st.UpsertSeries(store.Series{ID: seriesID, Name: name, FolderPath: t.TempDir() + "/" + name, IssueCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.ReplaceIssues(seriesID, []store.Issue{{ID: issueID, SeriesID: seriesID, Filename: name + " 001.cbz", Number: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertSeriesMatch(store.SeriesMatch{SeriesID: seriesID, Source: store.SourceComicVine, VolumeID: volumeID, State: state, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if cvIssue != "" {
		if err := e.st.UpsertIssueMatch(store.IssueMatch{IssueID: issueID, Source: store.SourceComicVine, UpstreamID: cvIssue, State: state, Confidence: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func (e *reviewEnv) do(method, url, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

type reviewList struct {
	Count int                      `json:"count"`
	Items []map[string]interface{} `json:"items"`
}

func (e *reviewEnv) list(t *testing.T) reviewList {
	t.Helper()
	w := e.do("GET", "/review", "")
	if w.Code != 200 {
		t.Fatalf("GET /review = %d: %s", w.Code, w.Body)
	}
	var l reviewList
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestReviewListShape(t *testing.T) {
	e := newReviewEnv(t)
	id, _ := e.seed(t, "Saga", store.MatchStateHighConfidence, "49019", "317966")
	l := e.list(t)
	if l.Count != 1 || len(l.Items) != 1 {
		t.Fatalf("count=%d items=%d", l.Count, len(l.Items))
	}
	it := l.Items[0]
	if it["local_cover_url"] != "/library/"+id+"/cover" {
		t.Errorf("local_cover_url = %v", it["local_cover_url"])
	}
	if it["guess_name"] != "Saga" || it["guess_volume_id"] != "49019" || it["guess_publisher"] != "Image" || it["guess_year"] != "2012" {
		t.Errorf("guess fields = %v", it)
	}
	if it["guess_cover_url"] != sagaCover {
		t.Errorf("guess_cover_url = %v", it["guess_cover_url"])
	}
}

func TestReviewListNoMatchHasEmptyGuess(t *testing.T) {
	e := newReviewEnv(t)
	e.seed(t, "Mystery", store.MatchStateNoMatch, "", "")
	it := e.list(t).Items[0]
	if it["guess_volume_id"] != "" || it["guess_cover_url"] != "" {
		t.Errorf("expected empty guess, got %v", it)
	}
}

func TestReviewConfirmEnqueuesWriteback(t *testing.T) {
	e := newReviewEnv(t)
	id, issueID := e.seed(t, "Saga", store.MatchStateHighConfidence, "49019", "317966")
	if w := e.do("POST", "/review/"+id+"/confirm", ""); w.Code != 204 {
		t.Fatalf("confirm = %d: %s", w.Code, w.Body)
	}
	m, _ := e.st.GetSeriesMatch(id)
	if m.State != store.MatchStateConfirmed {
		t.Errorf("state = %s", m.State)
	}
	got, ok, err := e.st.NextWritebackJob()
	if err != nil || !ok || got != issueID {
		t.Errorf("writeback job = %q ok=%v err=%v, want %q", got, ok, err, issueID)
	}
}

func TestReviewConfirmNoVolume400(t *testing.T) {
	e := newReviewEnv(t)
	id, _ := e.seed(t, "Mystery", store.MatchStateNoMatch, "", "")
	w := e.do("POST", "/review/"+id+"/confirm", "")
	if w.Code != 400 {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("body not a JSON error: %s", w.Body)
	}
}

func TestReviewConfirmUnknown404(t *testing.T) {
	e := newReviewEnv(t)
	if w := e.do("POST", "/review/nope/confirm", ""); w.Code != 404 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestReviewPinVolumeConfirms(t *testing.T) {
	e := newReviewEnv(t)
	id, _ := e.seed(t, "Saga", store.MatchStateNoMatch, "", "")
	w := e.do("POST", "/review/"+id+"/volume", `{"source":"comicvine","volume_id":"49019"}`)
	if w.Code != 204 {
		t.Fatalf("pin = %d: %s", w.Code, w.Body)
	}
	m, _ := e.st.GetSeriesMatch(id)
	if m.State != store.MatchStateConfirmed || m.VolumeID != "49019" {
		t.Errorf("match = state %s volume %q", m.State, m.VolumeID)
	}
}

func TestReviewPinVolumeBadBody400(t *testing.T) {
	e := newReviewEnv(t)
	id, _ := e.seed(t, "Saga", store.MatchStateNoMatch, "", "")
	for _, body := range []string{`nope`, `{"source":"comicvine"}`, `{"source":"bogus","volume_id":"1"}`} {
		if w := e.do("POST", "/review/"+id+"/volume", body); w.Code != 400 {
			t.Errorf("body %s: got %d", body, w.Code)
		}
	}
}

func TestReviewDontMatchLeavesQueue(t *testing.T) {
	e := newReviewEnv(t)
	id, _ := e.seed(t, "Mystery", store.MatchStateNoMatch, "", "")
	if w := e.do("POST", "/review/"+id+"/dont-match", ""); w.Code != 204 {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
	if c := e.list(t).Count; c != 0 {
		t.Errorf("count = %d, want 0", c)
	}
}

func TestReviewSearch(t *testing.T) {
	e := newReviewEnv(t)
	if w := e.do("GET", "/review/search?q=%20", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("blank: %d %q", w.Code, w.Body)
	}
	w := e.do("GET", "/review/search?q=saga", "")
	var hits []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &hits); err != nil || len(hits) != 1 {
		t.Fatalf("hits = %v err=%v body=%s", hits, err, w.Body)
	}
	if hits[0]["volume_id"] != "49019" || hits[0]["source"] != "comicvine" || hits[0]["cover_url"] != sagaCover {
		t.Errorf("hit = %v", hits[0])
	}
}

func TestReviewRoutesNotBehindAdminAuth(t *testing.T) {
	e := newReviewEnv(t)
	e.a.Config.AdminPassword = "s3cret"
	if w := e.do("GET", "/review", ""); w.Code != 200 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestReviewMatchingDisabled412(t *testing.T) {
	e := newReviewEnv(t)
	e.a.Match = nil
	if w := e.do("GET", "/review", ""); w.Code != 412 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestReviewDontMatch412WhenDisabled(t *testing.T) {
	e := newReviewEnv(t)
	e.a.Match = nil
	if w := e.do("POST", "/review/x/dont-match", ""); w.Code != 412 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestConfirmAllSkipsDontMatch(t *testing.T) {
	e := newReviewEnv(t)
	keep, _ := e.seed(t, "Saga", store.MatchStateHighConfidence, "49019", "317966")
	skip, _ := e.seed(t, "Other", store.MatchStateHighConfidence, "49019", "")
	if err := e.st.SetDontMatch(skip, true); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	e.a.handleMatchConfirmAll(w, httptest.NewRequest("POST", "/_admin/match/confirm-all", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("confirm-all = %d: %s", w.Code, w.Body)
	}
	if m, _ := e.st.GetSeriesMatch(keep); m.State != store.MatchStateConfirmed {
		t.Errorf("kept row state = %s", m.State)
	}
	if m, _ := e.st.GetSeriesMatch(skip); m.State == store.MatchStateConfirmed {
		t.Errorf("dont_match row was confirmed")
	}
}

func TestReviewConfirmTwice409Message(t *testing.T) {
	e := newReviewEnv(t)
	id, _ := e.seed(t, "Saga", store.MatchStateHighConfidence, "49019", "317966")
	e.do("POST", "/review/"+id+"/confirm", "")
	w := e.do("POST", "/review/"+id+"/volume", `{"source":"comicvine","volume_id":"49019"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "already confirmed") {
		t.Errorf("second confirm = %d %s", w.Code, w.Body)
	}
}
