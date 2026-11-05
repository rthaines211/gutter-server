package store

import (
	"testing"
	"time"
)

func TestListSeriesUpdatedSince(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertSeries(Series{ID: "s1", Name: "A", FolderPath: "/a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSeries(Series{ID: "s2", Name: "B", FolderPath: "/b"}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListSeriesUpdatedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("since=0: want 2 series, got %d", len(all))
	}

	none, err := s.ListSeriesUpdatedSince(time.Now().Unix() + 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("future since: want 0 series, got %d", len(none))
	}
}

func TestListProgressUpdatedSince(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertProgress(Progress{ComicID: "c1", CurrentPage: 3, TotalPages: 20}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListProgressUpdatedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ComicID != "c1" {
		t.Errorf("since=0: want [c1], got %v", all)
	}

	none, err := s.ListProgressUpdatedSince(time.Now().Unix() + 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("future since: want 0, got %d", len(none))
	}
}

func TestListIDs(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertSeries(Series{ID: "s1", Name: "A", FolderPath: "/a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProgress(Progress{ComicID: "c1", CurrentPage: 1, TotalPages: 5}); err != nil {
		t.Fatal(err)
	}

	sids, err := s.ListSeriesIDs()
	if err != nil || len(sids) != 1 || sids[0] != "s1" {
		t.Errorf("ListSeriesIDs: want [s1], got %v (%v)", sids, err)
	}
	pids, err := s.ListProgressComicIDs()
	if err != nil || len(pids) != 1 || pids[0] != "c1" {
		t.Errorf("ListProgressComicIDs: want [c1], got %v (%v)", pids, err)
	}
}
