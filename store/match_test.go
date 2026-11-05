package store

import "testing"

func TestSeriesMatchCRUD(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")

	if err := s.UpsertSeriesMatch(SeriesMatch{
		SeriesID: "saga", Source: SourceComicVine, VolumeID: "49019",
		State: MatchStateHighConfidence, Confidence: 0.04, Margin: 0.5,
		Method: "first_page_md5", UpdatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	m, err := s.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.VolumeID != "49019" || m.State != MatchStateHighConfidence || m.Method != "first_page_md5" {
		t.Fatalf("unexpected match %+v", m)
	}

	// Upsert replaces rather than duplicating.
	if err := s.UpsertSeriesMatch(SeriesMatch{
		SeriesID: "saga", Source: SourceComicVine, VolumeID: "49019",
		State: MatchStateAmbiguous, Confidence: 0.3, Method: "fts", UpdatedAt: 2000,
	}); err != nil {
		t.Fatal(err)
	}
	m, _ = s.GetSeriesMatch("saga")
	if m.State != MatchStateAmbiguous || m.UpdatedAt != 2000 {
		t.Fatalf("expected upsert to replace, got %+v", m)
	}

	counts, err := s.CountSeriesMatchesByState()
	if err != nil {
		t.Fatal(err)
	}
	if counts[MatchStateAmbiguous] != 1 {
		t.Fatalf("expected 1 ambiguous, got %+v", counts)
	}

	if _, err := s.GetSeriesMatch("nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestConfirmSeriesMatch(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	if err := s.UpsertSeriesMatch(SeriesMatch{
		SeriesID: "saga", Source: SourceComicVine, VolumeID: "49019",
		State: MatchStateHighConfidence, Method: "first_page_md5", UpdatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.ConfirmSeriesMatch("saga", 5000); err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetSeriesMatch("saga")
	if m.State != MatchStateConfirmed || m.ConfirmedAt != 5000 {
		t.Fatalf("expected confirmed at 5000, got %+v", m)
	}

	if err := s.ConfirmSeriesMatch("nope", 5000); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// dont_match is sticky: it survives a later matcher run overwriting the row.
func TestDontMatchIsSticky(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	if err := s.UpsertSeriesMatch(SeriesMatch{
		SeriesID: "saga", Source: SourceComicVine, State: MatchStateNoMatch, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDontMatch("saga", true); err != nil {
		t.Fatal(err)
	}

	// A subsequent matcher pass must not clear the user's decision.
	if err := s.UpsertSeriesMatch(SeriesMatch{
		SeriesID: "saga", Source: SourceComicVine, VolumeID: "49019",
		State: MatchStateHighConfidence, Method: "first_page_md5", UpdatedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetSeriesMatch("saga")
	if !m.DontMatch {
		t.Fatalf("expected dont_match preserved across upsert, got %+v", m)
	}
}

// SetSeriesMatchState is unconfirm's write: it must move the state and clear
// confirmed_at, since the series is no longer human-approved.
func TestSetSeriesMatchState(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	if err := s.UpsertSeriesMatch(SeriesMatch{
		SeriesID: "saga", Source: SourceComicVine, VolumeID: "49019",
		State: MatchStateHighConfidence, Method: "first_page_md5", UpdatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmSeriesMatch("saga", 5000); err != nil {
		t.Fatal(err)
	}

	if err := s.SetSeriesMatchState("saga", MatchStateAmbiguous, 9000); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != MatchStateAmbiguous {
		t.Fatalf("expected state ambiguous, got %s", m.State)
	}
	if m.ConfirmedAt != 0 {
		t.Fatalf("expected confirmed_at cleared, got %d", m.ConfirmedAt)
	}
	if m.UpdatedAt != 9000 {
		t.Fatalf("expected updated_at 9000, got %d", m.UpdatedAt)
	}

	if err := s.SetSeriesMatchState("nope", MatchStateNoMatch, 1); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestIssueMatchIsManyToMany(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	if err := s.ReplaceIssues("saga", []Issue{
		{ID: "saga-tpb1", SeriesID: "saga", Filename: "Saga v1.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	// A trade collects several upstream issues.
	for _, up := range []string{"317966", "317967", "317968"} {
		if err := s.UpsertIssueMatch(IssueMatch{
			IssueID: "saga-tpb1", Source: SourceComicVine, UpstreamID: up,
			State: MatchStateConfirmed, Confidence: 0, Method: "manual",
		}); err != nil {
			t.Fatal(err)
		}
	}

	ms, err := s.ListIssueMatchesForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("expected 3 issue matches, got %d", len(ms))
	}
}

// A human re-pinning a folder into another dump wipes its issue matches, and
// only that folder's: the delete is scoped through the issues table.
func TestDeleteIssueMatchesForSeries(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	seedSeries(t, s, "other", "Other")
	if err := s.ReplaceIssues("saga", []Issue{
		{ID: "saga-1", SeriesID: "saga", Filename: "Saga 1.cbz", Number: "1", Position: 0},
		{ID: "saga-2", SeriesID: "saga", Filename: "Saga 2.cbz", Number: "2", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssues("other", []Issue{
		{ID: "other-1", SeriesID: "other", Filename: "Other 1.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}
	for _, im := range []IssueMatch{
		{IssueID: "saga-1", Source: SourceComicVine, UpstreamID: "317966", State: MatchStateHighConfidence},
		{IssueID: "saga-2", Source: SourceComicVine, UpstreamID: "317967", State: MatchStateHighConfidence},
		{IssueID: "other-1", Source: SourceComicVine, UpstreamID: "400001", State: MatchStateHighConfidence},
	} {
		if err := s.UpsertIssueMatch(im); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.DeleteIssueMatchesForSeries("saga"); err != nil {
		t.Fatal(err)
	}
	if ms, err := s.ListIssueMatchesForSeries("saga"); err != nil || len(ms) != 0 {
		t.Fatalf("expected saga's matches gone, got %+v (%v)", ms, err)
	}
	ms, err := s.ListIssueMatchesForSeries("other")
	if err != nil || len(ms) != 1 || ms[0].UpstreamID != "400001" {
		t.Fatalf("the other series' match must survive, got %+v (%v)", ms, err)
	}
}

func TestListReviewQueueExcludesDontMatchAndOtherStates(t *testing.T) {
	s := newTestStore(t)
	states := map[string]string{
		"a": MatchStateHighConfidence, "b": MatchStateNoMatch, "c": MatchStateCollection,
		"d": MatchStateConfirmed, "e": MatchStateLowConfidence,
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		seedSeries(t, s, id, id)
		if err := s.UpsertSeriesMatch(SeriesMatch{
			SeriesID: id, Source: SourceComicVine, State: states[id], UpdatedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetDontMatch("b", true); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListReviewQueue(ReviewStates...)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SeriesID != "a" || got[1].SeriesID != "c" {
		t.Fatalf("want [a c], got %+v", got)
	}
	got, err = s.ListReviewQueue(MatchStateNoMatch)
	if err != nil || len(got) != 0 {
		t.Fatalf("want empty, got %+v (%v)", got, err)
	}
}
