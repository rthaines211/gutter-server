// metadata/chapters_detect_test.go
package metadata

import "testing"

func names(ns ...string) []string { return ns }

func TestHarvestFilenameDividers(t *testing.T) {
	// Superman-omnibus style: titled boundary pages among plain pages.
	in := names(
		"P00001 - Future State - Superman V.webp",
		"P00002.webp", "P00003.webp",
		"P00004 - Superman - Action Comics.webp",
		"P00005.webp",
	)
	got := HarvestStructural(in)
	if len(got) != 2 {
		t.Fatalf("want 2 candidates, got %d: %+v", len(got), got)
	}
	if got[0].Page != 0 || got[0].Title != "Future State - Superman V" {
		t.Errorf("c0 = %+v", got[0])
	}
	if got[1].Page != 3 || got[1].Source != "filename" {
		t.Errorf("c1 = %+v", got[1])
	}
}

func TestHarvestSingleTitledPageIsNoise(t *testing.T) {
	in := names("001.jpg", "002 - bonus.jpg", "003.jpg") // only 1 titled → below threshold
	if got := HarvestStructural(in); len(got) != 0 {
		t.Fatalf("want 0 (threshold), got %+v", got)
	}
}

func TestHarvestFolderPerIssue(t *testing.T) {
	in := names(
		"Issue 01/p01.jpg", "Issue 01/p02.jpg",
		"Issue 02/p01.jpg", "Issue 02/p02.jpg",
	)
	got := HarvestStructural(in)
	if len(got) != 2 || got[0].Page != 0 || got[1].Page != 2 {
		t.Fatalf("folder candidates wrong: %+v", got)
	}
	if got[0].Title != "Issue 01" || got[0].Source != "folder" {
		t.Errorf("c0 = %+v", got[0])
	}
}

func TestCleanChapterTitle(t *testing.T) {
	cases := map[string]string{
		"P00004 - Future State - Superman (": "Future State - Superman",
		"001_The_Beginning.jpg":              "The Beginning",
		"P01059 - z. P00001.webp":            "Chapter", // junk → caller defaults; cleaner returns ""
	}
	for in, want := range cases {
		got := CleanChapterTitle(in)
		if want == "Chapter" {
			if got != "" {
				t.Errorf("CleanChapterTitle(%q)=%q, want empty (junk)", in, got)
			}
			continue
		}
		if got != want {
			t.Errorf("CleanChapterTitle(%q)=%q want %q", in, got, want)
		}
	}
}
