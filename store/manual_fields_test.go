package store

import "testing"

func strp(s string) *string { return &s }

// A hand edit must survive a later enrichment write (Confirm/Apply, legacy
// worker), while fields the user didn't touch still take the new values.
func TestSeriesManualEditSurvivesEnrichmentUpsert(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "nightwing", "Nightwing (2016)")
	if err := s.UpsertSeriesEnrichment(SeriesEnrichment{SeriesID: "nightwing", VolumeID: 1,
		Description: "provider desc", Publisher: "DC", Status: "confirmed"}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateSeriesMetadata("nightwing", SeriesMetadataPatch{Description: strp("my desc")}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpsertSeriesEnrichment(SeriesEnrichment{SeriesID: "nightwing", VolumeID: 2,
		Description: "re-confirmed desc", Publisher: "DC Comics", Status: "confirmed"}); err != nil {
		t.Fatal(err)
	}

	e, err := s.GetSeriesEnrichment("nightwing")
	if err != nil {
		t.Fatal(err)
	}
	if e.Description != "my desc" {
		t.Errorf("manual description overwritten: got %q", e.Description)
	}
	if e.Publisher != "DC Comics" {
		t.Errorf("untouched publisher should update: got %q", e.Publisher)
	}
}

func TestIssueManualEditSurvivesEnrichmentUpsert(t *testing.T) {
	s := newTestStore(t)
	seedIssue(t, s, "i1")
	if err := s.UpsertIssueEnrichment(IssueEnrichment{IssueID: "i1", VineID: 1,
		Title: "provider title", Summary: "provider sum"}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateIssueMetadata("i1", IssueMetadataPatch{Title: strp("my title")}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpsertIssueEnrichment(IssueEnrichment{IssueID: "i1", VineID: 2,
		Title: "re-confirmed title", Summary: "re-confirmed sum"}); err != nil {
		t.Fatal(err)
	}

	e, err := s.GetIssueEnrichment("i1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Title != "my title" {
		t.Errorf("manual title overwritten: got %q", e.Title)
	}
	if e.Summary != "re-confirmed sum" {
		t.Errorf("untouched summary should update: got %q", e.Summary)
	}
}
