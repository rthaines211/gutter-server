package store

import "testing"

// TestReplaceIssuesPreservesEdits verifies that a rescan (ReplaceIssues) keeps
// format/writer/penciller/number and issue_enrichment.title on an existing issue
// while correctly adding a newly discovered issue row.
func TestReplaceIssuesPreservesEdits(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs", "iss1", "issue1.cbz")

	// Simulate in-app edits to the existing issue.
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`UPDATE issues SET number='42', format='tpb', writer='Alan Moore', penciller='Dave Gibbons' WHERE id='iss1'`)
	mustExec(`UPDATE issue_enrichment SET title='Edited Title' WHERE issue_id='iss1'`)

	// Rescan: same issue (position/filename updated) + a new issue.
	err := s.ReplaceIssues("srs", []Issue{
		{ID: "iss1", SeriesID: "srs", Filename: "issue1_renamed.cbz", Number: "1", Position: 0},
		{ID: "iss2", SeriesID: "srs", Filename: "issue2.cbz", Number: "2", Position: 1},
	})
	if err != nil {
		t.Fatalf("ReplaceIssues: %v", err)
	}

	// Edited fields must be preserved on iss1.
	var num, format, writer, penciller string
	s.db.QueryRow(`SELECT number, COALESCE(format,''), COALESCE(writer,''), COALESCE(penciller,'') FROM issues WHERE id='iss1'`).
		Scan(&num, &format, &writer, &penciller)
	if num != "42" {
		t.Errorf("number: want '42', got %q", num)
	}
	if format != "tpb" {
		t.Errorf("format: want 'tpb', got %q", format)
	}
	if writer != "Alan Moore" {
		t.Errorf("writer: want 'Alan Moore', got %q", writer)
	}
	if penciller != "Dave Gibbons" {
		t.Errorf("penciller: want 'Dave Gibbons', got %q", penciller)
	}

	// Structural fields (filename, position) must be updated.
	var fn string
	var pos int
	s.db.QueryRow(`SELECT filename, position FROM issues WHERE id='iss1'`).Scan(&fn, &pos)
	if fn != "issue1_renamed.cbz" {
		t.Errorf("filename: want 'issue1_renamed.cbz', got %q", fn)
	}
	if pos != 0 {
		t.Errorf("position: want 0, got %d", pos)
	}

	// issue_enrichment title must survive the rescan.
	var title string
	s.db.QueryRow(`SELECT title FROM issue_enrichment WHERE issue_id='iss1'`).Scan(&title)
	if title != "Edited Title" {
		t.Errorf("issue_enrichment title: want 'Edited Title', got %q", title)
	}

	// New issue row must have been added.
	if countWhere(t, s, "issues", "id", "iss2") != 1 {
		t.Error("new issue iss2 not inserted")
	}
}

// TestReplaceIssuesDeletesRemovedFile verifies that an issue row (and its
// enrichment) is deleted when the file is no longer in the scanned set.
func TestReplaceIssuesDeletesRemovedFile(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs", "iss1", "issue1.cbz")
	seedComic(t, s, "srs", "iss2", "issue2.cbz")

	// Rescan sees only iss1; iss2 has been removed from disk.
	err := s.ReplaceIssues("srs", []Issue{
		{ID: "iss1", SeriesID: "srs", Filename: "issue1.cbz", Number: "1", Position: 0},
	})
	if err != nil {
		t.Fatalf("ReplaceIssues: %v", err)
	}

	if countWhere(t, s, "issues", "id", "iss2") != 0 {
		t.Error("removed issue iss2 should have been deleted")
	}
	if countWhere(t, s, "issue_enrichment", "issue_id", "iss2") != 0 {
		t.Error("issue_enrichment for removed iss2 should have been deleted")
	}
	// iss1 must remain.
	if countWhere(t, s, "issues", "id", "iss1") != 1 {
		t.Error("existing issue iss1 should remain")
	}
}

// TestReplaceIssuesUpdatesFilenameAndPosition verifies that filename and
// position changes from a rescan are applied even when other fields are preserved.
func TestReplaceIssuesUpdatesFilenameAndPosition(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs", "iss1", "old.cbz")

	err := s.ReplaceIssues("srs", []Issue{
		{ID: "iss1", SeriesID: "srs", Filename: "new.cbz", Number: "5", Position: 3},
	})
	if err != nil {
		t.Fatalf("ReplaceIssues: %v", err)
	}

	var fn string
	var pos int
	s.db.QueryRow(`SELECT filename, position FROM issues WHERE id='iss1'`).Scan(&fn, &pos)
	if fn != "new.cbz" {
		t.Errorf("filename: want 'new.cbz', got %q", fn)
	}
	if pos != 3 {
		t.Errorf("position: want 3, got %d", pos)
	}
}
