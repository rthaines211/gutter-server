package creators

import (
	"path/filepath"
	"testing"
)

func TestOwnedState(t *testing.T) {
	cases := []struct {
		owned, total int
		want         string
	}{
		{12, 12, "owned"},
		{13, 12, "owned"},
		{7, 12, "partial"},
		{0, 12, "none"},
		// Edge cases left open by the brief:
		{5, 0, "owned"}, // unknown total count, but something is owned
		{0, 0, "none"},  // nothing owned always reads as "none"
	}
	for _, c := range cases {
		if got := OwnedState(c.owned, c.total); got != c.want {
			t.Errorf("OwnedState(%d, %d) = %q, want %q", c.owned, c.total, got, c.want)
		}
	}
}

func TestOwnershipDirect(t *testing.T) {
	cvPath := newCVFixture(t)
	// Volume 1 "Saga": 6 issues, IDs 101-106.
	for i, id := range []int64{101, 102, 103, 104, 105, 106} {
		seedIssue(t, cvPath, id, 1, "[]")
		_ = i
	}
	// Volume 3, a trade: 1 issue, ID 30.
	seedIssue(t, cvPath, 30, 3, "[]")

	idx, err := OpenIndex(filepath.Join(t.TempDir(), "creators_index.db"), cvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	owned := map[int64]string{101: "s1", 102: "s1", 30: "s-trade"}
	got, err := idx.Ownership(owned)
	if err != nil {
		t.Fatal(err)
	}

	want := map[int64]VolumeOwnership{
		1: {OwnedIssues: 2, SeriesID: "s1"},
		3: {OwnedIssues: 1, SeriesID: "s-trade"},
	}
	if len(got) != len(want) {
		t.Fatalf("Ownership() = %+v, want %+v", got, want)
	}
	for volID, w := range want {
		if g := got[volID]; g != w {
			t.Errorf("Ownership()[%d] = %+v, want %+v", volID, g, w)
		}
	}
	// No entry for a volume with nothing owned.
	if _, ok := got[2]; ok {
		t.Errorf("Ownership() has unexpected entry for volume 2: %+v", got[2])
	}
}
