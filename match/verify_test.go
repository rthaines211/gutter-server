package match

import (
	"testing"

	"github.com/rthaines211/gutter-server/match/localcv"
	"github.com/rthaines211/gutter-server/match/parse"
	"github.com/rthaines211/gutter-server/store"
)

func TestVerifyAgreement(t *testing.T) {
	v := Verify(
		parse.Folder{Series: "Saga", Year: 2012},
		parse.File{Series: "Saga", Number: "001", Year: 2012},
		&localcv.Issue{VolumeName: "Saga", IssueNumber: "1", VolumeStartYear: "2012"},
	)
	if v.State != store.MatchStateHighConfidence {
		t.Fatalf("expected high_confidence, got %s (%+v)", v.State, v.Penalties)
	}
	if len(v.Penalties) != 0 {
		t.Fatalf("expected no penalties, got %+v", v.Penalties)
	}
}

// Zero-padding differences must not count as disagreement.
func TestVerifyPaddingIsNotADisagreement(t *testing.T) {
	v := Verify(
		parse.Folder{Series: "Batman", Year: 2016},
		parse.File{Series: "Batman", Number: "008"},
		&localcv.Issue{VolumeName: "Batman", IssueNumber: "8", VolumeStartYear: "2016"},
	)
	if v.State != store.MatchStateHighConfidence {
		t.Fatalf("expected high_confidence, got %s (%+v)", v.State, v.Penalties)
	}
}

func TestVerifyNumberDisagreement(t *testing.T) {
	v := Verify(
		parse.Folder{Series: "Saga", Year: 2012},
		parse.File{Series: "Saga", Number: "5"},
		&localcv.Issue{VolumeName: "Saga", IssueNumber: "1", VolumeStartYear: "2012"},
	)
	if v.State != store.MatchStateAmbiguous {
		t.Fatalf("expected ambiguous, got %s", v.State)
	}
	if _, ok := v.Penalties["number"]; !ok {
		t.Fatalf("expected a number penalty, got %+v", v.Penalties)
	}
}

func TestVerifySeriesDisagreement(t *testing.T) {
	v := Verify(
		parse.Folder{Series: "Saga", Year: 2012},
		parse.File{Series: "Saga", Number: "1"},
		&localcv.Issue{VolumeName: "Paper Girls", IssueNumber: "1", VolumeStartYear: "2015"},
	)
	if v.State != store.MatchStateAmbiguous {
		t.Fatalf("expected ambiguous, got %s", v.State)
	}
	if _, ok := v.Penalties["series"]; !ok {
		t.Fatalf("expected a series penalty, got %+v", v.Penalties)
	}
}

// A missing year on our side is unknown, not wrong, and must not penalise.
func TestVerifyMissingYearDoesNotPenalise(t *testing.T) {
	v := Verify(
		parse.Folder{Series: "Fantastic Four", Year: 0},
		parse.File{Series: "Fantastic Four", Number: "4"},
		&localcv.Issue{VolumeName: "Fantastic Four", IssueNumber: "4", VolumeStartYear: "1961"},
	)
	if v.State != store.MatchStateHighConfidence {
		t.Fatalf("expected high_confidence, got %s (%+v)", v.State, v.Penalties)
	}
}

// A trade whose hash landed in a multi-issue trade volume (Saga TPB volume,
// 12 issues) is verified like a single issue: local volume number vs
// ComicVine issue number.
func TestVerifyCollectionInTradeVolumeChecksNumber(t *testing.T) {
	v := Verify(
		parse.Folder{Series: "Saga", Year: 2012},
		parse.File{Series: "Saga", Number: "01", IsCollection: true, Format: "tpb"},
		&localcv.Issue{VolumeName: "Saga", IssueNumber: "1", VolumeStartYear: "2012", VolumeIssueCount: 12},
	)
	if v.State != store.MatchStateHighConfidence {
		t.Fatalf("expected high_confidence, got %s (%+v)", v.State, v.Penalties)
	}
	if v.Format != "tpb" {
		t.Fatalf("expected format tpb on the verdict, got %q", v.Format)
	}

	bad := Verify(
		parse.Folder{Series: "Saga", Year: 2012},
		parse.File{Series: "Saga", Number: "03", IsCollection: true, Format: "tpb"},
		&localcv.Issue{VolumeName: "Saga", IssueNumber: "1", VolumeStartYear: "2012", VolumeIssueCount: 12},
	)
	if bad.State != store.MatchStateAmbiguous {
		t.Fatalf("expected ambiguous on number mismatch, got %s", bad.State)
	}
	if _, ok := bad.Penalties["number"]; !ok {
		t.Fatalf("expected a number penalty, got %+v", bad.Penalties)
	}
}

// A trade whose hash landed in a one-issue volume ("Criminal: Coward") has no
// meaningful ComicVine issue number, so the local volume number is not
// compared against it. Series and year are still checked.
func TestVerifyCollectionInOneShotVolumeSkipsNumber(t *testing.T) {
	v := Verify(
		parse.Folder{Series: "Criminal", Year: 2015},
		parse.File{Series: "Criminal", Number: "03", IsCollection: true, Format: "tpb"},
		&localcv.Issue{VolumeName: "Criminal: The Dead and the Dying", IssueNumber: "1", VolumeStartYear: "2015", VolumeIssueCount: 1},
	)
	if _, ok := v.Penalties["number"]; ok {
		t.Fatalf("number must not be penalised against a one-issue volume: %+v", v.Penalties)
	}
	// The series name still disagrees ("Criminal" vs "Criminal: The Dead and
	// the Dying"), so this one lands ambiguous — that is the runner's problem
	// (Task 4), not Verify's. Assert only what Verify owns.
	if v.Penalties == nil {
		t.Fatalf("expected non-nil Penalties map")
	}
}

// A single issue (not a collection) is unaffected by VolumeIssueCount.
func TestVerifySingleIssueIgnoresVolumeIssueCount(t *testing.T) {
	v := Verify(
		parse.Folder{Series: "Saga", Year: 2012},
		parse.File{Series: "Saga", Number: "5"},
		&localcv.Issue{VolumeName: "Saga", IssueNumber: "1", VolumeStartYear: "2012", VolumeIssueCount: 1},
	)
	if v.State != store.MatchStateAmbiguous {
		t.Fatalf("expected ambiguous, got %s", v.State)
	}
}

// Series and number agree, so the year is provably the only thing penalised.
func TestVerifyYearDisagreement(t *testing.T) {
	v := Verify(
		parse.Folder{Series: "Saga", Year: 2016},
		parse.File{Series: "Saga", Number: "1"},
		&localcv.Issue{VolumeName: "Saga", IssueNumber: "1", VolumeStartYear: "2020"},
	)
	if v.State != store.MatchStateAmbiguous {
		t.Fatalf("expected ambiguous, got %s (%+v)", v.State, v.Penalties)
	}
	if _, ok := v.Penalties["year"]; !ok {
		t.Fatalf("expected a year penalty, got %+v", v.Penalties)
	}
}

// Pins the slack at exactly +/-1 in both directions. Series and number agree
// throughout, so the year is the only variable.
func TestVerifyYearSlackBoundary(t *testing.T) {
	tests := []struct {
		name        string
		folderYear  int
		volumeYear  string
		wantState   string
		wantPenalty bool
	}{
		{"same year", 2016, "2016", store.MatchStateHighConfidence, false},
		{"one year later", 2017, "2016", store.MatchStateHighConfidence, false},
		{"one year earlier", 2015, "2016", store.MatchStateHighConfidence, false},
		{"two years later", 2018, "2016", store.MatchStateAmbiguous, true},
		{"two years earlier", 2014, "2016", store.MatchStateAmbiguous, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verify(
				parse.Folder{Series: "Batman", Year: tt.folderYear},
				parse.File{Series: "Batman", Number: "1"},
				&localcv.Issue{VolumeName: "Batman", IssueNumber: "1", VolumeStartYear: tt.volumeYear},
			)
			if v.State != tt.wantState {
				t.Fatalf("folder %d vs volume %s: expected %s, got %s (%+v)",
					tt.folderYear, tt.volumeYear, tt.wantState, v.State, v.Penalties)
			}
			if _, ok := v.Penalties["year"]; ok != tt.wantPenalty {
				t.Fatalf("folder %d vs volume %s: year penalty present=%v, want %v (%+v)",
					tt.folderYear, tt.volumeYear, ok, tt.wantPenalty, v.Penalties)
			}
		})
	}
}

// A garbage or empty upstream year is unknown, not wrong: no penalty, no panic.
func TestVerifyUnparseableVolumeYear(t *testing.T) {
	for _, volumeYear := range []string{"n/a", ""} {
		t.Run("volume year "+volumeYear, func(t *testing.T) {
			v := Verify(
				parse.Folder{Series: "Saga", Year: 2012},
				parse.File{Series: "Saga", Number: "1"},
				&localcv.Issue{VolumeName: "Saga", IssueNumber: "1", VolumeStartYear: volumeYear},
			)
			if v.State != store.MatchStateHighConfidence {
				t.Fatalf("expected high_confidence, got %s (%+v)", v.State, v.Penalties)
			}
			if _, ok := v.Penalties["year"]; ok {
				t.Fatalf("expected no year penalty, got %+v", v.Penalties)
			}
		})
	}
}
