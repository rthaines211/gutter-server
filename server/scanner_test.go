package server

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/server/admin"
	"github.com/rthaines211/gutter-server/store"
)

// createTestCBZ creates a minimal .cbz file with a ComicInfo.xml and a fake page image.
func createTestCBZ(t *testing.T, path string, writer string, penciller string, number string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)

	type comicInfo struct {
		XMLName   xml.Name `xml:"ComicInfo"`
		Title     string   `xml:"Title"`
		Writer    string   `xml:"Writer"`
		Penciller string   `xml:"Penciller"`
		Number    string   `xml:"Number"`
	}
	ci := comicInfo{
		Title:     filepath.Base(path),
		Writer:    writer,
		Penciller: penciller,
		Number:    number,
	}
	ciW, err := zw.Create("ComicInfo.xml")
	if err != nil {
		t.Fatal(err)
	}
	xml.NewEncoder(ciW).Encode(ci)

	pw, err := zw.Create("page001.jpg")
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	jpeg.Encode(pw, img, nil)

	zw.Close()
}

func TestScanLibrary(t *testing.T) {
	dir := t.TempDir()

	batmanDir := filepath.Join(dir, "Batman (2016)")
	xmenDir := filepath.Join(dir, "X-Men")
	os.MkdirAll(batmanDir, 0755)
	os.MkdirAll(xmenDir, 0755)

	createTestCBZ(t, filepath.Join(batmanDir, "Batman 001.cbz"), "Tom King", "David Finch", "001")
	createTestCBZ(t, filepath.Join(batmanDir, "Batman 002.cbz"), "Tom King", "David Finch", "002")
	createTestCBZ(t, filepath.Join(xmenDir, "X-Men 001.cbz"), "Hickman", "Yu", "001")
	createTestCBZ(t, filepath.Join(dir, "loose.cbz"), "", "", "")
	os.WriteFile(filepath.Join(batmanDir, "readme.txt"), []byte("ignore"), 0644)

	srv := newTestServer(t)
	srv.ComicDirs = []string{dir}

	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	err := ScanLibrary([]string{dir}, srv.Store, coverDir, nil)
	if err != nil {
		t.Fatalf("ScanLibrary failed: %v", err)
	}

	series, err := srv.Store.ListSeries()
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(series))
	}

	batman, err := srv.Store.GetSeries("batman-2016")
	if err != nil {
		t.Fatalf("GetSeries batman failed: %v", err)
	}
	if batman.Name != "Batman (2016)" {
		t.Errorf("expected name 'Batman (2016)', got %q", batman.Name)
	}
	if batman.IssueCount != 2 {
		t.Errorf("expected 2 issues, got %d", batman.IssueCount)
	}
	if batman.Writer != "Tom King" {
		t.Errorf("expected writer 'Tom King', got %q", batman.Writer)
	}
	if batman.Penciller != "David Finch" {
		t.Errorf("expected penciller 'David Finch', got %q", batman.Penciller)
	}

	xmen, err := srv.Store.GetSeries("x-men")
	if err != nil {
		t.Fatalf("GetSeries x-men failed: %v", err)
	}
	if xmen.IssueCount != 1 {
		t.Errorf("expected 1 issue, got %d", xmen.IssueCount)
	}

	// Verify series cover thumbnails were created
	batmanThumb := filepath.Join(coverDir, "series_batman-2016.jpg")
	if _, err := os.Stat(batmanThumb); err != nil {
		t.Errorf("batman series thumbnail not created: %v", err)
	}
	xmenThumb := filepath.Join(coverDir, "series_x-men.jpg")
	if _, err := os.Stat(xmenThumb); err != nil {
		t.Errorf("x-men series thumbnail not created: %v", err)
	}
}

// Mirror of TestOrganizeGuardBlockedByScan (server/admin/organize_test.go):
// a scan must refuse to start while an organize apply is running, since
// organize renames/moves folders on disk and a concurrent scan would see a
// stale directory listing and CASCADE-delete series whose folders just moved.
func TestScanLibrarySkippedWhileOrganizeRunning(t *testing.T) {
	dir := t.TempDir()
	seriesDir := filepath.Join(dir, "Batman (2016)")
	os.MkdirAll(seriesDir, 0755)
	createTestCBZ(t, filepath.Join(seriesDir, "Batman 001.cbz"), "Tom King", "David Finch", "001")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	ss := admin.NewScanState()
	ss.SetOrganizeRunning(true)

	if err := ScanLibrary([]string{dir}, srv.Store, coverDir, ss); err != nil {
		t.Fatalf("ScanLibrary returned error instead of skipping: %v", err)
	}
	if ss.Snapshot().IsScanning {
		t.Error("scan should not have started while organize is running")
	}
	series, _ := srv.Store.ListSeries()
	if len(series) != 0 {
		t.Errorf("expected scan to be skipped (no series added), got %d", len(series))
	}
}

func TestScanLibraryRemovesDeleted(t *testing.T) {
	dir := t.TempDir()

	seriesDir := filepath.Join(dir, "ToRemove")
	os.MkdirAll(seriesDir, 0755)
	createTestCBZ(t, filepath.Join(seriesDir, "issue.cbz"), "", "", "")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	ScanLibrary([]string{dir}, srv.Store, coverDir, nil)

	list, _ := srv.Store.ListSeries()
	if len(list) != 1 {
		t.Fatalf("expected 1 series, got %d", len(list))
	}

	os.RemoveAll(seriesDir)
	ScanLibrary([]string{dir}, srv.Store, coverDir, nil)

	list, _ = srv.Store.ListSeries()
	if len(list) != 0 {
		t.Fatalf("expected 0 series after removal, got %d", len(list))
	}
}

func TestScanLibraryMultipleDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	series1 := filepath.Join(dir1, "SeriesA")
	series2 := filepath.Join(dir2, "SeriesB")
	os.MkdirAll(series1, 0755)
	os.MkdirAll(series2, 0755)
	createTestCBZ(t, filepath.Join(series1, "a.cbz"), "", "", "")
	createTestCBZ(t, filepath.Join(series2, "b.cbz"), "", "", "")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	ScanLibrary([]string{dir1, dir2}, srv.Store, coverDir, nil)

	list, _ := srv.Store.ListSeries()
	if len(list) != 2 {
		t.Fatalf("expected 2 series, got %d", len(list))
	}
}

func TestNaturalLess(t *testing.T) {
	input := []string{"21.cbz", "2.cbz", "12.cbz", "11.cbz", "10.cbz", "1.cbz"}
	want := []string{"1.cbz", "2.cbz", "10.cbz", "11.cbz", "12.cbz", "21.cbz"}
	got := append([]string(nil), input...)
	sortStrings(got, naturalLess)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("naturalLess sort = %v, want %v", got, want)
			break
		}
	}

	cases := []struct {
		a, b string
		less bool
	}{
		{"Batman 002.cbz", "Batman 10.cbz", true},
		{"Batman 10.cbz", "Batman 2.cbz", false},
		{"issue-001.cbz", "issue-1.cbz", true}, // shorter raw run wins on tie
		{"a.cbz", "B.cbz", true},               // case-insensitive
		{"foo10.cbz", "foo10a.cbz", true},
	}
	for _, c := range cases {
		if got := naturalLess(c.a, c.b); got != c.less {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", c.a, c.b, got, c.less)
		}
	}
}

func sortStrings(s []string, less func(a, b string) bool) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Batman (2016)", "batman-2016"},
		{"X-Men", "x-men"},
		{"  Spaces  Everywhere  ", "spaces-everywhere"},
		{"UPPER CASE", "upper-case"},
		{"special!@#chars", "special-chars"},
		{"multiple---dashes", "multiple-dashes"},
	}
	for _, tt := range tests {
		got := slugify(tt.input)
		if got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAggregateMetadataMostCommon(t *testing.T) {
	values := []string{"DC Comics", "DC Comics", "Image Comics", "DC Comics"}
	got := mostCommon(values)
	if got != "DC Comics" {
		t.Errorf("mostCommon: got %q, want %q", got, "DC Comics")
	}
}

func TestAggregateMetadataMostCommonEmpty(t *testing.T) {
	got := mostCommon(nil)
	if got != "" {
		t.Errorf("mostCommon(nil): got %q, want empty", got)
	}
}

func TestAggregateMetadataMostCommonSkipsEmpty(t *testing.T) {
	values := []string{"", "", "Image Comics", ""}
	got := mostCommon(values)
	if got != "Image Comics" {
		t.Errorf("mostCommon: got %q, want %q", got, "Image Comics")
	}
}

func TestEarliestYear(t *testing.T) {
	years := []int{2018, 0, 2016, 2020}
	got := earliestYear(years)
	if got != 2016 {
		t.Errorf("earliestYear: got %d, want %d", got, 2016)
	}
}

func TestEarliestYearAllZero(t *testing.T) {
	years := []int{0, 0, 0}
	got := earliestYear(years)
	if got != 0 {
		t.Errorf("earliestYear: got %d, want 0", got)
	}
}

func TestCollectGenres(t *testing.T) {
	rawGenres := []string{
		"Superhero, Action, Crime",
		"Action, Drama",
		"Superhero",
		"",
	}
	got := collectGenres(rawGenres)
	expected := map[string]bool{"Superhero": true, "Action": true, "Crime": true, "Drama": true}
	if len(got) != len(expected) {
		t.Fatalf("collectGenres: got %d genres, want %d", len(got), len(expected))
	}
	for _, g := range got {
		if !expected[g] {
			t.Errorf("unexpected genre: %q", g)
		}
	}
}

func TestCollectGenresEmpty(t *testing.T) {
	got := collectGenres(nil)
	if len(got) != 0 {
		t.Errorf("collectGenres(nil): got %d, want 0", len(got))
	}
}

func TestScanLibraryIncremental(t *testing.T) {
	dir := t.TempDir()

	batmanDir := filepath.Join(dir, "Batman")
	os.MkdirAll(batmanDir, 0755)
	createTestCBZ(t, filepath.Join(batmanDir, "Batman 001.cbz"), "King", "Finch", "001")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	// First scan — full processing
	ss := admin.NewScanState()
	ScanLibrary([]string{dir}, srv.Store, coverDir, ss)

	snap := ss.Snapshot()
	if snap.NewCount != 1 {
		t.Errorf("first scan: expected new_count=1, got %d", snap.NewCount)
	}

	// Second scan — same series, same issue count → should skip (new_count=0)
	ss2 := admin.NewScanState()
	ScanLibrary([]string{dir}, srv.Store, coverDir, ss2)

	snap2 := ss2.Snapshot()
	if snap2.NewCount != 0 {
		t.Errorf("incremental scan: expected new_count=0, got %d", snap2.NewCount)
	}

	// Add an issue → should re-process (issue count changed)
	createTestCBZ(t, filepath.Join(batmanDir, "Batman 002.cbz"), "King", "Finch", "002")

	ss3 := admin.NewScanState()
	ScanLibrary([]string{dir}, srv.Store, coverDir, ss3)

	series, _ := srv.Store.GetSeries("batman")
	if series.IssueCount != 2 {
		t.Errorf("after adding issue: expected issue_count=2, got %d", series.IssueCount)
	}

	// Add a new series → new_count should be 1
	xmenDir := filepath.Join(dir, "X-Men")
	os.MkdirAll(xmenDir, 0755)
	createTestCBZ(t, filepath.Join(xmenDir, "X-Men 001.cbz"), "Hickman", "Yu", "001")

	ss4 := admin.NewScanState()
	ScanLibrary([]string{dir}, srv.Store, coverDir, ss4)

	snap4 := ss4.Snapshot()
	if snap4.NewCount != 1 {
		t.Errorf("new series scan: expected new_count=1, got %d", snap4.NewCount)
	}
}

func TestIssueNumberFromFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Batman 004.cbz", "4"},
		{"Batman #12.cbz", "12"},
		{"Batman 012.5.cbz", "12.5"},
		{"Batman Vol. 2 005.cbz", "5"},
		{"Saga 001 - The Beginning.cbz", "1"},
		{"X-Men 001 (2016).cbz", "1"},
		{"Daredevil v3 010 [Digital].cbr", "10"},
		{"X-Men 2099 014.cbz", ""},  // two numeric tokens → ambiguous, defer to ComicInfo
		{"Special Edition.cbz", ""}, // no issue token
		{"001.cbz", "1"},
	}
	for _, c := range cases {
		if got := issueNumberFromFilename(c.in); got != c.want {
			t.Errorf("issueNumberFromFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractMetadataAndCover(t *testing.T) {
	dir := t.TempDir()
	comic := filepath.Join(dir, "Batman 001.cbz")
	createTestCBZ(t, comic, "Tom King", "David Finch", "001")
	thumb := filepath.Join(dir, "thumb.jpg")

	m, ok := extractMetadataAndCover(comic, thumb)
	if !ok {
		t.Fatal("expected ok=true for a valid archive")
	}
	if m.Writer != "Tom King" || m.Penciller != "David Finch" {
		t.Errorf("metadata = %+v, want writer/penciller Tom King/David Finch", m)
	}
	if _, err := os.Stat(thumb); err != nil {
		t.Errorf("thumbnail not written: %v", err)
	}

	// A non-archive file must report ok=false (drive/corruption signal).
	bad := filepath.Join(dir, "bad.cbz")
	os.WriteFile(bad, []byte("not a zip"), 0644)
	if _, ok := extractMetadataAndCover(bad, filepath.Join(dir, "bad.jpg")); ok {
		t.Error("expected ok=false for an unreadable archive")
	}
}

func TestScanAbortsOnUnreadableRoot(t *testing.T) {
	dir := t.TempDir()
	sdir := filepath.Join(dir, "Keepers")
	os.MkdirAll(sdir, 0755)
	createTestCBZ(t, filepath.Join(sdir, "Keepers 001.cbz"), "", "", "001")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	// First scan populates the library.
	if err := ScanLibrary([]string{dir}, srv.Store, coverDir, nil); err != nil {
		t.Fatalf("initial scan failed: %v", err)
	}
	if list, _ := srv.Store.ListSeries(); len(list) != 1 {
		t.Fatalf("expected 1 series after initial scan, got %d", len(list))
	}

	// Second scan with a missing root must abort and must NOT prune.
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	err := ScanLibrary([]string{missing}, srv.Store, coverDir, nil)
	if err == nil {
		t.Fatal("expected error when a root is unreadable")
	}
	if list, _ := srv.Store.ListSeries(); len(list) != 1 {
		t.Fatalf("series were pruned despite unreadable root: got %d, want 1", len(list))
	}
}

func TestScanUsesFilenameNumbers(t *testing.T) {
	dir := t.TempDir()
	sdir := filepath.Join(dir, "Saga")
	os.MkdirAll(sdir, 0755)
	// ComicInfo Number is deliberately blank; number must come from filename.
	createTestCBZ(t, filepath.Join(sdir, "Saga 007.cbz"), "BKV", "Fiona", "")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	if err := ScanLibrary([]string{dir}, srv.Store, coverDir, nil); err != nil {
		t.Fatalf("ScanLibrary failed: %v", err)
	}
	issues, err := srv.Store.ListIssues("saga")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Number != "7" {
		t.Errorf("expected issue number from filename %q, got %q", "7", issues[0].Number)
	}
}

func TestSetScanFileDelay(t *testing.T) {
	// Default is zero; a scan with a small delay still completes correctly.
	SetScanFileDelay(1 * time.Millisecond)
	defer SetScanFileDelay(0)

	dir := t.TempDir()
	sdir := filepath.Join(dir, "Delayed")
	os.MkdirAll(sdir, 0755)
	createTestCBZ(t, filepath.Join(sdir, "Delayed 001.cbz"), "", "", "001")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	if err := ScanLibrary([]string{dir}, srv.Store, coverDir, nil); err != nil {
		t.Fatalf("ScanLibrary failed: %v", err)
	}
	if list, _ := srv.Store.ListSeries(); len(list) != 1 {
		t.Fatalf("expected 1 series, got %d", len(list))
	}
}

func TestScanAbortsOnConsecutiveFailures(t *testing.T) {
	dir := t.TempDir()
	// Six new series whose only file is an unreadable archive. Each fails
	// extractMetadataAndCover; the run should abort once the threshold (5)
	// consecutive failures is hit.
	for i := 0; i < 6; i++ {
		sdir := filepath.Join(dir, fmt.Sprintf("Series%02d", i))
		os.MkdirAll(sdir, 0755)
		os.WriteFile(filepath.Join(sdir, "issue 001.cbz"), []byte("not a zip"), 0644)
	}

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	err := ScanLibrary([]string{dir}, srv.Store, coverDir, nil)
	if err == nil {
		t.Fatal("expected scan to abort on consecutive archive-open failures")
	}
}

func TestScanTimeoutFor(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int64) string {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return p
	}
	small := write("small.cbz", 100<<20) // 0 GB -> base
	if got := scanTimeoutFor(small); got != scanFileTimeout {
		t.Fatalf("small: got %s, want %s", got, scanFileTimeout)
	}
	big := write("big.cbz", 2688<<20) // ~2.6 GB -> 2 GB step
	if got, want := scanTimeoutFor(big), scanFileTimeout+2*60*time.Second; got != want {
		t.Fatalf("big: got %s, want %s", got, want)
	}
	huge := write("huge.cbz", 20<<30) // clamps to 5min
	if got := scanTimeoutFor(huge); got != 5*time.Minute {
		t.Fatalf("huge: got %s, want 5m0s", got)
	}
	if got := scanTimeoutFor(filepath.Join(dir, "missing.cbz")); got != scanFileTimeout {
		t.Fatalf("missing: got %s, want %s", got, scanFileTimeout)
	}
}

func TestScanLibraryPicksUpRename(t *testing.T) {
	dir := t.TempDir()
	seriesDir := filepath.Join(dir, "Renamed")
	os.MkdirAll(seriesDir, 0755)
	createTestCBZ(t, filepath.Join(seriesDir, "old.cbz"), "", "", "")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)
	ScanLibrary([]string{dir}, srv.Store, coverDir, nil)

	// Same folder, same count — only the filename changes.
	os.Rename(filepath.Join(seriesDir, "old.cbz"), filepath.Join(seriesDir, "new.cbz"))
	ScanLibrary([]string{dir}, srv.Store, coverDir, nil)

	issues, _ := srv.Store.ListIssues("renamed")
	if len(issues) != 1 || issues[0].Filename != "new.cbz" {
		t.Fatalf("expected renamed issue new.cbz, got %+v", issues)
	}
}

// createTestCBZWithPublisher is createTestCBZ plus a <Publisher> tag, which the
// plain helper omits.
func createTestCBZWithPublisher(t *testing.T, path, writer, publisher string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	type comicInfo struct {
		XMLName   xml.Name `xml:"ComicInfo"`
		Title     string   `xml:"Title"`
		Writer    string   `xml:"Writer"`
		Publisher string   `xml:"Publisher"`
	}
	ciW, err := zw.Create("ComicInfo.xml")
	if err != nil {
		t.Fatal(err)
	}
	xml.NewEncoder(ciW).Encode(comicInfo{
		Title:     filepath.Base(path),
		Writer:    writer,
		Publisher: publisher,
	})

	pw, err := zw.Create("page001.jpg")
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	jpeg.Encode(pw, img, nil)
	zw.Close()
}

// A series whose row has no metadata must be re-read on the next scan even
// though its files are unchanged. Without the hasMetadata term in the skip
// guard, a row written by a failed extraction stays blank forever: the file
// identity checks all pass, so the scan skips it and never retries. This is
// what left 120 series blank in the live library while their first issue's
// ComicInfo.xml held the publisher all along.
func TestScanLibraryRetriesSeriesWithMissingMetadata(t *testing.T) {
	dir := t.TempDir()
	seriesDir := filepath.Join(dir, "Detective Comics (2016)")
	os.MkdirAll(seriesDir, 0755)
	createTestCBZWithPublisher(t,
		filepath.Join(seriesDir, "Detective Comics 001.cbz"), "James Tynion IV", "DC Comics")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	if err := ScanLibrary([]string{dir}, srv.Store, coverDir, nil); err != nil {
		t.Fatalf("initial scan: %v", err)
	}

	// Simulate the frozen row: blank the metadata as a failed extraction would,
	// leaving issue count, folder and filenames untouched.
	if _, err := srv.Store.DB().Exec(
		`UPDATE series SET publisher='', writer='' WHERE id=?`,
		"detective-comics-2016"); err != nil {
		t.Fatal(err)
	}

	if err := ScanLibrary([]string{dir}, srv.Store, coverDir, nil); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	got, err := srv.Store.GetSeries("detective-comics-2016")
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if got.Publisher != "DC Comics" {
		t.Errorf("publisher after rescan = %q, want %q — the blank row was skipped again",
			got.Publisher, "DC Comics")
	}
	if got.Writer != "James Tynion IV" {
		t.Errorf("writer after rescan = %q, want %q", got.Writer, "James Tynion IV")
	}
}

// The complement: a row that already has metadata must still be skipped, or
// every scan re-reads the whole library.
func TestScanLibrarySkipsSeriesThatHaveMetadata(t *testing.T) {
	dir := t.TempDir()
	seriesDir := filepath.Join(dir, "Saga (2012)")
	os.MkdirAll(seriesDir, 0755)
	createTestCBZWithPublisher(t,
		filepath.Join(seriesDir, "Saga 001.cbz"), "Brian K. Vaughan", "Image")

	srv := newTestServer(t)
	coverDir := filepath.Join(t.TempDir(), "covers")
	os.MkdirAll(coverDir, 0755)

	if err := ScanLibrary([]string{dir}, srv.Store, coverDir, nil); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	// Canonicalized on write: "Image" is stored as "Image Comics".
	if _, err := srv.Store.DB().Exec(
		`UPDATE series SET publisher='SENTINEL' WHERE id=?`, "saga-2012"); err != nil {
		t.Fatal(err)
	}
	if err := ScanLibrary([]string{dir}, srv.Store, coverDir, nil); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	got, err := srv.Store.GetSeries("saga-2012")
	if err != nil {
		t.Fatal(err)
	}
	if got.Publisher != "SENTINEL" {
		t.Errorf("publisher = %q, want SENTINEL — a populated row was re-read unnecessarily",
			got.Publisher)
	}
}

// Every series a scan creates or changes is queued for the auto pipeline;
// an unchanged rescan queues nothing.
func TestScanMarksChangedSeriesPending(t *testing.T) {
	root := t.TempDir()
	seriesDir := filepath.Join(root, "Saga (2012)")
	os.MkdirAll(seriesDir, 0755)
	createTestCBZWithPublisher(t, filepath.Join(seriesDir, "Saga 001.cbz"), "BKV", "Image")

	st, err := store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	coverDir := t.TempDir()
	os.MkdirAll(coverDir, 0755)

	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	ids, _ := st.ListPipelinePending()
	if len(ids) != 1 {
		t.Fatalf("after first scan pending = %v, want 1", ids)
	}

	if err := st.RemovePipelinePending(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("unchanged rescan queued %v", ids)
	}

	createTestCBZWithPublisher(t, filepath.Join(seriesDir, "Saga 002.cbz"), "BKV", "Image")
	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 1 {
		t.Fatalf("new issue did not queue series: %v", ids)
	}
}

// F2: a series with no ComicInfo is re-extracted on every scan (hasMetadata),
// but that alone is not a change — it must not requeue the pipeline.
func TestScanDoesNotRequeueUnchangedSeriesWithoutMetadata(t *testing.T) {
	root := t.TempDir()
	seriesDir := filepath.Join(root, "Saga (2012)")
	os.MkdirAll(seriesDir, 0755)
	createTestCBZWithPublisher(t, filepath.Join(seriesDir, "Saga 001.cbz"), "", "") // no writer/publisher

	st, err := store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	coverDir := t.TempDir()

	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	ids, _ := st.ListPipelinePending()
	if len(ids) != 1 {
		t.Fatalf("after first scan pending = %v, want 1", ids)
	}
	if err := st.RemovePipelinePending(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("unchanged metadata-less series requeued: %v", ids)
	}
}

// Production loop (Oct 2026): Mylar drops new issues into "Rook Exodus (2024)"
// while the organized series lives in "Rook: Exodus (2024)". Both slugify to
// the same series id, so every scan replaced the issue rows twice — the
// second ReplaceIssues recreated the organized folder's rows, and the
// ON DELETE CASCADE wiped their issue_match rows — and queued the series for
// the pipeline, which re-bound and re-wrote every issue every 30 minutes.
// The folder already on the series row must stay primary, the twin's issue
// joins the series once, and an unchanged rescan must be a no-op.
func TestScanTwinSlugFoldersAreStable(t *testing.T) {
	root := t.TempDir()
	organized := filepath.Join(root, "Rook: Exodus (2024)")
	twin := filepath.Join(root, "Rook Exodus (2024)")
	os.MkdirAll(organized, 0755)
	os.MkdirAll(twin, 0755)
	createTestCBZWithPublisher(t, filepath.Join(organized, "Rook: Exodus #1 (2024).cbz"), "Johns", "Image")
	createTestCBZWithPublisher(t, filepath.Join(organized, "Rook: Exodus #2 (2024).cbz"), "Johns", "Image")
	createTestCBZWithPublisher(t, filepath.Join(twin, "Rook Exodus 11 (2026).cbz"), "Johns", "Image")

	st, err := store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	coverDir := t.TempDir()

	// The series row already points at the organized folder (as in production).
	id := store.SlugifyName("Rook: Exodus (2024)")
	if err := st.UpsertSeries(store.Series{ID: id, Name: "Rook: Exodus (2024)", FolderPath: organized, IssueCount: 2, Publisher: "Image"}); err != nil {
		t.Fatal(err)
	}
	iss := []store.Issue{
		{ID: store.PathToID("Rook: Exodus (2024)/Rook: Exodus #1 (2024).cbz"), SeriesID: id, Filename: "Rook: Exodus #1 (2024).cbz", Number: "1", Position: 0},
		{ID: store.PathToID("Rook: Exodus (2024)/Rook: Exodus #2 (2024).cbz"), SeriesID: id, Filename: "Rook: Exodus #2 (2024).cbz", Number: "2", Position: 1},
	}
	if err := st.ReplaceIssues(id, iss); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssueMatch(store.IssueMatch{IssueID: iss[0].ID, Source: store.SourceComicVine, UpstreamID: "1", State: "high_confidence"}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 0 {
			want = 1 // the twin's #11 is new to the series
		}
		if ids, _ := st.ListPipelinePending(); len(ids) != want {
			t.Fatalf("scan %d queued %v, want %d", i+1, ids, want)
		}
		st.RemovePipelinePending(id)
		s, err := st.GetSeries(id)
		if err != nil {
			t.Fatal(err)
		}
		if s.FolderPath != organized {
			t.Fatalf("scan %d: folder_path = %q, want the organized folder", i+1, s.FolderPath)
		}
		if ims, _ := st.ListIssueMatchesForSeries(id); len(ims) != 1 {
			t.Fatalf("scan %d: issue matches = %d, want 1 (cascade wiped them)", i+1, len(ims))
		}
	}
}

// The server's own writeback rewrites ComicInfo.xml in place (new size and
// mtime, same filename). Change detection is by filename set and folder, so
// a rewritten file must not re-queue its series for the pipeline.
func TestScanIgnoresInPlaceRewrite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Saga (2012)")
	os.MkdirAll(dir, 0755)
	f := filepath.Join(dir, "Saga 001.cbz")
	createTestCBZWithPublisher(t, f, "BKV", "Image")

	st, err := store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	coverDir := t.TempDir()
	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	ids, _ := st.ListPipelinePending()
	for _, id := range ids {
		st.RemovePipelinePending(id)
	}

	createTestCBZWithPublisher(t, f, "Brian K. Vaughan", "Image Comics") // rewritten contents
	later := time.Now().Add(time.Hour)
	os.Chtimes(f, later, later)
	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("in-place rewrite queued %v", ids)
	}
}

// The DB can hold a folder name that differs from disk only by case (an
// earlier organize "renamed" it on a case-insensitive volume that kept the
// old spelling). The scan must adopt the disk spelling by migrating the
// issue ids — not by replacing the rows, which wipes their matches and
// orphans reading progress — and must not queue the series.
func TestScanAdoptsCaseOnlyFolderDrift(t *testing.T) {
	root := t.TempDir()
	disk := "Uncanny X-Men By Kieron Gillen (2012)"
	dbName := "Uncanny X-Men by Kieron Gillen (2012)"
	fn := "Uncanny X-Men by Kieron Gillen #1 (2012).cbz"
	os.MkdirAll(filepath.Join(root, disk), 0755)
	createTestCBZWithPublisher(t, filepath.Join(root, disk, fn), "Gillen", "Marvel")

	st, err := store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id := store.SlugifyName(disk)
	oldID := store.PathToID(dbName + "/" + fn)
	if err := st.UpsertSeries(store.Series{ID: id, Name: dbName, FolderPath: filepath.Join(root, dbName), IssueCount: 1, Publisher: "Marvel"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues(id, []store.Issue{{ID: oldID, SeriesID: id, Filename: fn, Number: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssueMatch(store.IssueMatch{IssueID: oldID, Source: store.SourceComicVine, UpstreamID: "1", State: "high_confidence"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProgress(store.Progress{ComicID: oldID, CurrentPage: 109, TotalPages: 109}); err != nil {
		t.Fatal(err)
	}

	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("case-only drift queued %v", ids)
	}
	s, _ := st.GetSeries(id)
	if s.FolderPath != filepath.Join(root, disk) || s.Name != disk {
		t.Fatalf("series = %q %q, want the disk spelling", s.Name, s.FolderPath)
	}
	newID := store.PathToID(disk + "/" + fn)
	if ims, _ := st.ListIssueMatchesForSeries(id); len(ims) != 1 || ims[0].IssueID != newID {
		t.Fatalf("issue matches = %+v, want one under the new id", ims)
	}
	if p, err := st.GetProgress(newID); err != nil || p == nil || p.CurrentPage != 109 {
		t.Fatalf("progress = %+v, %v; want it carried to the new id", p, err)
	}
}

// caseDriftFixture: DB says folder "by" / file "By"; disk has folder "By" /
// file "by" — both differ only by case. The issue carries a match, an
// enrichment row (both CASCADE on delete), progress and a thumbnail.
func caseDriftFixture(t *testing.T) (root, coverDir string, st *store.Store, id, oldID, newID string) {
	t.Helper()
	root = t.TempDir()
	coverDir = t.TempDir()
	diskDir, dbDir := "Uncanny X-Men By Kieron Gillen (2012)", "Uncanny X-Men by Kieron Gillen (2012)"
	diskFn, dbFn := "Uncanny X-Men by Kieron Gillen #1 (2012).cbz", "Uncanny X-Men By Kieron Gillen #1 (2012).cbz"
	os.MkdirAll(filepath.Join(root, diskDir), 0755)
	createTestCBZWithPublisher(t, filepath.Join(root, diskDir, diskFn), "Gillen", "Marvel")

	var err error
	st, err = store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id = store.SlugifyName(diskDir)
	oldID = store.PathToID(dbDir + "/" + dbFn)
	newID = store.PathToID(diskDir + "/" + diskFn)
	if err := st.UpsertSeries(store.Series{ID: id, Name: dbDir, FolderPath: filepath.Join(root, dbDir), IssueCount: 1, Publisher: "Marvel"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues(id, []store.Issue{{ID: oldID, SeriesID: id, Filename: dbFn, Number: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssueMatch(store.IssueMatch{IssueID: oldID, Source: store.SourceComicVine, UpstreamID: "1", State: "high_confidence"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO issue_enrichment(issue_id, comicvine_issue_id, enriched_at) VALUES (?, 1, 1)`, oldID); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProgress(store.Progress{ComicID: oldID, CurrentPage: 109, TotalPages: 109}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE series SET updated_at = '2000-01-01 00:00:00' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(coverDir, "comic_"+oldID+".jpg"), []byte("x"), 0644)
	return
}

// Review fix 1/3/6: folder AND file differ only by case. The adopt pairs DB
// rows with disk files case-insensitively, takes the disk spelling for ids
// and filenames, keeps match/enrichment/progress (no ReplaceIssues cascade),
// bumps updated_at for /sync, and drops the stale thumbnail.
func TestScanAdoptsCaseOnlyFolderAndFileDrift(t *testing.T) {
	root, coverDir, st, id, oldID, newID := caseDriftFixture(t)

	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("case-only drift queued %v", ids)
	}
	iss, _ := st.ListIssues(id)
	if len(iss) != 1 || iss[0].ID != newID || iss[0].Filename != "Uncanny X-Men by Kieron Gillen #1 (2012).cbz" {
		t.Fatalf("issues = %+v, want the disk spelling", iss)
	}
	if ims, _ := st.ListIssueMatchesForSeries(id); len(ims) != 1 || ims[0].IssueID != newID {
		t.Fatalf("issue matches = %+v", ims)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM issue_enrichment WHERE issue_id = ?`, newID).Scan(&n)
	if n != 1 {
		t.Fatal("issue_enrichment lost (ReplaceIssues cascade)")
	}
	if p, err := st.GetProgress(newID); err != nil || p == nil || p.CurrentPage != 109 {
		t.Fatalf("progress = %+v, %v", p, err)
	}
	if s, _ := st.GetSeries(id); s.UpdatedAt.Year() < 2020 {
		t.Fatalf("series updated_at not bumped: %v", s.UpdatedAt)
	}
	if _, err := os.Stat(filepath.Join(coverDir, "comic_"+oldID+".jpg")); !os.IsNotExist(err) {
		t.Fatal("stale thumbnail left behind")
	}
}

// Review fix 2: if the adopt fails, the scan leaves the series alone (the
// next scan retries) rather than falling through to the destructive replace.
func TestScanSkipsSeriesWhenCaseAdoptFails(t *testing.T) {
	root, coverDir, st, id, oldID, _ := caseDriftFixture(t)
	orig := adoptCase
	adoptCase = func(*store.Store, *store.Series, string, string, []string, string) error { return fmt.Errorf("boom") }
	defer func() { adoptCase = orig }()

	if err := ScanLibrary([]string{root}, st, coverDir, nil); err != nil {
		t.Fatal(err)
	}
	if ims, _ := st.ListIssueMatchesForSeries(id); len(ims) != 1 || ims[0].IssueID != oldID {
		t.Fatalf("issue matches = %+v, want untouched", ims)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("queued %v", ids)
	}
}

// Review fix 4: with no series row (fresh or rebuilt DB), the organized
// folder with more issues wins over a downloader's colon-less twin, even
// though the twin sorts first bytewise.
func TestScanTwinWithoutRowPrefersFullerFolder(t *testing.T) {
	root := t.TempDir()
	organized := filepath.Join(root, "Rook: Exodus (2024)")
	twin := filepath.Join(root, "Rook Exodus (2024)")
	os.MkdirAll(organized, 0755)
	os.MkdirAll(twin, 0755)
	for _, n := range []string{"1", "2"} {
		createTestCBZWithPublisher(t, filepath.Join(organized, "Rook: Exodus #"+n+" (2024).cbz"), "Johns", "Image")
	}
	createTestCBZWithPublisher(t, filepath.Join(twin, "Rook Exodus 11 (2026).cbz"), "Johns", "Image")

	st, err := store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := ScanLibrary([]string{root}, st, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	s, err := st.GetSeries(store.SlugifyName("Rook: Exodus (2024)"))
	if err != nil {
		t.Fatal(err)
	}
	if s.FolderPath != organized {
		t.Fatalf("folder_path = %q, want the organized folder", s.FolderPath)
	}
}

func TestPickTwinTieBreaks(t *testing.T) {
	// equal counts: the colon (organized) spelling wins
	if !betterTwin("Rook: Exodus (2024)", 1, "Rook Exodus (2024)", 1) {
		t.Fatal("colon spelling should win a tie")
	}
	if betterTwin("Rook Exodus (2024)", 1, "Rook: Exodus (2024)", 1) {
		t.Fatal("colon-less spelling should lose a tie")
	}
}
