package enrichment

import (
	"testing"

	"github.com/ryanhaines/comic-reader/comicvine"
)

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Batman (2016)":            "batman",
		"Saga":                     "saga",
		"East of West Vol. 2":      "east of west",
		"Paper Girls v3":           "paper girls",
		"Ms. Marvel: Beyond Limit": "ms marvel beyond limit",
		"  Hellboy   ":             "hellboy",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeIssueNumber(t *testing.T) {
	cases := map[string]string{
		"004": "4", "1": "1", "0": "0", "1.5": "1.5", "001.5": "1.5", "½": "½", " 12 ": "12",
	}
	for in, want := range cases {
		if got := NormalizeIssueNumber(in); got != want {
			t.Errorf("NormalizeIssueNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func vol(id int, name string, year, count int) comicvine.Volume {
	return comicvine.Volume{ID: id, Name: name, StartYear: year, CountOfIssues: count}
}

func TestMatchAutoOnExactAgreement(t *testing.T) {
	d := Match("Batman (2016)", 2016, 50, []comicvine.Volume{
		vol(1, "Batman", 2016, 130),
		vol(2, "Batman Beyond", 2016, 50),
	})
	if !d.Auto || d.Volume.ID != 1 {
		t.Fatalf("expected auto-match on volume 1, got %+v", d)
	}
}

func TestMatchAmbiguousGoesToReview(t *testing.T) {
	// Two volumes with the same normalized name and year: never auto.
	d := Match("Batman", 2011, 52, []comicvine.Volume{
		vol(1, "Batman", 2011, 57),
		vol(2, "Batman", 2011, 60),
	})
	if d.Auto {
		t.Fatalf("expected review for ambiguous duplicates, got auto %+v", d)
	}
	if len(d.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(d.Candidates))
	}
}

func TestMatchYearMismatchGoesToReview(t *testing.T) {
	d := Match("Batman", 2016, 50, []comicvine.Volume{
		vol(1, "Batman", 1940, 715),
	})
	if d.Auto {
		t.Fatalf("expected review when year disagrees by decades, got auto")
	}
}

func TestMatchCandidateWithFewerIssuesNotAuto(t *testing.T) {
	// Library has 50 issues; candidate volume only has 12 — strong negative.
	d := Match("Batman", 2016, 50, []comicvine.Volume{
		vol(1, "Batman", 2016, 12),
	})
	if d.Auto {
		t.Fatalf("expected review when candidate has fewer issues than library")
	}
}

func TestMatchNoYearScannedStillAutoWhenUnambiguous(t *testing.T) {
	// Scanned year 0 (no ComicInfo.xml): exact name + clear margin + count ok → auto.
	d := Match("Paper Girls", 0, 10, []comicvine.Volume{
		vol(1, "Paper Girls", 2015, 30),
		vol(2, "Paper Girls: The Animated Series Companion", 2023, 3),
	})
	if !d.Auto || d.Volume.ID != 1 {
		t.Fatalf("expected auto for unambiguous exact name without year, got %+v", d)
	}
}

func TestMatchNoResults(t *testing.T) {
	d := Match("Zzyzx Quarterly", 0, 1, nil)
	if d.Auto || len(d.Candidates) != 0 {
		t.Fatalf("expected unmatched, got %+v", d)
	}
}

func TestMatchCapsCandidatesAtFive(t *testing.T) {
	vols := make([]comicvine.Volume, 8)
	for i := range vols {
		vols[i] = vol(i+1, "Batman", 2011, 60)
	}
	d := Match("Batman", 2011, 52, vols)
	if len(d.Candidates) != 5 {
		t.Fatalf("expected candidates capped at 5, got %d", len(d.Candidates))
	}
}

// TestMatchEmptyNormalizedNameNeverAuto verifies that series whose names
// normalize to "" (e.g. "Vol 1", "(2016)") never trigger auto-apply, even
// when a candidate volume also normalizes to "" (which would otherwise look
// like an exact match).
func TestMatchEmptyNormalizedNameNeverAuto(t *testing.T) {
	vols := []comicvine.Volume{
		vol(1, "Vol. 2", 2016, 200), // also normalizes to ""
		vol(2, "Batman", 2016, 130),
	}
	for _, name := range []string{"Vol 1", "(2016)"} {
		d := Match(name, 2016, 50, vols)
		if d.Auto {
			t.Errorf("Match(%q): expected !Auto, got auto volume %+v", name, d.Volume)
		}
	}
}

// TestMatchNoYearSingleResultGoesToReview verifies that a year-less search
// with only one result does NOT auto-apply — the margin test is meaningless
// with no runner-up.
func TestMatchNoYearSingleResultGoesToReview(t *testing.T) {
	d := Match("Batman", 0, 50, []comicvine.Volume{
		vol(1, "Batman", 1940, 715),
	})
	if d.Auto {
		t.Fatalf("expected review (no year, single result), got auto %+v", d)
	}
	if len(d.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(d.Candidates))
	}
}

func TestNormalizeIssueNumberAlphanumeric(t *testing.T) {
	cases := map[string]string{
		"0a":  "0a",
		"00a": "00a",
	}
	for in, want := range cases {
		if got := NormalizeIssueNumber(in); got != want {
			t.Errorf("NormalizeIssueNumber(%q) = %q, want %q", in, got, want)
		}
	}
}
