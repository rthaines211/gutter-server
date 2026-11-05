package localcv

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newFixture builds a small SQLite database using the real localcv.db schema.
func newFixture(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/localcv.db"
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schema := `
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

	seed := `
	INSERT INTO cv_publisher(id, name) VALUES (10, 'Image');
	INSERT INTO cv_volume(id, name, aliases, start_year, publisher_id, count_of_issues)
		VALUES (49019, 'Saga', '', '2012', 10, 66);
	INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url,
		description, person_credits, character_credits)
		VALUES (317966, 49019, 'Chapter One', '1', '2012-03-14',
		        'https://example.invalid/saga1.jpg', 'It begins.', '[]', '[]');
	INSERT INTO comic_files(id, filename, file_size, md5_hash, first_page_md5, cvid)
		VALUES (1, 'Saga 001 (2012) (Digital) (Empire).cbz', 12345,
		        'ffffffffffffffffffffffffffffffff',
		        'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 317966);
	INSERT INTO comic_covers(id, cvid, ct_phash, ct_ahash, cv_url)
		VALUES (1, 317966, '13951148552733864898', '18446463014472778764',
		        'https://example.invalid/saga1.jpg');
	`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	return path
}

// seedHashCollision adds a second, higher-id issue sharing the given
// first-page hash, reproducing the variant-cover collisions that make 491
// hashes in the real dump ambiguous.
func seedHashCollision(t *testing.T, path, md5hex string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		INSERT INTO cv_issue(id, volume_id, name, issue_number, cover_date, image_url,
			description, person_credits, character_credits)
			VALUES (999999, 49019, 'Chapter One (Variant)', '1', '2012-03-14',
			        'https://example.invalid/saga1var.jpg', 'Variant.', '[]', '[]');
		-- rowid 0 deliberately sorts BEFORE the id-1 row that holds the correct
		-- issue, so an unordered scan returns this higher cvid first. That is
		-- what makes the determinism test bite instead of passing by luck.
		INSERT INTO comic_files(id, filename, file_size, md5_hash, first_page_md5, cvid)
			VALUES (0, 'Saga 001 (2012) (Variant).cbz', 12345,
			        'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', ?, 999999);`,
		md5hex); err != nil {
		t.Fatal(err)
	}
}
