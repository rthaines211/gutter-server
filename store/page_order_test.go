package store

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

var later = time.Now().Add(time.Hour) // rows written by the test count as older than the pass

func progressPage(t *testing.T, s *Store, id string) int {
	t.Helper()
	p, err := s.GetProgress(id)
	if err != nil || p == nil {
		t.Fatalf("GetProgress(%s): %v %v", id, p, err)
	}
	return p.CurrentPage
}

func TestRemapPagesProgress(t *testing.T) {
	s := newTestStore(t)
	s.UpsertProgress(Progress{ComicID: "c1", CurrentPage: 1, TotalPages: 3})
	if err := s.RemapPages("c1", []int{0, 2, 1}, later); err != nil {
		t.Fatal(err)
	}
	if got := progressPage(t, s, "c1"); got != 2 {
		t.Fatalf("current_page = %d, want 2", got)
	}
}

func TestRemapPagesLastPageKept(t *testing.T) {
	s := newTestStore(t)
	s.UpsertProgress(Progress{ComicID: "c1", CurrentPage: 2, TotalPages: 3})
	s.UpsertProgress(Progress{ComicID: "c2", CurrentPage: 3, TotalPages: 3})
	for _, id := range []string{"c1", "c2"} {
		if err := s.RemapPages(id, []int{2, 1, 0}, later); err != nil {
			t.Fatal(err)
		}
	}
	if got := progressPage(t, s, "c1"); got != 2 {
		t.Fatalf("last page moved to %d, want 2 (finished stays finished)", got)
	}
	if got := progressPage(t, s, "c2"); got != 3 {
		t.Fatalf("past-end progress moved to %d, want 3", got)
	}
}

func TestRemapPagesSkipsNewerProgress(t *testing.T) {
	s := newTestStore(t)
	s.UpsertProgress(Progress{ComicID: "c1", CurrentPage: 1, TotalPages: 3})
	if err := s.RemapPages("c1", []int{0, 2, 1}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := progressPage(t, s, "c1"); got != 1 {
		t.Fatalf("progress written after the pass started was remapped to %d", got)
	}
}

func pagesOf(t *testing.T, s *Store, q, id string) map[int]string {
	t.Helper()
	rows, err := s.db.Query(q, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var p int
		var v string
		rows.Scan(&p, &v)
		out[p] = v
	}
	return out
}

func TestRemapPagesUniqueSwap(t *testing.T) {
	s := newTestStore(t)
	for i, title := range []string{"A", "B", "C"} {
		if err := s.AddChapter(Chapter{ComicID: "c1", PageNum: i, Title: title}); err != nil {
			t.Fatal(err)
		}
		if err := s.FlagPage("c1", i, title, ""); err != nil {
			t.Fatal(err)
		}
	}
	s.db.Exec(`INSERT INTO bookmarks (comic_id, page_num, note) VALUES ('c1', 0, 'A'), ('c1', 1, 'B')`)

	if err := s.RemapPages("c1", []int{1, 2, 0}, later); err != nil {
		t.Fatal(err)
	}
	want := map[int]string{1: "A", 2: "B", 0: "C"}
	if got := pagesOf(t, s, `SELECT page_num, title FROM chapters WHERE comic_id=?`, "c1"); !reflect.DeepEqual(got, want) {
		t.Errorf("chapters = %v, want %v", got, want)
	}
	if got := pagesOf(t, s, `SELECT page, reason FROM page_flags WHERE comic_id=?`, "c1"); !reflect.DeepEqual(got, want) {
		t.Errorf("page_flags = %v, want %v", got, want)
	}
	if got := pagesOf(t, s, `SELECT page_num, note FROM bookmarks WHERE comic_id=?`, "c1"); !reflect.DeepEqual(got, map[int]string{1: "A", 2: "B"}) {
		t.Errorf("bookmarks = %v", got)
	}
}

func TestRemapPagesOutOfRangeKept(t *testing.T) {
	s := newTestStore(t)
	s.AddChapter(Chapter{ComicID: "c1", PageNum: 7, Title: "X"})
	if err := s.RemapPages("c1", []int{2, 1, 0}, later); err != nil {
		t.Fatal(err)
	}
	if got := pagesOf(t, s, `SELECT page_num, title FROM chapters WHERE comic_id=?`, "c1"); !reflect.DeepEqual(got, map[int]string{7: "X"}) {
		t.Fatalf("chapters = %v, want page 7 kept", got)
	}
}

func TestPageDataComicIDs(t *testing.T) {
	s := newTestStore(t)
	s.UpsertProgress(Progress{ComicID: "p", CurrentPage: 1, TotalPages: 3})
	s.AddChapter(Chapter{ComicID: "c", PageNum: 1, Title: "X"})
	s.FlagPage("f", 1, "", "")
	s.db.Exec(`INSERT INTO bookmarks (comic_id, page_num) VALUES ('b', 1), ('p', 2)`)
	got, err := s.PageDataComicIDs()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if want := []string{"b", "c", "f", "p"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PageDataComicIDs = %v, want %v", got, want)
	}
}

func TestMigrationFlag(t *testing.T) {
	s := newTestStore(t)
	if done, err := s.MigrationDone("x"); err != nil || done {
		t.Fatalf("fresh flag: done=%v err=%v", done, err)
	}
	if err := s.MarkMigrationDone("x"); err != nil {
		t.Fatal(err)
	}
	if done, _ := s.MigrationDone("x"); !done {
		t.Fatal("flag not set")
	}
}
