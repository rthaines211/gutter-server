package store

import "testing"

func TestPipelinePendingRoundTrip(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"b", "a", "b"} { // duplicate is ignored
		if err := s.AddPipelinePending(id); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := s.ListPipelinePending()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "b" || ids[1] != "a" {
		t.Fatalf("pending = %v, want [b a] (insertion order)", ids)
	}
	if err := s.RemovePipelinePending("b"); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.ListPipelinePending()
	if len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("after remove = %v", ids)
	}
}

func TestPipelineRunsInsertListPrune(t *testing.T) {
	s := newTestStore(t)
	old := PipelineRun{StartedAt: 1000, FinishedAt: 2000, SeriesCount: 1,
		Events: []PipelineEvent{{SeriesID: "x", SeriesName: "X", Kind: "error", Detail: "boom"}}}
	recent := PipelineRun{StartedAt: 5000, FinishedAt: 6000, SkippedReason: "busy"}
	for _, r := range []PipelineRun{old, recent} {
		if err := s.InsertPipelineRun(r); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.ListPipelineRuns(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].StartedAt != 5000 || runs[1].Events[0].Detail != "boom" {
		t.Fatalf("runs = %+v", runs)
	}
	if err := s.PrunePipelineRuns(3000); err != nil {
		t.Fatal(err)
	}
	runs, _ = s.ListPipelineRuns(10)
	if len(runs) != 1 || runs[0].SkippedReason != "busy" {
		t.Fatalf("after prune = %+v", runs)
	}
}

func TestConfirmSeriesMatchByRecordsWho(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "saga", "Saga")
	if err := s.UpsertSeriesMatch(SeriesMatch{SeriesID: "saga", Source: SourceComicVine,
		VolumeID: "49019", State: MatchStateHighConfidence, Method: "first_page_md5"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmSeriesMatchBy("saga", "pipeline", 42); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetSeriesMatch("saga")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != MatchStateConfirmed || m.ConfirmedBy != "pipeline" {
		t.Fatalf("state=%s by=%q", m.State, m.ConfirmedBy)
	}
}

func TestSeriesEnrichmentVolumeNameRoundTrip(t *testing.T) {
	s := newTestStore(t)
	seedSeries(t, s, "nw", "Nightwing (2016)")
	if err := s.UpsertSeriesEnrichment(SeriesEnrichment{SeriesID: "nw", VolumeID: 1,
		VolumeName: "Nightwing", Status: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	e, err := s.GetSeriesEnrichment("nw")
	if err != nil {
		t.Fatal(err)
	}
	if e.VolumeName != "Nightwing" {
		t.Fatalf("VolumeName = %q", e.VolumeName)
	}
}
