package admin_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/match"
	"github.com/rthaines211/gutter-server/notify"
	"github.com/rthaines211/gutter-server/server"
	"github.com/rthaines211/gutter-server/server/admin"
	"github.com/rthaines211/gutter-server/store"
	"github.com/rthaines211/gutter-server/writeback"
)

// twinPipeline is the real scan -> pipeline -> writeback cycle over lib.
type twinPipeline struct {
	t    *testing.T
	lib  string
	st   *store.Store
	a    *admin.Admin
	wb   *writeback.Worker
	got  chan store.PipelineRun
	runs []store.PipelineRun
}

func newTwinPipeline(t *testing.T, lib string, md5s map[string]int64) *twinPipeline {
	t.Helper()
	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cv := e2eCV(t, md5s)
	a := &admin.Admin{Store: st, Match: match.NewRunner(st, cv, []string{lib}, func() int64 { return 1 }),
		ComicDirs: []string{lib}, CoverDir: t.TempDir(), ScanState: admin.NewScanState()}
	p := &twinPipeline{t: t, lib: lib, st: st, a: a, got: make(chan store.PipelineRun, 10),
		wb: writeback.New(st, []string{lib}, a.CoverDir, 0, nil)}
	a.OnRun = func(r store.PipelineRun) { p.got <- r }
	return p
}

func (p *twinPipeline) cycle() {
	p.t.Helper()
	if err := server.ScanLibrary([]string{p.lib}, p.st, p.a.CoverDir, p.a.ScanState); err != nil {
		p.t.Fatal(err)
	}
	if err := p.a.RunPipeline(context.Background()); err != nil {
		p.t.Fatal(err)
	}
	for p.wb.ProcessOne() {
	}
}

// waitRuns waits until exactly n OnRun calls have arrived.
func (p *twinPipeline) waitRuns(n int) {
	p.t.Helper()
	for len(p.runs) < n {
		select {
		case r := <-p.got:
			p.runs = append(p.runs, r)
		case <-time.After(time.Second):
			p.t.Fatalf("OnRun calls = %d, want %d", len(p.runs), n)
		}
	}
	select {
	case r := <-p.got:
		p.runs = append(p.runs, r)
	case <-time.After(50 * time.Millisecond):
	}
	if len(p.runs) != n {
		p.t.Fatalf("OnRun calls = %d, want %d", len(p.runs), n)
	}
}

// assertSettled: two more cycles queue nothing and run nothing, and a forced
// re-run of the series binds, organizes, announces and writes back nothing.
func (p *twinPipeline) assertSettled(seriesID string, runsSoFar int) {
	p.t.Helper()
	for i := 0; i < 2; i++ {
		p.cycle()
		if ids, _ := p.st.ListPipelinePending(); len(ids) != 0 {
			p.t.Fatalf("pending after unchanged cycle = %v", ids)
		}
	}
	p.waitRuns(runsSoFar)
	if err := p.st.AddPipelinePending(seriesID); err != nil {
		p.t.Fatal(err)
	}
	if err := p.a.RunPipeline(context.Background()); err != nil {
		p.t.Fatal(err)
	}
	p.waitRuns(runsSoFar + 1)
	last := p.runs[len(p.runs)-1]
	for _, e := range last.Events {
		switch e.Kind {
		case "filled", "organized", "new_issues", "error":
			p.t.Fatalf("repeat run emitted %+v", e)
		}
	}
	if _, ok, _ := p.st.NextWritebackJob(); ok {
		p.t.Fatal("repeat run enqueued writeback")
	}
	if n, ok := notify.Summarize(last, true); ok {
		p.t.Fatalf("repeat run notifies %+v", n)
	}
}

func (p *twinPipeline) wantHashBound(seriesID, issueID string) {
	p.t.Helper()
	ims, _ := p.st.ListIssueMatchesForSeries(seriesID)
	for _, im := range ims {
		if im.IssueID == issueID {
			if im.Method != match.MethodFirstPageMD5 {
				p.t.Fatalf("twin issue bound by %q, want the first-page hash (read from its own folder)", im.Method)
			}
			return
		}
	}
	p.t.Fatalf("twin issue %s unbound; matches %+v", issueID, ims)
}

// A Mylar twin folder that appears after the series is confirmed: its new
// issue is filled (by hash, read from the twin's own path), announced once,
// left where Mylar put it, and an unchanged pair then settles for good.
func TestPipelineFillsTwinFolderIssueOnce(t *testing.T) {
	lib := t.TempDir()
	p1, p2 := []byte("saga-1"), []byte("saga-2")
	writeOne(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), p1)
	p := newTwinPipeline(t, lib, map[string]int64{hexMD5(p1): 317966, hexMD5(p2): 317967})

	p.cycle()
	all, _ := p.st.ListSeries()
	if len(all) != 1 {
		t.Fatalf("series = %+v", all)
	}
	id := all[0].ID

	twinFile := filepath.Join(lib, "Saga- (2012)", "Saga 002 (2012).cbz")
	writeOne(t, twinFile, p2)
	os.Chmod(twinFile, 0o644)
	p.cycle()
	twinID := store.PathToID("Saga- (2012)/Saga 002 (2012).cbz")
	p.wantHashBound(id, twinID)
	p.waitRuns(2)
	if n, ok := notify.Summarize(p.runs[1], true); !ok || n.Title != "New issues ready" {
		t.Fatalf("twin issue not announced: %+v %v; events %+v", n, ok, p.runs[1].Events)
	}
	if fi, err := os.Stat(twinFile); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("twin file after organize+writeback: %v %v", fi, err)
	}

	p.assertSettled(id, 2)
}

// The twin is already there on the series' first scan: the matcher's
// first pass hashes the twin's issue from its own folder too.
func TestPipelineMatchesTwinFolderOnFirstScan(t *testing.T) {
	lib := t.TempDir()
	p1, p2 := []byte("saga-1"), []byte("saga-2")
	writeOne(t, filepath.Join(lib, "Saga (2012)", "Saga 001 (2012).cbz"), p1)
	writeOne(t, filepath.Join(lib, "Saga- (2012)", "Saga 002 (2012).cbz"), p2)
	p := newTwinPipeline(t, lib, map[string]int64{hexMD5(p1): 317966, hexMD5(p2): 317967})

	p.cycle()
	all, _ := p.st.ListSeries()
	if len(all) != 1 {
		t.Fatalf("series = %+v", all)
	}
	p.wantHashBound(all[0].ID, store.PathToID("Saga- (2012)/Saga 002 (2012).cbz"))
	if _, err := os.Stat(filepath.Join(lib, "Saga- (2012)", "Saga 002 (2012).cbz")); err != nil {
		t.Fatalf("twin file moved: %v", err)
	}
	p.waitRuns(1)
	p.assertSettled(all[0].ID, 1)
}
