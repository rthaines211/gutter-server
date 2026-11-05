package rename

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name, tpl string
		fields    map[string]string
		want      string
	}{
		{"basic", "{series} #{number} ({year})",
			map[string]string{"series": "Saga", "number": "7", "year": "2013"},
			"Saga #7 (2013)"},
		{"zero pad", "{series} #{number:03}",
			map[string]string{"series": "Saga", "number": "7"},
			"Saga #007"},
		{"pad non-numeric passes through", "{number:03}",
			map[string]string{"number": "7.5"},
			"7.5"},
		{"missing year collapses parens", "{series} #{number:03} ({year})",
			map[string]string{"series": "Saga", "number": "7", "year": ""},
			"Saga #007"},
		{"missing number collapses hash", "{series} #{number} ({year})",
			map[string]string{"series": "Saga", "number": "", "year": "2013"},
			"Saga (2013)"},
		{"vol template", "{series} Vol. {number} ({year})",
			map[string]string{"series": "Monstress", "number": "2", "year": "2016"},
			"Monstress Vol. 2 (2016)"},
		{"unknown key collapses", "{series} {nope}",
			map[string]string{"series": "Saga"},
			"Saga"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Resolve(c.tpl, c.fields); got != c.want {
				t.Errorf("Resolve(%q) = %q, want %q", c.tpl, got, c.want)
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	if _, err := SanitizeFilename("   "); err == nil {
		t.Error("blank must error")
	}
	if _, err := SanitizeFilename(".hidden"); err == nil {
		t.Error("leading dot must error")
	}
	got, err := SanitizeFilename("A/B\\C")
	if err != nil || got != "A-B-C" {
		t.Errorf("got %q err %v", got, err)
	}
	// Team-up volume names keep their structure readable.
	got, err = SanitizeFilename("Batman/Green Arrow/The Question: Arcadia")
	if err != nil || got != "Batman-Green Arrow-The Question: Arcadia" {
		t.Errorf("got %q err %v", got, err)
	}

	// Test UTF-8 truncation: 100 x "é" (2 bytes each = 200 bytes) + extra
	got, err = SanitizeFilename(strings.Repeat("é", 100) + "extra")
	if err != nil {
		t.Errorf("UTF-8 repeat truncation failed: %v", err)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncated string is not valid UTF-8: %q", got)
	}
	if len(got) > 200 {
		t.Errorf("truncated string exceeds 200 bytes: got %d", len(got))
	}

	// Test multi-byte char straddling boundary: 199 x "a" + "é" + "tail"
	got, err = SanitizeFilename(strings.Repeat("a", 199) + "é" + "tail")
	if err != nil {
		t.Errorf("boundary straddle truncation failed: %v", err)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncated string is not valid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("truncated string contains replacement character: %q", got)
	}
}
