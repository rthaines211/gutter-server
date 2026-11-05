package admin_test

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/match"
	"github.com/rthaines211/gutter-server/match/localcv"
	"github.com/rthaines211/gutter-server/notify"
	"github.com/rthaines211/gutter-server/server"
	"github.com/rthaines211/gutter-server/server/admin"
	"github.com/rthaines211/gutter-server/store"
	"github.com/rthaines211/gutter-server/writeback"
)

// writeOne is pipeCBZ (server/admin/pipeline_test.go) copied into package
// admin_test, with a ComicInfo.xml added so the scanner's hasMetadata check
// (server/scanner.go) is satisfied from the first scan onward — a rescan
// with nothing changed must skip the series, not re-read it. The page bytes
// still drive the first-page MD5 used for hash matching.
func writeOne(t *testing.T, path string, page []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)

	type comicInfo struct {
		XMLName   xml.Name `xml:"ComicInfo"`
		Publisher string   `xml:"Publisher"`
	}
	ciW, err := zw.Create("ComicInfo.xml")
	if err != nil {
		t.Fatal(err)
	}
	if err := xml.NewEncoder(ciW).Encode(comicInfo{Publisher: "Image"}); err != nil {
		t.Fatal(err)
	}

	w, _ := zw.Create("001.jpg")
	w.Write(page)
	zw.Close()
	f.Close()
}

// hexMD5 is the MD5-hex helper pipeCBZ computes inline, pulled out so the
// test can derive the hash dump's lookup key straight from the page bytes.
func hexMD5(page []byte) string {
	sum := md5.Sum(page)
	return hex.EncodeToString(sum[:])
}

// e2eCV is pipeCV (server/admin/pipeline_test.go) copied into package
// admin_test.
func e2eCV(t *testing.T, md5s map[string]int64) *localcv.DB {
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
	INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, description, person_credits, character_credits)
		VALUES (317966, 49019, 'Chapter One', '1', '2012-03-14', 'd', '[]', '[]'),
		       (317967, 49019, 'Chapter Two', '2', '2012-04-11', 'd', '[]', '[]');
	`); err != nil {
		t.Fatal(err)
	}
	i := 1
	for sum, cvid := range md5s {
		if _, err := db.Exec(`INSERT INTO comic_files(id, filename, first_page_md5, cvid) VALUES (?, 'f', ?, ?)`, i, sum, cvid); err != nil {
			t.Fatal(err)
		}
		i++
	}
	db.Close()
	cv, err := localcv.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cv.Close() })
	return cv
}

// A weekly pull: a new hash-matchable series and a new issue in it on the
// next scan. Zero clicks from file drop to confirmed, enriched, organized,
// writeback-queued. Ruling D: after organize renames the file to the
// template name, a rescan with no file changes must not re-queue the
// series for the pipeline.
func TestScanThenPipelineEndToEnd(t *testing.T) {
	lib := t.TempDir()
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p1 := []byte("saga-1")
	writeOne(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), p1)
	cv := e2eCV(t, map[string]int64{hexMD5(p1): 317966})
	a := &admin.Admin{Store: st, Match: match.NewRunner(st, cv, []string{lib}, func() int64 { return 1 }),
		ComicDirs: []string{lib}, CoverDir: t.TempDir(), ScanState: admin.NewScanState()}

	if err := server.ScanLibrary([]string{lib}, st, a.CoverDir, a.ScanState); err != nil {
		t.Fatal(err)
	}
	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ListSeries()
	if len(all) != 1 {
		t.Fatalf("series = %d", len(all))
	}
	m, _ := st.GetSeriesMatch(all[0].ID)
	if m == nil || m.State != store.MatchStateConfirmed || m.ConfirmedBy != "pipeline" {
		t.Fatalf("match = %+v", m)
	}

	// Rescan with nothing changed: organize already renamed the file to the
	// template name on the first pipeline run, so a scan that sees the same
	// (now-renamed) file must not re-queue the series.
	if err := server.ScanLibrary([]string{lib}, st, a.CoverDir, a.ScanState); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("pending after unchanged rescan = %v", ids)
	}

	// Next week: #2 arrives (no hash in the dump; binds by number offline).
	writeOne(t, filepath.Join(all[0].FolderPath, "Saga 002 (2012).cbz"), []byte("saga-2"))
	if err := server.ScanLibrary([]string{lib}, st, a.CoverDir, a.ScanState); err != nil {
		t.Fatal(err)
	}
	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	ims, _ := st.ListIssueMatchesForSeries(all[0].ID)
	if len(ims) != 2 {
		t.Fatalf("issue matches = %d, want 2", len(ims))
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("pending = %v", ids)
	}
	// Two confirmed runs: the initial pull and the #2 pull. The unchanged
	// rescan in between found nothing pending, so it recorded no run row.
	runs, _ := st.ListPipelineRuns(5)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
}

// Production loop (Oct 2026): the same seven series were re-filled,
// re-written and re-organized every 30 minutes, and each run pushed "New
// issues ready". Run the real scan -> pipeline -> writeback cycle until the
// library settles, then again over the unchanged library (including a
// downloader twin folder and a forced re-run of a confirmed series): the
// repeat must bind nothing, enqueue no writeback, organize nothing and
// notify nobody.
func TestPipelineIsIdempotentOverUnchangedLibrary(t *testing.T) {
	lib := t.TempDir()
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p1 := []byte("saga-1")
	writeOne(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), p1)
	cv := e2eCV(t, map[string]int64{hexMD5(p1): 317966})
	a := &admin.Admin{Store: st, Match: match.NewRunner(st, cv, []string{lib}, func() int64 { return 1 }),
		ComicDirs: []string{lib}, CoverDir: t.TempDir(), ScanState: admin.NewScanState()}
	got := make(chan store.PipelineRun, 10) // OnRun runs on its own goroutine
	a.OnRun = func(r store.PipelineRun) { got <- r }
	var runs []store.PipelineRun
	wb := writeback.New(st, []string{lib}, a.CoverDir, 0, nil)

	cycle := func() {
		t.Helper()
		if err := server.ScanLibrary([]string{lib}, st, a.CoverDir, a.ScanState); err != nil {
			t.Fatal(err)
		}
		if err := a.RunPipeline(context.Background()); err != nil {
			t.Fatal(err)
		}
		for wb.ProcessOne() { // the server's own writeback rewrites the files
		}
	}

	cycle() // new series: auto-confirmed, organized, written back
	all, _ := st.ListSeries()
	if len(all) != 1 {
		t.Fatalf("series = %+v", all)
	}
	// Next pull: #2 arrives in the series folder (fill binds it by number),
	// and a downloader drops a twin folder that slugifies to the same id.
	writeOne(t, filepath.Join(all[0].FolderPath, "Saga 002 (2012).cbz"), []byte("saga-2"))
	twin := filepath.Join(lib, "Saga- (2012)")
	if store.SlugifyName(filepath.Base(twin)) != all[0].ID {
		t.Fatalf("twin slug %q != %q", store.SlugifyName(filepath.Base(twin)), all[0].ID)
	}
	writeOne(t, filepath.Join(twin, "Saga 003 (2012).cbz"), []byte("saga-3"))
	cycle()
	if ims, _ := st.ListIssueMatchesForSeries(all[0].ID); len(ims) != 2 {
		t.Fatalf("issue matches = %d, want 2", len(ims))
	}

	waitRuns := func(n int) {
		t.Helper()
		for len(runs) < n {
			select {
			case r := <-got:
				runs = append(runs, r)
			case <-time.After(time.Second):
				t.Fatalf("OnRun calls = %d, want %d", len(runs), n)
			}
		}
		select {
		case r := <-got:
			runs = append(runs, r)
		case <-time.After(50 * time.Millisecond):
		}
		if len(runs) != n {
			t.Fatalf("OnRun calls = %d, want %d", len(runs), n)
		}
	}
	waitRuns(2)
	for i, r := range runs {
		if n, ok := notify.Summarize(r, true); !ok || n.Title != "New issues ready" {
			t.Fatalf("run %d: notification %+v %v, want New issues ready; events %+v", i+1, n, ok, r.Events)
		}
	}

	// Unchanged library: nothing pending, so no run at all.
	cycle()
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("pending = %v", ids)
	}
	waitRuns(2)

	// Force the confirmed series through again (as any rescan trigger would).
	if err := st.AddPipelinePending(all[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitRuns(3)
	for _, e := range runs[2].Events {
		switch e.Kind {
		case "filled", "organized", "new_issues", "error":
			t.Fatalf("repeat run emitted %+v", e)
		}
	}
	if _, ok, _ := st.NextWritebackJob(); ok {
		t.Fatal("repeat run enqueued writeback")
	}
	if n, ok := notify.Summarize(runs[2], true); ok {
		t.Fatalf("repeat run notifies %+v", n)
	}
}
