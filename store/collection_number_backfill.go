package store

import (
	"database/sql"

	"github.com/rthaines211/gutter-server/match/parse"
)

// backfillCollectionNumbers fills in the volume number for collected editions
// whose number column was never written, and the "annual" format for annuals
// that never had one.
//
// The scanner's issueNumberFromFilename strips "v01" as a volume marker and
// returns "" — correct for singles ("Batman v2 755" is issue 755), wrong for a
// trade, where the volume number IS the number. The match pipeline parses it
// correctly but only learned to persist it alongside the format later, and
// UpsertIssues writes number on INSERT only, so an already-blank row can never
// heal itself on rescan. The organizer skips any issue with an empty number, so
// these files stay unorganizable forever without this repair.
//
// Idempotent and blank-only, like backfillPublishers, so it is safe on every
// startup instead of carrying a one-shot migration marker: it only considers
// rows that are still blank, and only writes when the parse yields a collection
// number. Once the library is repaired this scans a few hundred rows and writes
// nothing.
func backfillCollectionNumbers(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, filename FROM issues WHERE COALESCE(number, '') = ''`)
	if err != nil {
		return err
	}
	type fix struct{ id, number string }
	var fixes []fix
	for rows.Next() {
		var id, filename string
		if err := rows.Scan(&id, &filename); err != nil {
			rows.Close()
			return err
		}
		// Only collections: a single whose number the scanner could not resolve
		// is genuinely ambiguous, and guessing one would rename it wrongly.
		if f := parse.ParseFile(filename); f.IsCollection && f.Number != "" {
			fixes = append(fixes, fix{id, f.Number})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, f := range fixes {
		if _, err := db.Exec(
			`UPDATE issues SET number = ? WHERE id = ? AND COALESCE(number, '') = ''`,
			f.number, f.id); err != nil {
			return err
		}
	}
	return nil
}

// backfillAnnualFormats records the "annual" format on annuals that predate
// annual detection. Without it an annual resolves through the single template
// to the same "Series #13" as the ordinary issue 13 and the organizer flags
// both as a duplicate target, renaming neither.
//
// Blank-only and idempotent, for the same reason as the number backfill above.
func backfillAnnualFormats(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, filename FROM issues WHERE COALESCE(format, '') = ''`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id, filename string
		if err := rows.Scan(&id, &filename); err != nil {
			rows.Close()
			return err
		}
		if f := parse.ParseFile(filename); f.Format == "annual" {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := db.Exec(
			`UPDATE issues SET format = 'annual' WHERE id = ? AND COALESCE(format, '') = ''`, id); err != nil {
			return err
		}
	}
	return nil
}
