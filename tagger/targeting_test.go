package tagger

import "testing"

func TestIsLikelyCollection(t *testing.T) {
	if !IsLikelyCollection(120, []string{"p1.jpg", "p2.jpg"}, 60) {
		t.Error("120 pages over threshold should be a collection")
	}
	if IsLikelyCollection(24, []string{"p1.jpg"}, 60) {
		t.Error("24 pages is a single issue")
	}
	// Below page threshold but multiple issue-folder prefixes → collection.
	if !IsLikelyCollection(40, []string{"Issue 1/p1.jpg", "Issue 1/p2.jpg", "Issue 2/p1.jpg"}, 60) {
		t.Error("multiple subfolder prefixes signal a collection")
	}
	if IsLikelyCollection(0, nil, 60) {
		t.Error("empty input is not a collection")
	}
}

func TestQuietHours(t *testing.T) {
	s, e, ok := ParseQuietHours("2-7")
	if !ok || s != 2 || e != 7 {
		t.Fatalf("got %d-%d ok=%v", s, e, ok)
	}
	if _, _, ok := ParseQuietHours(""); ok {
		t.Error("empty = no window")
	}
	if !InQuietWindow(3, 2, 7) {
		t.Error("3 is within 2-7")
	}
	if InQuietWindow(9, 2, 7) {
		t.Error("9 is outside 2-7")
	}
	if !InQuietWindow(23, 22, 5) {
		t.Error("23 is within wrap window 22-5")
	}
	if !InQuietWindow(2, 22, 5) {
		t.Error("2 is within wrap window 22-5")
	}
}
