package store

import "testing"

// SlugifyName must produce exactly what the scanner's slugify produced —
// series IDs derived from it must not change across this refactor.
func TestSlugifyName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Astonishing X-Men (2004)", "astonishing-x-men-2004"},
		{"  Saga  ", "saga"},
		{"Batman: Year One!", "batman-year-one"},
		{"A--B", "a-b"},
	}
	for _, c := range cases {
		if got := SlugifyName(c.in); got != c.want {
			t.Errorf("SlugifyName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
