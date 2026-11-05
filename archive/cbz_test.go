package archive

import (
	"io"
	"testing"
)

func TestCBZOpen(t *testing.T) {
	r, err := OpenCBZ("testdata/test.cbz")
	if err != nil {
		t.Fatalf("OpenCBZ failed: %v", err)
	}
	defer r.Close()
	if r.PageCount() != 3 {
		t.Errorf("expected 3 pages, got %d", r.PageCount())
	}
}

func TestCBZPageAt(t *testing.T) {
	r, err := OpenCBZ("testdata/test.cbz")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rc, contentType, err := r.PageAt(0)
	if err != nil {
		t.Fatalf("PageAt(0) failed: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if len(data) == 0 {
		t.Error("page data is empty")
	}
	if contentType != "image/png" {
		t.Errorf("expected image/png, got %s", contentType)
	}
}

func TestCBZPageAtOutOfRange(t *testing.T) {
	r, err := OpenCBZ("testdata/test.cbz")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, _, err = r.PageAt(99)
	if err == nil {
		t.Error("expected error for out-of-range page")
	}
}

func TestCBZComicInfo(t *testing.T) {
	r, err := OpenCBZ("testdata/test.cbz")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	xml := r.ComicInfoXML()
	if xml == nil {
		t.Fatal("expected ComicInfo.xml, got nil")
	}
	if len(xml) == 0 {
		t.Error("ComicInfo.xml is empty")
	}
}
