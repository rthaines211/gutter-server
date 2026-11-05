package store

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB. Intended for use in tests only.
func (s *Store) DB() *sql.DB {
	return s.db
}

// idChunkSize caps how many ids go into a single "IN (...)" clause, staying
// well under SQLite's bound-parameter limit (SQLITE_MAX_VARIABLE_NUMBER,
// commonly 999+) regardless of build.
const idChunkSize = 500

// chunkIDs splits ids into groups of at most idChunkSize, for batching
// "IN (...)" lookups that would otherwise be one query per id.
func chunkIDs(ids []string) [][]string {
	if len(ids) == 0 {
		return nil
	}
	var chunks [][]string
	for len(ids) > idChunkSize {
		chunks = append(chunks, ids[:idChunkSize])
		ids = ids[idChunkSize:]
	}
	return append(chunks, ids)
}

// placeholders builds a "?,?,?" string for n bound parameters.
func placeholders(n int) string {
	b := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}

func migrate(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS reading_progress (
		comic_id     TEXT PRIMARY KEY,
		current_page INTEGER NOT NULL,
		total_pages  INTEGER NOT NULL,
		updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS bookmarks (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		comic_id   TEXT NOT NULL,
		page_num   INTEGER NOT NULL,
		note       TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(comic_id, page_num)
	);

	CREATE TABLE IF NOT EXISTS reader_settings (
		device_id         TEXT PRIMARY KEY,
		reading_mode      TEXT NOT NULL DEFAULT 'page',
		reading_direction TEXT NOT NULL DEFAULT 'ltr',
		theme             TEXT NOT NULL DEFAULT 'black',
		tap_zones         INTEGER NOT NULL DEFAULT 1,
		panel_detection   INTEGER NOT NULL DEFAULT 0,
		sleep_timer_min   INTEGER NOT NULL DEFAULT 0,
		sleep_warning_sec INTEGER NOT NULL DEFAULT 60
	);

	CREATE TABLE IF NOT EXISTS series (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		folder_path TEXT NOT NULL UNIQUE,
		issue_count INTEGER NOT NULL DEFAULT 0,
		cover_path  TEXT,
		writer      TEXT,
		penciller   TEXT,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS collections (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		icon       TEXT NOT NULL DEFAULT 'bookmark',
		color      TEXT NOT NULL DEFAULT '#FFB74D',
		position   INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS collection_series (
		collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
		series_id     TEXT NOT NULL,
		position      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (collection_id, series_id)
	);

	CREATE TABLE IF NOT EXISTS queues (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		icon       TEXT NOT NULL DEFAULT 'bookmark',
		color      TEXT NOT NULL DEFAULT '#FFB74D',
		position   INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS queue_series (
		queue_id  TEXT NOT NULL REFERENCES queues(id) ON DELETE CASCADE,
		series_id TEXT NOT NULL,
		position  INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (queue_id, series_id)
	);

	CREATE TABLE IF NOT EXISTS story_arcs (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL,
		image_url      TEXT NOT NULL DEFAULT '',
		self_contained INTEGER,
		cv_arc_id      TEXT NOT NULL DEFAULT '',
		total_issues   INTEGER NOT NULL DEFAULT 0,
		updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS story_arc_issues (
		arc_id   TEXT NOT NULL REFERENCES story_arcs(id) ON DELETE CASCADE,
		issue_id TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (arc_id, issue_id)
	);

	CREATE TABLE IF NOT EXISTS series_genres (
		series_id TEXT NOT NULL REFERENCES series(id) ON DELETE CASCADE,
		genre     TEXT NOT NULL,
		PRIMARY KEY (series_id, genre)
	);

	CREATE TABLE IF NOT EXISTS smart_filters (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		icon       TEXT NOT NULL DEFAULT 'filter_list',
		color      TEXT NOT NULL DEFAULT '#64B5F6',
		conditions TEXT NOT NULL DEFAULT '[]',
		position   INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS issues (
		id        TEXT PRIMARY KEY,
		series_id TEXT NOT NULL REFERENCES series(id) ON DELETE CASCADE,
		filename  TEXT NOT NULL,
		number    TEXT NOT NULL DEFAULT '',
		position  INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_issues_series ON issues(series_id);

	CREATE TABLE IF NOT EXISTS reading_sessions (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		comic_id    TEXT NOT NULL,
		series_id   TEXT,
		started_at  DATETIME NOT NULL,
		ended_at    DATETIME NOT NULL,
		pages_read  INTEGER NOT NULL DEFAULT 0,
		duration_s  INTEGER NOT NULL DEFAULT 0,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_sessions_started ON reading_sessions(started_at);
	CREATE INDEX IF NOT EXISTS idx_sessions_series ON reading_sessions(series_id);

	CREATE TABLE IF NOT EXISTS reader_presets (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id         TEXT NOT NULL,
		name              TEXT NOT NULL,
		builtin           INTEGER NOT NULL DEFAULT 0,
		reading_mode      TEXT NOT NULL DEFAULT 'page',
		reading_direction TEXT NOT NULL DEFAULT 'ltr',
		theme             TEXT NOT NULL DEFAULT 'black',
		page_fit          TEXT NOT NULL DEFAULT 'fitScreen',
		page_animation    TEXT NOT NULL DEFAULT 'slide',
		tap_zones         INTEGER NOT NULL DEFAULT 1,
		volume_keys       INTEGER NOT NULL DEFAULT 1,
		zoom_lock         INTEGER NOT NULL DEFAULT 0,
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS series_enrichment (
		series_id           TEXT PRIMARY KEY REFERENCES series(id) ON DELETE CASCADE,
		comicvine_volume_id INTEGER NOT NULL,
		description         TEXT NOT NULL DEFAULT '',
		publisher           TEXT NOT NULL DEFAULT '',
		start_year          INTEGER NOT NULL DEFAULT 0,
		creators            TEXT NOT NULL DEFAULT '[]',
		status              TEXT NOT NULL,
		enriched_at         INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS issue_enrichment (
		issue_id            TEXT PRIMARY KEY REFERENCES issues(id) ON DELETE CASCADE,
		comicvine_issue_id  INTEGER NOT NULL,
		title               TEXT NOT NULL DEFAULT '',
		summary             TEXT NOT NULL DEFAULT '',
		cover_date          TEXT NOT NULL DEFAULT '',
		enriched_at         INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS enrichment_state (
		series_id        TEXT PRIMARY KEY REFERENCES series(id) ON DELETE CASCADE,
		status           TEXT NOT NULL,
		candidates       TEXT NOT NULL DEFAULT '[]',
		chosen_volume_id INTEGER NOT NULL DEFAULT 0,
		attempts         INTEGER NOT NULL DEFAULT 0,
		last_attempt_at  INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_enrichment_state_status ON enrichment_state(status);

	CREATE TABLE IF NOT EXISTS chapters (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		comic_id   TEXT NOT NULL,
		page_num   INTEGER NOT NULL,
		title      TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(comic_id, page_num)
	);

	CREATE TABLE IF NOT EXISTS tagger_state (
		comic_id     TEXT PRIMARY KEY,
		status       TEXT NOT NULL,
		processed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS metadata_writeback (
		comic_id   TEXT PRIMARY KEY,
		status     TEXT NOT NULL DEFAULT 'pending',
		attempts   INTEGER NOT NULL DEFAULT 0,
		error      TEXT,
		updated_at INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS rename_templates (
		format   TEXT PRIMARY KEY,
		template TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS series_match (
		series_id    TEXT PRIMARY KEY REFERENCES series(id) ON DELETE CASCADE,
		source       TEXT NOT NULL DEFAULT 'comicvine',
		volume_id    TEXT NOT NULL DEFAULT '',
		state        TEXT NOT NULL,
		confidence   REAL NOT NULL DEFAULT 1.0,
		margin       REAL NOT NULL DEFAULT 0.0,
		method       TEXT NOT NULL DEFAULT '',
		candidates   TEXT NOT NULL DEFAULT '[]',
		penalties    TEXT NOT NULL DEFAULT '{}',
		dont_match   INTEGER NOT NULL DEFAULT 0,
		match_failed INTEGER NOT NULL DEFAULT 0,
		confirmed_at INTEGER NOT NULL DEFAULT 0,
		updated_at   INTEGER NOT NULL DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_series_match_state ON series_match(state);

	CREATE TABLE IF NOT EXISTS issue_match (
		issue_id    TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
		source      TEXT NOT NULL DEFAULT 'comicvine',
		upstream_id TEXT NOT NULL,
		state       TEXT NOT NULL,
		confidence  REAL NOT NULL DEFAULT 1.0,
		method      TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (issue_id, source, upstream_id)
	);

	-- Series a scan found new or changed, awaiting the auto pipeline. No FK:
	-- organize renames a series id mid-run, and a stale id is simply dropped.
	CREATE TABLE IF NOT EXISTS pipeline_pending (
		series_id TEXT PRIMARY KEY,
		added_at  INTEGER NOT NULL
	);
	-- APNs device tokens registered by the iPad/iPhone app (push notifications).
	CREATE TABLE IF NOT EXISTS device_tokens (
		token        TEXT PRIMARY KEY,
		env          TEXT NOT NULL,
		created_at   INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS pipeline_runs (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		started_at     INTEGER NOT NULL,
		finished_at    INTEGER NOT NULL DEFAULT 0,
		series_count   INTEGER NOT NULL DEFAULT 0,
		skipped_reason TEXT NOT NULL DEFAULT '',
		events         TEXT NOT NULL DEFAULT '[]'
	);
	-- Reader fixes to bubble-zoom reading order, keyed by the SHA-1 of the page's
	-- bytes (same key as the sidecar file), so they survive renames and rewrites.
	CREATE TABLE IF NOT EXISTS bubble_order (
		page_sha   TEXT PRIMARY KEY,
		labels     TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	);
	-- Pages the reader flagged as wrongly detected, for the panel labelling set.
	CREATE TABLE IF NOT EXISTS page_flags (
		comic_id   TEXT NOT NULL,
		page       INTEGER NOT NULL,
		flagged_at INTEGER NOT NULL,
		PRIMARY KEY (comic_id, page)
	);
	`
	_, err := db.Exec(schema)
	if err != nil {
		return err
	}
	if err := migrateAnnounced(db); err != nil {
		return err
	}

	// Add columns if they don't exist (idempotent).
	alters := []string{
		"ALTER TABLE series ADD COLUMN publisher TEXT",
		"ALTER TABLE series ADD COLUMN year INTEGER",
		"ALTER TABLE reader_settings ADD COLUMN page_animation TEXT NOT NULL DEFAULT 'slide'",
		"ALTER TABLE reader_settings ADD COLUMN zoom_lock INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE chapters ADD COLUMN source TEXT NOT NULL DEFAULT 'user'",
		"ALTER TABLE chapters ADD COLUMN confidence TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE chapters ADD COLUMN confirmed INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE issues ADD COLUMN format TEXT",
		"ALTER TABLE issues ADD COLUMN writer TEXT",
		"ALTER TABLE issues ADD COLUMN penciller TEXT",
		"ALTER TABLE reading_progress ADD COLUMN read_at INTEGER",
		"ALTER TABLE reader_settings ADD COLUMN tap_zone_layout TEXT",
		"ALTER TABLE reader_settings ADD COLUMN page_fit TEXT NOT NULL DEFAULT 'fitScreen'",
		"ALTER TABLE reader_settings ADD COLUMN keep_screen_on INTEGER NOT NULL DEFAULT 1",
		"ALTER TABLE reader_settings ADD COLUMN volume_keys INTEGER NOT NULL DEFAULT 1",
		"ALTER TABLE reader_settings ADD COLUMN zoom_mode TEXT NOT NULL DEFAULT 'off'",
		"ALTER TABLE reader_settings ADD COLUMN panel_dim INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE reader_settings ADD COLUMN sleep_warning_sec INTEGER NOT NULL DEFAULT 60",
		// SQLite forbids a CURRENT_TIMESTAMP default on ADD COLUMN, so add it
		// nullable and backfill below. New series get their true add-time from
		// the scanner; pre-existing rows have no recoverable add-date (the CBZ
		// conversion rewrote every file mtime), so stamp them with the epoch as
		// a "pre-tracking / unknown" sentinel — this keeps them out of the
		// recently-added window so they never falsely show as new.
		"ALTER TABLE series ADD COLUMN created_at DATETIME",
		"ALTER TABLE series_enrichment ADD COLUMN genre TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE series_enrichment ADD COLUMN gcd_series_id INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE series_enrichment ADD COLUMN secondary_source TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE issue_enrichment ADD COLUMN page_count REAL NOT NULL DEFAULT 0",
		"ALTER TABLE issue_enrichment ADD COLUMN characters TEXT NOT NULL DEFAULT '[]'",
		// JSON array of column names the user edited by hand; enrichment upserts keep them.
		"ALTER TABLE series_enrichment ADD COLUMN manual_fields TEXT NOT NULL DEFAULT '[]'",
		"ALTER TABLE issue_enrichment ADD COLUMN manual_fields TEXT NOT NULL DEFAULT '[]'",
		"ALTER TABLE series_match ADD COLUMN confirmed_by TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE series_enrichment ADD COLUMN volume_name TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE story_arcs ADD COLUMN source TEXT NOT NULL DEFAULT 'mylar'",
		"ALTER TABLE story_arcs ADD COLUMN publisher TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE story_arcs ADD COLUMN start_year INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE story_arcs ADD COLUMN end_year INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE story_arcs ADD COLUMN description TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE story_arcs ADD COLUMN description_fetched_at INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE story_arcs ADD COLUMN description_version INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE page_flags ADD COLUMN reason TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE page_flags ADD COLUMN zoom TEXT NOT NULL DEFAULT ''",
	}
	for _, stmt := range alters {
		db.Exec(stmt) // ignore "duplicate column" errors
	}
	db.Exec("UPDATE series SET created_at = '1970-01-01 00:00:00' WHERE created_at IS NULL")
	db.Exec("UPDATE series_match SET confirmed_by = 'human' WHERE state = 'confirmed' AND confirmed_by = ''")
	// Lift the old bubble_zoom flag into zoom_mode once. Idempotent: Upsert keeps
	// panel_detection equal to zoom_mode == 'bubbles', so a row later set to off
	// or panels has panel_detection 0 and is left alone.
	db.Exec("UPDATE reader_settings SET zoom_mode = 'bubbles' WHERE panel_detection = 1 AND zoom_mode = 'off'")

	if err := backfillPublishers(db); err != nil {
		return err
	}

	if err := backfillCollectionNumbers(db); err != nil {
		return err
	}

	if err := backfillAnnualFormats(db); err != nil {
		return err
	}

	// Seed default rename templates (idempotent).
	_, _ = db.Exec(`INSERT OR IGNORE INTO rename_templates(format, template) VALUES
		('single',  '{series} #{number:03} ({year})'),
		('annual',  '{series} Annual #{number:03} ({year})'),
		('one-shot','{series} #{number:03} ({year})'),
		('tpb',     '{series} Vol. {number} - {title} ({year})'),
		('hc',      '{series} Vol. {number} - {title} ({year})'),
		('omnibus', '{series} Omnibus Vol. {number} - {title} ({year})'),
		('default', '{series} #{number:03}')`)

	return nil
}

// migrateAnnounced creates announced_issues — issue ids a "New issues ready"
// push already covered — seeded with every issue already in the library, so
// the upgrade itself announces nothing. Create and seed share one
// transaction: an empty table left by a crash in between would announce the
// whole library. No FK: ids migrate with comicIDTables; rows of deleted
// issues linger harmlessly so a file that reappears is not news again.
func migrateAnnounced(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='announced_issues'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE announced_issues (comic_id TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO announced_issues(comic_id) SELECT id FROM issues`); err != nil {
		return err
	}
	return tx.Commit()
}
