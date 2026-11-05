package gcd_test

import (
	"testing"

	"github.com/ryanhaines/comic-reader/match/gcd"
	"github.com/ryanhaines/comic-reader/match/gcd/gcdtest"
)

const spideySeed = `
INSERT INTO gcd_publisher(id,name) VALUES (1,'Marvel');
INSERT INTO gcd_series(id,name,year_began,publisher_id,country_id,language_id,issue_count)
	VALUES (10,'Amazing Spider-Man',1963,1,225,25,3);
INSERT INTO gcd_issue(id,series_id,number,key_date,sort_code,page_count)
	VALUES (100,10,'1','1963-03-00',0,36);
-- primary comic story (type 19) + a backup ad (type 6) that must be ignored
INSERT INTO gcd_story(id,issue_id,title,type_id,sequence_number,genre,characters,synopsis)
	VALUES (1000,100,'Spidey',19,0,'superhero','Spider-Man [Peter Parker]; J. Jonah Jameson (cameo)','Peter gets bitten.');
INSERT INTO gcd_story(id,issue_id,title,type_id,sequence_number,genre,characters,synopsis)
	VALUES (1001,100,'Ad',6,1,'advertising','Buy Hostess','');
`

func TestParseCharacters(t *testing.T) {
	got := gcd.ParseCharacters("Spider-Man [Peter Parker]; J. Jonah Jameson (cameo) ; ; Aunt May")
	want := []string{"Spider-Man [Peter Parker]", "J. Jonah Jameson", "Aunt May"}
	if len(got) != len(want) {
		t.Fatalf("got %q want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

func TestIssueByIDReturnsMergedFields(t *testing.T) {
	db := gcdtest.Open(t, spideySeed)
	iss, _, err := db.IssueByID(100)
	if err != nil {
		t.Fatal(err)
	}
	if iss.Description != "Peter gets bitten." {
		t.Errorf("synopsis: got %q", iss.Description)
	}
	if iss.Genre != "superhero" {
		t.Errorf("genre: got %q", iss.Genre)
	}
	if iss.PageCount != 36 {
		t.Errorf("page_count: got %v", iss.PageCount)
	}
	if len(iss.CharacterNames) != 2 || iss.CharacterNames[0] != "Spider-Man [Peter Parker]" {
		t.Errorf("characters: got %q", iss.CharacterNames)
	}
}
