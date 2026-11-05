package organize

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/match"
	"github.com/rthaines211/gutter-server/store"
)

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be gone, stat err=%v", path, err)
	}
}

func TestApplyEndToEnd(t *testing.T) {
	st, m := newTestFixture(t)
	root := t.TempDir()
	seriesDir := filepath.Join(root, "saga v1")
	mustWriteFile(t, filepath.Join(seriesDir, "saga_01.cbz"))

	seed(t, st, store.Series{ID: "saga-v1", Name: "saga v1", FolderPath: seriesDir, IssueCount: 1},
		[]store.Issue{{ID: "i1", SeriesID: "saga-v1", Filename: "saga_01.cbz", Number: "1", Position: 0}},
		"49019", map[string]string{"i1": "317966"})
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	if err := st.UpsertProgress(store.Progress{ComicID: "i1", CurrentPage: 3, TotalPages: 10}); err != nil {
		t.Fatal(err)
	}

	o := New(st, m, []string{root}, "")
	report, err := o.Apply("")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.Applied != 2 || report.Skipped != 0 {
		t.Fatalf("report = %+v, want Applied:2 Skipped:0", report)
	}

	newDir := filepath.Join(root, "Saga (2012)")
	mustExist(t, filepath.Join(newDir, "Saga #001 (2012).cbz"))
	mustNotExist(t, seriesDir)
	mustNotExist(t, filepath.Join(newDir, "saga_01.cbz"))

	exists, err := st.SeriesExists("saga-2012")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected series saga-2012 to exist")
	}
	if exists, _ := st.SeriesExists("saga-v1"); exists {
		t.Fatal("expected old series id saga-v1 to be gone")
	}

	wantID := store.PathToID(filepath.Join("Saga (2012)", "Saga #001 (2012).cbz"))
	issues, err := st.ListIssues("saga-2012")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].ID != wantID || issues[0].Filename != "Saga #001 (2012).cbz" {
		t.Fatalf("issues = %+v, want single issue id=%s filename=Saga #001 (2012).cbz", issues, wantID)
	}

	prog, err := st.GetProgress(wantID)
	if err != nil {
		t.Fatalf("GetProgress(%s): %v", wantID, err)
	}
	if prog.CurrentPage != 3 || prog.TotalPages != 10 {
		t.Fatalf("progress = %+v, want current=3 total=10", prog)
	}

	ims, err := st.ListIssueMatchesForSeries("saga-2012")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 1 || ims[0].IssueID != wantID {
		t.Fatalf("issue_match = %+v, want single row with issue_id=%s", ims, wantID)
	}
}

// TestApplyMoveIntoRenamedFolder is the ordering test from the spec: series A
// ("old a", confirmed to volume 49019) gets renamed to "Saga (2012)" in phase
// 1. Series B holds a stray whose issue_match actually resolves into 49019 —
// it must be moved into A's NEW folder name, not the stale "old a", which
// only works if Apply re-plans between phases.
func TestApplyMoveIntoRenamedFolder(t *testing.T) {
	st, m := newTestFixture(t)
	root := t.TempDir()

	aDir := filepath.Join(root, "old a")
	mustWriteFile(t, filepath.Join(aDir, "Saga #001 (2012).cbz"))

	bDir := filepath.Join(root, "Other Book (2012)")
	mustWriteFile(t, filepath.Join(bDir, "Other Book #003 (2012).cbz"))
	mustWriteFile(t, filepath.Join(bDir, "strayfile.cbz"))

	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	// Series A: confirmed to Saga (49019); folder needs renaming, its one
	// issue is already canonically named so it gets no ops of its own.
	seed(t, st, store.Series{ID: "series-a", Name: "old a", FolderPath: aDir, IssueCount: 1},
		[]store.Issue{{ID: "a1", SeriesID: "series-a", Filename: "Saga #001 (2012).cbz", Number: "1", Position: 0}},
		"49019", map[string]string{"a1": "317966"})

	// Series B: confirmed to Other Book (51234); b1 is its real, canonically
	// named home issue. b2 is a stray whose issue_match resolves to volume
	// 49019 (Saga) — series A's volume — so it must move+retitle into A.
	seed(t, st, store.Series{ID: "series-b", Name: "Other Book", FolderPath: bDir, IssueCount: 2},
		[]store.Issue{
			{ID: "b1", SeriesID: "series-b", Filename: "Other Book #003 (2012).cbz", Number: "3", Position: 0},
			{ID: "b2", SeriesID: "series-b", Filename: "strayfile.cbz", Number: "2", Position: 1},
		},
		"51234", map[string]string{"b1": "400001", "b2": "317967"})

	o := New(st, m, []string{root}, "")
	report, err := o.Apply("")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.Applied != 3 || report.Skipped != 0 {
		t.Fatalf("report = %+v, want Applied:3 Skipped:0", report)
	}

	newADir := filepath.Join(root, "Saga (2012)")
	mustExist(t, filepath.Join(newADir, "Saga #001 (2012).cbz"))
	mustExist(t, filepath.Join(newADir, "Saga #002 (2012).cbz")) // moved + retitled stray
	mustNotExist(t, aDir)
	mustNotExist(t, filepath.Join(bDir, "strayfile.cbz"))
	mustExist(t, filepath.Join(bDir, "Other Book #003 (2012).cbz")) // untouched

	issues, err := st.ListIssues("saga-2012")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, iss := range issues {
		if iss.Filename == "Saga #002 (2012).cbz" {
			found = true
			if iss.SeriesID != "saga-2012" {
				t.Errorf("moved issue series_id = %s, want saga-2012", iss.SeriesID)
			}
		}
	}
	if !found {
		t.Fatalf("moved+retitled issue not found under saga-2012: %+v", issues)
	}

	bIssues, err := st.ListIssues("series-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(bIssues) != 1 || bIssues[0].Filename != "Other Book #003 (2012).cbz" {
		t.Fatalf("series-b issues = %+v, want only the untouched home issue", bIssues)
	}
}

// TestApplyConflictSkipsAndContinues pre-creates the file-rename target on
// disk (it rides along in the folder when phase 1 renames the folder), so
// phase 3's rename must skip with a recorded reason while phase 1's folder
// rename still goes through.
func TestApplyConflictSkipsAndContinues(t *testing.T) {
	st, m := newTestFixture(t)
	root := t.TempDir()
	seriesDir := filepath.Join(root, "saga v1")
	mustWriteFile(t, filepath.Join(seriesDir, "saga_01.cbz"))
	mustWriteFile(t, filepath.Join(seriesDir, "Saga #001 (2012).cbz")) // pre-existing conflict

	seed(t, st, store.Series{ID: "saga-v1", Name: "saga v1", FolderPath: seriesDir, IssueCount: 1},
		[]store.Issue{{ID: "i1", SeriesID: "saga-v1", Filename: "saga_01.cbz", Number: "1", Position: 0}},
		"49019", map[string]string{"i1": "317966"})
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	o := New(st, m, []string{root}, "")
	report, err := o.Apply("")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.Applied != 1 {
		t.Errorf("Applied = %d, want 1 (folder rename)", report.Applied)
	}
	if report.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", report.Skipped)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "already exists") {
		t.Fatalf("Errors = %+v, want one entry mentioning the conflict", report.Errors)
	}

	newDir := filepath.Join(root, "Saga (2012)")
	mustExist(t, filepath.Join(newDir, "saga_01.cbz"))          // rename skipped, old name survives
	mustExist(t, filepath.Join(newDir, "Saga #001 (2012).cbz")) // pre-existing file untouched
	mustNotExist(t, seriesDir)                                  // folder rename still applied
}

// caseOnlySame must require actual same-file identity (os.SameFile), not
// just EqualFold: on a case-sensitive filesystem "a" and "A" can be two
// distinct real entries, and bypassing the conflict skip there would let
// os.Rename silently replace the target.
func TestCaseOnlySameRequiresIdentity(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "book.cbz")
	mustWriteFile(t, a)

	variant := filepath.Join(dir, "Book.cbz")
	if _, err := os.Stat(variant); err == nil {
		// Case-insensitive FS (APFS): variant resolves to a — same file, so
		// the bypass is allowed.
		if !caseOnlySame(a, variant) {
			t.Error("case-variant path to the same file should satisfy caseOnlySame")
		}
	} else {
		// Case-sensitive FS: create a REAL distinct file at the variant path.
		// EqualFold holds but SameFile must not — bypass refused.
		mustWriteFile(t, variant)
		if caseOnlySame(a, variant) {
			t.Error("two distinct files differing only by case must NOT satisfy caseOnlySame")
		}
	}

	// Different names never qualify, regardless of FS.
	b := filepath.Join(dir, "other.cbz")
	mustWriteFile(t, b)
	if caseOnlySame(a, b) {
		t.Error("distinct files with different names must not satisfy caseOnlySame")
	}
	// Missing target never qualifies.
	if caseOnlySame(a, filepath.Join(dir, "Missing.cbz")) {
		t.Error("nonexistent path must not satisfy caseOnlySame")
	}
}

// Production loop (Oct 2026): the library sits on a case-insensitive host
// volume reached through virtiofs, where a case-only folder rename reported
// success but the scanner kept listing the old spelling. Each organize
// "renamed" By->by in the DB, each scan re-IDed the issues back from disk
// (wiping their matches), and the pipeline re-filled and re-organized the
// series every 30 minutes. A name that differs from the canonical one only
// by case is already correct: no op is planned, so an unchanged library
// organizes to nothing, and a second organize after an apply plans nothing.
func TestOrganizeTwiceSecondPlanIsEmpty(t *testing.T) {
	root := t.TempDir()
	st, m := newTestFixture(t)
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	// Case-only differences everywhere: folder and file.
	caseDir := filepath.Join(root, "saga (2012)")
	mustWriteFile(t, filepath.Join(caseDir, "saga #001 (2012).cbz"))
	seed(t, st, store.Series{ID: "saga-2012", Name: "saga (2012)", FolderPath: caseDir, IssueCount: 1},
		[]store.Issue{{ID: "i1", SeriesID: "saga-2012", Filename: "saga #001 (2012).cbz", Number: "1", Position: 0}},
		"49019", map[string]string{"i1": "317966"})
	// A real rename still happens on the first pass.
	otherDir := filepath.Join(root, "other v1")
	mustWriteFile(t, filepath.Join(otherDir, "other_03.cbz"))
	seed(t, st, store.Series{ID: "other-v1", Name: "other v1", FolderPath: otherDir, IssueCount: 1},
		[]store.Issue{{ID: "i3", SeriesID: "other-v1", Filename: "other_03.cbz", Number: "3", Position: 0}},
		"51234", map[string]string{"i3": "400001"})

	o := New(st, m, []string{root}, "")
	first, err := o.Plan()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range flatten(first) {
		if op.SeriesID == "saga-2012" {
			t.Fatalf("case-only difference planned %+v", op)
		}
	}
	if len(flatten(first)) == 0 {
		t.Fatal("first plan empty; want the real rename for other v1")
	}
	if _, err := o.Apply(""); err != nil {
		t.Fatal(err)
	}
	second, err := o.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if ops := flatten(second); len(ops) != 0 {
		t.Fatalf("second plan = %+v, want empty", ops)
	}
	mustExist(t, filepath.Join(caseDir, "saga #001 (2012).cbz"))
}

func TestApplyRefusesWhenRootMissing(t *testing.T) {
	st, m := newTestFixture(t)
	root := t.TempDir()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	o := New(st, m, []string{root}, "")
	report, err := o.Apply("")
	if err == nil {
		t.Fatal("expected an error for a missing library root")
	}
	if report.Applied != 0 || report.Skipped != 0 || len(report.Errors) != 0 {
		t.Fatalf("report = %+v, want zero value (no work done)", report)
	}
}

// TestApplyFolderOnlyRenameTouchesSeries: MigrateSeriesFolder doesn't bump
// series.updated_at itself, and a folder-only rename (filename already
// canonical) runs no other migration that would — so Apply must TouchSeries
// or delta-sync clients never learn about the rename. Stale-seed + strftime
// pattern from store/migrate_test.go, via a second connection to the store's
// sqlite file since the organize package can't reach store's private db.
func TestApplyFolderOnlyRenameTouchesSeries(t *testing.T) {
	dbPath := t.TempDir() + "/app.db"
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := match.NewRunner(st, newCVFixture(t), nil, func() int64 { return 1000 })

	root := t.TempDir()
	seriesDir := filepath.Join(root, "saga v1")
	mustWriteFile(t, filepath.Join(seriesDir, "Saga #001 (2012).cbz")) // already canonical: folder rename is the only op

	seed(t, st, store.Series{ID: "saga-v1", Name: "saga v1", FolderPath: seriesDir, IssueCount: 1},
		[]store.Issue{{ID: "i1", SeriesID: "saga-v1", Filename: "Saga #001 (2012).cbz", Number: "1", Position: 0}},
		"49019", map[string]string{"i1": "317966"})
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`UPDATE series SET updated_at='2000-01-01 00:00:00' WHERE id='saga-v1'`); err != nil {
		t.Fatal(err)
	}

	report, err := New(st, m, []string{root}, "").Apply("")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.Applied != 1 || report.Skipped != 0 {
		t.Fatalf("report = %+v, want Applied:1 Skipped:0 (folder rename only)", report)
	}

	const staleEpoch = 946684800 // 2000-01-01T00:00:00Z
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM series WHERE id='saga-2012'
		AND CAST(strftime('%s', updated_at) AS INTEGER) > ?`, staleEpoch).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("series updated_at was not bumped after folder-only rename; delta-sync would miss it")
	}
}

// A duplicate (empty) folder with the same name in an earlier root must not
// hijack path resolution: the applier uses the series row's absolute
// folder_path, not a basename search across roots. Regression for the real
// "empty Battleworld (2025) in root 1, real one in root 2" case.
func TestApplyUsesRecordedFolderPathNotBasenameSearch(t *testing.T) {
	st, m := newTestFixture(t)
	root1, root2 := t.TempDir(), t.TempDir()

	// Decoy: same folder name, EMPTY, in the first root.
	if err := os.MkdirAll(filepath.Join(root1, "Saga (2012)"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Real series lives in the second root, already canonically named.
	realFolder := filepath.Join(root2, "Saga (2012)")
	if err := os.MkdirAll(realFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realFolder, "saga_01.cbz"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	seed(t, st, store.Series{ID: "saga-2012", Name: "Saga (2012)", FolderPath: realFolder, IssueCount: 1},
		[]store.Issue{{ID: "i1", SeriesID: "saga-2012", Filename: "saga_01.cbz", Number: "1", Position: 0}},
		"49019", map[string]string{"i1": "317966"})
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")

	rep, err := New(st, m, []string{root1, root2}, "").Apply("")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied != 1 || rep.Skipped != 0 {
		t.Fatalf("want 1 applied 0 skipped, got %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(realFolder, "Saga #001 (2012).cbz")); err != nil {
		t.Fatalf("rename did not land in the real (second-root) folder: %v", err)
	}
}
