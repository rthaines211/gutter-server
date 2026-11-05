package creators

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newCVFixture builds an empty localcv.db using the real schema (copied from
// match/localcv/fixture_test.go:newFixture), with no seed rows: callers
// insert whatever cv_volume/cv_issue rows their test needs.
func newCVFixture(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/localcv.db"
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schema := `
	CREATE TABLE cv_person (
		id INTEGER PRIMARY KEY, name TEXT);
	CREATE TABLE cv_publisher (
		id INTEGER PRIMARY KEY, name TEXT, image_url TEXT, site_detail_url TEXT);
	CREATE TABLE cv_volume (
		id INTEGER PRIMARY KEY, name TEXT NOT NULL, aliases TEXT, start_year TEXT,
		publisher_id INTEGER, count_of_issues INTEGER, description TEXT,
		image_url TEXT, site_detail_url TEXT);
	CREATE TABLE cv_issue (
		id INTEGER PRIMARY KEY, volume_id INTEGER NOT NULL, name TEXT,
		issue_number TEXT, cover_date TEXT, store_date TEXT, description TEXT,
		image_url TEXT, site_detail_url TEXT, character_credits TEXT,
		person_credits TEXT, team_credits TEXT, location_credits TEXT,
		story_arc_credits TEXT, associated_images TEXT);
	CREATE TABLE comic_covers (
		id INTEGER PRIMARY KEY, cvid INTEGER, ct_phash TEXT, ct_ahash TEXT, cv_url TEXT);
	CREATE TABLE comic_files (
		id INTEGER PRIMARY KEY, filename TEXT, file_size INTEGER, md5_hash TEXT,
		first_page_md5 TEXT, tth_hash TEXT, ct_phash TEXT, ct_ahash TEXT, cvid INTEGER);
	CREATE INDEX idx_files_fp_md5 ON comic_files(first_page_md5);
	CREATE INDEX idx_files_md5 ON comic_files(md5_hash);
	CREATE INDEX idx_issue_number ON cv_issue(volume_id, issue_number);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return path
}

// openFixtureDB opens the fixture's localcv.db for ad-hoc seeding beyond
// what seedIssue covers (cv_volume details, cv_person, cv_publisher). The
// caller must Close it.
func openFixtureDB(t *testing.T, cvPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", cvPath)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// seedIssue inserts a minimal cv_volume (if missing) and a cv_issue row
// carrying the given person_credits JSON.
func seedIssue(t *testing.T, cvPath string, issueID, volumeID int64, personCredits string) {
	t.Helper()
	db, err := sql.Open("sqlite3", cvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO cv_volume(id, name) VALUES (?, 'Test Volume') ON CONFLICT(id) DO NOTHING`,
		volumeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO cv_issue(id, volume_id, person_credits) VALUES (?, ?, ?)`,
		issueID, volumeID, personCredits); err != nil {
		t.Fatal(err)
	}
}
