// Package gcdtest builds a throwaway gcd.db with the subset of the Grand
// Comics Database schema the reader touches, for tests in match/gcd and match.
package gcdtest

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rthaines211/gutter-server/match/gcd"
)

// Schema is the minimal GCD schema. Column names and the country/language
// ids (225 = US, 25 = English) match the real dump.
const Schema = `
CREATE TABLE gcd_publisher (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE gcd_series (id INTEGER PRIMARY KEY, name TEXT NOT NULL, year_began INTEGER NOT NULL,
	publisher_id INTEGER, country_id INTEGER NOT NULL, language_id INTEGER NOT NULL,
	issue_count INTEGER NOT NULL DEFAULT 0, deleted INTEGER NOT NULL DEFAULT 0);
CREATE TABLE gcd_issue (id INTEGER PRIMARY KEY, series_id INTEGER NOT NULL, number TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '', key_date TEXT NOT NULL DEFAULT '', variant_of_id INTEGER,
	sort_code INTEGER NOT NULL DEFAULT 0, page_count REAL, deleted INTEGER NOT NULL DEFAULT 0);
CREATE TABLE gcd_story (id INTEGER PRIMARY KEY, issue_id INTEGER NOT NULL, title TEXT NOT NULL DEFAULT '',
	type_id INTEGER NOT NULL, sequence_number INTEGER NOT NULL DEFAULT 0,
	genre TEXT NOT NULL DEFAULT '', characters TEXT NOT NULL DEFAULT '', synopsis TEXT NOT NULL DEFAULT '',
	deleted INTEGER NOT NULL DEFAULT 0);
CREATE TABLE gcd_story_credit (id INTEGER PRIMARY KEY, story_id INTEGER NOT NULL, credit_type_id INTEGER NOT NULL,
	creator_id INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0);
CREATE TABLE gcd_credit_type (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE gcd_creator_name_detail (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
INSERT INTO gcd_credit_type(id, name) VALUES (1,'script'),(2,'pencils'),(3,'inks'),(4,'colors'),(5,'letters'),
	(6,'editing'),(7,'pencils and inks'),(8,'pencils, inks and colors');
`

// Open creates a temp gcd.db from Schema plus seed SQL and opens it through
// the real reader. The file is removed with the test's temp dir.
func Open(t *testing.T, seed string) *gcd.DB {
	t.Helper()
	path := t.TempDir() + "/gcd.db"
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(Schema + seed); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := gcd.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
