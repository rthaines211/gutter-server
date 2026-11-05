package metadata

import "testing"

func TestDetectTextDividers(t *testing.T) {
	ocr := map[int]string{
		10:  `SUPERIOR SPIDER-MAN #32 "EDGE OF SPIDER-VERSE"`,
		11:  "regular comic dialogue here, lots of words to exceed the sparse threshold and not match",
		87:  `AMAZING SPIDER-MAN #7 "MS. MARVEL TEAM-UP"`,
		348: `AMAZING SPIDER-MAN #10 "SPIDER-VERSE PART TWO: SUPERIOR FORCE"`,
		319: `Amazing Spider-Man #9-14 combined variants by Jeff Wamester`, // gallery caption (mixed case, "variant by")
	}
	got := DetectTextDividers(ocr, nil)
	pages := map[int]bool{}
	for _, c := range got {
		pages[c.Page] = true
		if c.Source != "ocr" { t.Errorf("source = %q", c.Source) }
	}
	for _, want := range []int{10, 87, 348} {
		if !pages[want] { t.Errorf("missing divider page %d", want) }
	}
	if pages[11] { t.Error("dialogue page 11 should not be a divider") }
	if pages[319] { t.Error("variant-gallery caption 319 should not be a divider") }
}
