package admin

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhaines/comic-reader/match"
	"github.com/ryanhaines/comic-reader/match/localcv"
	"github.com/ryanhaines/comic-reader/store"
)

func pipeCBZ(t *testing.T, path string, page []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("001.jpg")
	w.Write(page)
	zw.Close()
	f.Close()
	sum := md5.Sum(page)
	return hex.EncodeToString(sum[:])
}

func pipeCV(t *testing.T, md5s map[string]int64) *localcv.DB {
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

// pipeAdmin builds an Admin over one library root holding Saga (2012).
func pipeAdmin(t *testing.T, md5s map[string]int64) (*Admin, *store.Store, string) {
	t.Helper()
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lib := t.TempDir()
	r := match.NewRunner(st, pipeCV(t, md5s), []string{lib}, func() int64 { return time.Now().UnixMilli() })
	a := &Admin{Store: st, Match: r, ComicDirs: []string{lib}, CoverDir: t.TempDir(), ScanState: NewScanState()}
	return a, st, lib
}

func addSeries(t *testing.T, st *store.Store, lib, folder string, files map[string]string) string {
	t.Helper()
	id := store.SlugifyName(folder)
	var issues []store.Issue
	i := 0
	for fn, num := range files {
		issues = append(issues, store.Issue{ID: store.PathToID(folder + "/" + fn), SeriesID: id, Filename: fn, Number: num, Position: i})
		i++
	}
	if err := st.UpsertSeries(store.Series{ID: id, Name: folder, FolderPath: filepath.Join(lib, folder), IssueCount: len(files)}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues(id, issues); err != nil {
		t.Fatal(err)
	}
	if err := st.AddPipelinePending(id); err != nil {
		t.Fatal(err)
	}
	return id
}

func lastRun(t *testing.T, st *store.Store) store.PipelineRun {
	t.Helper()
	runs, err := st.ListPipelineRuns(1)
	if err != nil || len(runs) == 0 {
		t.Fatalf("no run recorded: %v", err)
	}
	return runs[0]
}

func hasEvent(r store.PipelineRun, kind string) bool {
	for _, e := range r.Events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// A new folder whose first page hashes into the dump is confirmed by the
// pipeline, enriched, queued for writeback, and leaves the pending set.
func TestPipelineAutoConfirmsHashMatchedSeries(t *testing.T) {
	page := []byte("saga-1")
	a, st, lib := pipeAdmin(t, nil)
	sum := pipeCBZ(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), page)
	a.Match = match.NewRunner(st, pipeCV(t, map[string]int64{sum: 317966}), []string{lib}, func() int64 { return 1 })
	id := addSeries(t, st, lib, "Saga (2012)", map[string]string{"Saga 001 (2012).cbz": "1"})

	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ := st.GetSeriesMatch(id)
	if m == nil || m.State != store.MatchStateConfirmed || m.ConfirmedBy != "pipeline" {
		t.Fatalf("not auto-confirmed: %+v", m)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("still pending: %v", ids)
	}
	if !hasEvent(lastRun(t, st), "auto_confirmed") {
		t.Fatalf("events = %+v", lastRun(t, st).Events)
	}
	if _, ok, _ := st.NextWritebackJob(); !ok {
		t.Fatal("no writeback queued")
	}
}

// No hash hit means no auto-confirm: sent to review, out of pending.
func TestPipelineSendsUnhashedSeriesToReview(t *testing.T) {
	a, st, lib := pipeAdmin(t, nil)
	pipeCBZ(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), []byte("unknown"))
	id := addSeries(t, st, lib, "Saga (2012)", map[string]string{"Saga 001 (2012).cbz": "1"})

	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetSeriesMatch(id); m != nil && m.State == store.MatchStateConfirmed {
		t.Fatal("unhashed series was confirmed")
	}
	if !hasEvent(lastRun(t, st), "sent_to_review") {
		t.Fatalf("events = %+v", lastRun(t, st).Events)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("review item still pending: %v", ids)
	}
}

func TestPipelineDropsVanishedSeries(t *testing.T) {
	a, st, _ := pipeAdmin(t, nil)
	if err := st.AddPipelinePending("gone"); err != nil {
		t.Fatal(err)
	}
	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("vanished series kept pending: %v", ids)
	}
	if hasEvent(lastRun(t, st), "error") {
		t.Fatal("vanished series logged as error")
	}
}

// The writeback worker holds the gate per file; the pipeline waits for it.
func TestPipelineWaitsForWritebackGate(t *testing.T) {
	a, st, lib := pipeAdmin(t, nil)
	pipeCBZ(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), []byte("unknown"))
	addSeries(t, st, lib, "Saga (2012)", map[string]string{"Saga 001 (2012).cbz": "1"})
	if !a.BeginWriteback() {
		t.Fatal("could not take writeback gate")
	}
	go func() { time.Sleep(300 * time.Millisecond); a.EndWriteback() }()

	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := lastRun(t, st); r.SkippedReason != "" || r.SeriesCount != 1 {
		t.Fatalf("run skipped instead of waiting: %+v", r)
	}
}

// Organize renames the folder (new series id); the old pending row must go.
func TestPipelineRemovesPendingAfterFolderRename(t *testing.T) {
	page := []byte("saga-1")
	a, st, lib := pipeAdmin(t, nil)
	sum := pipeCBZ(t, filepath.Join(lib, "Saga", "Saga 001.cbz"), page)
	a.Match = match.NewRunner(st, pipeCV(t, map[string]int64{sum: 317966}), []string{lib}, func() int64 { return 1 })
	addSeries(t, st, lib, "Saga", map[string]string{"Saga 001.cbz": "1"})

	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("pending after rename = %v", ids)
	}
	if _, err := os.Stat(filepath.Join(lib, "Saga (2012)")); err != nil {
		t.Fatalf("folder not organized to canonical name: %v", err)
	}
	if !hasEvent(lastRun(t, st), "organized") {
		t.Fatalf("events = %+v", lastRun(t, st).Events)
	}
}

// F4 (Ruling F): the writeback backfill first re-applies confirmed series
// whose canonical volume_name was never stored, so <Series> is canonical.
func TestMatchWritebackBackfillsVolumeName(t *testing.T) {
	a, st, lib := pipeAdmin(t, nil)
	pipeCBZ(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), []byte("x"))
	id := addSeries(t, st, lib, "Saga (2012)", map[string]string{"Saga 001 (2012).cbz": "1"})
	if err := st.UpsertSeriesMatch(store.SeriesMatch{SeriesID: id, Source: store.SourceComicVine, VolumeID: "49019",
		State: store.MatchStateHighConfidence, Method: match.MethodFirstPageMD5}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssueMatch(store.IssueMatch{IssueID: store.PathToID("Saga (2012)/Saga 001 (2012).cbz"),
		Source: store.SourceComicVine, UpstreamID: "317966", State: store.MatchStateHighConfidence,
		Method: match.MethodFirstPageMD5}); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmSeriesMatch(id, 1); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	a.handleMatchWriteback(rec, httptest.NewRequest(http.MethodPost, "/_admin/match/writeback", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	se, err := st.GetSeriesEnrichment(id)
	if err != nil || se == nil || se.VolumeName != "Saga" {
		t.Fatalf("volume_name not backfilled: %+v %v", se, err)
	}
	if _, ok, _ := st.NextWritebackJob(); !ok {
		t.Fatal("no writeback queued")
	}
}

// F5: when the run's time cap trips, the series stay pending and the run row
// says why. An already-expired context stands in for the 30-minute cap.
func TestPipelineRecordsTimeCap(t *testing.T) {
	a, st, lib := pipeAdmin(t, nil)
	pipeCBZ(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), []byte("x"))
	addSeries(t, st, lib, "Saga (2012)", map[string]string{"Saga 001 (2012).cbz": "1"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := a.RunPipeline(ctx); err != nil {
		t.Fatal(err)
	}
	if r := lastRun(t, st); r.SkippedReason != "time cap reached; 1 series left pending" {
		t.Fatalf("skipped reason = %q", r.SkippedReason)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 1 {
		t.Fatalf("pending = %v, want the series kept", ids)
	}
}

func confirmPipe(t *testing.T, st *store.Store, id, vol string) {
	t.Helper()
	if err := st.UpsertSeriesMatch(store.SeriesMatch{SeriesID: id, Source: store.SourceComicVine, VolumeID: vol,
		State: store.MatchStateHighConfidence, Method: match.MethodFirstPageMD5}); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmSeriesMatch(id, 1); err != nil {
		t.Fatal(err)
	}
}

// F7a: a series whose step errors stays pending for the next run.
func TestPipelineErrorKeepsSeriesPending(t *testing.T) {
	a, st, lib := pipeAdmin(t, nil)
	pipeCBZ(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), []byte("x"))
	id := addSeries(t, st, lib, "Saga (2012)", map[string]string{"Saga 001 (2012).cbz": "1"})
	confirmPipe(t, st, id, "not-a-number") // FillConfirmed: bad volume id

	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasEvent(lastRun(t, st), "error") {
		t.Fatalf("events = %+v", lastRun(t, st).Events)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 1 || ids[0] != id {
		t.Fatalf("pending = %v, want [%s]", ids, id)
	}
}

// F7b: a don't-match series leaves pending silently.
func TestPipelineDropsDontMatchSilently(t *testing.T) {
	a, st, lib := pipeAdmin(t, nil)
	pipeCBZ(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), []byte("x"))
	id := addSeries(t, st, lib, "Saga (2012)", map[string]string{"Saga 001 (2012).cbz": "1"})
	if err := st.UpsertSeriesMatch(store.SeriesMatch{SeriesID: id, State: store.MatchStateNoMatch}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDontMatch(id, true); err != nil {
		t.Fatal(err)
	}

	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.ListPipelinePending(); len(ids) != 0 {
		t.Fatalf("don't-match series still pending: %v", ids)
	}
	for _, e := range lastRun(t, st).Events {
		if e.Kind != "new_issues" { // its file is still news; the series is not
			t.Fatalf("events = %+v, want none", lastRun(t, st).Events)
		}
	}
}

// F7c: a new issue in a confirmed folder is filled and queued for writeback.
func TestPipelineFillsConfirmedSeries(t *testing.T) {
	a, st, lib := pipeAdmin(t, nil)
	pipeCBZ(t, filepath.Join(lib, "Saga (2012)", "Saga 002 (2012).cbz"), []byte("no-hash"))
	id := addSeries(t, st, lib, "Saga (2012)", map[string]string{"Saga 002 (2012).cbz": "2"})
	confirmPipe(t, st, id, "49019")

	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasEvent(lastRun(t, st), "filled") {
		t.Fatalf("events = %+v", lastRun(t, st).Events)
	}
	// Organize renamed the file afterwards, so the job's id is the new path's.
	if _, ok, _ := st.NextWritebackJob(); !ok {
		t.Fatal("no writeback queued")
	}
}

func TestPipelineCallsOnRun(t *testing.T) {
	a, st, lib := pipeAdmin(t, nil)
	pipeCBZ(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), []byte("unknown"))
	addSeries(t, st, lib, "Saga (2012)", map[string]string{"Saga 001 (2012).cbz": "1"})
	got := make(chan store.PipelineRun, 1)
	a.OnRun = func(r store.PipelineRun) { got <- r }

	if err := a.RunPipeline(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if !hasEvent(r, "sent_to_review") {
			t.Fatalf("events = %+v", r.Events)
		}
	case <-time.After(time.Second):
		t.Fatal("OnRun not called")
	}
}
