package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhaines/comic-reader/cache"
	"github.com/ryanhaines/comic-reader/store"
)

// twinLib lays out the production shape: the organized folder holds #1-#2
// and Mylar's colon-less twin (its folder_format strips colons) holds #11.
func twinLib(t *testing.T) (root, organized, twin string, st *store.Store) {
	t.Helper()
	root = t.TempDir()
	organized = filepath.Join(root, "Rook: Exodus (2024)")
	twin = filepath.Join(root, "Rook Exodus (2024)")
	os.MkdirAll(organized, 0755)
	os.MkdirAll(twin, 0755)
	createTestCBZWithPublisher(t, filepath.Join(organized, "Rook: Exodus #1 (2024).cbz"), "Johns", "Image")
	createTestCBZWithPublisher(t, filepath.Join(organized, "Rook: Exodus #2 (2024).cbz"), "Johns", "Image")
	createTestCBZWithPublisher(t, filepath.Join(twin, "Rook Exodus 11 (2026).cbz"), "Johns", "Image")
	st, err := store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return root, organized, twin, st
}

var twinIssueID = store.PathToID("Rook Exodus (2024)/Rook Exodus 11 (2026).cbz")

// A series id with twin folders reads its issues from all of them; each
// twin-folder issue keeps an id derived from its own on-disk path.
func TestScanMergesTwinFolderIssues(t *testing.T) {
	root, organized, _, st := twinLib(t)
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ListSeries()
	if len(all) != 1 {
		t.Fatalf("series = %+v", all)
	}
	s := all[0]
	if s.FolderPath != organized || s.IssueCount != 3 {
		t.Fatalf("series folder=%q count=%d, want organized folder and 3", s.FolderPath, s.IssueCount)
	}
	issues, _ := st.ListIssues(s.ID)
	var got []string
	for _, iss := range issues {
		got = append(got, iss.ID)
	}
	want := []string{
		store.PathToID("Rook: Exodus (2024)/Rook: Exodus #1 (2024).cbz"),
		store.PathToID("Rook: Exodus (2024)/Rook: Exodus #2 (2024).cbz"),
		twinIssueID,
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("issue ids = %v, want %v", got, want)
	}
	if issues[2].Number != "11" || issues[2].Filename != "Rook Exodus 11 (2026).cbz" {
		t.Fatalf("twin issue = %+v", issues[2])
	}
}

// The same issue in both folders (same filename, or same parsed number and
// format) is listed once: the primary folder's copy.
func TestScanTwinDuplicateKeepsPrimaryCopy(t *testing.T) {
	root, _, twin, st := twinLib(t)
	// As in production, the series row already owns the organized folder.
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	createTestCBZWithPublisher(t, filepath.Join(twin, "Rook Exodus 2 (2024).cbz"), "Johns", "Image")
	createTestCBZWithPublisher(t, filepath.Join(twin, "Rook: Exodus #1 (2024).cbz"), "Johns", "Image")
	// An annual #1 is not issue #1.
	createTestCBZWithPublisher(t, filepath.Join(twin, "Rook Exodus Annual 1 (2025).cbz"), "Johns", "Image")
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	issues, _ := st.ListIssues(store.SlugifyName("Rook Exodus (2024)"))
	byID := map[string]bool{}
	for _, iss := range issues {
		byID[iss.ID] = true
	}
	if len(issues) != 4 || !byID[store.PathToID("Rook: Exodus (2024)/Rook: Exodus #1 (2024).cbz")] ||
		!byID[store.PathToID("Rook: Exodus (2024)/Rook: Exodus #2 (2024).cbz")] || !byID[twinIssueID] ||
		!byID[store.PathToID("Rook Exodus (2024)/Rook Exodus Annual 1 (2025).cbz")] {
		t.Fatalf("issues = %+v", issues)
	}
}

// An unchanged twin pair must not look changed on the next scan, or the
// PR #20 re-fill loop comes back. The first scan that sees the twin's issue
// queues the series once; after that, nothing.
func TestScanUnchangedTwinsQueueNothing(t *testing.T) {
	root, _, _, st := twinLib(t)
	coverDir := t.TempDir()
	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 1 {
		t.Fatalf("first scan queued %v, want the series once", ids)
	}
	st.RemovePipelinePending(store.SlugifyName("Rook Exodus (2024)"))
	for i := 0; i < 2; i++ {
		if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
			t.Fatal(err)
		}
		if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
			t.Fatalf("rescan %d queued %v", i+1, ids)
		}
	}
}

// Merging the folders by hand moves the twin's file under the primary
// folder with the same name: the stored id now points at a path that is
// gone, so the series must be rescanned.
func TestScanNoticesTwinFileMovedToPrimary(t *testing.T) {
	root, organized, twin, st := twinLib(t)
	coverDir := t.TempDir()
	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	id := store.SlugifyName("Rook Exodus (2024)")
	st.RemovePipelinePending(id)
	name := "Rook Exodus 11 (2026).cbz"
	if err := os.Rename(filepath.Join(twin, name), filepath.Join(organized, name)); err != nil {
		t.Fatal(err)
	}
	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	issues, _ := st.ListIssues(id)
	found := false
	for _, iss := range issues {
		found = found || iss.ID == store.PathToID("Rook: Exodus (2024)/"+name)
	}
	if len(issues) != 3 || !found {
		t.Fatalf("issues = %+v", issues)
	}
}

// Ids resolve against the series' root, so a same-slug folder in another
// root is not merged; it is skipped as before.
func TestScanTwinInOtherRootIsNotMerged(t *testing.T) {
	root, organized, _, st := twinLib(t)
	other := t.TempDir()
	os.MkdirAll(filepath.Join(other, "Rook - Exodus (2024)"), 0755)
	createTestCBZWithPublisher(t, filepath.Join(other, "Rook - Exodus (2024)", "Rook - Exodus 12 (2026).cbz"), "Johns", "Image")
	if err := ScanLibrary([]string{root, other}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	s, err := st.GetSeries(store.SlugifyName("Rook Exodus (2024)"))
	if err != nil {
		t.Fatal(err)
	}
	if s.FolderPath != organized || s.IssueCount != 3 {
		t.Fatalf("series = %+v", s)
	}
}

// Pages and covers of a twin-folder issue are served from its own folder.
func TestTwinIssuePagesAndCoverServe(t *testing.T) {
	root, _, _, st := twinLib(t)
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	srv := &Server{ComicDirs: []string{root}, Store: st, Cache: cache.NewLRU(10), CoverDir: t.TempDir()}

	req := httptest.NewRequest("GET", "/comics/"+twinIssueID+"/pages/0", nil)
	req.SetPathValue("id", twinIssueID)
	req.SetPathValue("num", "0")
	w := httptest.NewRecorder()
	srv.HandleComicPage(w, req)
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/comics/"+twinIssueID+"/cover", nil)
	req.SetPathValue("id", twinIssueID)
	w = httptest.NewRecorder()
	srv.HandleComicCover(w, req)
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatalf("cover: %d %s", w.Code, w.Body.String())
	}
}

// Read counts and the reading list place twin-folder issues in their series.
func TestTwinIssueProgressCountsForSeries(t *testing.T) {
	root, _, _, st := twinLib(t)
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	srv := &Server{ComicDirs: []string{root}, Store: st, Cache: cache.NewLRU(10)}
	if err := st.BatchUpsertProgress([]store.Progress{{ComicID: twinIssueID, CurrentPage: 1, TotalPages: 1}}); err != nil {
		t.Fatal(err)
	}
	s, _ := st.GetSeries(store.SlugifyName("Rook Exodus (2024)"))
	if n := srv.countReadIssues(*s); n != 1 {
		t.Fatalf("read count = %d, want 1", n)
	}
	if srv.lastReadTime(*s) == "" {
		t.Fatal("last read time empty")
	}

	if err := st.BatchUpsertProgress([]store.Progress{{ComicID: twinIssueID, CurrentPage: 0, TotalPages: 5}}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	srv.HandleGetReading(w, httptest.NewRequest("GET", "/reading", nil))
	var resp ReadingListResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.InProgress) != 1 || resp.InProgress[0].SeriesID != s.ID {
		t.Fatalf("reading list = %+v", resp.InProgress)
	}
}

// The manual rename endpoints never touch a twin folder: apply-template
// renames only primary-folder files, and a folder rename is refused.
func TestManualRenamesLeaveTwinFolderAlone(t *testing.T) {
	root, organized, twin, st := twinLib(t)
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	id := store.SlugifyName("Rook Exodus (2024)")
	srv := &Server{ComicDirs: []string{root}, Store: st, Cache: cache.NewLRU(10), CoverDir: t.TempDir()}

	req := httptest.NewRequest("POST", "/series/"+id+"/apply-template", nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.HandleApplyTemplate(w, req)
	if w.Code != 200 {
		t.Fatalf("apply-template: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(twin, "Rook Exodus 11 (2026).cbz")); err != nil {
		t.Fatalf("twin file renamed: %v", err)
	}
	if !st.ComicExists(twinIssueID) {
		t.Fatal("twin issue id changed")
	}

	req = httptest.NewRequest("PUT", "/series/"+id+"/rename-folder", strings.NewReader(`{"name":"Rook - Exodus (2024)"}`))
	req.SetPathValue("id", id)
	w = httptest.NewRecorder()
	srv.HandleRenameFolder(w, req)
	if w.Code != 409 {
		t.Fatalf("rename-folder: %d %s, want 409", w.Code, w.Body.String())
	}
	if _, err := os.Stat(organized); err != nil {
		t.Fatalf("primary folder renamed: %v", err)
	}
}

// The app's single-comic rename must not rename a file in Mylar's folder.
func TestRenameComicRefusesTwinFolderFile(t *testing.T) {
	root, _, twin, st := twinLib(t)
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	srv := &Server{ComicDirs: []string{root}, Store: st, Cache: cache.NewLRU(10)}
	req := httptest.NewRequest("PUT", "/comics/"+twinIssueID+"/rename", strings.NewReader(`{"name":"Rook: Exodus #11 (2026)"}`))
	req.SetPathValue("id", twinIssueID)
	w := httptest.NewRecorder()
	srv.HandleRenameComic(w, req)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("rename: %d %s, want 409 JSON error", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(twin, "Rook Exodus 11 (2026).cbz")); err != nil {
		t.Fatalf("twin file renamed: %v", err)
	}
}

// "X 1" in a twin is the primary's "X 001": zero padding is not a new issue.
func TestScanTwinDuplicateIgnoresZeroPadding(t *testing.T) {
	root, organized, twin, st := twinLib(t)
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	createTestCBZWithPublisher(t, filepath.Join(organized, "Rook: Exodus 005 (2025).cbz"), "Johns", "Image")
	createTestCBZWithPublisher(t, filepath.Join(twin, "Rook Exodus 5 (2025).cbz"), "Johns", "Image")
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	issues, _ := st.ListIssues(store.SlugifyName("Rook Exodus (2024)"))
	for _, iss := range issues {
		if iss.Filename == "Rook Exodus 5 (2025).cbz" {
			t.Fatalf("zero-padded duplicate listed twice: %+v", issues)
		}
	}
	if len(issues) != 4 {
		t.Fatalf("issues = %+v", issues)
	}
}

func filenames(st *store.Store, id string) []string {
	issues, _ := st.ListIssues(id)
	var out []string
	for _, iss := range issues {
		out = append(out, iss.Filename)
	}
	return out
}

// A twin issue that fills a gap is positioned in number order.
func TestScanTwinIssueSortsByNumber(t *testing.T) {
	root := t.TempDir()
	organized := filepath.Join(root, "Saga: X (2012)")
	twin := filepath.Join(root, "Saga X (2012)")
	os.MkdirAll(organized, 0755)
	os.MkdirAll(twin, 0755)
	for _, n := range []string{"2", "4"} {
		createTestCBZWithPublisher(t, filepath.Join(organized, "Saga: X #"+n+" (2012).cbz"), "V", "Image")
	}
	createTestCBZWithPublisher(t, filepath.Join(twin, "Saga X 3 (2012).cbz"), "V", "Image")
	createTestCBZWithPublisher(t, filepath.Join(twin, "Saga X 9 (2013).cbz"), "V", "Image")
	st, _ := store.NewStore(t.TempDir() + "/t.db")
	defer st.Close()
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(filenames(st, store.SlugifyName("Saga X (2012)")), " | ")
	want := "Saga: X #2 (2012).cbz | Saga X 3 (2012).cbz | Saga: X #4 (2012).cbz | Saga X 9 (2013).cbz"
	if got != want {
		t.Fatalf("order = %s\nwant    %s", got, want)
	}
}

// A single-folder series keeps the scanner's natural filename order, even
// where that differs from number order.
func TestScanSingleFolderOrderUnchanged(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Saga (2012)")
	os.MkdirAll(dir, 0755)
	names := []string{"Saga 002 (2012).cbz", "Saga 010 (2012).cbz", "Saga Annual 001 (2013).cbz", "Saga 001 (2012).cbz"}
	for _, n := range names {
		createTestCBZWithPublisher(t, filepath.Join(dir, n), "V", "Image")
	}
	st, _ := store.NewStore(t.TempDir() + "/t.db")
	defer st.Close()
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(filenames(st, "saga-2012"), " | ")
	want := "Saga 001 (2012).cbz | Saga 002 (2012).cbz | Saga 010 (2012).cbz | Saga Annual 001 (2013).cbz"
	if got != want {
		t.Fatalf("order = %s\nwant    %s", got, want)
	}
}
