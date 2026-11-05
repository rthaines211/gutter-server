package store

import "testing"

// seedArcIssue seeds a series+issue for arc-import tests. Named distinctly
// from tagger_test.go's seedIssue(t, s, id) — same package, different signature.
func seedArcIssue(t *testing.T, s *Store, id, seriesID, number string) {
	t.Helper()
	if _, err := s.db.Exec(
		`INSERT INTO series (id, name, folder_path) VALUES (?, ?, ?)`,
		seriesID, "S", "/p/"+seriesID); err != nil {
		// series may already exist across issues in the same series — ignore dup
	}
	if _, err := s.db.Exec(
		`INSERT INTO issues (id, series_id, filename, number) VALUES (?, ?, ?, ?)`,
		id, seriesID, id+".cbz", number); err != nil {
		t.Fatal(err)
	}
}

func TestImportArcsMatchesAndPrunes(t *testing.T) {
	s := newTestStore(t)
	// issueA resolvable by CV issue id; issueB only by (volume, number).
	seedArcIssue(t, s, "issueA", "ser1", "1")
	seedArcIssue(t, s, "issueB", "ser1", "2")
	if _, err := s.db.Exec(
		`INSERT INTO issue_enrichment (issue_id, comicvine_issue_id, enriched_at) VALUES (?, ?, 0)`,
		"issueA", 111); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO series_enrichment (series_id, comicvine_volume_id, status, enriched_at) VALUES (?, ?, '', 0)`,
		"ser1", 900); err != nil {
		t.Fatal(err)
	}

	yes := true
	arcs := []ArcInput{{
		Key: "arc1", Name: "Arc One", Image: "img", CVArcID: "4045-1",
		SelfContained: &yes, TotalIssues: 3,
		Issues: []ArcIssueInput{
			{CVIssueID: "111", CVVolumeID: "900", Number: "1", Order: 1},   // matches issueA by CV id
			{CVIssueID: "gcd-x", CVVolumeID: "900", Number: "2", Order: 2}, // matches issueB by volume+number
			{CVIssueID: "999", CVVolumeID: "901", Number: "9", Order: 3},   // no local issue → skipped
		},
	}}
	if err := s.ImportArcs(arcs); err != nil {
		t.Fatal(err)
	}

	ids, err := s.ListArcIssueIDs("arc1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "issueA" || ids[1] != "issueB" {
		t.Fatalf("want [issueA issueB], got %v", ids)
	}
	got, _ := s.ListArcsUpdatedSince(0)
	if len(got) != 1 || got[0].TotalIssues != 3 || got[0].SelfContained == nil || !*got[0].SelfContained {
		t.Fatalf("arc metadata wrong: %+v", got)
	}

	// Re-import without arc1 → it is pruned.
	if err := s.ImportArcs([]ArcInput{}); err != nil {
		t.Fatal(err)
	}
	allIDs, _ := s.ListArcIDs()
	if len(allIDs) != 0 {
		t.Fatalf("arc1 should be pruned, got %v", allIDs)
	}
}

// A real DB fault during issue resolution (not sql.ErrNoRows) must abort the
// import, not be silently swallowed into a dropped issue + "successful" return.
func TestImportArcsPropagatesDBError(t *testing.T) {
	s := newTestStore(t)
	seedArcIssue(t, s, "issueA", "ser1", "1")
	// Drop the table the tier-1 lookup reads so the query errors with
	// "no such table" (not ErrNoRows).
	if _, err := s.db.Exec(`DROP TABLE issue_enrichment`); err != nil {
		t.Fatal(err)
	}
	arcs := []ArcInput{{
		Key: "arc1", Name: "Arc One", TotalIssues: 1,
		Issues: []ArcIssueInput{{CVIssueID: "111", CVVolumeID: "900", Number: "1", Order: 1}},
	}}
	if err := s.ImportArcs(arcs); err == nil {
		t.Fatal("expected ImportArcs to propagate the DB fault, got nil")
	}
}

// A ComicVine-sourced arc (inserted directly, as the future CV rebuild will)
// must survive a Mylar import that doesn't mention it; Mylar's prune scopes
// to source='mylar' only.
func TestImportArcsKeepsComicVineArcs(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(
		`INSERT INTO story_arcs (id, name, source, updated_at) VALUES (?, ?, 'comicvine', CURRENT_TIMESTAMP)`,
		"cv-1", "X"); err != nil {
		t.Fatal(err)
	}

	arcs := []ArcInput{{Key: "arc1", Name: "Arc One", TotalIssues: 0}}
	if err := s.ImportArcs(arcs); err != nil {
		t.Fatal(err)
	}

	ids, err := s.ListArcIDs()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found["cv-1"] {
		t.Fatalf("cv-1 (comicvine arc) should survive Mylar import, got %v", ids)
	}
	if !found["arc1"] {
		t.Fatalf("arc1 (mylar arc) should be present, got %v", ids)
	}

	got, err := s.ListArcsUpdatedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	var mylarArc *Arc
	for i := range got {
		if got[i].ID == "arc1" {
			mylarArc = &got[i]
		}
	}
	if mylarArc == nil || mylarArc.Source != "mylar" {
		t.Fatalf("expected arc1.Source == mylar, got %+v", mylarArc)
	}
}

func TestArcsNeedingDescriptionFiltersAndParses(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`
		INSERT INTO story_arcs (id, name, source, cv_arc_id, description_fetched_at, description_version, updated_at) VALUES
			('cv1', 'CV Arc', 'comicvine', '57081', 0, 0, CURRENT_TIMESTAMP),
			('mylar1', 'Mylar Arc', 'mylar', '4045-57082', 0, 0, CURRENT_TIMESTAMP),
			('nocv', 'No CV id', 'mylar', '', 0, 0, CURRENT_TIMESTAMP),
			('badid', 'Unparseable', 'mylar', '4045-notanumber', 0, 0, CURRENT_TIMESTAMP),
			('done', 'Already fetched, current version', 'comicvine', '57083', 1700000000000, 2, CURRENT_TIMESTAMP),
			('stale', 'Fetched under an older version', 'comicvine', '57084', 1700000000000, 1, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	got, err := s.ArcsNeedingDescription(10, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"cv1": 57081, "mylar1": 57082, "stale": 57084}
	if len(got) != len(want) {
		t.Fatalf("want %d targets, got %+v", len(want), got)
	}
	for _, target := range got {
		cv, ok := want[target.ArcID]
		if !ok || cv != target.CVArcID {
			t.Fatalf("unexpected target %+v", target)
		}
	}
}

func TestSetArcDescriptionBumpsUpdatedAtOnlyOnChange(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`
		INSERT INTO story_arcs (id, name, updated_at) VALUES ('arc1', 'Arc One', '2020-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}

	if err := s.SetArcDescription("arc1", "A description.", 1700000000000, 1); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListArcsUpdatedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Description != "A description." {
		t.Fatalf("description not set: %+v", got)
	}
	if got[0].UpdatedAt.Year() == 2020 {
		t.Fatalf("updated_at should have moved on a real change: %+v", got[0].UpdatedAt)
	}
	firstUpdatedAt := got[0].UpdatedAt

	// Same description, different fetched_at/version: updated_at must NOT move again.
	if err := s.SetArcDescription("arc1", "A description.", 1800000000000, 2); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListArcsUpdatedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].UpdatedAt.Equal(firstUpdatedAt) {
		t.Fatalf("updated_at moved on an unchanged description: before=%v after=%v", firstUpdatedAt, got[0].UpdatedAt)
	}

	// But the version/fetched_at must still have moved, so a later
	// ArcsNeedingDescription(limit, 2) call doesn't pick this arc up again.
	var version int
	var fetchedAt int64
	if err := s.db.QueryRow(`SELECT description_version, description_fetched_at FROM story_arcs WHERE id = 'arc1'`).
		Scan(&version, &fetchedAt); err != nil {
		t.Fatal(err)
	}
	if version != 2 || fetchedAt != 1800000000000 {
		t.Fatalf("expected version/fetched_at updated even without a text change, got version=%d fetchedAt=%d", version, fetchedAt)
	}
}

func TestListArcsReturnsShelfFields(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(
		`INSERT INTO story_arcs (id, name, publisher, start_year, end_year, updated_at)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		"arc1", "Arc One", "Marvel", 2025, 2026); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListArcsUpdatedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 arc, got %d", len(got))
	}
	a := got[0]
	if a.Publisher != "Marvel" || a.StartYear != 2025 || a.EndYear != 2026 {
		t.Fatalf("shelf fields wrong: %+v", a)
	}
}
