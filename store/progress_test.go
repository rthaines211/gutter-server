package store

import (
	"errors"
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }

func TestUpsertProgressStaleWrite(t *testing.T) {
	cases := []struct {
		name    string
		first   *int64 // read_at of seed write
		second  *int64 // read_at of contested write
		wantErr bool   // ErrStaleWrite expected on second?
	}{
		{"newer accepted", i64(1000), i64(2000), false},
		{"equal rejected", i64(1000), i64(1000), true},
		{"older rejected", i64(2000), i64(1000), true},
		{"legacy write over stamped row accepted", i64(1000), nil, false},
		{"stamped write over legacy row accepted", nil, i64(1000), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)

			if err := s.UpsertProgress(Progress{ComicID: "c1", CurrentPage: 1, TotalPages: 10, ReadAt: c.first}); err != nil {
				t.Fatalf("seed write: %v", err)
			}

			err := s.UpsertProgress(Progress{ComicID: "c1", CurrentPage: 5, TotalPages: 10, ReadAt: c.second})

			if c.wantErr {
				if !errors.Is(err, ErrStaleWrite) {
					t.Fatalf("expected ErrStaleWrite, got %v", err)
				}
				p, gErr := s.GetProgress("c1")
				if gErr != nil {
					t.Fatalf("GetProgress: %v", gErr)
				}
				if p.CurrentPage != 1 {
					t.Errorf("stale write must not apply: expected CurrentPage=1, got %d", p.CurrentPage)
				}
			} else {
				if err != nil {
					t.Fatalf("expected accepted write, got err %v", err)
				}
				p, gErr := s.GetProgress("c1")
				if gErr != nil {
					t.Fatalf("GetProgress: %v", gErr)
				}
				if p.CurrentPage != 5 {
					t.Errorf("expected accepted write to apply: expected CurrentPage=5, got %d", p.CurrentPage)
				}
			}
		})
	}
}

func TestRecentFinishedReadings(t *testing.T) {
	s := newTestStore(t)

	// Seed: two series, multiple issues, mixed finished/in-progress
	now := time.Now()
	if _, err := s.db.Exec(`INSERT INTO series (id, name, folder_path, issue_count) VALUES ('s1', 'Series 1', '/x/s1', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO series (id, name, folder_path, issue_count) VALUES ('s2', 'Series 2', '/x/s2', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO issues (id, series_id, filename, number, position) VALUES ('c1', 's1', '1.cbz', '1', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO issues (id, series_id, filename, number, position) VALUES ('c2', 's1', '2.cbz', '2', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO issues (id, series_id, filename, number, position) VALUES ('c3', 's1', '3.cbz', '3', 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO issues (id, series_id, filename, number, position) VALUES ('c4', 's2', '1.cbz', '1', 0)`); err != nil {
		t.Fatal(err)
	}

	// c1: finished 24h ago, c2: finished 1h ago, c3: in progress, c4: finished but different series
	if _, err := s.db.Exec(`INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at) VALUES ('c1', 22, 22, ?)`, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at) VALUES ('c2', 22, 22, ?)`, now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at) VALUES ('c3', 10, 22, ?)`, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at) VALUES ('c4', 22, 22, ?)`, now); err != nil {
		t.Fatal(err)
	}

	rows, err := s.RecentFinishedReadings("s1", 3)
	if err != nil {
		t.Fatalf("RecentFinishedReadings: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].ComicID != "c2" {
		t.Errorf("rows[0]: want c2 (more recent), got %s", rows[0].ComicID)
	}
	if rows[1].ComicID != "c1" {
		t.Errorf("rows[1]: want c1, got %s", rows[1].ComicID)
	}

	// limit caps results
	rowsLim, _ := s.RecentFinishedReadings("s1", 1)
	if len(rowsLim) != 1 {
		t.Fatalf("limit=1: expected 1 row, got %d", len(rowsLim))
	}

	// series with zero finished returns empty (not nil)
	empty, err := s.RecentFinishedReadings("nonexistent", 3)
	if err != nil {
		t.Fatalf("nonexistent series error: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 rows for nonexistent series, got %d", len(empty))
	}

	// filter: total_pages == 0 excluded
	if _, err := s.db.Exec(`INSERT INTO issues (id, series_id, filename, number, position) VALUES ('c5', 's1', '5.cbz', '5', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO reading_progress (comic_id, current_page, total_pages, updated_at) VALUES ('c5', 0, 0, ?)`, now); err != nil {
		t.Fatal(err)
	}
	rows2, _ := s.RecentFinishedReadings("s1", 5)
	if len(rows2) != 2 {
		t.Errorf("expected total_pages=0 filtered out, got %d rows", len(rows2))
	}
}

// An explicit batch mark-read happens "now"; a queued offline write stamped
// earlier must not undo it.
func TestBatchMarkReadBeatsOlderQueuedWrite(t *testing.T) {
	s := newTestStore(t)
	before := time.Now().Add(-time.Minute).UnixMilli()

	if err := s.UpsertProgress(Progress{ComicID: "c1", CurrentPage: 3, TotalPages: 20, ReadAt: i64(before - 1000)}); err != nil {
		t.Fatal(err)
	}
	if err := s.BatchUpsertProgress([]Progress{{ComicID: "c1", CurrentPage: 20, TotalPages: 20}}); err != nil {
		t.Fatal(err)
	}
	err := s.UpsertProgress(Progress{ComicID: "c1", CurrentPage: 5, TotalPages: 20, ReadAt: i64(before)})
	if !errors.Is(err, ErrStaleWrite) {
		t.Fatalf("older queued write should be stale after batch mark-read, got %v", err)
	}
	p, _ := s.GetProgress("c1")
	if p.CurrentPage != 20 {
		t.Errorf("mark-read undone: page %d", p.CurrentPage)
	}
}
