package store

import (
	"testing"
)

func TestUpsertAndGetSeries(t *testing.T) {
	s := newTestStore(t)

	series := Series{
		ID:         "batman-2016",
		Name:       "Batman (2016)",
		FolderPath: "/comics/Batman (2016)",
		IssueCount: 24,
		CoverPath:  "Batman (2016)/Batman 001.cbz",
		Writer:     "Tom King",
		Penciller:  "David Finch",
	}
	if err := s.UpsertSeries(series); err != nil {
		t.Fatalf("UpsertSeries failed: %v", err)
	}

	got, err := s.GetSeries("batman-2016")
	if err != nil {
		t.Fatalf("GetSeries failed: %v", err)
	}
	if got.Name != "Batman (2016)" {
		t.Errorf("expected name 'Batman (2016)', got %q", got.Name)
	}
	if got.IssueCount != 24 {
		t.Errorf("expected 24 issues, got %d", got.IssueCount)
	}
	if got.Writer != "Tom King" {
		t.Errorf("expected writer 'Tom King', got %q", got.Writer)
	}
}

// TestUpsertSeriesUpdatePreservesMetadata verifies the edit-preservation semantics:
// a rescan (second UpsertSeries) must update issue_count and cover_path but must NOT
// overwrite editable metadata fields (writer, name, publisher, etc.) that were seeded
// on the first scan. In-app edits survive rescans.
func TestUpsertSeriesUpdatePreservesMetadata(t *testing.T) {
	s := newTestStore(t)

	s.UpsertSeries(Series{
		ID: "xmen", Name: "X-Men", FolderPath: "/comics/X-Men",
		IssueCount: 5, Writer: "Claremont",
	})
	s.UpsertSeries(Series{
		ID: "xmen", Name: "X-Men", FolderPath: "/comics/X-Men",
		IssueCount: 10, Writer: "Hickman",
	})

	got, err := s.GetSeries("xmen")
	if err != nil {
		t.Fatalf("GetSeries failed: %v", err)
	}
	// issue_count from the second call IS applied.
	if got.IssueCount != 10 {
		t.Errorf("expected 10 issues after update, got %d", got.IssueCount)
	}
	// writer from the first scan must be preserved — the second call must NOT overwrite it.
	if got.Writer != "Claremont" {
		t.Errorf("writer should be preserved as 'Claremont', got %q", got.Writer)
	}
}

func TestGetSeriesNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetSeries("nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListSeries(t *testing.T) {
	s := newTestStore(t)

	s.UpsertSeries(Series{ID: "b-series", Name: "B Series", FolderPath: "/b"})
	s.UpsertSeries(Series{ID: "a-series", Name: "A Series", FolderPath: "/a"})
	s.UpsertSeries(Series{ID: "c-series", Name: "C Series", FolderPath: "/c"})

	list, err := s.ListSeries()
	if err != nil {
		t.Fatalf("ListSeries failed: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 series, got %d", len(list))
	}
}

func TestDeleteSeriesNotIn(t *testing.T) {
	s := newTestStore(t)

	s.UpsertSeries(Series{ID: "keep", Name: "Keep", FolderPath: "/keep"})
	s.UpsertSeries(Series{ID: "remove", Name: "Remove", FolderPath: "/remove"})

	if err := s.DeleteSeriesNotIn([]string{"keep"}); err != nil {
		t.Fatalf("DeleteSeriesNotIn failed: %v", err)
	}

	list, _ := s.ListSeries()
	if len(list) != 1 {
		t.Fatalf("expected 1 series, got %d", len(list))
	}
	if list[0].ID != "keep" {
		t.Errorf("expected 'keep', got %q", list[0].ID)
	}
}

func TestDeleteSeriesNotInEmpty(t *testing.T) {
	s := newTestStore(t)

	s.UpsertSeries(Series{ID: "a", Name: "A", FolderPath: "/a"})

	if err := s.DeleteSeriesNotIn([]string{}); err != nil {
		t.Fatalf("DeleteSeriesNotIn with empty list failed: %v", err)
	}

	list, _ := s.ListSeries()
	if len(list) != 0 {
		t.Fatalf("expected 0 series, got %d", len(list))
	}
}

func TestUpsertSeriesWithMetadata(t *testing.T) {
	s := newTestStore(t)
	series := Series{
		ID:         "batman-2016",
		Name:       "Batman (2016)",
		FolderPath: "/comics/Batman (2016)",
		IssueCount: 10,
		CoverPath:  "Batman (2016)/Batman 001.cbz",
		Writer:     "Tom King",
		Penciller:  "David Finch",
		Publisher:  "DC Comics",
		Year:       2016,
	}
	if err := s.UpsertSeries(series); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSeries("batman-2016")
	if err != nil {
		t.Fatal(err)
	}
	if got.Publisher != "DC Comics" {
		t.Errorf("publisher: got %q, want %q", got.Publisher, "DC Comics")
	}
	if got.Year != 2016 {
		t.Errorf("year: got %d, want %d", got.Year, 2016)
	}
}

func TestUpsertSeriesGenres(t *testing.T) {
	s := newTestStore(t)
	series := Series{ID: "saga", Name: "Saga", FolderPath: "/comics/Saga", IssueCount: 54}
	if err := s.UpsertSeries(series); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSeriesGenres("saga", []string{"Sci-Fi", "Fantasy", "Romance"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListSeriesGenres("saga")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("genres count: got %d, want 3", len(got))
	}
	// Replace with different genres
	if err := s.ReplaceSeriesGenres("saga", []string{"Sci-Fi", "Drama"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListSeriesGenres("saga")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("genres after replace: got %d, want 2", len(got))
	}
}

func TestReplaceSeriesGenresEmpty(t *testing.T) {
	s := newTestStore(t)
	series := Series{ID: "test", Name: "Test", FolderPath: "/comics/Test", IssueCount: 1}
	if err := s.UpsertSeries(series); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSeriesGenres("test", []string{"Action"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSeriesGenres("test", nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListSeriesGenres("test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("genres after clear: got %d, want 0", len(got))
	}
}

func TestListSeriesIncludesMetadata(t *testing.T) {
	s := newTestStore(t)
	series := Series{
		ID: "invincible", Name: "Invincible", FolderPath: "/comics/Invincible",
		IssueCount: 144, Publisher: "Image Comics", Year: 2003,
	}
	if err := s.UpsertSeries(series); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSeries()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list count: got %d, want 1", len(list))
	}
	if list[0].Publisher != "Image Comics" {
		t.Errorf("publisher: got %q, want %q", list[0].Publisher, "Image Comics")
	}
	if list[0].Year != 2003 {
		t.Errorf("year: got %d, want %d", list[0].Year, 2003)
	}
}

// TestUpsertSeriesUpdatesFolderPath verifies that folder_path (a structural field
// that tracks where a series lives on disk) is always updated on rescan, unlike
// editable metadata fields which are preserved after first scan.
func TestUpsertSeriesUpdatesFolderPath(t *testing.T) {
	s := newTestStore(t)
	s.UpsertSeries(Series{
		ID: "srs-fp", Name: "MyComic", FolderPath: "/old/path/MyComic",
		IssueCount: 1,
	})
	// Rescan finds the series has moved (e.g. library root changed).
	s.UpsertSeries(Series{
		ID: "srs-fp", Name: "MyComic", FolderPath: "/new/path/MyComic",
		IssueCount: 1,
	})
	got, err := s.GetSeries("srs-fp")
	if err != nil {
		t.Fatal(err)
	}
	if got.FolderPath != "/new/path/MyComic" {
		t.Errorf("folder_path: want '/new/path/MyComic', got %q", got.FolderPath)
	}
}

func TestUpsertSeriesOnlyBumpsUpdatedAtOnChange(t *testing.T) {
	s := newTestStore(t)
	series := Series{ID: "s1", Name: "Batman", FolderPath: "/comics/batman", IssueCount: 2}
	if err := s.UpsertSeries(series); err != nil {
		t.Fatal(err)
	}
	first, err := s.GetSeries("s1")
	if err != nil {
		t.Fatal(err)
	}

	// Backdate the row so any re-stamp is observable without sleeping.
	if _, err := s.db.Exec(`UPDATE series SET updated_at = datetime('now', '-10 seconds') WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	// Re-read so first.UpdatedAt reflects the backdated value.
	first, err = s.GetSeries("s1")
	if err != nil {
		t.Fatal(err)
	}

	// Identical upsert must NOT bump updated_at.
	if err := s.UpsertSeries(series); err != nil {
		t.Fatal(err)
	}
	second, err := s.GetSeries("s1")
	if err != nil {
		t.Fatal(err)
	}
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("identical upsert bumped updated_at: %v -> %v", first.UpdatedAt, second.UpdatedAt)
	}

	// Changed upsert must bump.
	series.IssueCount = 3
	if err := s.UpsertSeries(series); err != nil {
		t.Fatal(err)
	}
	third, err := s.GetSeries("s1")
	if err != nil {
		t.Fatal(err)
	}
	if third.UpdatedAt.Equal(first.UpdatedAt) {
		t.Error("changed upsert did not bump updated_at")
	}
	if third.IssueCount != 3 {
		t.Errorf("changed upsert did not apply: issue_count = %d", third.IssueCount)
	}
}

// TestUpsertSeriesIssueCountCoverPathBumpsUpdatedAt verifies the fields that still
// trigger updated_at bumps under the edit-preservation semantics: issue_count and
// cover_path changes ARE applied on rescan, while publisher (an editable field) is
// seeded only on first insert and must not be overwritten by a subsequent rescan.
func TestUpsertSeriesIssueCountCoverPathBumpsUpdatedAt(t *testing.T) {
	s := newTestStore(t)

	// Seed a row with NULL publisher via raw INSERT (publisher column omitted).
	if _, err := s.db.Exec(
		`INSERT INTO series (id, name, folder_path, issue_count, cover_path, writer, penciller, year, updated_at)
		 VALUES ('np1', 'NullPub', '/comics/nullpub', 1, '/old/cover.jpg', '', '', 0, datetime('now', '-20 seconds'))`,
	); err != nil {
		t.Fatal(err)
	}
	baseline, err := s.GetSeries("np1")
	if err != nil {
		t.Fatal(err)
	}

	// Upsert with a changed issue_count and cover_path — updated_at must bump.
	if err := s.UpsertSeries(Series{
		ID: "np1", Name: "NullPub", FolderPath: "/comics/nullpub",
		IssueCount: 5, CoverPath: "/new/cover.jpg", Publisher: "Marvel",
	}); err != nil {
		t.Fatal(err)
	}
	afterFirst, err := s.GetSeries("np1")
	if err != nil {
		t.Fatal(err)
	}
	// updated_at must have bumped because issue_count/cover_path changed.
	if !afterFirst.UpdatedAt.After(baseline.UpdatedAt) {
		t.Errorf("issue_count/cover_path change should bump updated_at: baseline=%v after=%v", baseline.UpdatedAt, afterFirst.UpdatedAt)
	}
	// issue_count and cover_path from the rescan ARE applied.
	if afterFirst.IssueCount != 5 {
		t.Errorf("issue_count should be 5, got %d", afterFirst.IssueCount)
	}
	if afterFirst.CoverPath != "/new/cover.jpg" {
		t.Errorf("cover_path should be '/new/cover.jpg', got %q", afterFirst.CoverPath)
	}
	// Publisher was blank, so the rescan's value fills it. Editable metadata is
	// protected against being *overwritten*, not against being populated: a
	// blank row is what a failed extraction leaves behind, and refusing to fill
	// it is what left 120 series permanently without a publisher.
	// TestUpsertSeriesPreservesEditedMetadataButFillsBlanks covers the other
	// half — a non-blank value is never clobbered.
	if afterFirst.Publisher != "Marvel" {
		t.Errorf("blank publisher should be filled from the rescan, got %q", afterFirst.Publisher)
	}

	// Backdate so a potential second bump is observable.
	if _, err := s.db.Exec(`UPDATE series SET updated_at = datetime('now', '-10 seconds') WHERE id = 'np1'`); err != nil {
		t.Fatal(err)
	}
	afterFirst, err = s.GetSeries("np1")
	if err != nil {
		t.Fatal(err)
	}

	// Second identical upsert (same issue_count + cover_path) must NOT bump updated_at.
	if err := s.UpsertSeries(Series{
		ID: "np1", Name: "NullPub", FolderPath: "/comics/nullpub",
		IssueCount: 5, CoverPath: "/new/cover.jpg", Publisher: "Marvel",
	}); err != nil {
		t.Fatal(err)
	}
	afterSecond, err := s.GetSeries("np1")
	if err != nil {
		t.Fatal(err)
	}
	if !afterSecond.UpdatedAt.Equal(afterFirst.UpdatedAt) {
		t.Errorf("identical upsert bumped updated_at: %v -> %v", afterFirst.UpdatedAt, afterSecond.UpdatedAt)
	}
}

// The blank-fill in UpsertSeries must not become a general overwrite: an
// in-app metadata edit has to survive a rescan, which is why the ON CONFLICT
// clause guards every metadata column with NULLIF.
func TestUpsertSeriesPreservesEditedMetadataButFillsBlanks(t *testing.T) {
	s, err := NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	base := Series{
		ID: "saga", Name: "Saga", FolderPath: "/saga", IssueCount: 1,
		CoverPath: "c.jpg", Writer: "Scanner Writer", Publisher: "Image Comics",
	}
	if err := s.UpsertSeries(base); err != nil {
		t.Fatal(err)
	}

	// User edits the writer in-app and the penciller is still blank.
	if _, err := s.db.Exec(
		`UPDATE series SET writer='User Edit', publisher='', penciller='' WHERE id='saga'`); err != nil {
		t.Fatal(err)
	}

	// Rescan supplies scanner values for all three.
	rescan := base
	rescan.Writer = "Scanner Writer"
	rescan.Penciller = "Scanner Penciller"
	rescan.Publisher = "Image Comics"
	rescan.CoverPath = "c2.jpg" // structural change, forces the UPDATE to fire
	if err := s.UpsertSeries(rescan); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if got.Writer != "User Edit" {
		t.Errorf("writer = %q, want %q — a rescan clobbered an in-app edit", got.Writer, "User Edit")
	}
	if got.Publisher != "Image Comics" {
		t.Errorf("publisher = %q, want %q — a blank field was not refilled", got.Publisher, "Image Comics")
	}
	if got.Penciller != "Scanner Penciller" {
		t.Errorf("penciller = %q, want %q — a blank field was not refilled", got.Penciller, "Scanner Penciller")
	}
}

// TestSeriesPublisherFallsBackToEnrichment covers the Library "Unknown publisher"
// bug: the scanner leaves series.publisher blank when the files carry no
// ComicInfo.xml <Publisher>, so reads must fall back to the confirmed match's
// series_enrichment.publisher. A publisher already on the series row still wins.
func TestSeriesPublisherFallsBackToEnrichment(t *testing.T) {
	s := newTestStore(t)

	for _, tc := range []struct {
		id, seriesPub, enrichPub, wantPub string
		seriesYear, enrichYear, wantYear  int
	}{
		{"blank-row", "", "DC Comics", "DC Comics", 0, 2016, 2016},
		{"tagged-row", "Marvel", "Marvel", "Marvel", 2018, 2018, 2018},
		{"no-enrichment", "Image Comics", "", "Image Comics", 2021, 0, 2021},
		// Enrichment wins a disagreement, matching WritebackSource and the /sync
		// overlay. User edits are safe: UpdateSeriesMetadata writes both tables.
		{"enrichment-wins", "Dark Horse", "DC Comics", "DC Comics", 1999, 2016, 2016},
	} {
		if err := s.UpsertSeries(Series{
			ID: tc.id, Name: tc.id, FolderPath: "/comics/" + tc.id,
			Publisher: tc.seriesPub, Year: tc.seriesYear,
		}); err != nil {
			t.Fatalf("UpsertSeries(%s): %v", tc.id, err)
		}
		if tc.enrichPub != "" {
			if err := s.UpsertSeriesEnrichment(SeriesEnrichment{
				SeriesID: tc.id, Publisher: tc.enrichPub, StartYear: tc.enrichYear,
				Creators: "[]", Status: "confirmed",
			}); err != nil {
				t.Fatalf("UpsertSeriesEnrichment(%s): %v", tc.id, err)
			}
		}

		got, err := s.GetSeries(tc.id)
		if err != nil {
			t.Fatalf("GetSeries(%s): %v", tc.id, err)
		}
		if got.Publisher != tc.wantPub {
			t.Errorf("GetSeries(%s).Publisher = %q, want %q", tc.id, got.Publisher, tc.wantPub)
		}
		if got.Year != tc.wantYear {
			t.Errorf("GetSeries(%s).Year = %d, want %d", tc.id, got.Year, tc.wantYear)
		}
	}

	// ListSeries and the delta-sync read share the same projection.
	list, err := s.ListSeries()
	if err != nil {
		t.Fatalf("ListSeries: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("ListSeries returned %d rows, want 4 (join must not fan out)", len(list))
	}
	for _, got := range list {
		if got.Publisher == "" || got.Year == 0 {
			t.Errorf("ListSeries: %s has blank publisher/year (%q, %d)", got.ID, got.Publisher, got.Year)
		}
	}

	synced, err := s.ListSeriesUpdatedSince(0)
	if err != nil {
		t.Fatalf("ListSeriesUpdatedSince: %v", err)
	}
	if len(synced) != 4 {
		t.Fatalf("ListSeriesUpdatedSince returned %d rows, want 4", len(synced))
	}
}

// TestListSeriesUpdatedSinceSeesEnrichment covers the delta-sync gap: enrichment
// writes series_enrichment without bumping series.updated_at, so a client that
// already synced a series would never receive its newly matched publisher/year.
func TestListSeriesUpdatedSinceSeesEnrichment(t *testing.T) {
	s := newTestStore(t)

	const (
		since     = int64(1609459200)    // 2021-01-01, the client's watermark (seconds)
		freshMs   = int64(1700000000000) // 2023-11, after the watermark
		staleMs   = int64(1500000000000) // 2017-07, before it
		oldRowSQL = `UPDATE series SET updated_at = '2020-01-01 00:00:00' WHERE id = ?`
	)

	for id, enrichedAt := range map[string]int64{"fresh-enrich": freshMs, "stale-enrich": staleMs} {
		if err := s.UpsertSeries(Series{ID: id, Name: id, FolderPath: "/comics/" + id}); err != nil {
			t.Fatalf("UpsertSeries(%s): %v", id, err)
		}
		if _, err := s.db.Exec(oldRowSQL, id); err != nil {
			t.Fatalf("aging %s: %v", id, err)
		}
		if err := s.UpsertSeriesEnrichment(SeriesEnrichment{
			SeriesID: id, Publisher: "DC Comics", Creators: "[]",
			Status: "confirmed", EnrichedAt: enrichedAt,
		}); err != nil {
			t.Fatalf("UpsertSeriesEnrichment(%s): %v", id, err)
		}
	}

	got, err := s.ListSeriesUpdatedSince(since)
	if err != nil {
		t.Fatalf("ListSeriesUpdatedSince: %v", err)
	}
	if len(got) != 1 || got[0].ID != "fresh-enrich" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.ID
		}
		t.Fatalf("got %v, want [fresh-enrich] only (stale row must stay filtered out)", ids)
	}
}

// TestSetIssueNumberIfBlank: the scanner strips "v01" as a volume marker and
// leaves number empty, which makes the organizer skip trades forever. The match
// pipeline knows the real number and backfills it here - but must never clobber
// a number that is already set.
func TestSetIssueNumberIfBlank(t *testing.T) {
	s := newTestStore(t)
	mustExec(t, s, `INSERT INTO series(id,name,folder_path,issue_count) VALUES('bt','Birthright','/c/Birthright',2)`)
	mustExec(t, s, `INSERT INTO issues(id,series_id,filename,number,position) VALUES('i1','bt','v01.cbr','',0)`)
	mustExec(t, s, `INSERT INTO issues(id,series_id,filename,number,position) VALUES('i2','bt','v02.cbr','7',1)`)

	if err := s.SetIssueNumberIfBlank("i1", "01"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIssueNumberIfBlank("i2", "02"); err != nil { // must not overwrite
		t.Fatal(err)
	}
	if err := s.SetIssueNumberIfBlank("i1", ""); err != nil { // empty is a no-op
		t.Fatal(err)
	}

	for id, want := range map[string]string{"i1": "01", "i2": "7"} {
		var got string
		if err := s.db.QueryRow(`SELECT number FROM issues WHERE id=?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("issue %s number = %q, want %q", id, got, want)
		}
	}
}

// TestBackfillCollectionNumbers: trades land with a blank number because the
// scanner strips "v01", and the organizer skips anything unnumbered. Singles
// whose number is genuinely ambiguous must be left alone rather than guessed.
func TestBackfillCollectionNumbers(t *testing.T) {
	s := newTestStore(t)
	mustExec(t, s, `INSERT INTO series(id,name,folder_path,issue_count) VALUES('bt','Birthright','/c/Birthright',4)`)
	ins := func(id, fn, num string) {
		mustExec(t, s, `INSERT INTO issues(id,series_id,filename,number,position) VALUES(?,'bt',?,?,0)`, id, fn, num)
	}
	ins("i1", "Birthright v01 - Homecoming (2015) (Digital) (DR & Quinch-Empire).cbr", "")
	ins("i2", "Birthright v05 - Belly of the Beast (2017) (Digital) (DR & Quinch-Empire).cbr", "")
	ins("i3", "Some Ambiguous 12 34 Thing (2015).cbz", "")          // not a collection -> untouched
	ins("i4", "Birthright v02 - Call to Adventure (2015).cbr", "9") // already set -> untouched

	if err := backfillCollectionNumbers(s.db); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// Second run must be a no-op, not a re-write.
	if err := backfillCollectionNumbers(s.db); err != nil {
		t.Fatalf("backfill rerun: %v", err)
	}

	for id, want := range map[string]string{"i1": "01", "i2": "05", "i3": "", "i4": "9"} {
		var got string
		if err := s.db.QueryRow(`SELECT COALESCE(number,'') FROM issues WHERE id=?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("issue %s number = %q, want %q", id, got, want)
		}
	}
}

// TestBackfillAnnualFormats: annuals predating annual detection have a blank
// format, so they resolve through the single template to the same "#13" as the
// ordinary issue 13 and both get flagged as a duplicate target.
func TestBackfillAnnualFormats(t *testing.T) {
	s := newTestStore(t)
	mustExec(t, s, `INSERT INTO series(id,name,folder_path,issue_count) VALUES('glc','GLC','/c/GLC',4)`)
	ins := func(id, fn, fmtv string) {
		mustExec(t, s, `INSERT INTO issues(id,series_id,filename,number,format,position) VALUES(?,'glc',?,'1',?,0)`, id, fn, fmtv)
	}
	ins("a1", "Green Lantern Corps Annual 13 (2026).cbz", "")
	ins("a2", "Green Lantern Corps 13 (2026).cbz", "") // ordinary issue -> untouched
	ins("a3", "Nightwing Annual 001 (2018).cbz", "")
	ins("a4", "Batman Annual 3 (2019).cbz", "tpb") // already set -> untouched

	if err := backfillAnnualFormats(s.db); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if err := backfillAnnualFormats(s.db); err != nil { // idempotent
		t.Fatalf("rerun: %v", err)
	}
	for id, want := range map[string]string{"a1": "annual", "a2": "", "a3": "annual", "a4": "tpb"} {
		var got string
		if err := s.db.QueryRow(`SELECT COALESCE(format,'') FROM issues WHERE id=?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("issue %s format = %q, want %q", id, got, want)
		}
	}
}
