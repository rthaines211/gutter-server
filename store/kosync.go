package store

import (
	"database/sql"
	"errors"
	"sync"
	"time"
)

// KOReader sync (kosync) tables. They're created lazily on first use so the
// core migration stays untouched; both are disposable caches/mirrors.
//
//   - kosync_documents maps a library comic to KOReader's two document
//     digests (binary partial-MD5 and filename MD5), plus the file size and
//     mtime the binary digest was computed from.
//   - kosync_progress keeps the raw record KOReader last pushed per document
//     (its progress string, device, timestamp), so GET returns exactly what
//     KOReader sent. Gutter's own reading_progress stays the source of truth
//     for the app; kosync writes are mirrored into it.
const kosyncSchema = `
	CREATE TABLE IF NOT EXISTS kosync_documents (
		comic_id        TEXT PRIMARY KEY,
		binary_digest   TEXT NOT NULL,
		filename_digest TEXT NOT NULL,
		size            INTEGER NOT NULL,
		mtime           INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_kosync_documents_binary ON kosync_documents(binary_digest);
	CREATE INDEX IF NOT EXISTS idx_kosync_documents_filename ON kosync_documents(filename_digest);

	CREATE TABLE IF NOT EXISTS kosync_progress (
		document   TEXT PRIMARY KEY,
		comic_id   TEXT NOT NULL DEFAULT '',
		progress   TEXT NOT NULL,
		percentage REAL NOT NULL,
		device     TEXT NOT NULL,
		device_id  TEXT NOT NULL DEFAULT '',
		timestamp  INTEGER NOT NULL
	);
`

// kosyncReady records, per Store, that the kosync schema exists.
var kosyncReady sync.Map // *Store -> *kosyncOnce

type kosyncOnce struct {
	once sync.Once
	err  error
}

func (s *Store) ensureKosync() error {
	v, _ := kosyncReady.LoadOrStore(s, &kosyncOnce{})
	o := v.(*kosyncOnce)
	o.once.Do(func() { _, o.err = s.db.Exec(kosyncSchema) })
	return o.err
}

// KosyncDocument is one comic's KOReader digests.
type KosyncDocument struct {
	ComicID        string
	BinaryDigest   string
	FilenameDigest string
	Size           int64
	MTime          int64 // unix seconds
}

// UpsertKosyncDocument records (or refreshes) a comic's digests.
func (s *Store) UpsertKosyncDocument(d KosyncDocument) error {
	if err := s.ensureKosync(); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO kosync_documents (comic_id, binary_digest, filename_digest, size, mtime)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(comic_id) DO UPDATE SET
			binary_digest = excluded.binary_digest,
			filename_digest = excluded.filename_digest,
			size = excluded.size,
			mtime = excluded.mtime
	`, d.ComicID, d.BinaryDigest, d.FilenameDigest, d.Size, d.MTime)
	return err
}

// ListKosyncDocuments returns every indexed comic keyed by comic id.
func (s *Store) ListKosyncDocuments() (map[string]KosyncDocument, error) {
	if err := s.ensureKosync(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT comic_id, binary_digest, filename_digest, size, mtime FROM kosync_documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]KosyncDocument{}
	for rows.Next() {
		var d KosyncDocument
		if err := rows.Scan(&d.ComicID, &d.BinaryDigest, &d.FilenameDigest, &d.Size, &d.MTime); err != nil {
			return nil, err
		}
		out[d.ComicID] = d
	}
	return out, rows.Err()
}

// ComicIDsForKosyncDigest returns the comics whose binary or filename digest
// equals digest, binary matches first. Several comics can share a filename
// digest (same base name in two folders) or, for byte-identical files, a
// binary digest.
func (s *Store) ComicIDsForKosyncDigest(digest string) ([]string, error) {
	if err := s.ensureKosync(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT comic_id FROM kosync_documents
		WHERE binary_digest = ? OR filename_digest = ?
		ORDER BY (binary_digest = ?) DESC, comic_id
	`, digest, digest, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteKosyncDocument forgets a comic's digests (file gone or renamed).
func (s *Store) DeleteKosyncDocument(comicID string) error {
	if err := s.ensureKosync(); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM kosync_documents WHERE comic_id = ?`, comicID)
	return err
}

// KosyncProgress is the raw record KOReader last pushed for a document.
type KosyncProgress struct {
	Document   string
	ComicID    string // "" when the document isn't a library comic
	Progress   string
	Percentage float64
	Device     string
	DeviceID   string
	Timestamp  time.Time
}

// UpsertKosyncProgress stores KOReader's latest record for a document.
func (s *Store) UpsertKosyncProgress(p KosyncProgress) error {
	if err := s.ensureKosync(); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO kosync_progress (document, comic_id, progress, percentage, device, device_id, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(document) DO UPDATE SET
			comic_id = excluded.comic_id,
			progress = excluded.progress,
			percentage = excluded.percentage,
			device = excluded.device,
			device_id = excluded.device_id,
			timestamp = excluded.timestamp
	`, p.Document, p.ComicID, p.Progress, p.Percentage, p.Device, p.DeviceID, p.Timestamp.Unix())
	return err
}

// GetKosyncProgress returns the stored record for document, or ErrNotFound.
func (s *Store) GetKosyncProgress(document string) (*KosyncProgress, error) {
	if err := s.ensureKosync(); err != nil {
		return nil, err
	}
	var p KosyncProgress
	var ts int64
	err := s.db.QueryRow(`
		SELECT document, comic_id, progress, percentage, device, device_id, timestamp
		FROM kosync_progress WHERE document = ?
	`, document).Scan(&p.Document, &p.ComicID, &p.Progress, &p.Percentage, &p.Device, &p.DeviceID, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Timestamp = time.Unix(ts, 0).UTC()
	return &p, nil
}
