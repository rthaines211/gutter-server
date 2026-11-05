// Package gcd provides read-only access to the offline Grand Comics Database
// dump (gcd.db). Results are shaped as localcv types so the matcher, Verify
// and the console consume them unchanged. The dump is never written to.
package gcd

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/ryanhaines/comic-reader/match/localcv"
	"github.com/ryanhaines/comic-reader/match/parse"
	"github.com/ryanhaines/comic-reader/store"
)

var ErrNotFound = errors.New("gcd: not found")

// US/EN only: 91k of 232k series. The rest is the non-English world and
// adds namesake collisions without adding anything this library holds.
const seriesScope = `s.country_id = 225 AND s.language_id = 25 AND s.deleted = 0`

// Base issues only: a number has up to 14 variant-cover rows in GCD.
const issueScope = `i.variant_of_id IS NULL AND i.deleted = 0`

const comicStory = 19 // gcd_story_type.id for "comic story"

type DB struct{ db *sql.DB }

// Open opens the dump read-only. mode=ro needs the file: prefix to take
// effect with mattn/go-sqlite3; immutable is deliberately not used so the
// file can be replaced between runs.
func Open(path string) (*DB, error) {
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

// AllSeries returns every in-scope series as a localcv.Volume: id, name,
// start year, publisher and issue count. The runner indexes it by canonical
// name once per run.
func (d *DB) AllSeries() ([]localcv.Volume, error) {
	rows, err := d.db.Query(`
		SELECT s.id, s.name, s.year_began, COALESCE(p.name, ''), s.issue_count
		  FROM gcd_series s
		  LEFT JOIN gcd_publisher p ON p.id = s.publisher_id
		 WHERE ` + seriesScope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []localcv.Volume
	for rows.Next() {
		var v localcv.Volume
		var year int
		if err := rows.Scan(&v.VolumeID, &v.Name, &year, &v.Publisher, &v.IssueCount); err != nil {
			return nil, err
		}
		v.StartYear = yearString(year)
		out = append(out, v)
	}
	return out, rows.Err()
}

// SeriesByID resolves one in-scope series; ErrNotFound otherwise.
func (d *DB) SeriesByID(id int64) (*localcv.Volume, error) {
	var v localcv.Volume
	var year int
	err := d.db.QueryRow(`
		SELECT s.id, s.name, s.year_began, COALESCE(p.name, ''), s.issue_count
		  FROM gcd_series s
		  LEFT JOIN gcd_publisher p ON p.id = s.publisher_id
		 WHERE s.id = ? AND `+seriesScope, id).
		Scan(&v.VolumeID, &v.Name, &year, &v.Publisher, &v.IssueCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.StartYear = yearString(year)
	return &v, nil
}

// issueSelect joins the series and publisher onto each issue and picks the
// issue's own title, falling back to its first comic story's title. The
// trailing four columns (page_count, synopsis, genre, characters) come from
// gcd_issue and the primary comic story (type_id = comicStory).
const issueSelect = `
	SELECT i.id, i.series_id, i.number,
	       CASE WHEN i.title != '' THEN i.title ELSE COALESCE(
	         (SELECT st.title FROM gcd_story st
	           WHERE st.issue_id = i.id AND st.type_id = ` + "19" + ` AND st.deleted = 0
	           ORDER BY st.sequence_number LIMIT 1), '') END,
	       i.key_date, s.name, s.year_began, COALESCE(p.name, ''), s.issue_count,
	       COALESCE(i.page_count, 0),
	       COALESCE((SELECT st.synopsis FROM gcd_story st
	          WHERE st.issue_id = i.id AND st.type_id = ` + "19" + ` AND st.deleted = 0
	          ORDER BY st.sequence_number LIMIT 1), ''),
	       COALESCE((SELECT st.genre FROM gcd_story st
	          WHERE st.issue_id = i.id AND st.type_id = ` + "19" + ` AND st.deleted = 0
	          ORDER BY st.sequence_number LIMIT 1), ''),
	       COALESCE((SELECT st.characters FROM gcd_story st
	          WHERE st.issue_id = i.id AND st.type_id = ` + "19" + ` AND st.deleted = 0
	          ORDER BY st.sequence_number LIMIT 1), '')
	  FROM gcd_issue i
	  JOIN gcd_series s ON s.id = i.series_id
	  LEFT JOIN gcd_publisher p ON p.id = s.publisher_id`

func scanIssue(sc interface{ Scan(...any) error }) (*localcv.Issue, error) {
	var iss localcv.Issue
	var number, keyDate, characters string
	var year int
	if err := sc.Scan(&iss.IssueID, &iss.VolumeID, &number, &iss.IssueName, &keyDate,
		&iss.VolumeName, &year, &iss.Publisher, &iss.VolumeIssueCount,
		&iss.PageCount, &iss.Description, &iss.Genre, &characters); err != nil {
		return nil, err
	}
	iss.IssueNumber = parse.NormalizeGCDNumber(number)
	iss.CoverDate = coverDate(keyDate)
	iss.VolumeStartYear = yearString(year)
	iss.CharacterNames = ParseCharacters(characters)
	return &iss, nil
}

// ParseCharacters splits GCD's semicolon-delimited characters free text into
// names, trimming whitespace, dropping empties, and stripping trailing
// parenthetical appearance notes ("(cameo)"). Bracketed real names/aliases
// ("Batman [Bruce Wayne]") are kept as part of the name.
func ParseCharacters(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ";") {
		if i := strings.LastIndex(part, "("); i >= 0 && strings.HasSuffix(strings.TrimSpace(part), ")") {
			part = part[:i]
		}
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// IssuesBySeries lists a series' base issues in publication order.
func (d *DB) IssuesBySeries(seriesID int64) ([]localcv.Issue, error) {
	rows, err := d.db.Query(issueSelect+` WHERE i.series_id = ? AND `+issueScope+` ORDER BY i.sort_code`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []localcv.Issue
	for rows.Next() {
		iss, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *iss)
	}
	return out, rows.Err()
}

// IssueByID resolves one base issue plus its typed credits, deduped by
// (name, role) in first-seen order. Variant rows return ErrNotFound.
func (d *DB) IssueByID(id int64) (*localcv.Issue, []store.Creator, error) {
	iss, err := scanIssue(d.db.QueryRow(issueSelect+` WHERE i.id = ? AND `+issueScope, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err := d.db.Query(`
		SELECT ct.name, cnd.name
		  FROM gcd_story st
		  JOIN gcd_story_credit sc ON sc.story_id = st.id AND sc.deleted = 0
		  JOIN gcd_credit_type ct ON ct.id = sc.credit_type_id
		  JOIN gcd_creator_name_detail cnd ON cnd.id = sc.creator_id
		 WHERE st.issue_id = ? AND st.type_id = ? AND st.deleted = 0
		 ORDER BY st.sequence_number, sc.id`, id, comicStory)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var credits []store.Creator
	seen := map[string]bool{}
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			return nil, nil, err
		}
		for _, role := range splitRoles(typ) {
			key := name + "\x00" + role
			if name == "" || seen[key] {
				continue
			}
			seen[key] = true
			credits = append(credits, store.Creator{Name: name, Role: role})
		}
	}
	return iss, credits, rows.Err()
}

// splitRoles turns a GCD credit type ("pencils and inks", "script, pencils,
// inks, and colors") into single roles.
func splitRoles(typ string) []string {
	typ = strings.ReplaceAll(typ, " and ", ", ")
	var out []string
	for _, r := range strings.Split(typ, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// coverDate: GCD's key_date uses 00 for an unknown month or day
// ("2026-03-00"); the app wants a real date, so those become 01.
func coverDate(keyDate string) string {
	if len(keyDate) != 10 {
		return ""
	}
	y, m, d := keyDate[0:4], keyDate[5:7], keyDate[8:10]
	if m == "00" {
		m = "01"
	}
	if d == "00" {
		d = "01"
	}
	return y + "-" + m + "-" + d
}

func yearString(y int) string {
	if y == 0 {
		return ""
	}
	return strconv.Itoa(y)
}
