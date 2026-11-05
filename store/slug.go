package store

import (
	"regexp"
	"strings"
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// SlugifyName derives a series ID from a folder name. Moved from the scanner
// so non-server packages (organize) produce identical IDs; do not change the
// algorithm — every series_id in existing databases came from it.
func SlugifyName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}
