// Package rename resolves filename templates from metadata fields.
package rename

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var placeholderRe = regexp.MustCompile(`\{([a-z_]+)(?::0(\d+))?\}`)

// Resolve substitutes {key} and {key:0N} placeholders from fields.
// A missing or empty field removes the placeholder together with the
// literal characters immediately preceding it (the segment's "lead"),
// so "{series} ({year})" without a year yields "Series" rather than
// "Series ()". Orphaned closing brackets left in the static tail by a
// collapsed segment are also removed, and whitespace is normalised.
func Resolve(tpl string, fields map[string]string) string {
	type segment struct {
		lead string // literal text before this placeholder
		key  string
		pad  int
	}

	var segs []segment
	last := 0
	for _, m := range placeholderRe.FindAllStringSubmatchIndex(tpl, -1) {
		seg := segment{
			lead: tpl[last:m[0]],
			key:  tpl[m[2]:m[3]],
		}
		if m[4] >= 0 {
			seg.pad, _ = strconv.Atoi(tpl[m[4]:m[5]])
		}
		segs = append(segs, seg)
		last = m[1]
	}
	tail := tpl[last:]

	// Track which opening brackets we actually emitted so we can detect
	// whether closing brackets in the tail are orphaned.
	openCount := 0  // net unmatched '(' emitted so far
	squareCount := 0

	var b strings.Builder
	for _, seg := range segs {
		val := fields[seg.key]
		if val == "" {
			// Collapsed: drop this segment's lead entirely.
			// Any opening brackets in the lead that we would have emitted
			// are not emitted, so no adjustment needed.
			continue
		}
		// Apply zero-padding if requested and value is a plain integer.
		if seg.pad > 0 {
			if n, err := strconv.Atoi(val); err == nil {
				val = fmt.Sprintf("%0*d", seg.pad, n)
			}
		}
		// Count bracket balance for the lead we're about to emit.
		for _, ch := range seg.lead {
			switch ch {
			case '(':
				openCount++
			case ')':
				if openCount > 0 {
					openCount--
				}
			case '[':
				squareCount++
			case ']':
				if squareCount > 0 {
					squareCount--
				}
			}
		}
		b.WriteString(seg.lead)
		b.WriteString(val)
	}

	// Append the static tail, skipping orphaned closing brackets
	// (those that have no matching open bracket in what we emitted).
	var tailBuilder strings.Builder
	for _, ch := range tail {
		switch ch {
		case ')':
			if openCount > 0 {
				openCount--
				tailBuilder.WriteRune(ch)
			}
			// else: orphaned closing paren — drop it
		case ']':
			if squareCount > 0 {
				squareCount--
				tailBuilder.WriteRune(ch)
			}
			// else: orphaned closing bracket — drop it
		default:
			tailBuilder.WriteRune(ch)
		}
	}

	out := b.String() + tailBuilder.String()

	// Normalise whitespace and trim stray leading/trailing punctuation.
	out = strings.Join(strings.Fields(out), " ")
	out = strings.TrimRight(out, "-# ")
	out = strings.TrimSpace(out)
	return out
}

// SanitizeFilename makes a template result safe as a single path element.
func SanitizeFilename(name string) (string, error) {
	// Path separators become dashes, not nothing: team-up volumes are named
	// "Batman/Green Arrow/The Question" upstream, and deleting the slashes
	// produces the unreadable "BatmanGreen ArrowThe Question".
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\':
			return '-'
		case 0:
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty filename after sanitization")
	}
	if strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("filename may not start with a dot")
	}
	if len(name) > 200 {
		cut := 200
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = strings.TrimSpace(name[:cut])
	}
	return name, nil
}
