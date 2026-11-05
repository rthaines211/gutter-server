package metadata

import (
	"regexp"
	"sort"
	"strings"
)

// A divider line: an ALLCAPS series-ish title followed by an issue number.
var dividerLine = regexp.MustCompile(`[A-Z][A-Z0-9 .:&/'"-]{4,45}\s*[#*]\s*\d{1,4}`)

// "variant by" / "variants by" marks cover-gallery captions, not boundaries.
var galleryHint = regexp.MustCompile(`(?i)variants? by`)

func isDividerText(t string) bool {
	if galleryHint.MatchString(t) {
		return false
	}
	if len(strings.Fields(t)) > 22 { // sparse pages only (title cards)
		return false
	}
	head := t
	if len([]rune(head)) > 60 {
		head = truncRunes(head, 60)
	}
	return dividerLine.MatchString(strings.ToUpper(head))
}

// DetectTextDividers finds issue title-divider pages in OCR text. With an
// exemplar page, it computes the median inter-divider gap and drops candidates
// closer than half that gap to a neighbor (removes variant-gallery clusters).
func DetectTextDividers(ocrByPage map[int]string, exemplarPage *int) []Candidate {
	var pages []int
	for p := range ocrByPage {
		pages = append(pages, p)
	}
	sort.Ints(pages)

	var raw []Candidate
	for _, p := range pages {
		t := ocrByPage[p]
		if !isDividerText(t) {
			continue
		}
		conf := "medium"
		if exemplarPage != nil {
			conf = "high"
		}
		raw = append(raw, Candidate{
			Page: p, Title: cleanDividerTitle(t), Source: "ocr", Confidence: conf,
		})
	}
	if exemplarPage == nil || len(raw) < 3 {
		return raw
	}
	// Spacing filter: drop candidates whose gap to the previous kept candidate
	// is < half the median gap (gallery pages cluster 1-2 apart).
	gaps := make([]int, 0, len(raw)-1)
	for i := 1; i < len(raw); i++ {
		gaps = append(gaps, raw[i].Page-raw[i-1].Page)
	}
	sort.Ints(gaps)
	median := gaps[len(gaps)/2]
	minGap := median / 2
	out := []Candidate{raw[0]}
	for i := 1; i < len(raw); i++ {
		if raw[i].Page-out[len(out)-1].Page >= minGap {
			out = append(out, raw[i])
		}
	}
	return out
}

func cleanDividerTitle(t string) string {
	t = strings.Join(strings.Fields(t), " ")
	if len([]rune(t)) > 80 {
		t = strings.TrimSpace(truncRunes(t, 80))
	}
	return t
}
