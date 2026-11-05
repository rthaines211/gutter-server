package store

import (
	"database/sql"

	"github.com/rthaines211/gutter-server/metadata"
)

// backfillPublishers rewrites stored publisher values to their canonical
// spelling. Normalizing on write only fixes rows written after the change;
// rows scanned or enriched before it keep the variant that split them across
// two library sections and two facet chips.
//
// Idempotent, so it is safe to run on every startup: canonical values map to
// themselves, and the UPDATE only touches rows that actually differ. Once every
// row is canonical this scans a few hundred rows and writes nothing.
func backfillPublishers(db *sql.DB) error {
	for _, table := range []string{"series", "series_enrichment"} {
		if err := backfillPublisherTable(db, table); err != nil {
			return err
		}
	}
	return nil
}

func backfillPublisherTable(db *sql.DB, table string) error {
	// Read distinct values rather than rewriting every row: a library has a
	// handful of publishers across hundreds of series.
	rows, err := db.Query(
		`SELECT DISTINCT publisher FROM ` + table +
			` WHERE publisher IS NOT NULL AND TRIM(publisher) <> ''`)
	if err != nil {
		// The column is added by an idempotent ALTER that may not have run on a
		// fresh database yet; a missing column is not fatal.
		return nil
	}

	var raws []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		raws = append(raws, raw)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// Bumping updated_at matters: /sync sends only rows newer than the client's
	// watermark, so a silent rewrite would leave already-synced clients showing
	// the old split values until something else touched the row. Only the
	// series table carries updated_at.
	set := `SET publisher = ?`
	if table == "series" {
		set += `, updated_at = CURRENT_TIMESTAMP`
	}

	for _, raw := range raws {
		canon := metadata.CanonicalPublisher(raw)
		if canon == raw {
			continue
		}
		if _, err := db.Exec(
			`UPDATE `+table+` `+set+` WHERE publisher = ?`,
			canon, raw); err != nil {
			return err
		}
	}
	return nil
}
