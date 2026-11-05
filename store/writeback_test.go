package store

import "testing"

func mustExec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestWritebackQueueLifecycle(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnqueueWriteback("c1", "c2"); err != nil {
		t.Fatal(err)
	}

	id, ok, err := s.NextWritebackJob()
	if err != nil || !ok || id != "c1" {
		t.Fatalf("want c1, got %q ok=%v err=%v", id, ok, err)
	}
	if err := s.MarkWritebackDone("c1"); err != nil {
		t.Fatal(err)
	}

	// c2 errors out after 3 attempts.
	for i := 0; i < 3; i++ {
		id, ok, _ = s.NextWritebackJob()
		if !ok || id != "c2" {
			t.Fatalf("attempt %d: want c2, got %q ok=%v", i, id, ok)
		}
		s.MarkWritebackError("c2", "disk on fire")
	}
	if _, ok, _ = s.NextWritebackJob(); ok {
		t.Fatal("errored job must not be returned")
	}
}

func TestMigrateWritebackID(t *testing.T) {
	s := newTestStore(t)
	s.EnqueueWriteback("old")
	if err := s.MigrateWritebackID("old", "new"); err != nil {
		t.Fatal(err)
	}
	id, ok, _ := s.NextWritebackJob()
	if !ok || id != "new" {
		t.Fatalf("want new, got %q ok=%v", id, ok)
	}
}

func TestEnqueueWritebackForConfirmed(t *testing.T) {
	s := newTestStore(t)
	mustExec(t, s, `INSERT INTO series(id,name,folder_path) VALUES('c','C','/lib/C'),('u','U','/lib/U')`)
	mustExec(t, s, `INSERT INTO issues(id,series_id,filename,number) VALUES
		('c1','c','C 1.cbz','1'),('c2','c','C 2.cbz','2'),('u1','u','U 1.cbz','1')`)
	mustExec(t, s, `INSERT INTO series_match(series_id,volume_id,state,confirmed_at,updated_at)
		VALUES('c','42','confirmed',1,1),('u','43','high_confidence',0,1)`)

	n, err := s.EnqueueWritebackForConfirmed()
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if n != 2 {
		t.Errorf("enqueued %d, want 2 (only confirmed series' issues)", n)
	}
	// The unconfirmed series' issue must NOT be queued.
	if _, ok, _ := s.NextWritebackJob(); !ok {
		t.Fatal("expected a pending job")
	}
}

// TestConfirmedSeriesMissingCreatorIDs exercises the backfill query used by
// handleMatchWriteback's second loop: only a confirmed, CV-matched series
// whose stored creators have no id yet, and weren't hand-edited, is
// returned — a Metron/GCD series (comicvine_volume_id=0), a manually-edited
// one, and one already carrying ids must all be excluded.
func TestConfirmedSeriesMissingCreatorIDs(t *testing.T) {
	s := newTestStore(t)
	mustExec(t, s, `INSERT INTO series(id,name,folder_path) VALUES
		('needs-ids','A','/lib/A'),
		('has-ids','B','/lib/B'),
		('manual','C','/lib/C'),
		('metron','D','/lib/D'),
		('unconfirmed','E','/lib/E')`)
	mustExec(t, s, `INSERT INTO series_match(series_id,volume_id,state,confirmed_at,updated_at) VALUES
		('needs-ids','10','confirmed',1,1),
		('has-ids','11','confirmed',1,1),
		('manual','12','confirmed',1,1),
		('metron','13','confirmed',1,1),
		('unconfirmed','14','high_confidence',0,1)`)
	mustExec(t, s, `INSERT INTO series_enrichment
		(series_id,comicvine_volume_id,creators,manual_fields,status,enriched_at) VALUES
		('needs-ids',10,'[{"name":"Tom King","role":"writer"}]','[]','confirmed',1),
		('has-ids',11,'[{"id":123,"name":"Tom King","role":"writer"}]','[]','confirmed',1),
		('manual',12,'[{"name":"Tom King","role":"writer"}]','["creators"]','confirmed',1),
		('metron',0,'[{"name":"Tom King","role":"writer"}]','[]','confirmed',1),
		('unconfirmed',14,'[{"name":"Tom King","role":"writer"}]','[]','auto',1)`)

	ids, err := s.ConfirmedSeriesMissingCreatorIDs()
	if err != nil {
		t.Fatalf("ConfirmedSeriesMissingCreatorIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "needs-ids" {
		t.Errorf("got %v, want [needs-ids]", ids)
	}
}

func TestParseCoverDate(t *testing.T) {
	cases := []struct {
		in                  string
		wantY, wantM, wantD int
	}{
		{"2016-08-03", 2016, 8, 3},
		{"2016-08", 2016, 8, 0},
		{"2016", 2016, 0, 0},
		{"", 0, 0, 0},
		{"garbage", 0, 0, 0},
	}
	for _, c := range cases {
		y, m, d := parseCoverDate(c.in)
		if y != c.wantY || m != c.wantM || d != c.wantD {
			t.Errorf("parseCoverDate(%q) = %d-%d-%d, want %d-%d-%d",
				c.in, y, m, d, c.wantY, c.wantM, c.wantD)
		}
	}
}

func TestCountWritebackByStatus(t *testing.T) {
	s := newTestStore(t)
	mustExec(t, s, `INSERT INTO series(id,name,folder_path) VALUES('s','S','/lib/S')`)
	mustExec(t, s, `INSERT INTO issues(id,series_id,filename) VALUES
		('a','s','a'),('b','s','b'),('c','s','c'),('d','s','d')`)
	// 'gone' has no issue (file renamed away): it must not count.
	mustExec(t, s, `INSERT INTO metadata_writeback(comic_id,status,attempts,updated_at) VALUES
		('a','pending',0,1),('b','pending',0,1),('c','done',0,1),('d','error',3,1),('gone','error',3,1)`)
	got, err := s.CountWritebackByStatus()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got["pending"] != 2 || got["done"] != 1 || got["error"] != 1 {
		t.Errorf("counts = %v", got)
	}
}

func TestWritebackSourcePrefersEnrichment(t *testing.T) {
	s := newTestStore(t)
	// Seed a series + issue with SCANNED publisher, then enrichment that must win.
	mustExec(t, s, `INSERT INTO series(id,name,folder_path,publisher,year) VALUES('sid','S','/lib/S','ScannedPub',1999)`)
	mustExec(t, s, `INSERT INTO issues(id,series_id,filename,number) VALUES('iid','sid','S 1.cbz','1')`)
	mustExec(t, s, `INSERT INTO series_enrichment(series_id,comicvine_volume_id,publisher,start_year,creators,status,enriched_at)
		VALUES('sid',42,'CVPub',2016,'[{"name":"Tom King","role":"writer"}]','confirmed',1)`)
	mustExec(t, s, `INSERT INTO issue_enrichment(issue_id,comicvine_issue_id,title,summary,cover_date,enriched_at)
		VALUES('iid',7,'Ep1','A summary','2016-08-03',1)`)

	src, err := s.WritebackSource("iid")
	if err != nil {
		t.Fatalf("WritebackSource: %v", err)
	}
	if src.Publisher != "CVPub" {
		t.Errorf("publisher: enrichment must win, got %q", src.Publisher)
	}
	if src.CoverYear != 2016 || src.CoverMonth != 8 || src.CoverDay != 3 {
		t.Errorf("cover date: got %d-%d-%d", src.CoverYear, src.CoverMonth, src.CoverDay)
	}
	if len(src.Creators) != 1 || src.Creators[0].Role != "writer" {
		t.Errorf("creators not loaded: %+v", src.Creators)
	}
	if src.Title != "Ep1" || src.Summary != "A summary" {
		t.Errorf("title/summary: got %q / %q", src.Title, src.Summary)
	}
}

// Re-enqueueing an errored job resets it to pending with a fresh attempt
// budget. (Kept out of the lifecycle test: re-enqueue bumps updated_at, so
// queue order there depended on whether the calls shared a millisecond.)
func TestWritebackReenqueueResetsErroredJob(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnqueueWriteback("c1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s.MarkWritebackError("c1", "disk on fire")
	}
	if _, ok, _ := s.NextWritebackJob(); ok {
		t.Fatal("exhausted job must not be returned")
	}
	if err := s.EnqueueWriteback("c1"); err != nil {
		t.Fatal(err)
	}
	if id, ok, _ := s.NextWritebackJob(); !ok || id != "c1" {
		t.Fatalf("re-enqueued job not pending again: %q ok=%v", id, ok)
	}
}

func TestStripYearSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"Nightwing (2016)":         "Nightwing",
		"Saga":                     "Saga",
		"1776 (2025) Special":      "1776 (2025) Special",
		"Batman: Year One (1987) ": "Batman: Year One",
	} {
		if got := stripYearSuffix(in); got != want {
			t.Errorf("stripYearSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWritebackSourceUsesCanonicalSeriesName(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "nw", "c1", "Nightwing #001.cbz") // series name = "nw"
	mustExec(t, s, `UPDATE series SET name = 'Nightwing (2016)' WHERE id = 'nw'`)

	src, err := s.WritebackSource("c1")
	if err != nil {
		t.Fatal(err)
	}
	if src.SeriesName != "Nightwing" {
		t.Errorf("fallback SeriesName = %q, want Nightwing", src.SeriesName)
	}

	if err := s.UpsertSeriesEnrichment(SeriesEnrichment{SeriesID: "nw", VolumeName: "Nightwing: Rebirth", Status: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	src, _ = s.WritebackSource("c1")
	if src.SeriesName != "Nightwing: Rebirth" {
		t.Errorf("canonical SeriesName = %q", src.SeriesName)
	}
}

// F6: only the folder-name fallback loses its "(YYYY)"; a canonical name that
// itself ends in a year is written verbatim.
func TestWritebackSourceKeepsCanonicalYearVerbatim(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "foo", "c1", "Foo #001.cbz")
	if err := s.UpsertSeriesEnrichment(SeriesEnrichment{SeriesID: "foo", VolumeName: "Foo (1999)", Status: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	src, err := s.WritebackSource("c1")
	if err != nil {
		t.Fatal(err)
	}
	if src.SeriesName != "Foo (1999)" {
		t.Errorf("SeriesName = %q, want Foo (1999)", src.SeriesName)
	}
}
