package store

import (
	"testing"
	"time"
)

// insertSeriesRaw writes a publisher straight to the table, bypassing any
// normalization on the write path — this is what a pre-fix row looks like.
func insertSeriesRaw(t *testing.T, s *Store, id, publisher string) {
	t.Helper()
	_, err := s.db.Exec(
		`INSERT INTO series (id, name, folder_path, issue_count, cover_path,
			publisher, created_at, updated_at)
		 VALUES (?, ?, ?, 1, '', ?, '2020-01-01 00:00:00', '2020-01-01 00:00:00')`,
		id, id, "/"+id, publisher)
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func publisherOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	var got string
	if err := s.db.QueryRow(
		`SELECT COALESCE(publisher, '') FROM series WHERE id = ?`, id).
		Scan(&got); err != nil {
		t.Fatalf("select %s: %v", id, err)
	}
	return got
}

func TestBackfillPublishersCollapsesVariants(t *testing.T) {
	s, err := NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	// The splits actually observed in the live library.
	insertSeriesRaw(t, s, "superman", "DC")
	insertSeriesRaw(t, s, "batman", "DC Comics")
	insertSeriesRaw(t, s, "saga", "Image")
	insertSeriesRaw(t, s, "spidey", "Marvel Entertainment, Inc.")
	insertSeriesRaw(t, s, "abrams", "Harry N. Abrams")

	if err := backfillPublishers(s.db); err != nil {
		t.Fatalf("backfillPublishers: %v", err)
	}

	want := map[string]string{
		"superman": "DC Comics",
		"batman":   "DC Comics",
		"saga":     "Image Comics",
		"spidey":   "Marvel",
		"abrams":   "Harry N. Abrams", // unknown publisher left alone
	}
	for id, exp := range want {
		if got := publisherOf(t, s, id); got != exp {
			t.Errorf("series %s: publisher = %q, want %q", id, got, exp)
		}
	}

	// The point of the exercise: 5 rows, 5 raw spellings, but DC/DC Comics
	// collapse — so 4 groups, not 5.
	var distinct int
	if err := s.db.QueryRow(
		`SELECT COUNT(DISTINCT publisher) FROM series`).Scan(&distinct); err != nil {
		t.Fatalf("count distinct: %v", err)
	}
	if distinct != 4 {
		t.Errorf("distinct publishers = %d, want 4 "+
			"(DC Comics, Image Comics, Marvel, Harry N. Abrams)", distinct)
	}
}

// Delta sync only ships rows newer than the client's watermark, so a rewrite
// that leaves updated_at alone never reaches an already-synced client.
func TestBackfillPublishersBumpsUpdatedAt(t *testing.T) {
	s, err := NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	insertSeriesRaw(t, s, "superman", "DC")   // will change
	insertSeriesRaw(t, s, "batman", "Marvel") // already canonical

	if err := backfillPublishers(s.db); err != nil {
		t.Fatalf("backfillPublishers: %v", err)
	}

	// Compare instants, not strings: the driver reformats the stored value
	// ("2020-01-01 00:00:00" reads back as "2020-01-01T00:00:00Z"), so a string
	// comparison would flag an untouched row as changed.
	seeded := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	var changed, untouched time.Time
	if err := s.db.QueryRow(
		`SELECT updated_at FROM series WHERE id='superman'`).Scan(&changed); err != nil {
		t.Fatalf("scan changed: %v", err)
	}
	if err := s.db.QueryRow(
		`SELECT updated_at FROM series WHERE id='batman'`).Scan(&untouched); err != nil {
		t.Fatalf("scan untouched: %v", err)
	}

	if !changed.After(seeded) {
		t.Errorf("rewritten row kept updated_at %v; clients would never resync it", changed)
	}
	if !untouched.Equal(seeded) {
		t.Errorf("unchanged row had updated_at bumped to %v; that forces a pointless resync", untouched)
	}
}

func TestBackfillPublishersIsIdempotent(t *testing.T) {
	s, err := NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	insertSeriesRaw(t, s, "superman", "DC")

	if err := backfillPublishers(s.db); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	var first string
	s.db.QueryRow(`SELECT updated_at FROM series WHERE id='superman'`).Scan(&first)

	// A second pass must be a no-op — this runs on every startup.
	if err := backfillPublishers(s.db); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got := publisherOf(t, s, "superman"); got != "DC Comics" {
		t.Errorf("after second pass: publisher = %q, want %q", got, "DC Comics")
	}
	var second string
	s.db.QueryRow(`SELECT updated_at FROM series WHERE id='superman'`).Scan(&second)
	if first != second {
		t.Errorf("second pass bumped updated_at (%q -> %q); every restart would force a full resync", first, second)
	}
}
