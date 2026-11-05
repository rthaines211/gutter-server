package store

import (
	"reflect"
	"testing"
)

// seedCVIssue seeds a series (with enrichment publisher) plus an enriched issue.
func seedCVIssue(t *testing.T, s *Store, issueID, seriesName, number string, cvID int64, coverDate, publisher string) {
	t.Helper()
	seriesID := "ser-" + seriesName + "-" + publisher
	for _, q := range []struct {
		sql  string
		args []interface{}
	}{
		{`INSERT OR IGNORE INTO series (id, name, folder_path) VALUES (?, ?, ?)`, []interface{}{seriesID, seriesName, "/p/" + seriesID}},
		{`INSERT OR IGNORE INTO series_enrichment (series_id, comicvine_volume_id, publisher, status, enriched_at) VALUES (?, 1, ?, '', 0)`, []interface{}{seriesID, publisher}},
		{`INSERT INTO issues (id, series_id, filename, number) VALUES (?, ?, ?, ?)`, []interface{}{issueID, seriesID, issueID + ".cbz", number}},
		{`INSERT INTO issue_enrichment (issue_id, comicvine_issue_id, cover_date, enriched_at) VALUES (?, ?, ?, 0)`, []interface{}{issueID, cvID, coverDate}},
	} {
		if _, err := s.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func credits(arcID int64, name string, cvIDs ...int64) []CVArcCredit {
	out := make([]CVArcCredit, len(cvIDs))
	for i, id := range cvIDs {
		out[i] = CVArcCredit{CVIssueID: id, ArcID: arcID, ArcName: name}
	}
	return out
}

func mustRebuild(t *testing.T, s *Store, c []CVArcCredit) int {
	t.Helper()
	n, err := s.RebuildComicVineArcs(c)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func arcIDSet(t *testing.T, s *Store) map[string]bool {
	t.Helper()
	ids, err := s.ListArcIDs()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func TestEnrichedCVIssueIDs(t *testing.T) {
	s := newTestStore(t)
	seedCVIssue(t, s, "i1", "A", "1", 10, "", "Marvel")
	seedCVIssue(t, s, "i2", "A", "2", 10, "", "Marvel")
	seedCVIssue(t, s, "i3", "A", "3", 0, "", "Marvel")
	seedCVIssue(t, s, "i4", "A", "4", 11, "", "Marvel")
	got, err := s.EnrichedCVIssueIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int64{10, 11}) {
		t.Fatalf("got %v", got)
	}
}

func TestRebuildSkipsArcsUnder3(t *testing.T) {
	s := newTestStore(t)
	seedCVIssue(t, s, "i1", "A", "1", 10, "", "Marvel")
	seedCVIssue(t, s, "i2", "A", "2", 11, "", "Marvel")
	if n := mustRebuild(t, s, credits(5, "Small", 10, 11, 99)); n != 0 {
		t.Fatalf("changed = %d", n)
	}
	if arcIDSet(t, s)["cv-5"] {
		t.Fatal("arc with 2 library issues must not be stored")
	}
}

func TestRebuildDedupesByCVIssue(t *testing.T) {
	s := newTestStore(t)
	seedCVIssue(t, s, "copyA", "A", "1", 10, "2025-01-01", "Marvel")
	seedCVIssue(t, s, "copyB", "B", "1", 10, "2025-01-01", "Marvel")
	seedCVIssue(t, s, "i11", "A", "2", 11, "2025-02-01", "Marvel")
	seedCVIssue(t, s, "i12", "A", "3", 12, "2025-03-01", "Marvel")
	if _, err := s.db.Exec(`INSERT INTO reading_progress (comic_id, current_page, total_pages) VALUES ('copyB', 3, 20)`); err != nil {
		t.Fatal(err)
	}
	mustRebuild(t, s, credits(7, "Dup", 10, 11, 12))
	ids, _ := s.ListArcIssueIDs("cv-7")
	if !reflect.DeepEqual(ids, []string{"copyB", "i11", "i12"}) {
		t.Fatalf("got %v", ids)
	}
}

func TestRebuildReadingOrder(t *testing.T) {
	s := newTestStore(t)
	seedCVIssue(t, s, "may", "C", "1", 1, "2025-05-01", "Marvel")
	seedCVIssue(t, s, "mar", "C", "2", 2, "2025-03-01", "Marvel")
	seedCVIssue(t, s, "nodate", "C", "3", 3, "", "Marvel")
	seedCVIssue(t, s, "b10", "B", "10", 4, "2025-04-01", "Marvel")
	seedCVIssue(t, s, "a2", "A", "2", 5, "2025-04-01", "Marvel")
	mustRebuild(t, s, credits(8, "Order", 1, 2, 3, 4, 5))
	ids, _ := s.ListArcIssueIDs("cv-8")
	if !reflect.DeepEqual(ids, []string{"mar", "a2", "b10", "may", "nodate"}) {
		t.Fatalf("got %v", ids)
	}
}

func TestRebuildMylarWins(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO story_arcs (id, name, cv_arc_id, source) VALUES ('m1', 'Mylar', '57081', 'mylar')`); err != nil {
		t.Fatal(err)
	}
	seedCVIssue(t, s, "i1", "A", "1", 10, "", "Marvel")
	seedCVIssue(t, s, "i2", "A", "2", 11, "", "Marvel")
	seedCVIssue(t, s, "i3", "A", "3", 12, "", "Marvel")
	mustRebuild(t, s, credits(57081, "Same", 10, 11, 12))
	got := arcIDSet(t, s)
	if got["cv-57081"] || !got["m1"] {
		t.Fatalf("got %v", got)
	}
}

func TestRebuildMylarWinsWithPrefixedCVArcID(t *testing.T) {
	s := newTestStore(t)
	// Mylar stores cv_arc_id with the ComicVine story-arc type prefix ("4045-").
	if _, err := s.db.Exec(`INSERT INTO story_arcs (id, name, cv_arc_id, source) VALUES ('m1', 'Mylar', '4045-57081', 'mylar')`); err != nil {
		t.Fatal(err)
	}
	seedCVIssue(t, s, "i1", "A", "1", 10, "", "Marvel")
	seedCVIssue(t, s, "i2", "A", "2", 11, "", "Marvel")
	seedCVIssue(t, s, "i3", "A", "3", 12, "", "Marvel")
	mustRebuild(t, s, credits(57081, "Same", 10, 11, 12))
	got := arcIDSet(t, s)
	if got["cv-57081"] || !got["m1"] {
		t.Fatalf("got %v", got)
	}
}

func TestRebuildSkipsCreditsWithNoArcID(t *testing.T) {
	s := newTestStore(t)
	seedCVIssue(t, s, "i1", "A", "1", 10, "", "Marvel")
	seedCVIssue(t, s, "i2", "A", "2", 11, "", "Marvel")
	seedCVIssue(t, s, "i3", "A", "3", 12, "", "Marvel")
	c := append(credits(0, "NoArc", 10, 11, 12), credits(-1, "NegArc", 10, 11, 12)...)
	if n := mustRebuild(t, s, c); n != 0 {
		t.Fatalf("changed = %d, want 0", n)
	}
	got := arcIDSet(t, s)
	if got["cv-0"] || got["cv--1"] {
		t.Fatalf("got %v", got)
	}
}

func TestRebuildPrunesOnlyComicVine(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO story_arcs (id, name, cv_arc_id, source) VALUES
		('cv-9', 'Old', '9', 'comicvine'), ('m1', 'Mylar', '1', 'mylar')`); err != nil {
		t.Fatal(err)
	}
	if n := mustRebuild(t, s, nil); n != 1 {
		t.Fatalf("changed = %d, want 1", n)
	}
	got := arcIDSet(t, s)
	if got["cv-9"] || !got["m1"] {
		t.Fatalf("got %v", got)
	}
}

func TestRebuildNoChangeKeepsUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	seedCVIssue(t, s, "i1", "A", "1", 10, "2025-01-01", "Marvel")
	seedCVIssue(t, s, "i2", "A", "2", 11, "2025-02-01", "Marvel")
	seedCVIssue(t, s, "i3", "A", "3", 12, "2025-03-01", "Marvel")
	c := credits(3, "Stable", 10, 11, 12)
	if n := mustRebuild(t, s, c); n != 1 {
		t.Fatalf("first run changed = %d, want 1", n)
	}
	// Backdate so a spurious bump would be visible despite second-resolution timestamps.
	if _, err := s.db.Exec(`UPDATE story_arcs SET updated_at = '2000-01-01 00:00:00' WHERE id = 'cv-3'`); err != nil {
		t.Fatal(err)
	}
	if n := mustRebuild(t, s, c); n != 0 {
		t.Fatalf("second run changed = %d, want 0", n)
	}
	var ts string
	s.db.QueryRow(`SELECT updated_at FROM story_arcs WHERE id = 'cv-3'`).Scan(&ts)
	if ts != "2000-01-01T00:00:00Z" && ts != "2000-01-01 00:00:00" {
		t.Fatalf("updated_at bumped: %s", ts)
	}
}

func TestRebuildPublisherAndYears(t *testing.T) {
	s := newTestStore(t)
	seedCVIssue(t, s, "i1", "A", "1", 10, "2025-03-01", "Marvel")
	seedCVIssue(t, s, "i2", "B", "1", 11, "2026-01-01", "Marvel")
	seedCVIssue(t, s, "i3", "C", "1", 12, "", "DC")
	mustRebuild(t, s, credits(4, "Pub", 10, 11, 12))
	arcs, _ := s.ListArcsUpdatedSince(0)
	if len(arcs) != 1 {
		t.Fatalf("got %+v", arcs)
	}
	a := arcs[0]
	if a.ID != "cv-4" || a.Source != "comicvine" || a.CVArcID != "4" || a.Name != "Pub" ||
		a.Publisher != "Marvel" || a.StartYear != 2025 || a.EndYear != 2026 {
		t.Fatalf("got %+v", a)
	}
}

func TestRebuildRecomputesMylarPublisherAndYears(t *testing.T) {
	s := newTestStore(t)
	seedCVIssue(t, s, "i1", "A", "1", 10, "2019-06-01", "DC")
	seedCVIssue(t, s, "i2", "A", "2", 11, "2020-06-01", "DC")
	if _, err := s.db.Exec(`INSERT INTO story_arcs (id, name, source, updated_at) VALUES ('m1', 'Mylar', 'mylar', '2000-01-01 00:00:00');
		INSERT INTO story_arc_issues (arc_id, issue_id, position) VALUES ('m1', 'i1', 0), ('m1', 'i2', 1)`); err != nil {
		t.Fatal(err)
	}
	if n := mustRebuild(t, s, nil); n != 1 {
		t.Fatalf("changed = %d, want 1", n)
	}
	arcs, _ := s.ListArcsUpdatedSince(0)
	if len(arcs) != 1 || arcs[0].Publisher != "DC" || arcs[0].StartYear != 2019 || arcs[0].EndYear != 2020 ||
		arcs[0].UpdatedAt.Year() == 2000 {
		t.Fatalf("got %+v", arcs)
	}
	if n := mustRebuild(t, s, nil); n != 0 {
		t.Fatalf("second run changed = %d, want 0", n)
	}
}
