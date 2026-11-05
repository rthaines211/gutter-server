package store

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

type Arc struct {
	ID            string
	Name          string
	ImageURL      string
	SelfContained *bool
	CVArcID       string
	TotalIssues   int
	UpdatedAt     time.Time
	Source        string
	Publisher     string
	StartYear     int
	EndYear       int
	Description   string
}

// ArcDescTarget is a story arc eligible for a ComicVine description fetch:
// its own id plus the bare numeric ComicVine story-arc id to fetch.
type ArcDescTarget struct {
	ArcID   string
	CVArcID int
}

type ArcIssueInput struct {
	CVIssueID  string
	CVVolumeID string
	Number     string
	Order      int
}

type ArcInput struct {
	Key           string
	Name          string
	Image         string
	CVArcID       string
	SelfContained *bool
	TotalIssues   int
	Issues        []ArcIssueInput
}

// resolveIssueID maps a Mylar arc issue onto a local issues.id: first by the
// ComicVine issue id (issue_enrichment), then by (volume id, issue number).
// A returned error is a real DB fault (not a miss) — the caller aborts the
// import rather than silently dropping issues. sql.ErrNoRows means "no match",
// which falls through to the next tier / a clean (\"\", false, nil).
func (s *Store) resolveIssueID(cvIssueID, cvVolumeID, number string) (string, bool, error) {
	if n, err := strconv.Atoi(cvIssueID); err == nil {
		var id string
		// Callers only pass real ComicVine ids (>0); 0 would match Metron-sourced rows that lack a CV cross-reference.
		err := s.db.QueryRow(
			`SELECT issue_id FROM issue_enrichment WHERE comicvine_issue_id = ?`,
			n).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
	}
	if v, err := strconv.Atoi(cvVolumeID); err == nil && number != "" {
		var id string
		err := s.db.QueryRow(`
			SELECT i.id FROM issues i
			JOIN series_enrichment se ON se.series_id = i.series_id
			WHERE se.comicvine_volume_id = ? AND i.number = ?
			LIMIT 1`, v, number).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
	}
	return "", false, nil
}

// ImportArcs upserts the given arcs (keyed by ArcInput.Key = Mylar StoryArcID),
// resolving each arc's issues to local issue ids, and prunes any arc no longer
// present. An arc with zero resolvable issues is still stored (present count 0).
// ponytail: bumps updated_at on every import → the handful of arcs re-sync each
// hour; add a change-check only if arc counts ever grow large.
func (s *Store) ImportArcs(arcs []ArcInput) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	keep := make([]string, 0, len(arcs))
	for _, a := range arcs {
		var sc interface{}
		if a.SelfContained != nil {
			if *a.SelfContained {
				sc = 1
			} else {
				sc = 0
			}
		}
		if _, err := tx.Exec(`
			INSERT INTO story_arcs (id, name, image_url, self_contained, cv_arc_id, total_issues, source, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'mylar', CURRENT_TIMESTAMP)
			ON CONFLICT(id) DO UPDATE SET
				name=excluded.name, image_url=excluded.image_url,
				self_contained=excluded.self_contained, cv_arc_id=excluded.cv_arc_id,
				total_issues=excluded.total_issues, source='mylar', updated_at=CURRENT_TIMESTAMP`,
			a.Key, a.Name, a.Image, sc, a.CVArcID, a.TotalIssues); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM story_arc_issues WHERE arc_id = ?`, a.Key); err != nil {
			return err
		}
		pos := 0
		for _, iss := range a.Issues {
			localID, ok, err := s.resolveIssueID(iss.CVIssueID, iss.CVVolumeID, iss.Number)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO story_arc_issues (arc_id, issue_id, position) VALUES (?, ?, ?)`,
				a.Key, localID, pos); err != nil {
				return err
			}
			pos++
		}
		keep = append(keep, a.Key)
	}

	// Prune arcs absent from this import (and their issues via ON DELETE CASCADE).
	// Scoped to source = 'mylar' so a comicvine-sourced rebuild survives Mylar's import.
	if len(keep) == 0 {
		if _, err := tx.Exec(`DELETE FROM story_arcs WHERE source = 'mylar'`); err != nil {
			return err
		}
	} else {
		q := `DELETE FROM story_arcs WHERE source = 'mylar' AND id NOT IN (?` + repeatComma(len(keep)-1) + `)`
		args := make([]interface{}, len(keep))
		for i, k := range keep {
			args[i] = k
		}
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func repeatComma(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += ",?"
	}
	return out
}

func (s *Store) ListArcsUpdatedSince(since int64) ([]Arc, error) {
	rows, err := s.db.Query(`
		SELECT id, name, image_url, self_contained, cv_arc_id, total_issues, updated_at, source, publisher, start_year, end_year, description
		FROM story_arcs WHERE updated_at >= datetime(?, 'unixepoch') ORDER BY name ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Arc
	for rows.Next() {
		var a Arc
		var sc *int64
		if err := rows.Scan(&a.ID, &a.Name, &a.ImageURL, &sc, &a.CVArcID, &a.TotalIssues, &a.UpdatedAt,
			&a.Source, &a.Publisher, &a.StartYear, &a.EndYear, &a.Description); err != nil {
			return nil, err
		}
		if sc != nil {
			b := *sc != 0
			a.SelfContained = &b
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ListArcIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM story_arcs ORDER BY name ASC`)
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

// ArcsNeedingDescription returns up to limit arcs that need a ComicVine
// description fetch or re-derive: either never fetched
// (description_fetched_at = 0) or fetched under an older description
// algorithm version than minVersion. Bumping minVersion therefore re-derives
// arcs that already have a description — cheap, since GetStoryArc results
// are disk-cached and a cache hit never touches the API budget. Only arcs
// with a usable ComicVine story-arc id are returned: comicvine-sourced arcs
// store the bare id directly; Mylar arcs store it with an optional "4045-"
// story-arc-type prefix, which is stripped before parsing. Rows with no id,
// or one that doesn't parse as a positive int, are skipped.
func (s *Store) ArcsNeedingDescription(limit, minVersion int) ([]ArcDescTarget, error) {
	rows, err := s.db.Query(
		`SELECT id, cv_arc_id FROM story_arcs
		 WHERE description_fetched_at = 0 OR description_version < ?
		 ORDER BY id LIMIT ?`, minVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArcDescTarget
	for rows.Next() {
		var id, cvArcID string
		if err := rows.Scan(&id, &cvArcID); err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimPrefix(cvArcID, "4045-"))
		if err != nil || n <= 0 {
			continue
		}
		out = append(out, ArcDescTarget{ArcID: id, CVArcID: n})
	}
	return out, rows.Err()
}

// SetArcDescription stores a fetched/re-derived ComicVine description and
// marks the arc as fetched under the given description algorithm version
// (fetchedAt, unix ms), so it isn't re-fetched/re-derived until that version
// moves again. updated_at only moves when the description text actually
// changed, so GET /sync doesn't re-send arcs whose description was merely
// re-confirmed (including a re-derive that produced the same text).
func (s *Store) SetArcDescription(arcID, desc string, fetchedAt int64, version int) error {
	var cur string
	if err := s.db.QueryRow(`SELECT description FROM story_arcs WHERE id = ?`, arcID).Scan(&cur); err != nil {
		return err
	}
	if cur == desc {
		_, err := s.db.Exec(
			`UPDATE story_arcs SET description_fetched_at = ?, description_version = ? WHERE id = ?`,
			fetchedAt, version, arcID)
		return err
	}
	_, err := s.db.Exec(
		`UPDATE story_arcs SET description = ?, description_fetched_at = ?, description_version = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		desc, fetchedAt, version, arcID)
	return err
}

// ListIssueIDsForArcs returns issue ids for every given arc id in one batch
// (chunked at idChunkSize), grouped by arc id and ordered by position within
// each — the same shape ListArcIssueIDs would return per id. An arc id with
// no issues is absent from the map, matching ListArcIssueIDs' nil-on-empty
// behavior.
func (s *Store) ListIssueIDsForArcs(arcIDs []string) (map[string][]string, error) {
	result := make(map[string][]string, len(arcIDs))
	for _, chunk := range chunkIDs(arcIDs) {
		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.Query(
			`SELECT arc_id, issue_id FROM story_arc_issues WHERE arc_id IN (`+placeholders(len(chunk))+`) ORDER BY arc_id ASC, position ASC`,
			args...,
		)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var arcID, issueID string
			if err := rows.Scan(&arcID, &issueID); err != nil {
				rows.Close()
				return nil, err
			}
			result[arcID] = append(result[arcID], issueID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) ListArcIssueIDs(arcID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT issue_id FROM story_arc_issues WHERE arc_id = ? ORDER BY position ASC`, arcID)
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
