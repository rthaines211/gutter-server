package writeback

import (
	"testing"

	"github.com/rthaines211/gutter-server/metadata"
	"github.com/rthaines211/gutter-server/store"
)

func TestApplyCredits(t *testing.T) {
	ci := &metadata.ComicInfo{}
	applyCredits(ci, []store.Creator{
		{Name: "Tom King", Role: "writer"},
		{Name: "David Finch", Role: "penciler"},   // CV one-l spelling
		{Name: "Matt Banning", Role: "inker"},
		{Name: "Jordie Bellaire", Role: "Colourist"}, // case + British spelling
		{Name: "John Workman", Role: "letterer"},
		{Name: "David Finch", Role: "cover"},
		{Name: "Jamie Rich", Role: "editor"},
		{Name: "Second Cover", Role: "cover"},       // multi-name join
		{Name: "Nobody", Role: "gaffer"},            // unknown -> dropped
	})
	cases := map[string]string{
		"Writer":      "Tom King",
		"Penciller":   "David Finch",
		"Inker":       "Matt Banning",
		"Colorist":    "Jordie Bellaire",
		"Letterer":    "John Workman",
		"CoverArtist": "David Finch, Second Cover",
		"Editor":      "Jamie Rich",
	}
	got := map[string]string{
		"Writer": ci.Writer, "Penciller": ci.Penciller, "Inker": ci.Inker,
		"Colorist": ci.Colorist, "Letterer": ci.Letterer,
		"CoverArtist": ci.CoverArtist, "Editor": ci.Editor,
	}
	for field, want := range cases {
		if got[field] != want {
			t.Errorf("%s = %q, want %q", field, got[field], want)
		}
	}
}

func TestApplyCreditsEmpty(t *testing.T) {
	ci := &metadata.ComicInfo{Writer: "Pre-existing"}
	applyCredits(ci, nil)
	if ci.Writer != "Pre-existing" {
		t.Errorf("empty creators must not clobber existing Writer, got %q", ci.Writer)
	}
}
