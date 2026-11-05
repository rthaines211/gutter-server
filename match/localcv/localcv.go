// Package localcv provides read-only access to the offline ComicVine dump
// (localcv.db). The dump is never written to.
package localcv

import (
	"database/sql"
	"encoding/json"
	"errors"

	_ "github.com/mattn/go-sqlite3"
)

var ErrNotFound = errors.New("localcv: not found")

// Issue is a fully-resolved issue: its own fields plus the volume and
// publisher it belongs to.
type Issue struct {
	IssueID          int64
	VolumeID         int64
	IssueNumber      string
	IssueName        string
	CoverDate        string
	ImageURL         string
	Description      string
	PersonCredits    string // raw JSON array as stored in the dump
	CharacterCredits string
	VolumeName       string
	VolumeStartYear  string
	Publisher        string
	// VolumeIssueCount is cv_volume.count_of_issues; 0 when unknown. A
	// one-issue volume is how ComicVine files a standalone trade.
	VolumeIssueCount int
	// Genre, PageCount, CharacterNames are populated by the GCD reader
	// (CV dump lacks genre/page_count); CharacterNames is also filled by the
	// CV reader (Task 3) from character_credits so consumers read one field.
	Genre          string
	PageCount      float64
	CharacterNames []string
}

type DB struct {
	db *sql.DB
}

// Open opens the ComicVine dump read-only. Immutable mode is deliberately not
// used: the dump may be replaced on disk between runs.
func Open(path string) (*DB, error) {
	// mattn/go-sqlite3 only honors query-string params (mode=ro included) when
	// the DSN carries a "file:" prefix — otherwise it silently drops them and
	// opens SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE regardless.
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

const issueSelect = `
	SELECT i.id, i.volume_id,
	       COALESCE(i.issue_number, ''), COALESCE(i.name, ''),
	       COALESCE(i.cover_date, ''), COALESCE(i.image_url, ''),
	       COALESCE(i.description, ''), COALESCE(i.person_credits, ''),
	       COALESCE(i.character_credits, ''),
	       COALESCE(v.name, ''), COALESCE(v.start_year, ''),
	       COALESCE(p.name, ''), COALESCE(v.count_of_issues, 0)
	  FROM cv_issue i
	  JOIN cv_volume v ON v.id = i.volume_id
	  LEFT JOIN cv_publisher p ON p.id = v.publisher_id`

// characterNames extracts names from ComicVine's character_credits JSON
// (`[{"id":…,"name":"…"}]`). A blank or malformed value yields nil.
func characterNames(raw string) []string {
	if raw == "" {
		return nil
	}
	var arr []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, c := range arr {
		if c.Name != "" {
			out = append(out, c.Name)
		}
	}
	return out
}

func (d *DB) scanIssue(row *sql.Row) (*Issue, error) {
	var iss Issue
	err := row.Scan(&iss.IssueID, &iss.VolumeID, &iss.IssueNumber, &iss.IssueName,
		&iss.CoverDate, &iss.ImageURL, &iss.Description, &iss.PersonCredits,
		&iss.CharacterCredits, &iss.VolumeName, &iss.VolumeStartYear, &iss.Publisher,
		&iss.VolumeIssueCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	iss.CharacterNames = characterNames(iss.CharacterCredits)
	return &iss, nil
}

// IssueByFirstPageMD5 resolves a comic archive's first-page MD5 to a
// ComicVine issue via the dump's 560k-row scene-release index.
//
// The ORDER BY is load-bearing, not decoration. 491 hashes in the dump map to
// more than one issue id (variant covers and reprints share a first page), and
// without an explicit ordering SQLite is free to return either one — so the
// same file could match different issues on different runs. That
// nondeterminism is unacceptable in a tool whose contract is that a human can
// trust what they confirmed, so we pin the lowest id: an arbitrary choice, but
// a stable one.
func (d *DB) IssueByFirstPageMD5(md5hex string) (*Issue, error) {
	if md5hex == "" {
		return nil, ErrNotFound
	}
	return d.scanIssue(d.db.QueryRow(
		issueSelect+` WHERE i.id = (SELECT cvid FROM comic_files WHERE first_page_md5 = ? ORDER BY cvid LIMIT 1)`,
		md5hex))
}

// IssueByID resolves a known ComicVine issue id.
func (d *DB) IssueByID(issueID int64) (*Issue, error) {
	return d.scanIssue(d.db.QueryRow(issueSelect+` WHERE i.id = ?`, issueID))
}

// IssuesByVolume returns every issue in a volume, for number-based binding.
// Fetch the whole volume once per series rather than querying per issue —
// volumes hold tens to low hundreds of issues, so one query beats N.
func (d *DB) IssuesByVolume(volumeID int64) ([]Issue, error) {
	rows, err := d.db.Query(issueSelect+` WHERE i.volume_id = ?`, volumeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	issues := make([]Issue, 0)
	for rows.Next() {
		var iss Issue
		if err := rows.Scan(&iss.IssueID, &iss.VolumeID, &iss.IssueNumber, &iss.IssueName,
			&iss.CoverDate, &iss.ImageURL, &iss.Description, &iss.PersonCredits,
			&iss.CharacterCredits, &iss.VolumeName, &iss.VolumeStartYear, &iss.Publisher,
			&iss.VolumeIssueCount); err != nil {
			return nil, err
		}
		iss.CharacterNames = characterNames(iss.CharacterCredits)
		issues = append(issues, iss)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return issues, nil
}

// Volume is a bare cv_volume row plus its publisher name.
type Volume struct {
	VolumeID    int64
	Name        string
	StartYear   string
	Publisher   string
	Description string
	// IssueCount is cv_volume.count_of_issues; 0 when unknown. Filled by
	// AllVolumes, not VolumeByID.
	IssueCount int
}

// AllVolumes returns every volume's id, name, start year, publisher and issue
// count — no descriptions, so the whole dump fits in a few tens of MB and
// loads in well under a second. Callers index it by canonical name.
func (d *DB) AllVolumes() ([]Volume, error) {
	rows, err := d.db.Query(`
		SELECT v.id, v.name, COALESCE(v.start_year, ''), COALESCE(p.name, ''),
		       COALESCE(v.count_of_issues, 0)
		  FROM cv_volume v
		  LEFT JOIN cv_publisher p ON p.id = v.publisher_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Volume
	for rows.Next() {
		var v Volume
		if err := rows.Scan(&v.VolumeID, &v.Name, &v.StartYear, &v.Publisher, &v.IssueCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ArcCredit is one (issue, story arc) pairing read out of cv_issue's
// story_arc_credits JSON.
type ArcCredit struct {
	IssueID int64
	ArcID   int64
	ArcName string
}

// arcCreditsChunk caps the IN-clause size per query; SQLite's default
// SQLITE_MAX_VARIABLE_NUMBER is far higher, but staying well under it keeps
// this robust across builds that lower it.
const arcCreditsChunk = 500

// ArcCredits returns one ArcCredit per (issue, arc) pairing for the given
// ComicVine issue ids. An issue with empty or malformed story_arc_credits
// JSON contributes nothing (not an error).
func (d *DB) ArcCredits(issueIDs []int64) ([]ArcCredit, error) {
	var out []ArcCredit
	for len(issueIDs) > 0 {
		n := len(issueIDs)
		if n > arcCreditsChunk {
			n = arcCreditsChunk
		}
		chunk := issueIDs[:n]
		issueIDs = issueIDs[n:]

		args := make([]interface{}, len(chunk))
		placeholders := make([]byte, 0, len(chunk)*2)
		for i, id := range chunk {
			args[i] = id
			if i > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, '?')
		}

		rows, err := d.db.Query(
			`SELECT id, COALESCE(story_arc_credits, '') FROM cv_issue WHERE id IN (`+string(placeholders)+`)`,
			args...)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var issueID int64
				var raw string
				if err := rows.Scan(&issueID, &raw); err != nil {
					return err
				}
				if raw == "" {
					continue
				}
				var arcs []struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal([]byte(raw), &arcs); err != nil {
					continue
				}
				for _, a := range arcs {
					out = append(out, ArcCredit{IssueID: issueID, ArcID: a.ID, ArcName: a.Name})
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// VolumeByID resolves one volume; ErrNotFound when the dump lacks it.
func (d *DB) VolumeByID(id int64) (*Volume, error) {
	var v Volume
	err := d.db.QueryRow(`
		SELECT v.id, v.name, COALESCE(v.start_year, ''), COALESCE(p.name, ''),
		       COALESCE(v.description, '')
		  FROM cv_volume v
		  LEFT JOIN cv_publisher p ON p.id = v.publisher_id
		 WHERE v.id = ?`, id).
		Scan(&v.VolumeID, &v.Name, &v.StartYear, &v.Publisher, &v.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}
