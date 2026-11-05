package tagger

import (
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

type fakeDetector struct {
	pages    map[string]int
	names    map[string][]string
	detect   map[string][]DetectedChapter
	active   bool
	detected []string // comic ids actually OCR'd
}

func (f *fakeDetector) PageMeta(id string) (int, []string, error) { return f.pages[id], f.names[id], nil }
func (f *fakeDetector) Detect(id string) ([]DetectedChapter, error) {
	f.detected = append(f.detected, id)
	return f.detect[id], nil
}
func (f *fakeDetector) RecentlyActive() bool { return f.active }

func newStore(t *testing.T) *store.Store {
	s, err := store.NewStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seed(t *testing.T, s *store.Store, id string) {
	if _, err := s.DB().Exec(`INSERT OR IGNORE INTO series (id,name,folder_path,issue_count) VALUES ('s','S','/s',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO issues (id,series_id,filename) VALUES (?, 's','f.cbz')`, id); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerTagsCollectionSkipsSingle(t *testing.T) {
	s := newStore(t)
	seed(t, s, "omni")
	seed(t, s, "single")
	d := &fakeDetector{
		pages:  map[string]int{"omni": 300, "single": 22},
		names:  map[string][]string{},
		detect: map[string][]DetectedChapter{"omni": {{Page: 0, Title: "A"}, {Page: 100, Title: "B"}}},
	}
	w := NewWorker(s, d, Options{MinPages: 60, PauseBetween: 0, Now: time.Now})

	// Process the collection.
	did, err := w.processOne()
	if err != nil || !did {
		t.Fatalf("expected work done, did=%v err=%v", did, err)
	}
	chs, _ := s.ListChapters("omni")
	if len(chs) != 2 {
		t.Fatalf("omni should have 2 auto chapters, got %d", len(chs))
	}

	// Process the single issue: skipped (no OCR), marked processed.
	did, _ = w.processOne()
	if !did {
		t.Fatal("expected single issue processed")
	}
	for _, id := range d.detected {
		if id == "single" {
			t.Fatal("single issue must NOT be OCR'd")
		}
	}

	// Queue now empty.
	did, _ = w.processOne()
	if did {
		t.Fatal("queue should be drained")
	}
}

func TestWorkerYieldsWhenReaderActive(t *testing.T) {
	s := newStore(t)
	seed(t, s, "omni")
	d := &fakeDetector{pages: map[string]int{"omni": 300}, active: true,
		detect: map[string][]DetectedChapter{"omni": {{Page: 0, Title: "A"}}}}
	w := NewWorker(s, d, Options{MinPages: 60, Now: time.Now})
	did, _ := w.processOne()
	if did {
		t.Fatal("should yield (do nothing) while reader is active")
	}
	if len(d.detected) != 0 {
		t.Fatal("must not OCR while reader active")
	}
}
