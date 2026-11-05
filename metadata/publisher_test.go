package metadata

import "testing"

func TestCanonicalPublisher(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// The variants actually observed splitting the live library.
		{"dc short form", "DC", "DC Comics"},
		{"dc canonical", "DC Comics", "DC Comics"},
		{"marvel legal name", "Marvel Entertainment, Inc.", "Marvel"},
		{"marvel canonical", "Marvel", "Marvel"},
		{"image short form", "Image", "Image Comics"},
		{"vault short form", "Vault", "Vault Comics"},
		{"boom casing", "Boom! Studios", "BOOM! Studios"},
		{"boom canonical", "BOOM! Studios", "BOOM! Studios"},

		// Whitespace and casing.
		{"leading/trailing space", "  DC Comics  ", "DC Comics"},
		{"lowercase input", "dc comics", "DC Comics"},
		{"uppercase input", "MARVEL", "Marvel"},

		// Pass-through: unknown publishers keep their original casing.
		{"unknown preserved", "Harry N. Abrams", "Harry N. Abrams"},
		{"unknown trimmed", "  Splitter  ", "Splitter"},

		// Empty stays empty so callers can distinguish "no publisher".
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanonicalPublisher(tc.in); got != tc.want {
				t.Errorf("CanonicalPublisher(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Every alias must map to a value that is itself stable, or grouping still
// splits: canon(canon(x)) must equal canon(x).
func TestCanonicalPublisherIsIdempotent(t *testing.T) {
	for variant := range publisherAliases {
		once := CanonicalPublisher(variant)
		twice := CanonicalPublisher(once)
		if once != twice {
			t.Errorf("not idempotent for %q: %q -> %q", variant, once, twice)
		}
	}
}
