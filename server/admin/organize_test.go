package admin

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/organize"
	"github.com/rthaines211/gutter-server/store"
)

// capOps caps each series card at opCap rows unless showAll is set, mirroring
// applyIssueCap in match.go. A folder with 25 planned ops must not render all
// 25 by default (spec: cap ~10, with "show all").
func TestCapOpsCapsAndUncaps(t *testing.T) {
	var ops []organize.Op
	for i := 0; i < 25; i++ {
		ops = append(ops, organize.Op{Kind: organize.OpRenameFile})
	}
	sp := organize.SeriesPlan{SeriesID: "s1", Ops: ops}

	capped := capOps(sp, false)
	if capped.ShowAll {
		t.Fatal("expected ShowAll false for 25 ops over the cap")
	}
	if len(capped.VisibleOps) != opCap {
		t.Fatalf("VisibleOps = %d, want %d", len(capped.VisibleOps), opCap)
	}
	if capped.MoreCount != 25-opCap {
		t.Fatalf("MoreCount = %d, want %d", capped.MoreCount, 25-opCap)
	}

	all := capOps(sp, true)
	if !all.ShowAll || len(all.VisibleOps) != 25 || all.MoreCount != 0 {
		t.Fatalf("showAll=true: got %+v", all)
	}

	small := organize.SeriesPlan{SeriesID: "s2", Ops: ops[:3]}
	under := capOps(small, false)
	if !under.ShowAll || len(under.VisibleOps) != 3 || under.MoreCount != 0 {
		t.Fatalf("under cap: got %+v, want ShowAll=true with all 3 visible", under)
	}
}

// Guard test in the style of TestMatchRunGuardIsExclusive: organize and match
// must be mutually exclusive — a match pass and an apply-to-disk pass must
// never run concurrently against the same library.
func TestOrganizeGuardExcludesMatch(t *testing.T) {
	a := &Admin{}
	if !a.organizeRunning.CompareAndSwap(false, true) {
		t.Fatal("first acquisition failed")
	}
	if a.canStartMatchRun() {
		t.Fatal("match run allowed while organize is running")
	}
	a.organizeRunning.Store(false)
	if !a.canStartMatchRun() {
		t.Fatal("match run refused after organize released")
	}
}

// Mirror case: a match run in flight must block organize from starting.
func TestOrganizeGuardBlockedByMatch(t *testing.T) {
	a := &Admin{}
	if !a.matchRunning.CompareAndSwap(false, true) {
		t.Fatal("first acquisition failed")
	}
	if a.canStartOrganize() {
		t.Fatal("organize allowed while match is running")
	}
	a.matchRunning.Store(false)
	if !a.canStartOrganize() {
		t.Fatal("organize refused after match released")
	}
}

// A background library scan overlapping an organize apply can delete series
// whose folders organize just renamed mid-scan (stale slug set -> CASCADE
// wipes issues/matches/enrichment). Organize must refuse to start while a
// scan is running. The mirror direction (scan refused while organize is
// running) is covered by TestScanLibrarySkippedWhileOrganizeRunning in
// package server, since that's where the real ScanLibrary entry point lives.
func TestOrganizeGuardBlockedByScan(t *testing.T) {
	ss := NewScanState()
	a := &Admin{ScanState: ss}
	ss.StartScan()
	if a.canStartOrganize() {
		t.Fatal("organize allowed while a scan is running")
	}
	ss.FinishScan(ScanResult{})
	if !a.canStartOrganize() {
		t.Fatal("organize refused after scan finished")
	}
	a.organizeRunning.Store(false)
}

// The template editor must keep "default" first and never let a blank field
// wipe a stored template.
func TestOrganizeTemplateEditor(t *testing.T) {
	rows := templateRows(map[string]string{"tpb": "b", "annual": "a", "default": "d"})
	if rows[0].Format != "default" || rows[1].Format != "annual" || rows[2].Format != "tpb" {
		t.Fatalf("bad order: %+v", rows)
	}

	st, err := store.NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveRenameTemplates(map[string]string{"default": "{series} #{number:03}"}); err != nil {
		t.Fatal(err)
	}
	a := &Admin{Store: st}

	form := url.Values{"tpl_default": {"  "}} // blank: must not wipe
	r := httptest.NewRequest("POST", "/_admin/organize/templates", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.handleOrganizeTemplates(httptest.NewRecorder(), r)

	form = url.Values{"tpl_default": {"{series} {number}"}}
	r = httptest.NewRequest("POST", "/_admin/organize/templates", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.handleOrganizeTemplates(httptest.NewRecorder(), r)

	got, _ := st.ListRenameTemplates()
	if got["default"] != "{series} {number}" {
		t.Fatalf("template not saved (or wiped by blank): %q", got["default"])
	}
}
