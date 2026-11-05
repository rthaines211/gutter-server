// Package enrichment matches library series to ComicVine volumes and runs
// the background worker that applies enrichment.
package enrichment

import (
	"regexp"
	"sort"
	"strings"

	"github.com/rthaines211/gutter-server/comicvine"
)

var (
	parenRe = regexp.MustCompile(`\([^)]*\)`)
	volRe   = regexp.MustCompile(`\b(?:v|vol\.?|volume)\s*\d+\b`)
	punctRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// NormalizeName lowercases, drops parenthesized groups ("(2016)") and
// volume suffixes ("Vol. 2", "v3"), and collapses punctuation/whitespace.
func NormalizeName(s string) string {
	s = strings.ToLower(s)
	s = parenRe.ReplaceAllString(s, " ")
	s = volRe.ReplaceAllString(s, " ")
	s = punctRe.ReplaceAllString(s, " ")
	return spaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
}

// NormalizeIssueNumber strips whitespace and leading zeros so "004"
// matches ComicVine's "4". Decimal and unicode numbers pass through.
// If stripping leading zeros leaves a remainder that starts with neither
// a digit nor a dot (e.g. "0a" → "a"), the input is alphanumeric in an
// unexpected way; return the original whitespace-trimmed string unchanged.
func NormalizeIssueNumber(s string) string {
	s = strings.TrimSpace(s)
	trimmed := strings.TrimLeft(s, "0")
	if trimmed == "" {
		if s == "" {
			return ""
		}
		return "0"
	}
	if strings.HasPrefix(trimmed, ".") {
		return "0" + trimmed
	}
	// Guard: if the first rune after zero-stripping is not a digit or dot,
	// the value is an alphanumeric label (e.g. "0a"). Return the trimmed
	// original so "0a" stays "0a" rather than becoming "a".
	r := []rune(trimmed)[0]
	if (r < '0' || r > '9') && r != '.' {
		return s
	}
	return trimmed
}

type MatchDecision struct {
	Auto       bool
	Volume     comicvine.Volume   // valid when Auto
	Candidates []comicvine.Volume // top 5 by score when !Auto; empty = unmatched
}

const (
	scoreExactName  = 100
	scorePrefixName = 40
	scoreWithinName = 20
	scoreYearAgree  = 50
	scoreCountAgree = 25
	scoreCountBad   = -50

	// Auto requires exact name + count agreement (175 with year, 125
	// without when the scanned year is unknown) and a clear margin.
	autoThresholdWithYear = scoreExactName + scoreYearAgree + scoreCountAgree
	autoThresholdNoYear   = scoreExactName + scoreCountAgree
	autoMargin            = 50
)

func score(name string, year, issueCount int, v comicvine.Volume) int {
	s := 0
	n := NormalizeName(name)
	vn := NormalizeName(v.Name)
	switch {
	case n == "" || vn == "":
		// no name signal — award nothing (empty normalized name matches everything
		// via HasPrefix and would produce spurious auto-matches)
	case vn == n:
		s += scoreExactName
	case strings.HasPrefix(vn, n) || strings.HasPrefix(n, vn):
		s += scorePrefixName
	case strings.Contains(vn, n) || strings.Contains(n, vn):
		s += scoreWithinName
	}
	if year > 0 && v.StartYear > 0 {
		diff := year - v.StartYear
		if diff >= -1 && diff <= 1 {
			s += scoreYearAgree
		}
	}
	if v.CountOfIssues >= issueCount {
		s += scoreCountAgree
	} else {
		s += scoreCountBad
	}
	return s
}

// Match scores search results against the scanned series. Auto-apply only
// when the top candidate hits the full-agreement threshold AND clearly
// beats the runner-up; otherwise return the top 5 as review candidates.
func Match(name string, year, issueCount int, results []comicvine.Volume) MatchDecision {
	if len(results) == 0 {
		return MatchDecision{}
	}
	type scored struct {
		v comicvine.Volume
		s int
	}
	list := make([]scored, 0, len(results))
	for _, v := range results {
		list = append(list, scored{v: v, s: score(name, year, issueCount, v)})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].s > list[j].s })

	// When the name normalizes to empty (e.g. "Vol 1", "(2016)") every volume
	// receives zero name signal, so a score threshold cannot be trusted.
	// Skip auto-apply entirely and fall through to the review candidates path.
	if NormalizeName(name) == "" {
		goto review
	}

	{
		threshold := autoThresholdWithYear
		if year == 0 {
			threshold = autoThresholdNoYear
		}
		margin := autoMargin + 1 // satisfied when there is no runner-up
		if len(list) > 1 {
			margin = list[0].s - list[1].s
		}
		// Year-less auto requires at least two results so the margin test is
		// meaningful — a single result trivially "wins" with no real comparison.
		if year == 0 && len(list) < 2 {
			goto review
		}
		if list[0].s >= threshold && margin >= autoMargin {
			return MatchDecision{Auto: true, Volume: list[0].v}
		}
	}

review:

	n := len(list)
	if n > 5 {
		n = 5
	}
	candidates := make([]comicvine.Volume, 0, n)
	for _, sc := range list[:n] {
		candidates = append(candidates, sc.v)
	}
	return MatchDecision{Candidates: candidates}
}
