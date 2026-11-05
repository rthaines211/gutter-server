package match

import "strings"

// International houses that reprint US comics with identical digital pages.
// A first-page hash pointing into one of these volumes is a doppelgänger, not
// a match: the AXM case was a US #37 whose page hash the corpus attributed to
// Panini España's reprint #20. Hash hits on these publishers are ignored so
// pass 2 can bind the file by number into the volume the folder actually is.
//
// ponytail: curated by hand from the dump's publisher list; extend it when a
// new reprint house shows up in the ambiguous queue.
var reprintPrefixes = []string{
	"Panini",
	"Semic",
	"TM-Semic",
	"Egmont",
	"Glénat",
	"Glenat",
	"Planeta",
	"Grupo Planeta",
	"Ivrea",
	"Editorial Televisa",
	"Editorial Panini",
}

var reprintExact = map[string]bool{
	"Norma Editorial":      true,
	"Splitter":             true,
	"Delcourt":             true,
	"Urban Comics":         true,
	"Ediciones Zinco":      true,
	"Abril":                true,
	"Edizioni Star Comics": true,
	"RW Edizioni":          true,
	"Ovni Press":           true,
	"Salvat":               true,
}

func isReprintPublisher(name string) bool {
	if reprintExact[name] {
		return true
	}
	for _, p := range reprintPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
