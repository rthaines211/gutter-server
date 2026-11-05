package store

import (
	"strings"
	"testing"
)

func seedComic(t *testing.T, s *Store, seriesID, comicID, filename string) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT OR IGNORE INTO series(id, name, folder_path, issue_count) VALUES(?,?,?,1)`,
		seriesID, seriesID, "/lib/"+seriesID)
	mustExec(`INSERT INTO issues(id, series_id, filename, number, position) VALUES(?,?,?,?,0)`,
		comicID, seriesID, filename, "1")
	mustExec(`INSERT INTO reading_progress(comic_id, current_page, total_pages) VALUES(?,?,?)`, comicID, 3, 20)
	mustExec(`INSERT INTO bookmarks(comic_id, page_num, note) VALUES(?,?,?)`, comicID, 1, "n")
	mustExec(`INSERT INTO reading_sessions(comic_id, series_id, started_at, ended_at) VALUES(?,?,?,?)`,
		comicID, seriesID, "2026-01-01", "2026-01-01")
	mustExec(`INSERT INTO chapters(comic_id, page_num, title) VALUES(?,?,?)`,
		comicID, 1, "ch1")
	mustExec(`INSERT INTO tagger_state(comic_id, status) VALUES(?,?)`, comicID, "processed")
	mustExec(`INSERT INTO issue_enrichment(issue_id, comicvine_issue_id, enriched_at) VALUES(?,?,?)`, comicID, 0, 0)
	mustExec(`INSERT INTO metadata_writeback(comic_id, status) VALUES(?, 'pending')`, comicID)
}

func countWhere(t *testing.T, s *Store, table, col, val string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+col+`=?`, val).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrateComicIDFull(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs", "oldid", "old.cbz")

	if err := s.MigrateComicIDFull("oldid", "newid", "new.cbz"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ table, col string }{
		{"issues", "id"}, {"reading_progress", "comic_id"}, {"bookmarks", "comic_id"}, {"reading_sessions", "comic_id"},
		{"chapters", "comic_id"}, {"tagger_state", "comic_id"}, {"issue_enrichment", "issue_id"}, {"metadata_writeback", "comic_id"},
	} {
		if countWhere(t, s, tc.table, tc.col, "oldid") != 0 {
			t.Errorf("%s still has oldid", tc.table)
		}
		if countWhere(t, s, tc.table, tc.col, "newid") != 1 {
			t.Errorf("%s missing newid", tc.table)
		}
	}
	var fn string
	s.db.QueryRow(`SELECT filename FROM issues WHERE id='newid'`).Scan(&fn)
	if fn != "new.cbz" {
		t.Errorf("filename not updated: %s", fn)
	}
}

// The migration must bump updated_at on the moved progress row and its series,
// or delta-sync (/sync filters by updated_at) never conveys the rename and
// clients keep showing the stale old ID.
func TestMigrateComicIDFullBumpsUpdatedAtForSync(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs", "oldid", "old.cbz")
	// Force stale timestamps predating the migration.
	if _, err := s.db.Exec(`UPDATE reading_progress SET updated_at='2000-01-01 00:00:00' WHERE comic_id='oldid'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE series SET updated_at='2000-01-01 00:00:00' WHERE id='srs'`); err != nil {
		t.Fatal(err)
	}

	if err := s.MigrateComicIDFull("oldid", "newid", "new.cbz"); err != nil {
		t.Fatal(err)
	}

	// A sync watermark taken just after the stale timestamp must now surface both
	// rows — the same strftime('%s') comparison ListProgress/SeriesUpdatedSince use.
	const staleEpoch = 946684800 // 2000-01-01T00:00:00Z
	newer := func(table, col, val string) bool {
		var n int
		q := `SELECT count(*) FROM ` + table + ` WHERE ` + col + `=? AND CAST(strftime('%s', updated_at) AS INTEGER) > ?`
		if err := s.db.QueryRow(q, val, staleEpoch).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if !newer("reading_progress", "comic_id", "newid") {
		t.Errorf("migrated progress row's updated_at was not bumped; delta-sync would miss the rename")
	}
	if !newer("series", "id", "srs") {
		t.Errorf("series updated_at was not bumped; delta-sync would not re-send the new issue id")
	}
}

func TestMigrateSeriesFolder(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "old-series", "issue-old", "a.cbz")
	if _, err := s.db.Exec(`INSERT INTO series_genres(series_id, genre) VALUES('old-series','Action')`); err != nil {
		t.Fatal(err)
	}

	err := s.MigrateSeriesFolder("old-series", "new-series", "/lib/New Series", "New Series",
		[]IDPair{{Old: "issue-old", New: "issue-new"}})
	if err != nil {
		t.Fatal(err)
	}
	if countWhere(t, s, "series", "id", "old-series") != 0 || countWhere(t, s, "series", "id", "new-series") != 1 {
		t.Error("series id not migrated")
	}
	if countWhere(t, s, "series_genres", "series_id", "new-series") != 1 {
		t.Error("series_genres not migrated")
	}
	if countWhere(t, s, "issues", "id", "issue-old") != 0 {
		t.Error("issues.id old value not removed")
	}
	if countWhere(t, s, "issues", "series_id", "new-series") != 1 {
		t.Error("issues.series_id not migrated")
	}
	if countWhere(t, s, "issues", "id", "issue-new") != 1 {
		t.Error("issue id not migrated")
	}
	if countWhere(t, s, "reading_progress", "comic_id", "issue-new") != 1 {
		t.Error("progress not migrated with issue id")
	}
	var name, fp string
	s.db.QueryRow(`SELECT name, folder_path FROM series WHERE id='new-series'`).Scan(&name, &fp)
	if name != "New Series" || fp != "/lib/New Series" {
		t.Errorf("series row not updated: %s %s", name, fp)
	}
}

// TestMigrateSeriesFolderUpdatesCoverPath verifies I3: when a series folder is
// renamed, cover_path (stored as "OldFolderName/filename.cbz") must be rewritten
// to use the new folder name so cover thumbnails remain resolvable.
func TestMigrateSeriesFolderUpdatesCoverPath(t *testing.T) {
	s := newTestStore(t)
	// Seed series with a cover_path that references the old folder name.
	if _, err := s.db.Exec(`
		INSERT INTO series(id, name, folder_path, issue_count, cover_path)
		VALUES('old-srs', 'Old Name', '/lib/Old Name', 3, 'Old Name/issue001.cbz')
	`); err != nil {
		t.Fatal(err)
	}
	// Seed a minimal issue so the transaction can complete.
	if _, err := s.db.Exec(`INSERT INTO issues(id, series_id, filename, number, position) VALUES('i1','old-srs','issue001.cbz','1',0)`); err != nil {
		t.Fatal(err)
	}

	err := s.MigrateSeriesFolder("old-srs", "new-srs", "/lib/New Name", "New Name",
		[]IDPair{{Old: "i1", New: "i1-new"}})
	if err != nil {
		t.Fatalf("MigrateSeriesFolder: %v", err)
	}

	var coverPath string
	s.db.QueryRow(`SELECT cover_path FROM series WHERE id='new-srs'`).Scan(&coverPath)
	if coverPath != "New Name/issue001.cbz" {
		t.Errorf("cover_path: want 'New Name/issue001.cbz', got %q", coverPath)
	}
}

// TestUpdateSeriesCoverPath verifies the UpdateSeriesCoverPath helper updates the row.
func TestUpdateSeriesCoverPath(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO series(id, name, folder_path, issue_count, cover_path) VALUES('s1','S','/',1,'old/cover.cbz')`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSeriesCoverPath("s1", "new/cover.cbz"); err != nil {
		t.Fatalf("UpdateSeriesCoverPath: %v", err)
	}
	var cp string
	s.db.QueryRow(`SELECT cover_path FROM series WHERE id='s1'`).Scan(&cp)
	if cp != "new/cover.cbz" {
		t.Errorf("cover_path: want 'new/cover.cbz', got %q", cp)
	}
}

func TestMigrateComicToSeries(t *testing.T) {
	s, err := NewStore(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, se := range []Series{
		{ID: "src", Name: "Src (2004)", FolderPath: "/lib/Src (2004)", IssueCount: 2},
		{ID: "dst", Name: "Dst (2010)", FolderPath: "/lib/Dst (2010)", IssueCount: 1},
	} {
		if err := s.UpsertSeries(se); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReplaceIssues("src", []Issue{
		{ID: "old-id", SeriesID: "src", Filename: "Stray 037.cbz", Number: "37", Position: 0},
		{ID: "keep", SeriesID: "src", Filename: "Src 001.cbz", Number: "1", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}
	// A comic-keyed row that must follow the ID.
	if err := s.UpsertIssueMatch(IssueMatch{
		IssueID: "old-id", Source: SourceComicVine, UpstreamID: "42",
		State: MatchStateConfirmed, Method: "human_repin",
	}); err != nil {
		t.Fatal(err)
	}
	// Seed a progress row and force stale timestamps predating the migration so
	// the delta-sync updated_at bumps are observable (same pattern as
	// TestMigrateComicIDFullBumpsUpdatedAtForSync).
	if _, err := s.db.Exec(`INSERT INTO reading_progress(comic_id, current_page, total_pages, updated_at)
		VALUES('old-id', 3, 20, '2000-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE series SET updated_at='2000-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}

	if err := s.MigrateComicToSeries("old-id", "new-id", "dst"); err != nil {
		t.Fatal(err)
	}

	issues, err := s.ListIssues("dst")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].ID != "new-id" || issues[0].Filename != "Stray 037.cbz" {
		t.Fatalf("issue did not move: %+v", issues)
	}
	src, _ := s.GetSeries("src")
	dst, _ := s.GetSeries("dst")
	if src.IssueCount != 1 || dst.IssueCount != 2 {
		t.Fatalf("issue counts not adjusted: src=%d dst=%d", src.IssueCount, dst.IssueCount)
	}
	ims, _ := s.ListIssueMatchesForSeries("dst")
	if len(ims) != 1 || ims[0].IssueID != "new-id" {
		t.Fatalf("issue_match did not follow: %+v", ims)
	}

	// Delta-sync visibility: the migrated progress row and both series must
	// carry fresh updated_at, or /sync clients never learn about the move.
	const staleEpoch = 946684800 // 2000-01-01T00:00:00Z
	newer := func(table, col, val string) bool {
		var n int
		q := `SELECT count(*) FROM ` + table + ` WHERE ` + col + `=? AND CAST(strftime('%s', updated_at) AS INTEGER) > ?`
		if err := s.db.QueryRow(q, val, staleEpoch).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if !newer("reading_progress", "comic_id", "new-id") {
		t.Error("migrated progress row's updated_at was not bumped; delta-sync would miss the move")
	}
	if !newer("series", "id", "src") {
		t.Error("src series updated_at was not bumped; delta-sync would not convey the count change")
	}
	if !newer("series", "id", "dst") {
		t.Error("dst series updated_at was not bumped; delta-sync would not convey the new issue")
	}
}

// Delta sync filters progress by updated_at; a folder rename that moves
// progress to new IDs without bumping it leaves clients on dead IDs.
func TestMigrateSeriesFolderBumpsProgressUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "old", "c-old", "a.cbz")
	mustExec(t, s, `UPDATE reading_progress SET updated_at = '2000-01-01 00:00:00' WHERE comic_id = 'c-old'`)

	if err := s.MigrateSeriesFolder("old", "new", "/lib/new", "New", []IDPair{{Old: "c-old", New: "c-new"}}); err != nil {
		t.Fatal(err)
	}

	var ts string
	if err := s.db.QueryRow(`SELECT updated_at FROM reading_progress WHERE comic_id = 'c-new'`).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(ts, "2000-01-01") {
		t.Errorf("progress updated_at not bumped: %s", ts)
	}
}
