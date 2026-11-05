// Package creators builds and serves a sidecar SQLite index of
// (person, volume, issue, role) credits, read out of the offline ComicVine
// dump (localcv.db) so creator-page queries don't have to scan person_credits
// JSON on every request.
package creators

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Index is a sidecar SQLite database (cv_credit, meta) built from localcv's
// cv_issue.person_credits. It is safe for concurrent use: Build runs once in
// a background goroutine while Ready/queries are called from request
// handlers.
type Index struct {
	db          *sql.DB
	localcvPath string
	ready       atomic.Bool
}

// OpenIndex opens (creating if needed) the sidecar database at sidecarPath
// and ATTACHes localcv at localcvPath read-only as "cv". The returned Index
// is not ready to query until Build has completed at least once.
func OpenIndex(sidecarPath, localcvPath string) (*Index, error) {
	// "file:" prefix enables URI filename handling for this connection, which
	// is what makes ATTACH's own "?mode=ro" below take effect (see the same
	// note in match/localcv/localcv.go).
	db, err := sql.Open("sqlite3", "file:"+sidecarPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	// ATTACH is connection-scoped, so this *sql.DB must never hand out more
	// than one physical connection, or a later query could land on a
	// connection that never ran the ATTACH.
	db.SetMaxOpenConns(1)

	escaped := strings.ReplaceAll(localcvPath, "'", "''")
	if _, err := db.Exec(fmt.Sprintf(`ATTACH DATABASE 'file:%s?mode=ro' AS cv`, escaped)); err != nil {
		db.Close()
		return nil, err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS cv_credit (
			person_id INTEGER NOT NULL,
			volume_id INTEGER NOT NULL,
			issue_id  INTEGER NOT NULL,
			role      TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
	`); err != nil {
		db.Close()
		return nil, err
	}

	return &Index{db: db, localcvPath: localcvPath}, nil
}

// Close closes the sidecar database (and detaches cv).
func (x *Index) Close() error { return x.db.Close() }

// Ready reports whether Build has completed at least once.
func (x *Index) Ready() bool { return x.ready.Load() }

// Build (re)builds cv_credit from localcv's cv_issue.person_credits.
// Idempotent: if localcv's size and mtime match what's recorded in meta, it
// just marks the index ready without touching the data. Safe to call from a
// background goroutine at startup.
func (x *Index) Build(ctx context.Context) error {
	info, err := os.Stat(x.localcvPath)
	if err != nil {
		return err
	}
	size := strconv.FormatInt(info.Size(), 10)
	mtime := strconv.FormatInt(info.ModTime().UnixNano(), 10)

	if x.sourceUnchanged(size, mtime) {
		x.ready.Store(true)
		return nil
	}

	if err := x.rebuild(ctx, size, mtime); err != nil {
		return err
	}
	x.ready.Store(true)
	return nil
}

func (x *Index) sourceUnchanged(size, mtime string) bool {
	var gotSize, gotMtime string
	if err := x.db.QueryRow(`SELECT value FROM meta WHERE key='source_size'`).Scan(&gotSize); err != nil {
		return false
	}
	if err := x.db.QueryRow(`SELECT value FROM meta WHERE key='source_mtime'`).Scan(&gotMtime); err != nil {
		return false
	}
	return gotSize == size && gotMtime == mtime
}

// personCredit mirrors one entry of ComicVine's person_credits JSON array,
// e.g. {"id":45932,"name":"Abe Simon","role":"penciler, inker"}.
type personCredit struct {
	ID   int64  `json:"id"`
	Role string `json:"role"`
}

func (x *Index) rebuild(ctx context.Context, size, mtime string) error {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once Commit succeeds

	// Drop indexes before the bulk insert and recreate them after: 1.1M
	// cv_issue rows is slow to insert against a maintained index.
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_cv_credit_person`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_cv_credit_volume`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cv_credit`); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO cv_credit(person_id, volume_id, issue_id, role) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	rows, err := tx.QueryContext(ctx, `SELECT id, volume_id, person_credits FROM cv.cv_issue`)
	if err != nil {
		return err
	}
	insertErr := func() error {
		defer rows.Close()
		for rows.Next() {
			var issueID, volumeID int64
			var raw sql.NullString
			if err := rows.Scan(&issueID, &volumeID, &raw); err != nil {
				return err
			}
			if !raw.Valid || raw.String == "" || raw.String == "[]" {
				continue
			}
			var credits []personCredit
			if err := json.Unmarshal([]byte(raw.String), &credits); err != nil {
				// Malformed person_credits in the real dump is data, not a
				// bug: skip the issue rather than failing the whole build.
				continue
			}
			for _, c := range credits {
				for _, role := range SplitRoles(c.Role) {
					if _, err := stmt.ExecContext(ctx, c.ID, volumeID, issueID, role); err != nil {
						return err
					}
				}
			}
		}
		return rows.Err()
	}()
	if insertErr != nil {
		return insertErr
	}

	if _, err := tx.ExecContext(ctx, `CREATE INDEX idx_cv_credit_person ON cv_credit(person_id)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX idx_cv_credit_volume ON cv_credit(volume_id)`); err != nil {
		return err
	}

	meta := map[string]string{
		"source_size":  size,
		"source_mtime": mtime,
		"built_at":     strconv.FormatInt(time.Now().UnixNano(), 10),
	}
	for k, v := range meta {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			k, v); err != nil {
			return err
		}
	}

	return tx.Commit()
}
