package store

import (
	"path/filepath"
	"testing"
)

// Upgrading a library must announce nothing: the migration that creates
// announced_issues seeds it with every issue already there. Issues that
// arrive later are claimed exactly once, and an id migration (organize
// rename) carries the announcement instead of making the issue "new".
func TestClaimNewIssuesOncePerIssue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	st, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeries(Series{ID: "saga", Name: "Saga (2012)", FolderPath: "/lib/Saga (2012)"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("saga", []Issue{{ID: "old1", SeriesID: "saga", Filename: "a.cbz"}}); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-upgrade DB: no announced_issues table yet.
	if _, err := st.DB().Exec(`DROP TABLE announced_issues`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got, err := st.ClaimNewIssues(); err != nil || len(got) != 0 {
		t.Fatalf("after upgrade claimed %+v, %v; want nothing", got, err)
	}

	if err := st.ReplaceIssues("saga", []Issue{
		{ID: "old1", SeriesID: "saga", Filename: "a.cbz"},
		{ID: "new2", SeriesID: "saga", Filename: "b.cbz", Position: 1},
		{ID: "new3", SeriesID: "saga", Filename: "c.cbz", Position: 2},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ClaimNewIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SeriesID != "saga" || got[0].SeriesName != "Saga (2012)" || got[0].Count != 2 {
		t.Fatalf("claimed %+v, want saga x2", got)
	}
	if again, _ := st.ClaimNewIssues(); len(again) != 0 {
		t.Fatalf("second claim %+v, want nothing", again)
	}

	if err := st.MigrateComicIDFull("new2", "renamed2", "B.cbz"); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.ClaimNewIssues(); len(again) != 0 {
		t.Fatalf("renamed issue re-announced: %+v", again)
	}
}
