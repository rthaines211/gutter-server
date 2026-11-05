package store

import "testing"

func seedIssue(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO series (id, name, folder_path, issue_count) VALUES ('s','S','/s',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO issues (id, series_id, filename) VALUES (?, 's', 'f.cbz')`, id); err != nil {
		t.Fatal(err)
	}
}

func TestTaggerQueueLifecycle(t *testing.T) {
	s, err := NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedIssue(t, s, "comicA")
	seedIssue(t, s, "comicB")

	// Both are taggable initially.
	id, ok, err := s.NextTaggableComic()
	if err != nil || !ok {
		t.Fatalf("want a taggable comic, ok=%v err=%v", ok, err)
	}
	if id != "comicA" {
		t.Fatalf("want comicA first, got %q", id)
	}

	// Writing auto chapters + marking processed removes it from the queue.
	if err := s.WriteAutoChapters("comicA", []Chapter{{PageNum: 5, Title: "Ch1"}}, "medium"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTagged("comicA", "processed"); err != nil {
		t.Fatal(err)
	}
	id2, _, _ := s.NextTaggableComic()
	if id2 != "comicB" {
		t.Fatalf("comicA should be done; want comicB, got %q", id2)
	}

	// A comic that already has chapters is never queued.
	if err := s.MarkTagged("comicB", "processed"); err != nil {
		t.Fatal(err)
	}
	_, ok3, _ := s.NextTaggableComic()
	if ok3 {
		t.Fatal("queue should be empty")
	}

	// Auto chapters carry source/confirmed.
	chs, _ := s.ListChapters("comicA")
	if len(chs) != 1 {
		t.Fatalf("want 1 chapter, got %d", len(chs))
	}
}
