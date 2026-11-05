package store

import "testing"

func TestSeriesEnrichmentRoundTripsMergedFields(t *testing.T) {
	st, err := NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertSeries(Series{ID: "s1", Name: "S", FolderPath: "/s1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeriesEnrichment(SeriesEnrichment{
		SeriesID: "s1", VolumeID: 7, Genre: "superhero",
		GCDSeriesID: 226636, SecondarySource: "gcd", Status: "confirmed", EnrichedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSeriesEnrichment("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Genre != "superhero" || got.GCDSeriesID != 226636 || got.SecondarySource != "gcd" {
		t.Fatalf("series merged fields not persisted: %+v", got)
	}
}

func TestIssueEnrichmentRoundTripsMergedFields(t *testing.T) {
	st, err := NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertSeries(Series{ID: "s1", Name: "S", FolderPath: "/s1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceIssues("s1", []Issue{{ID: "i1", SeriesID: "s1", Filename: "a.cbz", Number: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssueEnrichment(IssueEnrichment{
		IssueID: "i1", VineID: 0, PageCount: 22, Characters: `[{"name":"Batman"}]`, EnrichedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	m, err := st.ListIssueEnrichmentForSeries("s1")
	if err != nil {
		t.Fatal(err)
	}
	if m["i1"].PageCount != 22 || m["i1"].Characters != `[{"name":"Batman"}]` {
		t.Fatalf("issue merged fields not persisted: %+v", m["i1"])
	}
}
