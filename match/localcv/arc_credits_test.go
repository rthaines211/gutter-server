package localcv

import (
	"database/sql"
	"reflect"
	"testing"
)

// seedArcCredits inserts three issues onto the fixture volume: one with a
// real arc credit, one with an empty array, and one with malformed JSON —
// covering the "yields nothing for that issue" cases alongside the real hit.
func seedArcCredits(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date,
			person_credits, character_credits, story_arc_credits)
			VALUES
			(400001, 49019, 'Has Arc', '2', '2012-04-14', '[]', '[]',
			 '[{"id":57081,"name":"\"Avengers\" Dark Reign"}]'),
			(400002, 49019, 'No Arc', '3', '2012-05-14', '[]', '[]', '[]'),
			(400003, 49019, 'Bad JSON', '4', '2012-06-14', '[]', '[]', 'not json');`); err != nil {
		t.Fatal(err)
	}
}

func TestArcCredits(t *testing.T) {
	path := newFixture(t)
	seedArcCredits(t, path)

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	got, err := d.ArcCredits([]int64{400001, 400002, 400003})
	if err != nil {
		t.Fatal(err)
	}
	want := []ArcCredit{{IssueID: 400001, ArcID: 57081, ArcName: `"Avengers" Dark Reign`}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
