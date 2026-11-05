package writeback

import (
	"archive/zip"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhaines/comic-reader/metadata"
	"github.com/ryanhaines/comic-reader/store"
)

// writeTestCbz builds a minimal CBZ at path. Duplicated from archive/writer_test.go
// (cross-package; that helper is unexported to other packages).
func writeTestCbz(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range []string{"001.jpg", "002.jpg"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("fake-image-" + name))
	}
	w, _ := zw.Create("ComicInfo.xml")
	w.Write([]byte("<ComicInfo><Title>Old</Title></ComicInfo>"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// readComicInfoFromCbz extracts and parses ComicInfo.xml from a CBZ.
func readComicInfoFromCbz(t *testing.T, path string) *metadata.ComicInfo {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("opening cbz %s: %v", path, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == "ComicInfo.xml" {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			var buf []byte
			b := make([]byte, 4096)
			for {
				n, err2 := rc.Read(b)
				buf = append(buf, b[:n]...)
				if err2 != nil {
					break
				}
			}
			ci, err := metadata.ParseComicInfo(buf)
			if err != nil {
				t.Fatalf("ParseComicInfo: %v", err)
			}
			return ci
		}
	}
	t.Fatalf("ComicInfo.xml not found in %s", path)
	return nil
}

// setupTestStore creates a store in a temp dir and returns it along with the
// library root directory.
func setupTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	libRoot := t.TempDir()
	return st, libRoot
}

// seedIssue inserts series + issue rows and enqueues a writeback job.
// seriesFolder is the name of the series directory (base name only).
// comicID is computed via store.PathToID(seriesFolder/filename).
func seedIssue(t *testing.T, st *store.Store, libRoot, seriesFolder, filename string, patch store.IssueMetadataPatch) string {
	t.Helper()
	db := st.DB()
	seriesID := store.PathToID(seriesFolder)
	comicID := store.PathToID(filepath.Join(seriesFolder, filename))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seedIssue exec %q: %v", q, err)
		}
	}
	// series row: folder_path is absolute (matches real scanner behavior).
	mustExec(`INSERT OR IGNORE INTO series(id, name, folder_path, issue_count) VALUES(?,?,?,1)`,
		seriesID, seriesFolder, filepath.Join(libRoot, seriesFolder))
	mustExec(`INSERT OR IGNORE INTO issues(id, series_id, filename, number, position) VALUES(?,?,?,?,0)`,
		comicID, seriesID, filename, "1")

	// Apply metadata patch via DB directly (simulates prior UpdateIssueMetadata calls).
	if patch.Number != nil {
		mustExec(`UPDATE issues SET number=? WHERE id=?`, *patch.Number, comicID)
	}
	if patch.Format != nil {
		mustExec(`UPDATE issues SET format=? WHERE id=?`, *patch.Format, comicID)
	}
	if patch.Writer != nil {
		mustExec(`UPDATE issues SET writer=? WHERE id=?`, *patch.Writer, comicID)
	}
	if patch.Penciller != nil {
		mustExec(`UPDATE issues SET penciller=? WHERE id=?`, *patch.Penciller, comicID)
	}
	if patch.Title != nil || patch.Summary != nil {
		// Ensure enrichment row exists.
		mustExec(`INSERT OR IGNORE INTO issue_enrichment(issue_id, comicvine_issue_id, enriched_at) VALUES(?,0,0)`, comicID)
		if patch.Title != nil {
			mustExec(`UPDATE issue_enrichment SET title=? WHERE issue_id=?`, *patch.Title, comicID)
		}
		if patch.Summary != nil {
			mustExec(`UPDATE issue_enrichment SET summary=? WHERE issue_id=?`, *patch.Summary, comicID)
		}
	}

	if err := st.EnqueueWriteback(comicID); err != nil {
		t.Fatalf("EnqueueWriteback: %v", err)
	}
	return comicID
}

func sp(s string) *string { return &s }

func TestProcessOneWritesComicInfo(t *testing.T) {
	st, libRoot := setupTestStore(t)

	seriesFolder := "Test Series"
	filename := "issue.cbz"
	seriesDir := filepath.Join(libRoot, seriesFolder)
	if err := os.MkdirAll(seriesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestCbz(t, filepath.Join(seriesDir, filename))

	comicID := seedIssue(t, st, libRoot, seriesFolder, filename, store.IssueMetadataPatch{
		Number:  sp("42"),
		Format:  sp("single"),
		Writer:  sp("Alan Moore"),
		Title:   sp("Chapter One"),
		Summary: sp("A story begins."),
	})

	// Also seed publisher on the series.
	st.DB().Exec(`UPDATE series SET publisher='DC Comics' WHERE id=?`, store.PathToID(seriesFolder))

	w := New(st, []string{libRoot}, t.TempDir(), time.Millisecond, nil)
	if worked := w.ProcessOne(); !worked {
		t.Fatal("expected a job to be processed")
	}

	// Verify ComicInfo.xml was written correctly.
	ci := readComicInfoFromCbz(t, filepath.Join(seriesDir, filename))
	if ci.Number != "42" {
		t.Errorf("Number: want 42, got %q", ci.Number)
	}
	if ci.Format != "Single Issue" {
		t.Errorf("Format: want 'Single Issue', got %q", ci.Format)
	}
	if ci.Publisher != "DC Comics" {
		t.Errorf("Publisher: want 'DC Comics', got %q", ci.Publisher)
	}
	if ci.Writer != "Alan Moore" {
		t.Errorf("Writer: want 'Alan Moore', got %q", ci.Writer)
	}
	if ci.Title != "Chapter One" {
		t.Errorf("Title: want 'Chapter One', got %q", ci.Title)
	}

	// Verify job status is 'done'.
	var status string
	st.DB().QueryRow(`SELECT status FROM metadata_writeback WHERE comic_id=?`, comicID).Scan(&status)
	if status != "done" {
		t.Errorf("job status: want 'done', got %q", status)
	}
}

func TestProcessWritesCreditsAndCoverDate(t *testing.T) {
	st, libRoot := setupTestStore(t)

	seriesFolder := "Test Series"
	filename := "issue.cbz"
	seriesDir := filepath.Join(libRoot, seriesFolder)
	if err := os.MkdirAll(seriesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestCbz(t, filepath.Join(seriesDir, filename))

	comicID := seedIssue(t, st, libRoot, seriesFolder, filename, store.IssueMetadataPatch{})
	seriesID := store.PathToID(seriesFolder)

	db := st.DB()
	if _, err := db.Exec(`INSERT INTO series_enrichment(series_id, comicvine_volume_id, creators, status, enriched_at)
		VALUES(?, 0, ?, 'confirmed', 0)`,
		seriesID, `[{"name":"Tom King","role":"writer"},{"name":"Matt Banning","role":"inker"}]`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO issue_enrichment(issue_id, comicvine_issue_id, title, summary, cover_date, enriched_at)
		VALUES(?, 0, ?, ?, ?, 0)`,
		comicID, "Ep1", "Sum", "2016-08-03"); err != nil {
		t.Fatal(err)
	}

	w := New(st, []string{libRoot}, "", time.Millisecond, nil)
	if !w.ProcessOne() {
		t.Fatal("expected a job to be processed")
	}

	ci := readComicInfoFromCbz(t, filepath.Join(seriesDir, filename))
	if ci.Writer != "Tom King" || ci.Inker != "Matt Banning" {
		t.Errorf("credits not written: %+v", ci)
	}
	if ci.Year != 2016 || ci.Month != 8 || ci.Day != 3 {
		t.Errorf("cover date not written: %d-%d-%d", ci.Year, ci.Month, ci.Day)
	}
}

// writeTestCbzWithMonthDay builds a minimal CBZ whose ComicInfo.xml already has
// Month/Day set, simulating a file previously written by another tool.
func writeTestCbzWithMonthDay(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("ComicInfo.xml")
	w.Write([]byte("<ComicInfo><Title>Old</Title><Year>2016</Year><Month>5</Month><Day>15</Day></ComicInfo>"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestProcessPreservesMonthDayWhenCoverDateEmpty(t *testing.T) {
	st, libRoot := setupTestStore(t)

	seriesFolder := "Test Series"
	filename := "issue.cbz"
	seriesDir := filepath.Join(libRoot, seriesFolder)
	if err := os.MkdirAll(seriesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestCbzWithMonthDay(t, filepath.Join(seriesDir, filename))

	comicID := seedIssue(t, st, libRoot, seriesFolder, filename, store.IssueMetadataPatch{})

	db := st.DB()
	// Confirmed enrichment with an empty cover_date (common: CV cover dates are
	// often YYYY-only or missing entirely) must not zero pre-existing Month/Day.
	if _, err := db.Exec(`INSERT INTO issue_enrichment(issue_id, comicvine_issue_id, title, summary, cover_date, enriched_at)
		VALUES(?, 0, ?, ?, ?, 0)`, comicID, "Ep1", "Sum", ""); err != nil {
		t.Fatal(err)
	}

	w := New(st, []string{libRoot}, "", time.Millisecond, nil)
	if !w.ProcessOne() {
		t.Fatal("expected a job to be processed")
	}

	ci := readComicInfoFromCbz(t, filepath.Join(seriesDir, filename))
	if ci.Month != 5 || ci.Day != 15 {
		t.Errorf("Month/Day clobbered by empty cover date: got month=%d day=%d, want 5/15", ci.Month, ci.Day)
	}
}

func TestProcessOneConvertsCbr(t *testing.T) {
	st, libRoot := setupTestStore(t)

	seriesFolder := "Test Series"
	filename := "issue.cbr"
	seriesDir := filepath.Join(libRoot, seriesFolder)
	if err := os.MkdirAll(seriesDir, 0755); err != nil {
		t.Fatal(err)
	}
	// CBR with zip payload — go-unarr reads by content, not extension.
	// We write a valid zip file and name it .cbr; ConvertToCbz will rename it.
	writeTestCbz(t, filepath.Join(seriesDir, filename)) // zip-format payload

	oldComicID := seedIssue(t, st, libRoot, seriesFolder, filename, store.IssueMetadataPatch{
		Number: sp("1"),
	})

	w := New(st, []string{libRoot}, t.TempDir(), time.Millisecond, nil)
	if worked := w.ProcessOne(); !worked {
		t.Fatal("expected a job to be processed")
	}

	newFilename := "issue.cbz"
	newComicID := store.PathToID(filepath.Join(seriesFolder, newFilename))

	// .cbz must exist; .cbr must be gone.
	if _, err := os.Stat(filepath.Join(seriesDir, newFilename)); err != nil {
		t.Errorf("expected issue.cbz to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(seriesDir, filename)); err == nil {
		t.Error("expected issue.cbr to be gone")
	}

	// issues row ID must equal the new PathToID.
	var gotID string
	if err := st.DB().QueryRow(`SELECT id FROM issues WHERE id=?`, newComicID).Scan(&gotID); err != nil {
		t.Errorf("issues row for new ID %q not found: %v", newComicID, err)
	}

	// Old ID must no longer exist.
	var oldExists int
	st.DB().QueryRow(`SELECT COUNT(*) FROM issues WHERE id=?`, oldComicID).Scan(&oldExists)
	if oldExists != 0 {
		t.Error("old issues row should have been migrated away")
	}

	// Job should be done under new ID.
	// This also validates the effectiveID contract in ProcessOne: even if a post-conversion
	// step had failed, MarkWritebackError would be called with the NEW id (not the old one),
	// so the writeback row is always findable after migration.
	var status string
	st.DB().QueryRow(`SELECT status FROM metadata_writeback WHERE comic_id=?`, newComicID).Scan(&status)
	if status != "done" {
		t.Errorf("job status: want 'done', got %q", status)
	}

	// No stale writeback row should remain for the old ID.
	var oldWBCount int
	st.DB().QueryRow(`SELECT COUNT(*) FROM metadata_writeback WHERE comic_id=?`, oldComicID).Scan(&oldWBCount)
	if oldWBCount != 0 {
		t.Errorf("stale metadata_writeback row for old ID %q: want 0, got %d", oldComicID, oldWBCount)
	}
}

func TestProcessOneMarksError(t *testing.T) {
	st, libRoot := setupTestStore(t)

	seriesFolder := "Missing Series"
	filename := "nonexistent.cbz"
	// No series dir created, no file on disk.
	comicID := seedIssue(t, st, libRoot, seriesFolder, filename, store.IssueMetadataPatch{})
	// We need the series row to exist for WritebackSource to succeed.
	// seedIssue already inserted it (folder_path won't exist on disk).

	w := New(st, []string{libRoot}, t.TempDir(), time.Millisecond, nil)
	worked := w.ProcessOne()
	if !worked {
		t.Fatal("expected ProcessOne to return true (job was attempted)")
	}

	var attempts int
	var status string
	if err := st.DB().QueryRow(`SELECT attempts, status FROM metadata_writeback WHERE comic_id=?`, comicID).Scan(&attempts, &status); err != nil {
		t.Fatalf("querying writeback row: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts: want 1, got %d", attempts)
	}
	if status != "pending" {
		t.Errorf("status after 1 error: want 'pending', got %q", status)
	}
}

func TestProcessOneSkipsPdf(t *testing.T) {
	st, libRoot := setupTestStore(t)

	seriesFolder := "PDF Series"
	filename := "issue.pdf"
	// No file needed on disk — PDF is DB-only metadata.
	comicID := seedIssue(t, st, libRoot, seriesFolder, filename, store.IssueMetadataPatch{})

	w := New(st, []string{libRoot}, t.TempDir(), time.Millisecond, nil)
	worked := w.ProcessOne()
	if !worked {
		t.Fatal("expected ProcessOne to return true for PDF job")
	}

	var status string
	if err := st.DB().QueryRow(`SELECT status FROM metadata_writeback WHERE comic_id=?`, comicID).Scan(&status); err == sql.ErrNoRows {
		t.Fatal("writeback row missing")
	}
	if status != "done" {
		t.Errorf("PDF job status: want 'done', got %q", status)
	}
}

// A file tagged by another tool must not lose fields the DB has no value for.
func TestProcessPreservesFileFieldsWhenDBEmpty(t *testing.T) {
	st, libRoot := setupTestStore(t)

	seriesFolder := "Test Series"
	filename := "issue.cbz"
	seriesDir := filepath.Join(libRoot, seriesFolder)
	if err := os.MkdirAll(seriesDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(seriesDir, filename)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	cw, _ := zw.Create("ComicInfo.xml")
	cw.Write([]byte("<ComicInfo><Title>Kept Title</Title><Summary>Kept summary.</Summary>" +
		"<Publisher>Kept Pub</Publisher><Format>Annual</Format></ComicInfo>"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// No enrichment, no format, no publisher in the DB.
	seedIssue(t, st, libRoot, seriesFolder, filename, store.IssueMetadataPatch{})

	w := New(st, []string{libRoot}, "", time.Millisecond, nil)
	if !w.ProcessOne() {
		t.Fatal("expected a job to be processed")
	}

	ci := readComicInfoFromCbz(t, path)
	if ci.Title != "Kept Title" || ci.Summary != "Kept summary." ||
		ci.Publisher != "Kept Pub" || ci.Format != "Annual" {
		t.Errorf("file fields blanked: title=%q summary=%q publisher=%q format=%q",
			ci.Title, ci.Summary, ci.Publisher, ci.Format)
	}
}

// A container restart mid-rewrite orphans the temp file; the worker sweeps
// stale ones on start but leaves fresh ones (a concurrent writer's) alone.
func TestSweepTempsRemovesOnlyStaleTempFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Saga (2012)")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	files := map[string]bool{ // name -> should survive
		".rewrite-1.cbz": false,
		".convert-2.cbz": false,
		".rewrite-3.cbz": true, // fresh
		"Saga #001.cbz":  true,
	}
	for n, survive := range files {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, nil, 0644); err != nil {
			t.Fatal(err)
		}
		if !survive || n == "Saga #001.cbz" {
			os.Chtimes(p, old, old)
		}
	}

	w := New(nil, []string{root}, "", time.Millisecond, nil)
	w.sweepTemps()

	for n, survive := range files {
		_, err := os.Stat(filepath.Join(dir, n))
		if exists := err == nil; exists != survive {
			t.Errorf("%s: exists=%v, want %v", n, exists, survive)
		}
	}
}

// Enrichment stores ComicVine's HTML verbatim; the file must get plain text,
// matching what every read path serves.
func TestProcessWritesPlainTextSummary(t *testing.T) {
	st, libRoot := setupTestStore(t)
	seriesFolder, filename := "Test Series", "issue.cbz"
	seriesDir := filepath.Join(libRoot, seriesFolder)
	if err := os.MkdirAll(seriesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestCbz(t, filepath.Join(seriesDir, filename))
	seedIssue(t, st, libRoot, seriesFolder, filename, store.IssueMetadataPatch{
		Summary: sp("<p><em>Hi</em> it&#39;s   here</p>"),
	})

	w := New(st, []string{libRoot}, "", time.Millisecond, nil)
	if !w.ProcessOne() {
		t.Fatal("expected a job to be processed")
	}
	if ci := readComicInfoFromCbz(t, filepath.Join(seriesDir, filename)); ci.Summary != "Hi it's here" {
		t.Errorf("Summary = %q, want plain text", ci.Summary)
	}
}
