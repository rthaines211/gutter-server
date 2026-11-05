package store

import (
	"testing"
)

func TestNewStore(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"

	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer s.Close()

	tables := []string{"reading_progress", "bookmarks", "reader_settings"}
	for _, table := range tables {
		var name string
		err := s.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s does not exist: %v", table, err)
		}
	}
}

// Helper used by all subsequent tests
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertAndGetProgress(t *testing.T) {
	s := newTestStore(t)
	p := Progress{ComicID: "abc123", CurrentPage: 5, TotalPages: 20}
	if err := s.UpsertProgress(p); err != nil {
		t.Fatalf("UpsertProgress failed: %v", err)
	}
	got, err := s.GetProgress("abc123")
	if err != nil {
		t.Fatalf("GetProgress failed: %v", err)
	}
	if got.CurrentPage != 5 || got.TotalPages != 20 {
		t.Errorf("expected page 5/20, got %d/%d", got.CurrentPage, got.TotalPages)
	}
	// Update
	p.CurrentPage = 10
	if err := s.UpsertProgress(p); err != nil {
		t.Fatalf("UpsertProgress update failed: %v", err)
	}
	got, err = s.GetProgress("abc123")
	if err != nil {
		t.Fatalf("GetProgress after update failed: %v", err)
	}
	if got.CurrentPage != 10 {
		t.Errorf("expected page 10, got %d", got.CurrentPage)
	}
}

func TestGetProgressNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetProgress("nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListInProgress(t *testing.T) {
	s := newTestStore(t)
	s.UpsertProgress(Progress{ComicID: "a", CurrentPage: 1, TotalPages: 10})
	s.UpsertProgress(Progress{ComicID: "b", CurrentPage: 10, TotalPages: 10}) // finished
	s.UpsertProgress(Progress{ComicID: "c", CurrentPage: 3, TotalPages: 20})
	list, err := s.ListInProgress()
	if err != nil {
		t.Fatalf("ListInProgress failed: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 in-progress, got %d", len(list))
	}
}

func TestAddAndListBookmarks(t *testing.T) {
	s := newTestStore(t)
	if err := s.AddBookmark(Bookmark{ComicID: "abc", PageNum: 3, Note: "cool panel"}); err != nil {
		t.Fatalf("AddBookmark failed: %v", err)
	}
	if err := s.AddBookmark(Bookmark{ComicID: "abc", PageNum: 7, Note: ""}); err != nil {
		t.Fatalf("AddBookmark failed: %v", err)
	}
	bms, err := s.ListBookmarks("abc")
	if err != nil {
		t.Fatalf("ListBookmarks failed: %v", err)
	}
	if len(bms) != 2 {
		t.Errorf("expected 2 bookmarks, got %d", len(bms))
	}
	if bms[0].PageNum != 3 || bms[0].Note != "cool panel" {
		t.Errorf("unexpected bookmark: %+v", bms[0])
	}
}

func TestDeleteBookmark(t *testing.T) {
	s := newTestStore(t)
	s.AddBookmark(Bookmark{ComicID: "abc", PageNum: 5, Note: ""})
	if err := s.DeleteBookmark("abc", 5); err != nil {
		t.Fatalf("DeleteBookmark failed: %v", err)
	}
	bms, _ := s.ListBookmarks("abc")
	if len(bms) != 0 {
		t.Errorf("expected 0 bookmarks after delete, got %d", len(bms))
	}
}

func TestAddBookmarkDuplicatePage(t *testing.T) {
	s := newTestStore(t)
	s.AddBookmark(Bookmark{ComicID: "abc", PageNum: 3, Note: "first"})
	err := s.AddBookmark(Bookmark{ComicID: "abc", PageNum: 3, Note: "updated"})
	if err != nil {
		t.Fatalf("duplicate AddBookmark should upsert, got: %v", err)
	}
	bms, _ := s.ListBookmarks("abc")
	if len(bms) != 1 {
		t.Fatalf("expected 1 bookmark, got %d", len(bms))
	}
	if bms[0].Note != "updated" {
		t.Errorf("expected updated note, got %q", bms[0].Note)
	}
}

func TestGetSettingsDefault(t *testing.T) {
	s := newTestStore(t)
	settings, err := s.GetSettings("device-1")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if settings.ReadingMode != "page" {
		t.Errorf("expected default reading_mode 'page', got %q", settings.ReadingMode)
	}
	if settings.Theme != "black" {
		t.Errorf("expected default theme 'black', got %q", settings.Theme)
	}
	if settings.TapZones != true {
		t.Error("expected default tap_zones true")
	}
}

func TestUpsertSettings(t *testing.T) {
	s := newTestStore(t)
	settings := ReaderSettings{
		DeviceID:         "device-1",
		ReadingMode:      "scroll",
		ReadingDirection: "rtl",
		Theme:            "sepia",
		TapZones:         false,
		BubbleZoom:       true,
		SleepTimerMin:    30,
	}
	if err := s.UpsertSettings(settings); err != nil {
		t.Fatalf("UpsertSettings failed: %v", err)
	}
	got, err := s.GetSettings("device-1")
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got.ReadingMode != "scroll" {
		t.Errorf("expected scroll, got %q", got.ReadingMode)
	}
	if got.ReadingDirection != "rtl" {
		t.Errorf("expected rtl, got %q", got.ReadingDirection)
	}
	if got.Theme != "sepia" {
		t.Errorf("expected sepia, got %q", got.Theme)
	}
	if got.TapZones != false {
		t.Error("expected tap_zones false")
	}
	if got.BubbleZoom != true {
		t.Error("expected bubble_zoom true")
	}
	if got.SleepTimerMin != 30 {
		t.Errorf("expected sleep_timer 30, got %d", got.SleepTimerMin)
	}
}

func TestBatchUpsertProgress(t *testing.T) {
	s := newTestStore(t)
	items := []Progress{
		{ComicID: "comic-1", CurrentPage: 10, TotalPages: 10},
		{ComicID: "comic-2", CurrentPage: 20, TotalPages: 20},
		{ComicID: "comic-3", CurrentPage: 5, TotalPages: 15},
	}
	if err := s.BatchUpsertProgress(items); err != nil {
		t.Fatalf("BatchUpsertProgress failed: %v", err)
	}
	p1, _ := s.GetProgress("comic-1")
	if p1.CurrentPage != 10 || p1.TotalPages != 10 {
		t.Errorf("comic-1: expected 10/10, got %d/%d", p1.CurrentPage, p1.TotalPages)
	}
	p3, _ := s.GetProgress("comic-3")
	if p3.CurrentPage != 5 || p3.TotalPages != 15 {
		t.Errorf("comic-3: expected 5/15, got %d/%d", p3.CurrentPage, p3.TotalPages)
	}
}

func TestBatchUpsertProgressEmpty(t *testing.T) {
	s := newTestStore(t)
	if err := s.BatchUpsertProgress(nil); err != nil {
		t.Fatalf("BatchUpsertProgress with nil should not error: %v", err)
	}
}

func TestChaptersAddListReplace(t *testing.T) {
	s := newTestStore(t)
	if err := s.AddChapter(Chapter{ComicID: "c1", PageNum: 0, Title: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChapter(Chapter{ComicID: "c1", PageNum: 5, Title: "Two"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListChapters("c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].PageNum != 0 || got[1].PageNum != 5 {
		t.Fatalf("list: got %+v", got)
	}
	// Replace-set drops removed, keeps provided.
	if err := s.ReplaceChapters("c1", []Chapter{{ComicID: "c1", PageNum: 5, Title: "Two!"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListChapters("c1")
	if len(got) != 1 || got[0].PageNum != 5 || got[0].Title != "Two!" {
		t.Fatalf("after replace: got %+v", got)
	}
}
