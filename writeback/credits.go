package writeback

import (
	"strings"

	"github.com/rthaines211/gutter-server/metadata"
	"github.com/rthaines211/gutter-server/store"
)

// roleField maps a normalized ComicVine role to a pointer-selector over the
// matching ComicInfo credit field. Roles absent from this table are dropped.
var roleField = map[string]func(*metadata.ComicInfo) *string{
	"writer":      func(ci *metadata.ComicInfo) *string { return &ci.Writer },
	"penciler":    func(ci *metadata.ComicInfo) *string { return &ci.Penciller },
	"penciller":   func(ci *metadata.ComicInfo) *string { return &ci.Penciller },
	"artist":      func(ci *metadata.ComicInfo) *string { return &ci.Penciller },
	"inker":       func(ci *metadata.ComicInfo) *string { return &ci.Inker },
	"colorist":    func(ci *metadata.ComicInfo) *string { return &ci.Colorist },
	"colourist":   func(ci *metadata.ComicInfo) *string { return &ci.Colorist },
	"letterer":    func(ci *metadata.ComicInfo) *string { return &ci.Letterer },
	"cover":       func(ci *metadata.ComicInfo) *string { return &ci.CoverArtist },
	"coverartist": func(ci *metadata.ComicInfo) *string { return &ci.CoverArtist },
	"editor":      func(ci *metadata.ComicInfo) *string { return &ci.Editor },
}

// applyCredits populates ci's credit fields from a typed roster. Multiple
// people in one role are comma-joined in input order. Unknown roles are
// dropped rather than stuffed into a generic field. An empty roster leaves
// ci untouched (so it never clobbers pre-existing values).
func applyCredits(ci *metadata.ComicInfo, creators []store.Creator) {
	byField := map[*string][]string{}
	order := []*string{}
	for _, c := range creators {
		if c.Name == "" {
			continue
		}
		sel, ok := roleField[strings.ToLower(strings.TrimSpace(c.Role))]
		if !ok {
			continue
		}
		field := sel(ci)
		if _, seen := byField[field]; !seen {
			order = append(order, field)
		}
		byField[field] = append(byField[field], c.Name)
	}
	for _, field := range order {
		*field = strings.Join(byField[field], ", ")
	}
}
