package creators

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSplitRolesAndGroup(t *testing.T) {
	if got, want := SplitRoles("penciler, inker"), []string{"penciler", "inker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitRoles(%q) = %v, want %v", "penciler, inker", got, want)
	}
	if g := RoleGroup("penciller"); g != "art" {
		t.Fatalf("RoleGroup(penciller) = %q, want %q", g, "art")
	}
	if g := RoleGroup("cover"); g != "covers" {
		t.Fatalf("RoleGroup(cover) = %q, want %q", g, "covers")
	}
	if g := RoleGroup("letterer"); g != "" {
		t.Fatalf("RoleGroup(letterer) = %q, want \"\"", g)
	}
	if g := RoleGroup("plotter"); g != "" {
		t.Fatalf("RoleGroup(plotter) = %q, want \"\"", g)
	}
	if g := RoleGroup("cover artist"); g != "" {
		t.Fatalf("RoleGroup(\"cover artist\") = %q, want \"\"", g)
	}
}

func TestBuildIndexesCredits(t *testing.T) {
	cvPath := newCVFixture(t)
	seedIssue(t, cvPath, 10, 1, `[{"id":5,"name":"Abe Simon","role":"penciler, inker"}]`)
	seedIssue(t, cvPath, 11, 1, `[{"id":5,"name":"Abe Simon","role":"writer"}]`)

	idx, err := OpenIndex(filepath.Join(t.TempDir(), "creators_index.db"), cvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !idx.Ready() {
		t.Fatal("Ready() = false after Build")
	}

	var count int
	if err := idx.db.QueryRow(`SELECT count(*) FROM cv_credit WHERE person_id=5`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("count(*) WHERE person_id=5 = %d, want 3", count)
	}

	var volCount int
	if err := idx.db.QueryRow(
		`SELECT count(DISTINCT volume_id) FROM cv_credit WHERE person_id=5 AND role IN ('penciler','inker')`,
	).Scan(&volCount); err != nil {
		t.Fatal(err)
	}
	if volCount != 1 {
		t.Fatalf("distinct volume_id count = %d, want 1", volCount)
	}
}

func TestBuildIsIdempotentAndRebuildsOnSourceChange(t *testing.T) {
	cvPath := newCVFixture(t)
	seedIssue(t, cvPath, 10, 1, `[{"id":5,"role":"writer"}]`)

	idx, err := OpenIndex(filepath.Join(t.TempDir(), "creators_index.db"), cvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	builtAt1 := metaValue(t, idx, "built_at")

	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	builtAt2 := metaValue(t, idx, "built_at")
	if builtAt1 != builtAt2 {
		t.Fatalf("built_at changed on unchanged source: %s -> %s", builtAt1, builtAt2)
	}

	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(cvPath, future, future); err != nil {
		t.Fatal(err)
	}

	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	builtAt3 := metaValue(t, idx, "built_at")
	if builtAt3 == builtAt2 {
		t.Fatal("built_at unchanged after touching localcv mtime")
	}
}

func metaValue(t *testing.T, idx *Index, key string) string {
	t.Helper()
	var v string
	if err := idx.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
