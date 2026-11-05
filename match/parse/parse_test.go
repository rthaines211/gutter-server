package parse

import "testing"

func TestParseFolder(t *testing.T) {
	cases := []struct {
		in     string
		series string
		year   int
	}{
		{"Batman (2016)", "Batman", 2016},
		{"Saga (2012-2024)", "Saga", 2012},
		{"Fantastic Four", "Fantastic Four", 0},
		{"The Punisher (1986)", "The Punisher", 1986},
		{"Batman v2", "Batman", 0},
		{"Icon (001-042)(1993-1997)", "Icon", 1993},
		{"Marvel Zombies 1 (01 - 05) (2006) (digital) (Minutemen-PhD)", "Marvel Zombies 1", 2006},
		{"Fall of the House of X V2024", "Fall of the House of X", 0},
	}
	for _, c := range cases {
		got := ParseFolder(c.in)
		if got.Series != c.series || got.Year != c.year {
			t.Errorf("ParseFolder(%q) = %+v, want series=%q year=%d",
				c.in, got, c.series, c.year)
		}
	}
}

func TestParseFile(t *testing.T) {
	cases := []struct {
		in         string
		series     string
		number     string
		year       int
		collection bool
		format     string
	}{
		{"Saga 001 (2012).cbz", "Saga", "001", 2012, false, ""},
		{"Wonder Woman 039.cbz", "Wonder Woman", "039", 0, false, ""},
		{"Absolute Batman #8.cbz", "Absolute Batman", "8", 0, false, ""},
		{"Astonishing X-Men 4 (2004).cbz", "Astonishing X-Men", "4", 2004, false, ""},
		{"Batman Beyond 022 (2018) (Digital) (Zone-Empire).cbz", "Batman Beyond", "022", 2018, false, ""},
		{"Saga v10 (2022).cbz", "Saga", "10", 2022, true, "tpb"},
		// An annual is a numbered single (collection=false) but needs its own
		// format, or it resolves to the same "Batman #3" as the ordinary issue 3.
		{"Batman Annual 3 (2019).cbz", "Batman Annual", "3", 2019, false, "annual"},
		{"Nightwing Annual 001 (2018).cbz", "Nightwing Annual", "001", 2018, false, "annual"},
		// tpb wins: a collected edition of annuals is still a collection.
		{"Batman Annuals TPB (2019).cbz", "Batman Annuals", "", 2019, true, "tpb"},
		{"Watchmen TPB (1987).cbz", "Watchmen", "", 1987, true, "tpb"},
		{"Daredevil 12.1 (2011).cbz", "Daredevil", "12.1", 2011, false, ""},
		// Legacy numbering: a volume marker followed by another number is
		// "volume N, issue M", not a collected edition.
		{"Avengers Twilight V2024 169 (2024).cbz", "Avengers Twilight", "169", 2024, false, ""},
		{"Invincible Iron Man V2 755 (2009).cbz", "Invincible Iron Man", "755", 2009, false, ""},
		{"Fantastic Four By Jonathan Hickman V1 515 (2010).cbz", "Fantastic Four By Jonathan Hickman", "515", 2010, false, ""},
		// A four-digit V-number is a year, never a volume.
		{"Fall of the House of X V2024.cbz", "Fall of the House of X", "", 0, false, ""},
		// A legacy-numbering issue number >= 1000 must not be mistaken for a
		// bare trailing year.
		{"Action Comics V1 1000 (2018).cbz", "Action Comics", "1000", 2018, false, ""},
		// Format words.
		{"Batman and Son Deluxe Edition (2014).cbz", "Batman and Son", "", 2014, true, "hc"},
		{"New X-Men Omnibus (2001).cbz", "New X-Men", "", 2001, true, "omnibus"},
		// A bare trailing year on a collection is the year, not issue #2022.
		{"Devil's Reign TPB 2022.cbz", "Devil's Reign", "", 2022, true, "tpb"},
		{"Kingdom Come HC (2019).cbz", "Kingdom Come", "", 2019, true, "hc"},
	}
	for _, c := range cases {
		got := ParseFile(c.in)
		if got.Series != c.series || got.Number != c.number ||
			got.Year != c.year || got.IsCollection != c.collection || got.Format != c.format {
			t.Errorf("ParseFile(%q) = %+v, want series=%q number=%q year=%d collection=%v format=%q",
				c.in, got, c.series, c.number, c.year, c.collection, c.format)
		}
	}
}

func TestCanonicalKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Batman", "batman"},
		{"The Punisher", "punisher"},
		{"Spider-Man: Blue", "spidermanblue"},
		{"Green Lantern & Green Arrow", "greenlanterngreenarrow"}, // & is deleted, not expanded
		{"X-Men!", "xmen!"},
		{"Café Racer", "caferacer"},
	}
	for _, c := range cases {
		if got := CanonicalKey(c.in); got != c.want {
			t.Errorf("CanonicalKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeNumber(t *testing.T) {
	cases := []struct{ in, want string }{
		{"001", "1"},
		{"1", "1"},
		{"012.1", "12.1"},
		{"0", "0"},
		{"", ""},
		{"8AU", "8AU"},
	}
	for _, c := range cases {
		if got := NormalizeNumber(c.in); got != c.want {
			t.Errorf("NormalizeNumber(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// GCD writes issue numbers its own way; every shape here is real (US/EN
// issues since 2025). The output feeds NormalizeNumber, so leading zeros
// are left alone.
func TestNormalizeGCDNumber(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1 (681)", "1"}, // legacy numbering: relaunch number, then historical
		{"5 (284)", "5"},
		{"[nn]", ""}, // unnumbered one-shot / collection
		{"nn", ""},
		{"[nn] (Premiere Edition)", ""},
		{"[1]", "1"}, // number inferred by the indexer
		{"Vol. 1", "1"},
		{"Vol. 2", "2"},
		{"1 Ashcan", "1"},
		{"001", "001"}, // untouched; NormalizeNumber strips zeros
		{"12.1", "12.1"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeGCDNumber(c.in); got != c.want {
			t.Errorf("NormalizeGCDNumber(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
