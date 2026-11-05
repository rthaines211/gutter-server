package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/server/admin"
	"github.com/rthaines211/gutter-server/store"
)

type librarySeriesResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IssueCount int    `json:"issue_count"`
	ReadCount  int    `json:"read_count"`
	CoverURL   string `json:"cover_url"`
	Writer     string `json:"writer"`
	Penciller  string `json:"penciller"`
}

type seriesDetailResponse struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	IssueCount int             `json:"issue_count"`
	ReadCount  int             `json:"read_count"`
	CoverURL   string          `json:"cover_url"`
	Writer     string          `json:"writer"`
	Penciller  string          `json:"penciller"`
	Issues     []issueResponse `json:"issues"`
}

type issueResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Number      string `json:"number"`
	CoverURL    string `json:"cover_url"`
	Status      string `json:"status"`
	CurrentPage *int   `json:"current_page"`
	TotalPages  *int   `json:"total_pages"`
}

func setupLibraryTest(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()

	batmanDir := filepath.Join(dir, "Batman (2016)")
	os.MkdirAll(batmanDir, 0755)
	createTestCBZ(t, filepath.Join(batmanDir, "Batman 001.cbz"), "Tom King", "David Finch", "001")
	createTestCBZ(t, filepath.Join(batmanDir, "Batman 002.cbz"), "Tom King", "David Finch", "002")

	xmenDir := filepath.Join(dir, "X-Men")
	os.MkdirAll(xmenDir, 0755)
	createTestCBZ(t, filepath.Join(xmenDir, "X-Men 001.cbz"), "Hickman", "Yu", "001")

	srv := newTestServer(t)
	srv.ComicDirs = []string{dir}

	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)
	srv.CoverDir = coverDir

	ss := admin.NewScanState()
	srv.Admin = &admin.Admin{ScanState: ss}

	ScanLibrary([]string{dir}, srv.Store, coverDir, nil)
	return srv, dir
}

func TestHandleListLibrary(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	req := httptest.NewRequest("GET", "/library", nil)
	w := httptest.NewRecorder()
	srv.HandleListLibrary(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var series []librarySeriesResponse
	json.NewDecoder(w.Body).Decode(&series)

	if len(series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(series))
	}
	if series[0].Name != "Batman (2016)" {
		t.Errorf("expected first series 'Batman (2016)', got %q", series[0].Name)
	}
	if series[0].IssueCount != 2 {
		t.Errorf("expected 2 issues, got %d", series[0].IssueCount)
	}
	if series[0].ReadCount != 0 {
		t.Errorf("expected 0 read, got %d", series[0].ReadCount)
	}
}

func TestHandleListLibraryWithSearch(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	req := httptest.NewRequest("GET", "/library?q=batman", nil)
	w := httptest.NewRecorder()
	srv.HandleListLibrary(w, req)

	var series []librarySeriesResponse
	json.NewDecoder(w.Body).Decode(&series)

	if len(series) != 1 {
		t.Fatalf("expected 1 series for search 'batman', got %d", len(series))
	}
	if series[0].Name != "Batman (2016)" {
		t.Errorf("expected 'Batman (2016)', got %q", series[0].Name)
	}
}

func TestHandleListLibraryWithReadProgress(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	comicPath := filepath.Join("Batman (2016)", "Batman 001.cbz")
	comicID := pathToID(comicPath)
	srv.Store.UpsertProgress(store.Progress{
		ComicID: comicID, CurrentPage: 1, TotalPages: 1,
	})

	req := httptest.NewRequest("GET", "/library", nil)
	w := httptest.NewRecorder()
	srv.HandleListLibrary(w, req)

	var series []librarySeriesResponse
	json.NewDecoder(w.Body).Decode(&series)

	var batman *librarySeriesResponse
	for i := range series {
		if series[i].Name == "Batman (2016)" {
			batman = &series[i]
			break
		}
	}
	if batman == nil {
		t.Fatal("Batman not found in library response")
	}
	if batman.ReadCount != 1 {
		t.Errorf("expected read_count 1, got %d", batman.ReadCount)
	}

	// Test completed filter — Batman shouldn't be included (1/2 read)
	req = httptest.NewRequest("GET", "/library?status=completed", nil)
	w = httptest.NewRecorder()
	srv.HandleListLibrary(w, req)
	json.NewDecoder(w.Body).Decode(&series)
	for _, s := range series {
		if s.Name == "Batman (2016)" {
			t.Error("Batman should not appear in completed filter")
		}
	}

	// Test in_progress filter — Batman should be included
	req = httptest.NewRequest("GET", "/library?status=in_progress", nil)
	w = httptest.NewRecorder()
	srv.HandleListLibrary(w, req)
	json.NewDecoder(w.Body).Decode(&series)
	found := false
	for _, s := range series {
		if s.Name == "Batman (2016)" {
			found = true
		}
	}
	if !found {
		t.Error("Batman should appear in in_progress filter")
	}
}

func TestHandleGetSeries(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	req := httptest.NewRequest("GET", "/library/batman-2016", nil)
	req.SetPathValue("id", "batman-2016")
	w := httptest.NewRecorder()
	srv.HandleGetSeries(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var detail seriesDetailResponse
	json.NewDecoder(w.Body).Decode(&detail)

	if detail.Name != "Batman (2016)" {
		t.Errorf("expected 'Batman (2016)', got %q", detail.Name)
	}
	if len(detail.Issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(detail.Issues))
	}
	if detail.Issues[0].Name != "Batman 001.cbz" {
		t.Errorf("expected first issue 'Batman 001.cbz', got %q", detail.Issues[0].Name)
	}
	if detail.Issues[0].Status != "unread" {
		t.Errorf("expected status 'unread', got %q", detail.Issues[0].Status)
	}
}

// TestHandleGetSeriesProgressBatchedMatchesPerRow pins buildIssueList's
// batched progress lookup (GetProgressForComics) against the per-issue
// GetProgress it replaced: for every issue returned by GET /library/{id},
// status/current_page/total_pages must equal what GetProgress for that one
// comic id would give — including the issue with no progress row at all.
func TestHandleGetSeriesProgressBatchedMatchesPerRow(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	issue1 := pathToID("Batman (2016)/Batman 001.cbz")
	if err := srv.Store.UpsertProgress(store.Progress{ComicID: issue1, CurrentPage: 5, TotalPages: 20}); err != nil {
		t.Fatal(err)
	}
	// Batman 002.cbz is left with no progress row at all.

	req := httptest.NewRequest("GET", "/library/batman-2016", nil)
	req.SetPathValue("id", "batman-2016")
	w := httptest.NewRecorder()
	srv.HandleGetSeries(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var detail SeriesDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(detail.Issues))
	}

	for _, iss := range detail.Issues {
		want, err := srv.Store.GetProgress(iss.ID) // old per-row path, kept as ground truth
		wantStatus, wantCurrent, wantTotal := "unread", (*int)(nil), (*int)(nil)
		if err == nil {
			wantStatus = "in_progress"
			if want.CurrentPage >= want.TotalPages {
				wantStatus = "completed"
			}
			cp, tp := want.CurrentPage, want.TotalPages
			wantCurrent, wantTotal = &cp, &tp
		}
		if iss.Status != wantStatus {
			t.Errorf("issue %s: want status %q, got %q", iss.ID, wantStatus, iss.Status)
		}
		if (wantCurrent == nil) != (iss.CurrentPage == nil) {
			t.Errorf("issue %s: want current_page nil=%v, got nil=%v", iss.ID, wantCurrent == nil, iss.CurrentPage == nil)
		} else if wantCurrent != nil && *wantCurrent != *iss.CurrentPage {
			t.Errorf("issue %s: want current_page %d, got %d", iss.ID, *wantCurrent, *iss.CurrentPage)
		}
		if (wantTotal == nil) != (iss.TotalPages == nil) {
			t.Errorf("issue %s: want total_pages nil=%v, got nil=%v", iss.ID, wantTotal == nil, iss.TotalPages == nil)
		} else if wantTotal != nil && *wantTotal != *iss.TotalPages {
			t.Errorf("issue %s: want total_pages %d, got %d", iss.ID, *wantTotal, *iss.TotalPages)
		}
	}
}

func TestHandleGetSeriesNotFound(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	req := httptest.NewRequest("GET", "/library/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	srv.HandleGetSeries(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleListLibrarySortRecent(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	// Make X-Men appear more recently updated than Batman by backdating Batman.
	_, err := srv.Store.DB().Exec(
		"UPDATE series SET updated_at = '2020-01-01T00:00:00Z' WHERE id = 'batman-2016'",
	)
	if err != nil {
		t.Fatalf("failed to update updated_at: %v", err)
	}

	req := httptest.NewRequest("GET", "/library?sort=recent", nil)
	w := httptest.NewRecorder()
	srv.HandleListLibrary(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var series []librarySeriesResponse
	json.NewDecoder(w.Body).Decode(&series)

	if len(series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(series))
	}
	// X-Men has the more recent updated_at, so it should appear first.
	if series[0].Name != "X-Men" {
		t.Errorf("expected first series 'X-Men' (most recent), got %q", series[0].Name)
	}
	if series[1].Name != "Batman (2016)" {
		t.Errorf("expected second series 'Batman (2016)' (oldest), got %q", series[1].Name)
	}
}

func TestHandleRefreshLibrary(t *testing.T) {
	srv, dir := setupLibraryTest(t)

	newDir := filepath.Join(dir, "Spider-Man")
	os.MkdirAll(newDir, 0755)
	createTestCBZ(t, filepath.Join(newDir, "Spider-Man 001.cbz"), "Slott", "Ramos", "001")

	req := httptest.NewRequest("POST", "/library/refresh", nil)
	w := httptest.NewRecorder()
	srv.HandleRefreshLibrary(w, req)

	if w.Code != 202 {
		t.Fatalf("expected 202, got %d", w.Code)
	}

	// Wait for background scan to finish
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		snap := srv.Admin.ScanState.Snapshot()
		if !snap.IsScanning {
			break
		}
	}

	list, _ := srv.Store.ListSeries()
	if len(list) != 3 {
		t.Fatalf("expected 3 series after refresh, got %d", len(list))
	}
}

// --- Extended library filter/sort tests ---

func seedLibraryData(t *testing.T, srv *Server) {
	t.Helper()
	series := []store.Series{
		{ID: "batman", Name: "Batman", FolderPath: t.TempDir() + "/Batman", IssueCount: 10, Publisher: "DC Comics", Year: 2016, Writer: "Tom King"},
		{ID: "saga", Name: "Saga", FolderPath: t.TempDir() + "/Saga", IssueCount: 54, Publisher: "Image Comics", Year: 2012, Writer: "Brian K. Vaughan"},
		{ID: "xmen", Name: "X-Men", FolderPath: t.TempDir() + "/X-Men", IssueCount: 5, Publisher: "Marvel", Year: 2019, Writer: "Jonathan Hickman"},
	}
	for _, s := range series {
		if err := srv.Store.UpsertSeries(s); err != nil {
			t.Fatal(err)
		}
	}
	srv.Store.ReplaceSeriesGenres("batman", []string{"Action", "Superhero"})
	srv.Store.ReplaceSeriesGenres("saga", []string{"Sci-Fi", "Fantasy", "Romance"})
	srv.Store.ReplaceSeriesGenres("xmen", []string{"Action", "Superhero", "Sci-Fi"})
}

func getLibrary(t *testing.T, srv *Server, query string) []LibrarySeriesResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/library?"+query, nil)
	w := httptest.NewRecorder()
	srv.HandleListLibrary(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /library?%s: got %d, body: %s", query, w.Code, w.Body.String())
	}
	var result []LibrarySeriesResponse
	json.NewDecoder(w.Body).Decode(&result)
	return result
}

func TestLibraryFilterByPublisher(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getLibrary(t, srv, "publisher=DC+Comics")
	if len(result) != 1 || result[0].ID != "batman" {
		t.Errorf("publisher filter: got %d results, want 1 (batman)", len(result))
	}
}

func TestLibraryFilterByGenre(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getLibrary(t, srv, "genre=Sci-Fi")
	if len(result) != 2 {
		t.Errorf("genre filter: got %d results, want 2 (saga, xmen)", len(result))
	}
}

func TestLibraryFilterByWriter(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getLibrary(t, srv, "writer=Tom+King")
	if len(result) != 1 || result[0].ID != "batman" {
		t.Errorf("writer filter: got %d results, want 1 (batman)", len(result))
	}
}

func TestLibraryFilterByYearRange(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getLibrary(t, srv, "year_min=2015&year_max=2017")
	if len(result) != 1 || result[0].ID != "batman" {
		t.Errorf("year range filter: got %d results, want 1 (batman)", len(result))
	}
}

func TestLibraryFilterStacked(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getLibrary(t, srv, "genre=Action&publisher=DC+Comics")
	if len(result) != 1 || result[0].ID != "batman" {
		t.Errorf("stacked filter: got %d results, want 1 (batman)", len(result))
	}
}

func TestLibrarySortByPublisher(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getLibrary(t, srv, "sort=publisher&order=asc")
	if len(result) != 3 {
		t.Fatalf("got %d results, want 3", len(result))
	}
	if result[0].ID != "batman" {
		t.Errorf("first by publisher asc: got %s, want batman (DC Comics)", result[0].ID)
	}
}

func TestLibrarySortByYear(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getLibrary(t, srv, "sort=year&order=asc")
	if len(result) != 3 {
		t.Fatalf("got %d results, want 3", len(result))
	}
	if result[0].ID != "saga" {
		t.Errorf("first by year asc: got %s, want saga (2012)", result[0].ID)
	}
}

func TestLibrarySortByIssueCount(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getLibrary(t, srv, "sort=issue_count&order=desc")
	if len(result) != 3 {
		t.Fatalf("got %d results, want 3", len(result))
	}
	if result[0].ID != "saga" {
		t.Errorf("first by issue_count desc: got %s, want saga (54)", result[0].ID)
	}
}

func TestHandleScanStatus(t *testing.T) {
	srv := newTestServer(t)
	// Attach a ScanState via Admin
	ss := admin.NewScanState()
	srv.Admin = &admin.Admin{ScanState: ss}

	req := httptest.NewRequest("GET", "/library/refresh/status", nil)
	w := httptest.NewRecorder()
	srv.HandleScanStatus(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var status struct {
		IsScanning bool  `json:"is_scanning"`
		Scanned    int   `json:"scanned"`
		Total      int   `json:"total"`
		DurationMs int64 `json:"duration_ms"`
	}
	json.NewDecoder(w.Body).Decode(&status)

	if status.IsScanning {
		t.Error("expected is_scanning=false initially")
	}
}

func TestLibraryResponseIncludesGenres(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getLibrary(t, srv, "")
	for _, r := range result {
		if r.ID == "batman" {
			if len(r.Genres) != 2 {
				t.Errorf("batman genres: got %d, want 2", len(r.Genres))
			}
			if r.Publisher != "DC Comics" {
				t.Errorf("batman publisher: got %q, want %q", r.Publisher, "DC Comics")
			}
			if r.Year != 2016 {
				t.Errorf("batman year: got %d, want %d", r.Year, 2016)
			}
		}
	}
}

func TestHandleRefreshLibraryAsync(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	// Trigger refresh — should return immediately
	req := httptest.NewRequest("POST", "/library/refresh", nil)
	w := httptest.NewRecorder()
	srv.HandleRefreshLibrary(w, req)

	if w.Code != 202 {
		t.Fatalf("expected 202, got %d", w.Code)
	}

	// Wait for background scan to finish
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		snap := srv.Admin.ScanState.Snapshot()
		if !snap.IsScanning {
			break
		}
	}

	list, _ := srv.Store.ListSeries()
	if len(list) != 2 {
		t.Fatalf("expected 2 series after async scan, got %d", len(list))
	}
}

func TestHandleRefreshLibraryRejectsConcurrent(t *testing.T) {
	srv, _ := setupLibraryTest(t)

	// Start a scan manually so IsScanning is true
	srv.Admin.ScanState.StartScan()

	req := httptest.NewRequest("POST", "/library/refresh", nil)
	w := httptest.NewRecorder()
	srv.HandleRefreshLibrary(w, req)

	if w.Code != 409 {
		t.Fatalf("expected 409 Conflict, got %d", w.Code)
	}

	// Clean up
	srv.Admin.ScanState.FinishScan(admin.ScanResult{})
}

func TestRefreshUsesRescan(t *testing.T) {
	srv, _ := setupLibraryTest(t)
	done := make(chan struct{})
	srv.Rescan = func() { close(done) }
	w := httptest.NewRecorder()
	srv.HandleRefreshLibrary(w, httptest.NewRequest("POST", "/library/refresh", nil))
	if w.Code != 202 {
		t.Fatalf("expected 202, got %d", w.Code)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Rescan not called")
	}
}

func TestRefreshConflictWhilePipelineRunning(t *testing.T) {
	srv, _ := setupLibraryTest(t)
	called := make(chan struct{}, 1)
	srv.Rescan = func() { called <- struct{}{} }
	srv.Admin.ScanState.SetPipelineRunning(true)
	w := httptest.NewRecorder()
	srv.HandleRefreshLibrary(w, httptest.NewRequest("POST", "/library/refresh", nil))
	if w.Code != 409 {
		t.Fatalf("expected 409, got %d", w.Code)
	}
	select {
	case <-called:
		t.Fatal("Rescan must not run")
	case <-time.After(100 * time.Millisecond):
	}
}
