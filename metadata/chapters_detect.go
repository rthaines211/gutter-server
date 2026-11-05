// metadata/chapters_detect.go
package metadata

import (
	"path"
	"regexp"
	"strings"
)

type Candidate struct {
	Page       int    `json:"page"`
	Title      string `json:"title"`
	Source     string `json:"source"`     // comicinfo|folder|filename|ocr
	Confidence string `json:"confidence"` // high|medium|low
}

var (
	imgExt      = regexp.MustCompile(`(?i)\.(jpg|jpeg|png|gif|webp)$`)
	titledName  = regexp.MustCompile(`^[Pp]?0*\d+\s*[-_]\s*\S`)
	titlePrefix = regexp.MustCompile(`^[Pp]?0*\d+\s*[-_]\s*`)
	junkTitle   = regexp.MustCompile(`^(?i)z\.?\s`)
)

// truncRunes caps s to at most n runes without splitting a multibyte rune.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// CleanChapterTitle strips a page-number prefix and extension, normalizes
// separators/whitespace, trims a dangling "(" and stray punctuation, caps at
// 80 chars, and returns "" for junk (caller substitutes "Chapter N").
func CleanChapterTitle(raw string) string {
	s := path.Base(raw)
	s = imgExt.ReplaceAllString(s, "")
	s = titlePrefix.ReplaceAllString(s, "")
	if junkTitle.MatchString(s) {
		return ""
	}
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, " (-–—:·")
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 80 {
		s = strings.TrimSpace(truncRunes(s, 80))
	}
	return s
}

// HarvestStructural derives chapter candidates from folder layout and titled
// filenames over the sorted page-name list. Folder signal takes precedence
// over filename. Each requires >=2 groups to emit. Returns nil if neither
// applies.
func HarvestStructural(pageNames []string) []Candidate {
	if c := folderCandidates(pageNames); len(c) >= 2 {
		return c
	}
	if c := filenameCandidates(pageNames); len(c) >= 2 {
		return c
	}
	return nil
}

func folderCandidates(pageNames []string) []Candidate {
	var out []Candidate
	prevDir := "\x00"
	for i, n := range pageNames {
		dir := path.Dir(n)
		if dir != prevDir {
			title := CleanChapterTitle(path.Base(dir))
			if title == "" {
				title = path.Base(dir)
			}
			out = append(out, Candidate{Page: i, Title: title, Source: "folder", Confidence: "high"})
			prevDir = dir
		}
	}
	// One directory (or none) is not a multi-issue collection.
	if len(out) < 2 {
		return nil
	}
	return out
}

func filenameCandidates(pageNames []string) []Candidate {
	var out []Candidate
	for i, n := range pageNames {
		base := path.Base(n)
		if !titledName.MatchString(base) {
			continue
		}
		title := CleanChapterTitle(base)
		if title == "" {
			title = "Chapter"
		}
		out = append(out, Candidate{Page: i, Title: title, Source: "filename", Confidence: "high"})
	}
	return out
}
