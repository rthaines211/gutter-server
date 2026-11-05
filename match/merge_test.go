package match

import (
	"testing"

	"github.com/ryanhaines/comic-reader/store"
)

func TestMergeSeriesPrefersCVSharedFieldsGCDGenre(t *testing.T) {
	// primary = GCD (has genre, thin description), secondary = CV (rich shared).
	gcd := store.SeriesEnrichment{Description: "", Publisher: "DC", StartYear: 2016,
		Creators: "[]", Genre: "superhero", GCDSeriesID: 42, Status: "confirmed"}
	cv := store.SeriesEnrichment{Description: "The Dark Knight.", Publisher: "DC Comics",
		StartYear: 2016, Creators: `[{"name":"Tom King","role":"writer"}]`}
	got := mergeSeriesFields(gcd, cv, false /* primaryIsCV */)
	if got.Description != "The Dark Knight." { // CV preferred
		t.Errorf("description: %q", got.Description)
	}
	if got.Publisher != "DC Comics" { // CV preferred over non-empty GCD
		t.Errorf("publisher: %q", got.Publisher)
	}
	if got.Creators != `[{"name":"Tom King","role":"writer"}]` { // CV list preferred
		t.Errorf("creators: %q", got.Creators)
	}
	if got.Genre != "superhero" { // GCD preferred
		t.Errorf("genre: %q", got.Genre)
	}
	if got.GCDSeriesID != 42 || got.SecondarySource != "comicvine" {
		t.Errorf("provenance: %+v", got)
	}
}

func TestMergeNeverClobbersWithBlank(t *testing.T) {
	// primary = CV (rich), secondary = GCD (mostly blank shared fields).
	cv := store.SeriesEnrichment{Description: "Rich.", Publisher: "Image", StartYear: 2012,
		Creators: `[{"name":"BKV","role":"writer"}]`}
	gcd := store.SeriesEnrichment{Description: "", Publisher: "", Genre: "sci-fi", GCDSeriesID: 7}
	got := mergeSeriesFields(cv, gcd, true /* primaryIsCV */)
	if got.Description != "Rich." || got.Publisher != "Image" || got.StartYear != 2012 {
		t.Errorf("blank GCD clobbered CV: %+v", got)
	}
	if got.Creators != `[{"name":"BKV","role":"writer"}]` {
		t.Errorf("creators clobbered: %q", got.Creators)
	}
	if got.Genre != "sci-fi" { // GCD fills the gap CV lacks
		t.Errorf("genre: %q", got.Genre)
	}
	if got.GCDSeriesID != 7 || got.SecondarySource != "gcd" {
		t.Errorf("provenance: %+v", got)
	}
}

func TestMergeIssuePrefersCVGCDPageCount(t *testing.T) {
	cv := store.IssueEnrichment{Title: "Ch. 1", Summary: "CV summary", CoverDate: "2012-03-14",
		Characters: `[{"name":"Alana"}]`, PageCount: 0, VineID: 99}
	gcd := store.IssueEnrichment{Title: "Chapter One", Summary: "GCD synopsis", CoverDate: "2012-03-00",
		Characters: `[{"name":"The Will"}]`, PageCount: 24}
	got := mergeIssueFields(cv, gcd, true)
	if got.Title != "Ch. 1" || got.Summary != "CV summary" || got.CoverDate != "2012-03-14" {
		t.Errorf("shared not CV-preferred: %+v", got)
	}
	if got.Characters != `[{"name":"Alana"}]` { // CV preferred
		t.Errorf("characters: %q", got.Characters)
	}
	if got.PageCount != 24 { // GCD only
		t.Errorf("page_count: %v", got.PageCount)
	}
	if got.VineID != 99 { // keep CV id
		t.Errorf("vine id: %v", got.VineID)
	}
}
