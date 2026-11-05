package store

import (
	"encoding/json"
	"testing"
)

func seedSeries(t *testing.T, s *Store, id, name string) {
	t.Helper()
	if err := s.UpsertSeries(Series{ID: id, Name: name, FolderPath: "/lib/" + id, IssueCount: 5}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedCVIssues(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	if err := s.ReplaceIssues("saga", []Issue{
		{ID: "saga-1", Filename: "Saga 001.cbz"},
		{ID: "saga-2", Filename: "Saga 002.cbz"},
		{ID: "saga-unmatched", Filename: "Saga 003.cbz"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssueEnrichment(IssueEnrichment{IssueID: "saga-1", VineID: 101}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssueEnrichment(IssueEnrichment{IssueID: "saga-2", VineID: 102}); err != nil {
		t.Fatal(err)
	}
	// comicvine_issue_id = 0 means "not matched" and must be excluded.
	if err := s.UpsertIssueEnrichment(IssueEnrichment{IssueID: "saga-unmatched", VineID: 0}); err != nil {
		t.Fatal(err)
	}

	owned, err := s.OwnedCVIssues()
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{101: "saga", 102: "saga"}
	if len(owned) != len(want) {
		t.Fatalf("OwnedCVIssues() = %v, want %v", owned, want)
	}
	for k, v := range want {
		if owned[k] != v {
			t.Fatalf("OwnedCVIssues()[%d] = %q, want %q", k, owned[k], v)
		}
	}
}

func TestEnqueueMissingEnrichment(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "batman-2016", "Batman (2016)")
	seedSeries(t, s, "saga", "Saga")

	added, err := s.EnqueueMissingEnrichment()
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("expected 2 enqueued, got %d", added)
	}

	// Idempotent: second call adds nothing.
	added, err = s.EnqueueMissingEnrichment()
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("expected 0 on second enqueue, got %d", added)
	}

	counts, err := s.CountEnrichmentByStatus()
	if err != nil {
		t.Fatal(err)
	}
	if counts["queued"] != 2 {
		t.Fatalf("expected 2 queued, got %v", counts)
	}
}

func TestNextQueuedEnrichmentBackoff(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	if _, err := s.EnqueueMissingEnrichment(); err != nil {
		t.Fatal(err)
	}

	now := int64(10_000_000)
	state, err := s.NextQueuedEnrichment(now)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || state.SeriesID != "saga" {
		t.Fatalf("expected saga queued, got %+v", state)
	}

	// Record an attempt at `now`: row is ineligible until now + 60s<<2.
	if err := s.RecordEnrichmentAttempt("saga", now); err != nil {
		t.Fatal(err)
	}
	state, err = s.NextQueuedEnrichment(now + 1000)
	if err != nil {
		t.Fatal(err)
	}
	if state != nil {
		t.Fatalf("expected backoff to hide row, got %+v", state)
	}
	state, err = s.NextQueuedEnrichment(now + 5*60_000)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		t.Fatal("expected row eligible after backoff")
	}
	if state.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", state.Attempts)
	}

	// 5 attempts = permanently parked.
	for i := 0; i < 4; i++ {
		if err := s.RecordEnrichmentAttempt("saga", now); err != nil {
			t.Fatal(err)
		}
	}
	state, err = s.NextQueuedEnrichment(now + 100*365*24*3600*1000)
	if err != nil {
		t.Fatal(err)
	}
	if state != nil {
		t.Fatalf("expected nil after 5 attempts, got %+v", state)
	}
}

func TestEnrichmentStateTransitions(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	if _, err := s.EnqueueMissingEnrichment(); err != nil {
		t.Fatal(err)
	}

	if err := s.SetEnrichmentCandidates("saga", `[{"volume_id":42}]`); err != nil {
		t.Fatal(err)
	}
	state, err := s.GetEnrichmentState("saga")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "pending_review" || state.Candidates != `[{"volume_id":42}]` {
		t.Fatalf("unexpected state %+v", state)
	}

	pending, err := s.ListPendingReview()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].SeriesID != "saga" {
		t.Fatalf("unexpected pending %+v", pending)
	}

	// Confirm: chosen volume re-queues with attempts reset.
	if err := s.SetEnrichmentChosen("saga", 42); err != nil {
		t.Fatal(err)
	}
	state, err = s.GetEnrichmentState("saga")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "queued" || state.ChosenVolumeID != 42 || state.Attempts != 0 {
		t.Fatalf("unexpected state after confirm %+v", state)
	}

	// Worker reads the chosen volume through NextQueuedEnrichment.
	next, err := s.NextQueuedEnrichment(1<<62 - 1)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.SeriesID != "saga" || next.ChosenVolumeID != 42 {
		t.Fatalf("expected NextQueued to return saga with ChosenVolumeID=42, got %+v", next)
	}

	if err := s.SetEnrichmentStatus("saga", "enriched"); err != nil {
		t.Fatal(err)
	}

	// Missing row returns ErrNotFound.
	if err := s.SetEnrichmentStatus("nonexistent", "enriched"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for nonexistent series, got %v", err)
	}

	// Requeue resets everything.
	if err := s.RequeueEnrichment("saga"); err != nil {
		t.Fatal(err)
	}
	state, err = s.GetEnrichmentState("saga")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "queued" || state.ChosenVolumeID != 0 || state.Candidates != "[]" {
		t.Fatalf("unexpected state after requeue %+v", state)
	}
}

func TestSeriesAndIssueEnrichmentCRUD(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	if err := s.ReplaceIssues("saga", []Issue{
		{ID: "saga-001", SeriesID: "saga", Filename: "Saga 001.cbz", Number: "1", Position: 0},
	}); err != nil {
		t.Fatal(err)
	}

	creators, _ := json.Marshal([]Creator{{Name: "Brian K. Vaughan", Role: "writer"}})
	if err := s.UpsertSeriesEnrichment(SeriesEnrichment{
		SeriesID: "saga", VolumeID: 49019, Description: "Space opera.",
		Publisher: "Image", StartYear: 2012, Creators: string(creators),
		Status: "auto", EnrichedAt: 123,
	}); err != nil {
		t.Fatal(err)
	}

	e, err := s.GetSeriesEnrichment("saga")
	if err != nil {
		t.Fatal(err)
	}
	if e.Publisher != "Image" || e.StartYear != 2012 || e.Status != "auto" {
		t.Fatalf("unexpected enrichment %+v", e)
	}

	m, err := s.ListSeriesEnrichmentMap()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["saga"]; !ok {
		t.Fatal("expected saga in enrichment map")
	}

	if err := s.UpsertIssueEnrichment(IssueEnrichment{
		IssueID: "saga-001", VineID: 1, Title: "Chapter One",
		Summary: "It begins.", CoverDate: "2012-03-14", EnrichedAt: 123,
	}); err != nil {
		t.Fatal(err)
	}
	im, err := s.ListIssueEnrichmentForSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if im["saga-001"].Title != "Chapter One" {
		t.Fatalf("unexpected issue enrichment %+v", im)
	}

	// Delete clears both series- and issue-level rows.
	if err := s.DeleteSeriesEnrichment("saga"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSeriesEnrichment("saga"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	im, _ = s.ListIssueEnrichmentForSeries("saga")
	if len(im) != 0 {
		t.Fatalf("expected issue enrichment cleared, got %+v", im)
	}
}

func TestTouchSeries(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	before, err := s.GetSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	// CURRENT_TIMESTAMP has second resolution; just assert no error and not-earlier.
	if err := s.TouchSeries("saga"); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetSeries("saga")
	if err != nil {
		t.Fatal(err)
	}
	if after.UpdatedAt.Before(before.UpdatedAt) {
		t.Fatal("updated_at went backwards")
	}
}
