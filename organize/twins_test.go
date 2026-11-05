package organize

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

// Mylar owns a twin folder ("saga v1" stripped of punctuation, here
// "sagav1"): it tracks each file's path in its own DB and re-downloads
// anything that goes missing. Organize must plan nothing for a twin-folder
// issue — no rename, no move — and must not rename the primary folder either,
// since the twin's issue ids hang off the series id that rename would change.
func TestPlanNeverTouchesTwinFolderIssues(t *testing.T) {
	st, m := newTestFixture(t)
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")
	primaryID := store.PathToID("saga v1/saga_01.cbz")
	twinID := store.PathToID("sagav1/saga_02.cbz")
	strayID := store.PathToID("sagav1/stray.cbz")
	seed(t, st, store.Series{ID: "saga-v1", Name: "saga v1", FolderPath: "/lib/saga v1", IssueCount: 3},
		[]store.Issue{
			{ID: primaryID, SeriesID: "saga-v1", Filename: "saga_01.cbz", Number: "1", Position: 0},
			{ID: twinID, SeriesID: "saga-v1", Filename: "saga_02.cbz", Number: "2", Position: 1},
			{ID: strayID, SeriesID: "saga-v1", Filename: "stray.cbz", Number: "3", Position: 2},
		},
		"49019", map[string]string{primaryID: "317966", twinID: "317967", strayID: "400001"})
	// The stray's volume has a confirmed home, so a primary-folder file
	// there would be moved.
	seed(t, st, store.Series{ID: "series-b", Name: "Other Book", FolderPath: "/lib/Other Book (2012)"}, nil, "51234", nil)

	p, err := New(st, m, nil, "").Plan()
	if err != nil {
		t.Fatal(err)
	}
	ops := flatten(p)
	wantOps(t, ops, Op{Kind: OpRenameFile, SeriesID: "saga-v1", IssueID: primaryID,
		Old: "saga_01.cbz", New: "Saga #001 (2012).cbz"})
	for _, op := range ops {
		if op.IssueID == twinID || op.IssueID == strayID {
			t.Fatalf("planned %+v for a twin-folder issue", op)
		}
		if op.Kind == OpRenameFolder {
			t.Fatalf("planned %+v for a series with a twin folder", op)
		}
	}
}

// Apply end to end on disk: the twin folder and its file are untouched.
func TestApplyLeavesTwinFolderAlone(t *testing.T) {
	st, m := newTestFixture(t)
	saveDefaultTemplate(t, st, "{series} #{number:03} ({year})")
	root := t.TempDir()
	primary := filepath.Join(root, "Saga (2012)")
	twin := filepath.Join(root, "Saga- (2012)")
	for _, f := range []string{filepath.Join(primary, "saga_01.cbz"), filepath.Join(twin, "saga_02.cbz")} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	primaryID := store.PathToID("Saga (2012)/saga_01.cbz")
	twinID := store.PathToID("Saga- (2012)/saga_02.cbz")
	seed(t, st, store.Series{ID: "saga-2012", Name: "Saga (2012)", FolderPath: primary, IssueCount: 2},
		[]store.Issue{
			{ID: primaryID, SeriesID: "saga-2012", Filename: "saga_01.cbz", Number: "1", Position: 0},
			{ID: twinID, SeriesID: "saga-2012", Filename: "saga_02.cbz", Number: "2", Position: 1},
		},
		"49019", map[string]string{primaryID: "317966", twinID: "317967"})

	if _, err := New(st, m, []string{root}, "").Apply(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(primary, "Saga #001 (2012).cbz")); err != nil {
		t.Fatalf("primary file not renamed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(twin, "saga_02.cbz")); err != nil {
		t.Fatalf("twin file moved: %v", err)
	}
	if !st.ComicExists(twinID) {
		t.Fatal("twin issue id changed")
	}
}
