package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/rthaines211/gutter-server/creators"
	"github.com/rthaines211/gutter-server/store"
)

// newUnbuiltCreatorsIndex opens a creators.Index against a bare (schemaless
// beyond one throwaway table) localcv.db, without calling Build — enough to
// exercise the "index present but not ready" path. No cv_credit data is
// needed: none of these tests query it.
func newUnbuiltCreatorsIndex(t *testing.T) *creators.Index {
	t.Helper()
	dir := t.TempDir()
	localcvPath := dir + "/localcv.db"
	db, err := sql.Open("sqlite3", localcvPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE placeholder(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	idx, err := creators.OpenIndex(dir+"/creators_index.db", localcvPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

// newUnbuiltCreatorsIndexWithPerson is like newUnbuiltCreatorsIndex, but the
// attached localcv has a real cv_person row the index *could* answer from if
// queried — used to prove the handler skips PersonName while the index
// isn't Ready(), rather than merely that an empty/schemaless index can't
// answer it.
func newUnbuiltCreatorsIndexWithPerson(t *testing.T, personID int64, name string) *creators.Index {
	t.Helper()
	dir := t.TempDir()
	localcvPath := dir + "/localcv.db"
	db, err := sql.Open("sqlite3", localcvPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cv_person (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cv_person (id, name) VALUES (?, ?)`, personID, name); err != nil {
		t.Fatal(err)
	}
	db.Close()

	idx, err := creators.OpenIndex(dir+"/creators_index.db", localcvPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

// newReadyCreatorsIndex opens a creators.Index against a localcv with just
// the cv_issue table (empty) and runs Build so Ready() is true — enough to
// take the "index present and built" path without a full dump.
func newReadyCreatorsIndex(t *testing.T) *creators.Index {
	t.Helper()
	dir := t.TempDir()
	localcvPath := dir + "/localcv.db"
	db, err := sql.Open("sqlite3", localcvPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`CREATE TABLE cv_issue (id INTEGER PRIMARY KEY, volume_id INTEGER, person_credits TEXT)`,
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	idx, err := creators.OpenIndex(dir+"/creators_index.db", localcvPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func putSeriesEnrichmentCreators(t *testing.T, s *Server, seriesID string, creatorsJSON string) {
	t.Helper()
	if _, err := s.Store.DB().Exec(
		`INSERT INTO series (id, name, folder_path, issue_count) VALUES (?, ?, ?, 0)
		 ON CONFLICT(id) DO NOTHING`,
		seriesID, seriesID, "/x/"+seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB().Exec(`
		INSERT INTO series_enrichment (series_id, comicvine_volume_id, description, publisher, start_year, creators,
			genre, gcd_series_id, secondary_source, volume_name, status, enriched_at)
		VALUES (?, 0, '', '', 0, ?, '', 0, '', '', 'auto', 0)
	`, seriesID, creatorsJSON); err != nil {
		t.Fatal(err)
	}
}

func TestCreatorsListFromEnrichment(t *testing.T) {
	srv := newTestServer(t)
	credits := `[{"id":5,"name":"Tom King","role":"writer"}]`
	putSeriesEnrichmentCreators(t, srv, "series-a", credits)
	putSeriesEnrichmentCreators(t, srv, "series-b", credits)

	req := httptest.NewRequest("GET", "/creators", nil)
	w := httptest.NewRecorder()
	srv.HandleCreatorsList(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var entries []CreatorListEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 creator entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].ID != 5 || entries[0].Name != "Tom King" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
	if len(entries[0].SeriesIds) != 2 {
		t.Errorf("expected 2 seriesIds, got %d: %+v", len(entries[0].SeriesIds), entries[0].SeriesIds)
	}
	if len(entries[0].Roles) != 1 || entries[0].Roles[0] != "writer" {
		t.Errorf("expected roles=[writer], got %+v", entries[0].Roles)
	}
}

// TestCreatorsListSplitsCommaRoles guards against treating ComicVine's raw
// (possibly multi-role) role string as a single role: "penciler, inker"
// must count as two in-scope roles, both grouped as "art" — not get
// silently dropped because "penciler, inker" itself isn't a known role.
func TestCreatorsListSplitsCommaRoles(t *testing.T) {
	srv := newTestServer(t)
	putSeriesEnrichmentCreators(t, srv, "series-a",
		`[{"id":7,"name":"Abe Simon","role":"penciler, inker"}]`)

	req := httptest.NewRequest("GET", "/creators", nil)
	w := httptest.NewRecorder()
	srv.HandleCreatorsList(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var entries []CreatorListEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 creator entry, got %d: %+v", len(entries), entries)
	}
	if len(entries[0].Roles) != 2 {
		t.Fatalf("expected 2 split roles (penciler, inker), got %+v", entries[0].Roles)
	}
}

func TestCreatorDetailNoIndex(t *testing.T) {
	srv := newTestServer(t)
	credits := `[{"id":5,"name":"Tom King","role":"writer"}]`
	putSeriesEnrichmentCreators(t, srv, "series-a", credits)

	req := httptest.NewRequest("GET", "/creators/5", nil)
	req.SetPathValue("personId", "5")
	w := httptest.NewRecorder()
	srv.HandleCreatorDetail(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CreatorDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Bibliography != nil {
		t.Errorf("expected bibliography null, got %+v", resp.Bibliography)
	}
	if resp.BibliographyPending {
		t.Error("expected bibliographyPending=false with no index")
	}
	if len(resp.SeriesIds) != 1 || resp.SeriesIds[0] != "series-a" {
		t.Errorf("expected seriesIds=[series-a], got %+v", resp.SeriesIds)
	}
	if resp.VolumesOwned != 1 {
		t.Errorf("expected volumesOwned=1 (len(seriesIds)), got %d", resp.VolumesOwned)
	}

	// Raw JSON must literally contain "bibliography":null, not an omitted
	// field or empty array.
	if !jsonHasNullBibliography(w.Body.Bytes()) {
		t.Errorf("expected literal bibliography:null in response, got %s", w.Body.String())
	}
}

func jsonHasNullBibliography(b []byte) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return false
	}
	v, ok := raw["bibliography"]
	return ok && string(v) == "null"
}

func TestCreatorDetailPending(t *testing.T) {
	srv := newTestServer(t)
	srv.Creators = newUnbuiltCreatorsIndex(t)
	credits := `[{"id":5,"name":"Tom King","role":"writer"}]`
	putSeriesEnrichmentCreators(t, srv, "series-a", credits)

	req := httptest.NewRequest("GET", "/creators/5", nil)
	req.SetPathValue("personId", "5")
	w := httptest.NewRecorder()
	srv.HandleCreatorDetail(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CreatorDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.BibliographyPending {
		t.Error("expected bibliographyPending=true (index present, not built)")
	}
	if resp.Bibliography != nil {
		t.Errorf("expected bibliography null while pending, got %+v", resp.Bibliography)
	}
}

// TestCreatorDetailNotReadySkipsPersonNameLookup guards the fix for
// PersonName blocking on the index's single pinned connection during a
// rebuild (~15s): while the index isn't Ready(), the handler must fall back
// to the library-derived name instead of calling PersonName at all.
func TestCreatorDetailNotReadySkipsPersonNameLookup(t *testing.T) {
	srv := newTestServer(t)
	srv.Creators = newUnbuiltCreatorsIndexWithPerson(t, 5, "Index Name")
	putSeriesEnrichmentCreators(t, srv, "series-a",
		`[{"id":5,"name":"Library Name","role":"writer"}]`)

	req := httptest.NewRequest("GET", "/creators/5", nil)
	req.SetPathValue("personId", "5")
	w := httptest.NewRecorder()
	srv.HandleCreatorDetail(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp CreatorDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Name != "Library Name" {
		t.Errorf("expected library-derived name while index isn't Ready, got %q", resp.Name)
	}
}

func TestCreatorDetailUnknown404(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest("GET", "/creators/999", nil)
	req.SetPathValue("personId", "999")
	w := httptest.NewRecorder()
	srv.HandleCreatorDetail(w, req)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreatorDetailBadPersonID(t *testing.T) {
	srv := newTestServer(t)

	for _, bad := range []string{"0", "-1", "abc"} {
		req := httptest.NewRequest("GET", "/creators/"+bad, nil)
		req.SetPathValue("personId", bad)
		w := httptest.NewRecorder()
		srv.HandleCreatorDetail(w, req)
		if w.Code != 400 {
			t.Errorf("personId=%q: expected 400, got %d", bad, w.Code)
		}
	}
}

func TestCreatorLookupNoIndexUsesLibrary(t *testing.T) {
	srv := newTestServer(t)
	credits := `[{"id":5,"name":"Tom King","role":"writer"}]`
	putSeriesEnrichmentCreators(t, srv, "series-a", credits)

	req := httptest.NewRequest("GET", "/creators/lookup?name=Tom%20King", nil)
	w := httptest.NewRecorder()
	srv.HandleCreatorLookup(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var matches []creatorMatchJSON
	if err := json.Unmarshal(w.Body.Bytes(), &matches); err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != 5 || matches[0].Name != "Tom King" {
		t.Errorf("unexpected matches: %+v", matches)
	}
}

func TestCreatorLookupEmptyNameIs400(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/creators/lookup", nil)
	w := httptest.NewRecorder()
	srv.HandleCreatorLookup(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreatorLookupNoMatchIsEmptyArrayNotNull(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/creators/lookup?name=Nobody", nil)
	w := httptest.NewRecorder()
	srv.HandleCreatorLookup(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "[]\n" && w.Body.String() != "[]" {
		t.Errorf("expected empty JSON array, got %q", w.Body.String())
	}
}

func TestCreatorVolumesNoIndexIs503(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/creators/volumes/123", nil)
	req.SetPathValue("id", "123")
	w := httptest.NewRecorder()
	srv.HandleCreatorVolume(w, req)
	if w.Code != 503 {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIssueCredits404WithoutVineID(t *testing.T) {
	srv := newTestServer(t)
	// No issue_enrichment row at all for this comic ID — IssueComicVineID
	// reads issue_enrichment directly, so no issues/series rows are needed.
	id := pathToID("series/issue1.cbz")

	req := httptest.NewRequest("GET", "/creators/issues/"+id, nil)
	req.SetPathValue("comicId", id)
	w := httptest.NewRecorder()
	srv.HandleCreatorIssueCredits(w, req)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIssueCreditsNoIndexIs503(t *testing.T) {
	srv := newTestServer(t)
	id := pathToID("series/issue2.cbz")
	if _, err := srv.Store.DB().Exec(
		`INSERT INTO series (id, name, folder_path, issue_count) VALUES ('series-a', 'series-a', '/x/series-a', 0)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Store.DB().Exec(
		`INSERT INTO issues (id, series_id, filename, number) VALUES (?, 'series-a', 'issue2.cbz', '2')`,
		id); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.UpsertIssueEnrichment(store.IssueEnrichment{IssueID: id, VineID: 777}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/creators/issues/"+id, nil)
	req.SetPathValue("comicId", id)
	w := httptest.NewRecorder()
	srv.HandleCreatorIssueCredits(w, req)
	if w.Code != 503 {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// TestIssueCreditsMissingFromLocalcvIs404 covers a CV issue id the comic is
// matched to that's missing from localcv (a stale sidecar or partial
// rebuild): Index.IssueCredits returns sql.ErrNoRows, which must surface as
// a 404, not the generic 500 writeInternalError gives every other error.
func TestIssueCreditsMissingFromLocalcvIs404(t *testing.T) {
	srv := newTestServer(t)
	srv.Creators = newReadyCreatorsIndex(t)
	id := pathToID("series/issue3.cbz")
	if _, err := srv.Store.DB().Exec(
		`INSERT INTO series (id, name, folder_path, issue_count) VALUES ('series-a', 'series-a', '/x/series-a', 0)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Store.DB().Exec(
		`INSERT INTO issues (id, series_id, filename, number) VALUES (?, 'series-a', 'issue3.cbz', '3')`,
		id); err != nil {
		t.Fatal(err)
	}
	// Matched to a vine id with no corresponding cv_issue row in localcv.
	if err := srv.Store.UpsertIssueEnrichment(store.IssueEnrichment{IssueID: id, VineID: 999999}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/creators/issues/"+id, nil)
	req.SetPathValue("comicId", id)
	w := httptest.NewRecorder()
	srv.HandleCreatorIssueCredits(w, req)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
