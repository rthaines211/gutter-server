package creators

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func buildTestIndex(t *testing.T, cvPath string) *Index {
	t.Helper()
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "creators_index.db"), cvPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	return idx
}

// seedVolume inserts/updates a cv_volume row with the fields Bibliography
// and VolumeInfo read, and a cv_publisher row if publisherID is nonzero.
func seedVolume(t *testing.T, cvPath string, volumeID int64, name, startYear string, publisherID int64, countOfIssues int, publisherName string) {
	t.Helper()
	db := openFixtureDB(t, cvPath)
	defer db.Close()

	if publisherID != 0 {
		if _, err := db.Exec(
			`INSERT INTO cv_publisher(id, name) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET name=excluded.name`,
			publisherID, publisherName); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO cv_volume(id, name, start_year, publisher_id, count_of_issues) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, start_year=excluded.start_year,
			publisher_id=excluded.publisher_id, count_of_issues=excluded.count_of_issues`,
		volumeID, name, startYear, publisherID, countOfIssues); err != nil {
		t.Fatal(err)
	}
}

func seedPerson(t *testing.T, cvPath string, id int64, name string) {
	t.Helper()
	db := openFixtureDB(t, cvPath)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO cv_person(id, name) VALUES (?, ?)`, id, name); err != nil {
		t.Fatal(err)
	}
}

func TestBibliographySortedNewestFirst(t *testing.T) {
	cvPath := newCVFixture(t)
	// Person 5 writes on volume 1 (2020) and volume 2 (2018).
	seedIssue(t, cvPath, 10, 1, `[{"id":5,"name":"Abe Simon","role":"writer"}]`)
	seedIssue(t, cvPath, 20, 2, `[{"id":5,"name":"Abe Simon","role":"writer"}]`)
	seedVolume(t, cvPath, 1, "Later Volume", "2020", 0, 0, "")
	seedVolume(t, cvPath, 2, "Earlier Volume", "2018", 0, 0, "")

	idx := buildTestIndex(t, cvPath)

	got, err := idx.Bibliography(5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Bibliography() returned %d volumes, want 2: %+v", len(got), got)
	}
	if got[0].VolumeID != 1 || got[1].VolumeID != 2 {
		t.Fatalf("Bibliography() order = %+v, want volume 1 (2020) before volume 2 (2018)", got)
	}
	if got[0].StartYear != 2020 || got[1].StartYear != 2018 {
		t.Fatalf("Bibliography() StartYear = %d, %d, want 2020, 2018", got[0].StartYear, got[1].StartYear)
	}
}

func TestBibliographyRolesDeduped(t *testing.T) {
	cvPath := newCVFixture(t)
	seedIssue(t, cvPath, 10, 1, `[{"id":5,"name":"Abe Simon","role":"penciler, inker"}]`)
	seedIssue(t, cvPath, 11, 1, `[{"id":5,"name":"Abe Simon","role":"penciler, inker"}]`)
	seedVolume(t, cvPath, 1, "Test Volume", "2016", 0, 0, "")

	idx := buildTestIndex(t, cvPath)

	got, err := idx.Bibliography(5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Bibliography() returned %d volumes, want 1: %+v", len(got), got)
	}
	v := got[0]
	wantRoles := []string{"inker", "penciler"}
	if len(v.Roles) != len(wantRoles) {
		t.Fatalf("Roles = %v, want %v", v.Roles, wantRoles)
	}
	for i, r := range wantRoles {
		if v.Roles[i] != r {
			t.Fatalf("Roles = %v, want %v", v.Roles, wantRoles)
		}
	}
	if v.PersonIssues != 2 {
		t.Fatalf("PersonIssues = %d, want 2", v.PersonIssues)
	}
}

func TestBibliographyExcludesOutOfScopeRoles(t *testing.T) {
	cvPath := newCVFixture(t)
	// Letterer-only credit: RoleGroup("letterer") == "", so this volume
	// must not appear in the bibliography at all.
	seedIssue(t, cvPath, 10, 1, `[{"id":5,"name":"Abe Simon","role":"letterer"}]`)
	seedVolume(t, cvPath, 1, "Test Volume", "2016", 0, 0, "")

	idx := buildTestIndex(t, cvPath)

	got, err := idx.Bibliography(5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Bibliography() = %+v, want empty (letterer is out of scope)", got)
	}
}

func TestBibliographyOwnership(t *testing.T) {
	cvPath := newCVFixture(t)
	seedIssue(t, cvPath, 10, 1, `[{"id":5,"name":"Abe Simon","role":"writer"}]`)
	seedVolume(t, cvPath, 1, "Test Volume", "2016", 0, 12, "")

	idx := buildTestIndex(t, cvPath)

	own := map[int64]VolumeOwnership{1: {OwnedIssues: 7, SeriesID: "s1"}}
	got, err := idx.Bibliography(5, own)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Bibliography() = %+v, want 1 volume", got)
	}
	v := got[0]
	if v.TotalIssues != 12 || v.OwnedIssues != 7 || v.SeriesID != "s1" || v.Owned != "partial" {
		t.Fatalf("Bibliography()[0] = %+v, want TotalIssues=12 OwnedIssues=7 SeriesID=s1 Owned=partial", v)
	}
}

func TestPersonName(t *testing.T) {
	cvPath := newCVFixture(t)
	seedPerson(t, cvPath, 5, "Abe Simon")

	idx := buildTestIndex(t, cvPath)

	got, err := idx.PersonName(5)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Abe Simon" {
		t.Fatalf("PersonName(5) = %q, want %q", got, "Abe Simon")
	}
}

func TestPersonNameUnknown(t *testing.T) {
	cvPath := newCVFixture(t)
	idx := buildTestIndex(t, cvPath)

	if _, err := idx.PersonName(999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("PersonName(999) err = %v, want wrapped sql.ErrNoRows", err)
	}
}

func TestLookupByNameMultiple(t *testing.T) {
	cvPath := newCVFixture(t)
	seedPerson(t, cvPath, 5, "John Smith")
	seedPerson(t, cvPath, 6, "John Smith")
	seedPerson(t, cvPath, 7, "Someone Else")

	idx := buildTestIndex(t, cvPath)

	got, err := idx.LookupByName("john smith")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("LookupByName(\"john smith\") = %+v, want 2 matches", got)
	}
}

func TestLookupByNameVolumeCount(t *testing.T) {
	cvPath := newCVFixture(t)
	seedPerson(t, cvPath, 5, "Abe Simon")
	seedIssue(t, cvPath, 10, 1, `[{"id":5,"name":"Abe Simon","role":"writer"}]`)
	seedIssue(t, cvPath, 20, 2, `[{"id":5,"name":"Abe Simon","role":"writer"}]`)
	// Out-of-scope role on a third volume: must not count toward Volumes.
	seedIssue(t, cvPath, 30, 3, `[{"id":5,"name":"Abe Simon","role":"letterer"}]`)

	idx := buildTestIndex(t, cvPath)

	got, err := idx.LookupByName("Abe Simon")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("LookupByName() = %+v, want 1 match", got)
	}
	if got[0].Volumes != 2 {
		t.Fatalf("LookupByName()[0].Volumes = %d, want 2", got[0].Volumes)
	}
}

func TestVolumeInfo(t *testing.T) {
	cvPath := newCVFixture(t)
	seedVolume(t, cvPath, 1, "Saga", "2012", 9, 60, "Image")
	db := openFixtureDB(t, cvPath)
	if _, err := db.Exec(
		`UPDATE cv_volume SET image_url = ?, description = ? WHERE id = 1`,
		"https://example.com/saga.jpg", "<p>A <b>space opera</b>.</p>&amp; more"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	idx := buildTestIndex(t, cvPath)

	got, err := idx.VolumeInfo(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.VolumeID != 1 || got.Name != "Saga" || got.StartYear != 2012 ||
		got.Publisher != "Image" || got.CoverURL != "https://example.com/saga.jpg" || got.TotalIssues != 60 {
		t.Fatalf("VolumeInfo(1) = %+v", got)
	}
	if got.Description != "A space opera . & more" {
		t.Fatalf("VolumeInfo(1).Description = %q, want tags stripped and entities unescaped", got.Description)
	}
}

func TestVolumeInfoNullableColumns(t *testing.T) {
	// Real localcv rows can have NULL start_year, count_of_issues,
	// image_url and description (e.g. a volume ComicVine never fully
	// hydrated) — these must not error the scan.
	cvPath := newCVFixture(t)
	db := openFixtureDB(t, cvPath)
	if _, err := db.Exec(`INSERT INTO cv_volume(id, name) VALUES (1, 'Bare Volume')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	idx := buildTestIndex(t, cvPath)

	got, err := idx.VolumeInfo(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartYear != 0 || got.TotalIssues != 0 || got.CoverURL != "" || got.Description != "" {
		t.Fatalf("VolumeInfo(1) = %+v, want zero values for NULL columns", got)
	}
}

func TestVolumeInfoNotFound(t *testing.T) {
	cvPath := newCVFixture(t)
	idx := buildTestIndex(t, cvPath)

	if _, err := idx.VolumeInfo(999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("VolumeInfo(999) err = %v, want wrapped sql.ErrNoRows", err)
	}
}

func TestIssueCreditsSplitsRoles(t *testing.T) {
	cvPath := newCVFixture(t)
	seedIssue(t, cvPath, 10, 1,
		`[{"id":5,"name":"Abe Simon","role":"penciler, inker"},{"id":6,"name":"Jo Lark","role":"letterer"}]`)

	idx := buildTestIndex(t, cvPath)

	got, err := idx.IssueCredits(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("IssueCredits(10) = %+v, want 3 entries", got)
	}
	if got[0].ID != 5 || got[0].Name != "Abe Simon" || got[0].Role != "penciler" {
		t.Fatalf("IssueCredits(10)[0] = %+v", got[0])
	}
	if got[1].ID != 5 || got[1].Name != "Abe Simon" || got[1].Role != "inker" {
		t.Fatalf("IssueCredits(10)[1] = %+v", got[1])
	}
	// Letterer is out of scope for the bibliography, but IssueCredits
	// returns every role so the app can decide what's tappable.
	if got[2].ID != 6 || got[2].Name != "Jo Lark" || got[2].Role != "letterer" {
		t.Fatalf("IssueCredits(10)[2] = %+v", got[2])
	}
}

func TestBibliographyNotReady(t *testing.T) {
	cvPath := newCVFixture(t)
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "creators_index.db"), cvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if _, err := idx.Bibliography(5, nil); err != ErrNotReady {
		t.Fatalf("Bibliography() before Build err = %v, want ErrNotReady", err)
	}
	if _, err := idx.LookupByName("Abe Simon"); err != ErrNotReady {
		t.Fatalf("LookupByName() before Build err = %v, want ErrNotReady", err)
	}
}
