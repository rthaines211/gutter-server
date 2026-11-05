package localcv

import (
	"database/sql"
	"testing"
)

// TestIssueByIDPopulatesCharacterNames verifies the reader parses the raw
// character_credits JSON (`[{"id":…,"name":"…"}]`) into CharacterNames, so
// apply.go can read characters uniformly across the CV and GCD providers.
func TestIssueByIDPopulatesCharacterNames(t *testing.T) {
	path := newFixture(t)

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url,
			description, person_credits, character_credits)
			VALUES (317967, 49019, 'Chapter Two', '2', '2012-04-11',
			        'https://example.invalid/saga2.jpg', 'It continues.', '[]',
			        '[{"id":1,"name":"Hercules"},{"id":2,"name":"Venus"}]');`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	iss, err := d.IssueByID(317967)
	if err != nil {
		t.Fatal(err)
	}
	if len(iss.CharacterNames) != 2 || iss.CharacterNames[0] != "Hercules" {
		t.Fatalf("CharacterNames: got %q", iss.CharacterNames)
	}
}
