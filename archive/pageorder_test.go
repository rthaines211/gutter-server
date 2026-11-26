package archive

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestPageOrderMapIdentityNil(t *testing.T) {
	if m := PageOrderMap([]string{"001.jpg", "002.jpg"}); m != nil {
		t.Fatalf("unchanged order must give nil, got %v", m)
	}
}

func TestPageOrderMapUnpadded(t *testing.T) {
	got := PageOrderMap([]string{"page1.jpg", "page10.jpg", "page2.jpg"})
	if want := []int{0, 2, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PageOrderMap = %v, want %v", got, want)
	}
}

func TestPageOrderMapDuplicates(t *testing.T) {
	got := PageOrderMap([]string{"b/1.jpg", "a/10.jpg", "a/2.jpg", "a/2.jpg"})
	s := append([]int(nil), got...)
	sort.Ints(s)
	if !reflect.DeepEqual(s, []int{0, 1, 2, 3}) {
		t.Fatalf("map must be a permutation of 0..3, got %v", got)
	}
}

func TestImageEntryNamesCBZ(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.cbz")
	writeZip(t, p, "page2.jpg", "ComicInfo.xml", "page10.jpg")
	got, err := ImageEntryNames(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"page2.jpg", "page10.jpg"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ImageEntryNames = %v, want %v", got, want)
	}
}
