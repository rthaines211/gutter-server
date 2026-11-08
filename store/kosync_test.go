package store

import (
	"errors"
	"testing"
	"time"
)

func TestKosyncDocuments(t *testing.T) {
	s := newTestStore(t)
	docs := []KosyncDocument{
		{ComicID: "a", BinaryDigest: "bin-a", FilenameDigest: "fn-shared", Size: 10, MTime: 1},
		{ComicID: "b", BinaryDigest: "bin-b", FilenameDigest: "fn-shared", Size: 20, MTime: 2},
		{ComicID: "c", BinaryDigest: "fn-shared", FilenameDigest: "fn-c", Size: 30, MTime: 3},
	}
	for _, d := range docs {
		if err := s.UpsertKosyncDocument(d); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		digest string
		want   []string
	}{
		{"bin-a", []string{"a"}},
		{"fn-shared", []string{"c", "a", "b"}}, // binary match first
		{"nope", nil},
	}
	for _, tc := range tests {
		got, err := s.ComicIDsForKosyncDigest(tc.digest)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v want %v", tc.digest, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %v want %v", tc.digest, got, tc.want)
			}
		}
	}

	// Refresh replaces the digests.
	if err := s.UpsertKosyncDocument(KosyncDocument{ComicID: "a", BinaryDigest: "bin-a2", FilenameDigest: "fn-a", Size: 11, MTime: 4}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListKosyncDocuments()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all["a"].BinaryDigest != "bin-a2" || all["a"].Size != 11 {
		t.Errorf("after refresh: %+v", all)
	}
	if err := s.DeleteKosyncDocument("a"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ComicIDsForKosyncDigest("bin-a2"); len(ids) != 0 {
		t.Errorf("deleted doc still found: %v", ids)
	}
}

func TestKosyncProgress(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetKosyncProgress("doc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	ts := time.Unix(1_700_000_000, 0).UTC()
	in := KosyncProgress{Document: "doc", ComicID: "c1", Progress: "7", Percentage: 0.25, Device: "Kobo", DeviceID: "dev", Timestamp: ts}
	if err := s.UpsertKosyncProgress(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetKosyncProgress("doc")
	if err != nil {
		t.Fatal(err)
	}
	if *got != in {
		t.Errorf("got %+v want %+v", *got, in)
	}
	in.Progress, in.Timestamp = "8", ts.Add(time.Minute)
	if err := s.UpsertKosyncProgress(in); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetKosyncProgress("doc")
	if got.Progress != "8" || !got.Timestamp.Equal(in.Timestamp) {
		t.Errorf("update not applied: %+v", *got)
	}
}
