package tagger

import (
	"path"
	"strconv"
	"strings"
)

// IsLikelyCollection decides — from cheap archive metadata only — whether a
// comic is a multi-issue collection worth OCR'ing for chapters.
func IsLikelyCollection(pageCount int, pageNames []string, minPages int) bool {
	if pageCount <= 0 {
		return false
	}
	if pageCount >= minPages {
		return true
	}
	prefixes := map[string]struct{}{}
	for _, n := range pageNames {
		if dir := path.Dir(n); dir != "." && dir != "/" {
			prefixes[dir] = struct{}{}
		}
	}
	return len(prefixes) >= 2
}

// ParseQuietHours parses "START-END" (24h). Empty or malformed → ok=false.
func ParseQuietHours(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || start < 0 || start > 23 || end < 0 || end > 23 {
		return 0, 0, false
	}
	return start, end, true
}

// InQuietWindow reports whether hour is inside [start,end), handling wrap.
func InQuietWindow(hour, start, end int) bool {
	if start <= end {
		return hour >= start && hour < end
	}
	return hour >= start || hour < end // wraps past midnight
}
