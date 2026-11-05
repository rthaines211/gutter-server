package metadata

import "strings"

// publisherAliases maps a lowercased, trimmed publisher string to its
// canonical display name. ComicInfo.xml publisher tags are free text, so the
// same imprint arrives under several spellings ("DC" vs "DC Comics") and
// casings ("BOOM! Studios" vs "Boom! Studios"). Left unnormalized these split
// into separate library sections, facet chips and stats entries.
//
// Only the variants actually observed in a library need an entry; anything
// unlisted passes through trimmed but otherwise untouched.
var publisherAliases = map[string]string{
	"dc":                         "DC Comics",
	"dc comics":                  "DC Comics",
	"marvel":                     "Marvel",
	"marvel comics":              "Marvel",
	"marvel entertainment":       "Marvel",
	"marvel entertainment, inc":  "Marvel",
	"marvel entertainment, inc.": "Marvel",
	"image":                      "Image Comics",
	"image comics":               "Image Comics",
	"vault":                      "Vault Comics",
	"vault comics":               "Vault Comics",
	"boom! studios":              "BOOM! Studios",
	"boom studios":               "BOOM! Studios",
	"idw":                        "IDW Publishing",
	"idw publishing":             "IDW Publishing",
	"dark horse":                 "Dark Horse Comics",
	"dark horse comics":          "Dark Horse Comics",
	"dynamite":                   "Dynamite Entertainment",
	"dynamite entertainment":     "Dynamite Entertainment",
}

// CanonicalPublisher normalizes a raw publisher string to a single canonical
// spelling. Unknown publishers are returned trimmed, preserving their original
// casing — this collapses known variants without mangling imprints we have no
// opinion about.
func CanonicalPublisher(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if canon, ok := publisherAliases[strings.ToLower(trimmed)]; ok {
		return canon
	}
	return trimmed
}
