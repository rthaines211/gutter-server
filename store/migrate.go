package store

import (
	"database/sql"
	"path/filepath"
	"strings"
)

// IDPair holds an old and new path-derived ID for a single comic/issue.
type IDPair struct {
	Old, New string
	// Filename, when set, replaces issues.filename too (a case-only file
	// rename); empty keeps it.
	Filename string
}

// comicIDTables lists every table that keys rows by comic/issue ID (excluding issues itself).
var comicIDTables = []struct{ table, col string }{
	{"reading_progress", "comic_id"},
	{"bookmarks", "comic_id"},
	{"reading_sessions", "comic_id"},
	{"chapters", "comic_id"},
	{"tagger_state", "comic_id"},
	{"issue_enrichment", "issue_id"}, // FK to issues(id) — update issues.id first
	{"metadata_writeback", "comic_id"},
	{"story_arc_issues", "issue_id"},
	{"issue_match", "issue_id"},
	{"announced_issues", "comic_id"},
	{"page_flags", "comic_id"},
}

// migrateComicIDTx remaps all comic-keyed rows within an existing transaction.
// Caller must have already updated issues.id to newID before calling this.
func migrateComicIDTx(tx *sql.Tx, oldID, newID string) error {
	for _, m := range comicIDTables {
		// UPDATE OR REPLACE: if a row with newID already exists (including secondary UNIQUE constraints
		// like bookmarks' (comic_id,page_num)), the conflicting row is silently replaced. Callers must
		// ensure the new ID has no live data; renames guarantee this because the target path didn't exist.
		if _, err := tx.Exec(`UPDATE OR REPLACE `+m.table+` SET `+m.col+`=? WHERE `+m.col+`=?`,
			newID, oldID); err != nil {
			return err
		}
	}
	return nil
}

// MigrateComicIDFull remaps a comic's path-derived ID everywhere,
// including the issues row itself (id + filename). Runs in one transaction.
func (s *Store) MigrateComicIDFull(oldID, newID, newFilename string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Defer FK checks so we can reorder statements freely within the tx.
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys=ON`); err != nil {
		return err
	}

	// Update issues.id first so issue_enrichment FK (→ issues.id) stays valid.
	if _, err := tx.Exec(`UPDATE issues SET id=?, filename=? WHERE id=?`,
		newID, newFilename, oldID); err != nil {
		return err
	}
	if err := migrateComicIDTx(tx, oldID, newID); err != nil {
		return err
	}
	// Bump updated_at so delta-sync clients learn about the rename. /sync filters
	// by updated_at; without this the migrated progress row and its series keep
	// their old timestamps, so a client that had already synced the old ID never
	// fetches the new one (and keeps showing the stale, now-nonexistent file).
	if _, err := tx.Exec(`UPDATE reading_progress SET updated_at=CURRENT_TIMESTAMP WHERE comic_id=?`, newID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE series SET updated_at=CURRENT_TIMESTAMP
		WHERE id=(SELECT series_id FROM issues WHERE id=?)`, newID); err != nil {
		return err
	}
	return tx.Commit()
}

// MigrateSeriesFolder remaps a series ID (and all of its issues' IDs) after the
// series folder is renamed on disk. Caller computes the new IDs; this method only
// rewrites rows, in one transaction.
func (s *Store) MigrateSeriesFolder(oldSeriesID, newSeriesID, newFolderPath, newName string, issueIDs []IDPair) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Defer FK checks so we can reorder statements freely within the tx.
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys=ON`); err != nil {
		return err
	}

	// 1. Update series.id, name, folder_path, and cover_path first — child tables reference it.
	// cover_path is stored as "FolderName/filename.cbz" (relative). When the folder is renamed,
	// replace the leading folder-name segment so cover_path stays accurate.
	var oldCoverPath string
	_ = tx.QueryRow(`SELECT COALESCE(cover_path,'') FROM series WHERE id=?`, oldSeriesID).Scan(&oldCoverPath)
	newCoverPath := oldCoverPath
	if oldCoverPath != "" {
		// cover_path format: "OldFolderName/filename.cbz" — replace first path segment.
		oldFolderName := strings.SplitN(oldCoverPath, "/", 2)[0]
		newFolderName := filepath.Base(newFolderPath)
		if oldFolderName != "" && oldFolderName != newFolderName {
			rest := strings.TrimPrefix(oldCoverPath, oldFolderName+"/")
			newCoverPath = newFolderName + "/" + rest
		}
	}
	if _, err := tx.Exec(`UPDATE series SET id=?, name=?, folder_path=?, cover_path=? WHERE id=?`,
		newSeriesID, newName, newFolderPath, newCoverPath, oldSeriesID); err != nil {
		return err
	}

	// 2. Remap all series-keyed child tables.
	// UPDATE OR REPLACE: if a conflicting row with newSeriesID already exists, it is silently replaced.
	// Caller guarantees the target series ID has no live data (folder rename ensures this).
	for _, tbl := range []struct{ table, col string }{
		{"issues", "series_id"},
		{"series_genres", "series_id"},
		{"series_enrichment", "series_id"},
		{"enrichment_state", "series_id"},
		{"collection_series", "series_id"},
		{"queue_series", "series_id"},
		{"reading_sessions", "series_id"},
		{"series_match", "series_id"},
	} {
		if _, err := tx.Exec(`UPDATE OR REPLACE `+tbl.table+` SET `+tbl.col+`=? WHERE `+tbl.col+`=?`,
			newSeriesID, oldSeriesID); err != nil {
			return err
		}
	}

	// 3. Per-issue ID migration: update issues.id then all comic-keyed tables.
	for _, p := range issueIDs {
		if _, err := tx.Exec(`UPDATE issues SET id=?, filename=COALESCE(NULLIF(?, ''), filename) WHERE id=?`,
			p.New, p.Filename, p.Old); err != nil {
			return err
		}
		if err := migrateComicIDTx(tx, p.Old, p.New); err != nil {
			return err
		}
		// Bump so delta-sync clients pick up the new ID (see MigrateComicIDFull).
		if _, err := tx.Exec(`UPDATE reading_progress SET updated_at=CURRENT_TIMESTAMP WHERE comic_id=?`, p.New); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// MigrateComicToSeries moves one comic to another series after its file moved
// folders on disk: new path-derived ID, new series membership, counts and
// sync timestamps on both series. Filename is unchanged — a cross-folder move
// keeps the name; retitling is a separate rename.
func (s *Store) MigrateComicToSeries(oldID, newID, newSeriesID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`PRAGMA defer_foreign_keys=ON`); err != nil {
		return err
	}

	var oldSeriesID string
	if err := tx.QueryRow(`SELECT series_id FROM issues WHERE id=?`, oldID).
		Scan(&oldSeriesID); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE issues SET id=?, series_id=? WHERE id=?`,
		newID, newSeriesID, oldID); err != nil {
		return err
	}
	if err := migrateComicIDTx(tx, oldID, newID); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE series SET issue_count=issue_count-1,
		updated_at=CURRENT_TIMESTAMP WHERE id=?`, oldSeriesID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE series SET issue_count=issue_count+1,
		updated_at=CURRENT_TIMESTAMP WHERE id=?`, newSeriesID); err != nil {
		return err
	}
	// Same delta-sync rationale as MigrateComicIDFull: the client must refetch
	// the progress row under its new ID.
	if _, err := tx.Exec(`UPDATE reading_progress SET updated_at=CURRENT_TIMESTAMP
		WHERE comic_id=?`, newID); err != nil {
		return err
	}
	return tx.Commit()
}
