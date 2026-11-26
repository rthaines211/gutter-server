package archive

// NaturalLess compares two strings with embedded digit runs treated as
// numbers (case-insensitive), so "2.cbz" sorts before "10.cbz".
func NaturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ad := isDigit(a[i])
		bd := isDigit(b[j])
		if ad && bd {
			ai, an := i, 0
			for i < len(a) && isDigit(a[i]) {
				i++
				an++
			}
			bj, bn := j, 0
			for j < len(b) && isDigit(b[j]) {
				j++
				bn++
			}
			as := trimLeadingZeros(a[ai : ai+an])
			bs := trimLeadingZeros(b[bj : bj+bn])
			if len(as) != len(bs) {
				return len(as) < len(bs)
			}
			if as != bs {
				return as < bs
			}
			// Numbers equal; padded variant (more leading zeros) sorts first.
			if an != bn {
				return an > bn
			}
			continue
		}
		ac := toLower(a[i])
		bc := toLower(b[j])
		if ac != bc {
			return ac < bc
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func toLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

func trimLeadingZeros(s string) string {
	k := 0
	for k < len(s)-1 && s[k] == '0' {
		k++
	}
	return s[k:]
}

// PageLess is the page order: NaturalLess, with byte order breaking ties
// (names equal ignoring case) so the order is total and stable.
func PageLess(a, b string) bool {
	if NaturalLess(a, b) {
		return true
	}
	return !NaturalLess(b, a) && a < b
}
