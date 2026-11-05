// Package parse extracts series names, years and issue numbers from comic
// folder and file names.
//
// Phase 1A uses this only to verify hash-based matches, not to search. It is
// deliberately conservative: when a name is ambiguous it returns less rather
// than guessing.
package parse

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	// A 4-digit year in parentheses, optionally a range: (2016), (2012-2024).
	yearRe = regexp.MustCompile(`\((19|20)\d{2}(\s*-\s*(19|20)\d{2})?\)`)
	// Trailing volume marker: "v2", "Vol. 3", "Volume 10".
	volRe = regexp.MustCompile(`(?i)\b(v|vol\.?|volume)\s*(\d+)\b`)
	// Legacy numbering: a volume marker immediately followed by a bare issue
	// number ("V2024 169", "V2 755"). The volume is noise; the issue number is
	// what matters.
	legacyRe = regexp.MustCompile(`(?i)\b(v|vol\.?|volume)\s*\d+\s+(\d+(?:\.\d+)?)\s*$`)
	// A four-digit V-number is a year ("V2024"), never a volume.
	yearVolRe = regexp.MustCompile(`(?i)\bv(19|20)\d{2}\b`)
	// Issue number: "001", "#8", "12.1". Anchored to the end of the stem so a
	// digit inside the series name ("Fantastic Four") is not mistaken for one.
	numRe = regexp.MustCompile(`(?:^|\s)#?(\d+(?:\.\d+)?[A-Za-z]*)\s*$`)
	// Collection markers. Word-bounded so "Annual" (which must stay part of
	// the series name) never matches.
	collRe = regexp.MustCompile(`(?i)\b(tpb|hc|hardcover|omnibus|deluxe(?: edition)?|absolute edition)\b`)
	// An annual is a numbered single, not a collection, so it sets Format
	// without IsCollection. It needs its own format because the annual template
	// is the only thing that keeps "Series Annual 13" from resolving to the
	// same "Series #13" as the ordinary issue 13 and colliding with it.
	annualRe = regexp.MustCompile(`(?i)\bannual\b`)
	// Scanner/scene tags: anything parenthesised or bracketed.
	tagRe = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)
	// Anything that is not a letter, digit, + or !.
	nonKeyRe = regexp.MustCompile(`[^\p{L}0-9+!]`)
	// Bare 4-digit year, used to pull the first year out of a matched range.
	bareYearRe = regexp.MustCompile(`(19|20)\d{2}`)
	// GCD number shapes: "1 (681)" carries the historical number in
	// parentheses; "Vol. 1" is a collected-edition volume number.
	gcdParenRe = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
	gcdVolRe   = regexp.MustCompile(`(?i)^vol\.?\s*(\d+)$`)
)

// Folder is the result of parsing a series folder name.
type Folder struct {
	Series string
	Year   int
}

// File is the result of parsing a comic file name.
type File struct {
	Series       string
	Number       string
	Year         int
	IsCollection bool
	// Format is "tpb", "hc" or "omnibus" when IsCollection; empty otherwise.
	Format string
}

// ParseFolder extracts the series name and start year from a folder name.
func ParseFolder(name string) Folder {
	var f Folder
	f.Year = extractYear(name)
	stem := yearRe.ReplaceAllString(name, "")
	// Issue ranges "(001-042)" and scanner tags are parenthesised or
	// bracketed; the year was already taken above, so nothing of value
	// remains inside. Same ordering as ParseFile.
	stem = tagRe.ReplaceAllString(stem, "")
	stem = yearVolRe.ReplaceAllString(stem, "")
	stem = volRe.ReplaceAllString(stem, "")
	f.Series = strings.TrimSpace(collapseSpaces(stem))
	return f
}

// ParseFile extracts series, issue number and year from a comic filename.
func ParseFile(name string) File {
	var f File
	stem := strings.TrimSuffix(name, filepath.Ext(name))

	f.Year = extractYear(stem)
	stem = yearRe.ReplaceAllString(stem, "")

	// Scanner/scene tags are always parenthesised or bracketed; drop them all.
	// Years were already taken above, so nothing of value remains inside.
	stem = tagRe.ReplaceAllString(stem, "")

	if m := collRe.FindStringSubmatch(stem); m != nil {
		f.IsCollection = true
		f.Format = collFormat(m[1])
		stem = collRe.ReplaceAllString(stem, "")
	}

	// Deliberately not stripped from the stem: the word is often part of the
	// series name ("Nightwing 2024 Annual"), and removing it would change what
	// the matcher searches for. Only the format is recorded.
	if f.Format == "" && annualRe.MatchString(stem) {
		f.Format = "annual"
	}

	// Legacy numbering ("V2024 169") before the volume-marker rule, so the
	// trailing issue number wins and the V-token is dropped. Go's regexp has
	// no lookahead, so a captured number that is itself a 4-digit year
	// ("V1 2018") is rejected here rather than in the pattern — that's not
	// legacy numbering, it's a bare trailing year, and falls through to the
	// yearVolRe/volRe path below like it always did.
	if m := legacyRe.FindStringSubmatch(stem); m != nil && !isFourDigitYear(m[2]) {
		f.Number = m[2]
		stem = legacyRe.ReplaceAllString(stem, "")
		f.Series = strings.TrimSpace(collapseSpaces(stem))
		return f
	}
	stem = yearVolRe.ReplaceAllString(stem, "")

	// A volume marker means a collected edition, and its digits are the volume
	// number, not an issue number.
	if m := volRe.FindStringSubmatch(stem); m != nil {
		f.IsCollection = true
		if f.Format == "" {
			f.Format = "tpb"
		}
		f.Number = m[2]
		stem = volRe.ReplaceAllString(stem, "")
		f.Series = strings.TrimSpace(collapseSpaces(stem))
		return f
	}

	stem = collapseSpaces(stem)
	if m := numRe.FindStringSubmatch(stem); m != nil {
		// "Devil's Reign TPB 2022" — an unparenthesised trailing year on a
		// collection is its year, not issue #2022. Singles keep the number:
		// legacy numbering runs past 1000 and never looks like a year.
		if f.IsCollection && isFourDigitYear(m[1]) {
			if f.Year == 0 {
				f.Year, _ = strconv.Atoi(m[1])
			}
		} else {
			f.Number = m[1]
		}
		stem = numRe.ReplaceAllString(stem, "")
	}

	f.Series = strings.TrimSpace(collapseSpaces(stem))
	return f
}

// collFormat maps a collRe word to the issues.format vocabulary organize and
// writeback already use.
func collFormat(word string) string {
	switch strings.ToLower(word) {
	case "omnibus":
		return "omnibus"
	case "hc", "hardcover", "absolute edition":
		return "hc"
	}
	if strings.HasPrefix(strings.ToLower(word), "deluxe") {
		return "hc"
	}
	return "tpb"
}

// isFourDigitYear reports whether s looks like a bare year (1900s/2000s),
// used to keep legacyRe from swallowing a trailing year as an issue number.
func isFourDigitYear(s string) bool {
	return len(s) == 4 && (strings.HasPrefix(s, "19") || strings.HasPrefix(s, "20"))
}

func extractYear(s string) int {
	m := yearRe.FindString(s)
	if m == "" {
		return 0
	}
	// Take the first year of a range: (2012-2024) -> 2012.
	digits := bareYearRe.FindString(m)
	y, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return y
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// CanonicalKey reduces a title to a comparison key: lowercase, diacritics
// folded, everything but letters, digits, + and ! removed, and a leading
// article dropped.
//
// "&" is deleted rather than expanded to "and" — every surveyed comic tagger
// does this, because expanding it breaks more titles than it fixes.
func CanonicalKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, article) {
			s = s[len(article):]
			break
		}
	}
	s = stripDiacritics(s)
	return nonKeyRe.ReplaceAllString(s, "")
}

// stripDiacritics maps the Latin-1 accented letters that appear in this
// library to their ASCII base. A full Unicode fold would need
// golang.org/x/text, which is not a dependency and is not worth adding for a
// library that is 98% US Big Three and has zero non-English titles.
func stripDiacritics(s string) string {
	var b strings.Builder
	for _, r := range s {
		if repl, ok := diacritics[r]; ok {
			b.WriteRune(repl)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var diacritics = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ä': 'a', 'ã': 'a', 'å': 'a',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ñ': 'n', 'ç': 'c',
}

// NormalizeNumber strips leading zeros so "001" and "1" compare equal.
// Zero-padding is inconsistent within single folders in this library.
func NormalizeNumber(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	trimmed := strings.TrimLeft(s, "0")
	if trimmed == "" || strings.HasPrefix(trimmed, ".") {
		return "0" + trimmed
	}
	return trimmed
}

// NormalizeGCDNumber maps the Grand Comics Database's issue-number shapes to
// the plain form the matcher compares: "1 (681)" -> "1", "[1]" -> "1",
// "Vol. 1" -> "1", "1 Ashcan" -> "1". "[nn]" and "nn" mean unnumbered and
// become "". Leading zeros are left for NormalizeNumber.
func NormalizeGCDNumber(s string) string {
	s = strings.TrimSpace(gcdParenRe.ReplaceAllString(strings.TrimSpace(s), ""))
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	if strings.EqualFold(s, "nn") {
		return ""
	}
	if m := gcdVolRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if i := strings.IndexByte(s, ' '); i > 0 {
		s = s[:i]
	}
	return s
}
