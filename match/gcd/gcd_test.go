package gcd_test

import (
	"errors"
	"testing"

	"github.com/ryanhaines/comic-reader/match/gcd"
	"github.com/ryanhaines/comic-reader/match/gcd/gcdtest"
)

// Iron Man (2026) as GCD records it: a US/EN series, legacy numbering, one
// base row per number plus variant covers, a non-US namesake, a deleted row.
const ironMan = `
INSERT INTO gcd_publisher(id, name) VALUES (78, 'Marvel');
INSERT INTO gcd_series(id, name, year_began, publisher_id, country_id, language_id, issue_count)
	VALUES (232591, 'Iron Man', 2026, 78, 225, 25, 8),
	       (999001, 'Iron Man', 2026, 78, 150, 40, 3),   -- Italian edition: out of scope
	       (999002, 'Old Book', 1990, 78, 225, 25, 1);
UPDATE gcd_series SET deleted = 1 WHERE id = 999002;
INSERT INTO gcd_issue(id, series_id, number, title, key_date, variant_of_id, sort_code)
	VALUES (1, 232591, '1 (681)', '', '2026-03-00', NULL, 1),
	       (2, 232591, '1 (681)', '', '2026-03-00', 1, 2),      -- variant cover of #1
	       (3, 232591, '2 (682)', 'Two', '2026-04-00', NULL, 3),
	       (4, 999001, '1', '', '2026-05-00', NULL, 1);
INSERT INTO gcd_story(id, issue_id, title, type_id, sequence_number)
	VALUES (10, 1, '', 6, 0),                       -- cover
	       (11, 1, 'A New Nightmare', 19, 1),       -- comic story
	       (12, 3, 'Chapter Two', 19, 1);
INSERT INTO gcd_creator_name_detail(id, name) VALUES (100, 'Joshua Williamson'), (101, 'Carmen Carnero');
INSERT INTO gcd_story_credit(id, story_id, credit_type_id, creator_id)
	VALUES (1000, 11, 1, 100), (1001, 11, 7, 101), (1002, 11, 7, 101);   -- dup credit row
`

func TestAllSeriesIsUSEnglishAndNotDeleted(t *testing.T) {
	db := gcdtest.Open(t, ironMan)
	got, err := db.AllSeries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].VolumeID != 232591 || got[0].StartYear != "2026" || got[0].IssueCount != 8 || got[0].Publisher != "Marvel" {
		t.Fatalf("expected only the US/EN Iron Man series, got %+v", got)
	}
}

func TestIssuesBySeriesSkipsVariantsAndNormalizesNumbers(t *testing.T) {
	db := gcdtest.Open(t, ironMan)
	got, err := db.IssuesBySeries(232591)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected the two base issues, got %+v", got)
	}
	if got[0].IssueID != 1 || got[0].IssueNumber != "1" || got[0].IssueName != "A New Nightmare" || got[0].CoverDate != "2026-03-01" {
		t.Fatalf("#1: number/title/date wrong: %+v", got[0])
	}
	if got[1].IssueNumber != "2" || got[1].IssueName != "Two" {
		t.Fatalf("#2: issue title should win over story title: %+v", got[1])
	}
	if got[0].VolumeID != 232591 || got[0].VolumeName != "Iron Man" || got[0].VolumeStartYear != "2026" || got[0].VolumeIssueCount != 8 || got[0].Publisher != "Marvel" {
		t.Fatalf("series fields missing on the issue: %+v", got[0])
	}
}

func TestIssueByIDCreditsTypedAndDeduped(t *testing.T) {
	db := gcdtest.Open(t, ironMan)
	iss, credits, err := db.IssueByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if iss.IssueNumber != "1" {
		t.Fatalf("got %+v", iss)
	}
	want := []string{"Joshua Williamson|script", "Carmen Carnero|pencils", "Carmen Carnero|inks"}
	if len(credits) != len(want) {
		t.Fatalf("expected %v, got %+v", want, credits)
	}
	for i, c := range credits {
		if c.Name+"|"+c.Role != want[i] {
			t.Fatalf("credit %d: expected %s, got %+v", i, want[i], c)
		}
	}
	if _, _, err := db.IssueByID(2); !errors.Is(err, gcd.ErrNotFound) {
		t.Fatalf("a variant row must not resolve, got %v", err)
	}
}

func TestSeriesByID(t *testing.T) {
	db := gcdtest.Open(t, ironMan)
	s, err := db.SeriesByID(232591)
	if err != nil || s.Name != "Iron Man" || s.StartYear != "2026" {
		t.Fatalf("got %+v, %v", s, err)
	}
	if _, err := db.SeriesByID(999001); !errors.Is(err, gcd.ErrNotFound) {
		t.Fatalf("non-US/EN series must be invisible, got %v", err)
	}
}
