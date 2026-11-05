package archive

import "testing"

func TestCBZPageNamesMatchOrder(t *testing.T) {
	// testdata/sample.cbz must contain image entries "002.jpg","001.jpg" (any order)
	r, err := OpenCBZ("testdata/sample.cbz")
	if err != nil { t.Skipf("no sample.cbz: %v", err) }
	defer r.Close()
	names := r.PageNames()
	if len(names) != r.PageCount() {
		t.Fatalf("PageNames len %d != PageCount %d", len(names), r.PageCount())
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("PageNames not sorted at %d: %q > %q", i, names[i-1], names[i])
		}
	}
}
