package store

import (
	"database/sql"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// CVArcCredit is one (ComicVine issue, story arc) credit. Store-local so the
// store package does not import localcv.
type CVArcCredit struct {
	CVIssueID, ArcID int64
	ArcName          string
}

// EnrichedCVIssueIDs returns the distinct ComicVine issue ids in the library.
func (s *Store) EnrichedCVIssueIDs() ([]int64, error) {
	rows, err := s.db.Query(`SELECT DISTINCT comicvine_issue_id FROM issue_enrichment
		WHERE comicvine_issue_id > 0 ORDER BY comicvine_issue_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// arcCopy is one library issue carrying a ComicVine issue id.
type arcCopy struct {
	issueID, seriesName, number, coverDate, publisher string
	hasProgress                                       bool
}

func parseNum(n string) (float64, bool) {
	f, err := strconv.ParseFloat(n, 64)
	return f, err == nil
}

// betterCopy: has progress > number parses > first by series name (then id, for determinism).
func betterCopy(a, b arcCopy) bool {
	if a.hasProgress != b.hasProgress {
		return a.hasProgress
	}
	_, ap := parseNum(a.number)
	_, bp := parseNum(b.number)
	if ap != bp {
		return ap
	}
	if a.seriesName != b.seriesName {
		return a.seriesName < b.seriesName
	}
	return a.issueID < b.issueID
}

// readingOrderLess: cover date asc (none last), series name, issue number
// numerically where it parses (parsed before unparsed), then id.
func readingOrderLess(a, b arcCopy) bool {
	if (a.coverDate == "") != (b.coverDate == "") {
		return b.coverDate == ""
	}
	if a.coverDate != b.coverDate {
		return a.coverDate < b.coverDate
	}
	if a.seriesName != b.seriesName {
		return a.seriesName < b.seriesName
	}
	an, aok := parseNum(a.number)
	bn, bok := parseNum(b.number)
	switch {
	case aok && bok && an != bn:
		return an < bn
	case aok != bok:
		return aok
	case !aok && a.number != b.number:
		return a.number < b.number
	}
	return a.issueID < b.issueID
}

// arcMeta: most common non-empty publisher (ties alphabetical), and min/max
// cover-date year.
func arcMeta(publishers, coverDates []string) (pub string, start, end int) {
	counts := map[string]int{}
	for _, p := range publishers {
		if p != "" {
			counts[p]++
		}
	}
	for p, c := range counts {
		if c > counts[pub] || (c == counts[pub] && p < pub) {
			pub = p
		}
	}
	for _, d := range coverDates {
		if len(d) < 4 {
			continue
		}
		y, err := strconv.Atoi(d[:4])
		if err != nil || y <= 0 {
			continue
		}
		if start == 0 || y < start {
			start = y
		}
		if y > end {
			end = y
		}
	}
	return pub, start, end
}

type arcState struct {
	name, cvArcID, publisher string
	start, end               int
	members                  []string
}

func queryStrings(tx *sql.Tx, q string, args ...interface{}) ([]string, error) {
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// loadArcs returns story_arcs rows of one source keyed by id.
func loadArcs(tx *sql.Tx, source string, withMembers bool) (map[string]*arcState, error) {
	rows, err := tx.Query(`SELECT id, name, cv_arc_id, publisher, start_year, end_year
		FROM story_arcs WHERE source = ?`, source)
	if err != nil {
		return nil, err
	}
	out := map[string]*arcState{}
	for rows.Next() {
		var id string
		a := &arcState{}
		if err := rows.Scan(&id, &a.name, &a.cvArcID, &a.publisher, &a.start, &a.end); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if withMembers {
		for id, a := range out {
			if a.members, err = queryStrings(tx,
				`SELECT issue_id FROM story_arc_issues WHERE arc_id = ? ORDER BY position`, id); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// RebuildComicVineArcs replaces the source='comicvine' arcs with those derived
// from credits (see the arc-shelves constraints: >=3 library issues, dedupe by
// CV issue, Mylar cv_arc_id suppresses, reading order, prune comicvine only),
// and refreshes publisher/years on Mylar arcs. updated_at moves only on change.
// Returns the number of arcs inserted, updated or deleted.
func (s *Store) RebuildComicVineArcs(credits []CVArcCredit) (changed int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Library copies per ComicVine issue id.
	rows, err := tx.Query(`
		SELECT ie.comicvine_issue_id, i.id, COALESCE(sr.name, ''), i.number, ie.cover_date,
			COALESCE(se.publisher, ''),
			EXISTS(SELECT 1 FROM reading_progress rp WHERE rp.comic_id = i.id)
		FROM issue_enrichment ie
		JOIN issues i ON i.id = ie.issue_id
		LEFT JOIN series sr ON sr.id = i.series_id
		LEFT JOIN series_enrichment se ON se.series_id = i.series_id
		WHERE ie.comicvine_issue_id > 0`)
	if err != nil {
		return 0, err
	}
	best := map[int64]arcCopy{}
	for rows.Next() {
		var cv int64
		var c arcCopy
		if err := rows.Scan(&cv, &c.issueID, &c.seriesName, &c.number, &c.coverDate, &c.publisher, &c.hasProgress); err != nil {
			rows.Close()
			return 0, err
		}
		if cur, ok := best[cv]; !ok || betterCopy(c, cur) {
			best[cv] = c
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	mylar, err := loadArcs(tx, "mylar", false)
	if err != nil {
		return 0, err
	}
	suppressed := map[string]bool{}
	for _, a := range mylar {
		// Mylar stores cv_arc_id with the ComicVine story-arc type prefix
		// ("4045-57081"), while we key arcs by the bare numeric id.
		if id := strings.TrimPrefix(a.cvArcID, "4045-"); id != "" {
			suppressed[id] = true
		}
	}
	existing, err := loadArcs(tx, "comicvine", true)
	if err != nil {
		return 0, err
	}

	// Group credits by arc, keeping distinct library CV issues in first-seen order.
	type arcAgg struct {
		name string
		seen map[int64]bool
		cvs  []int64
	}
	byArc := map[int64]*arcAgg{}
	var arcOrder []int64
	for _, c := range credits {
		if c.ArcID <= 0 {
			continue // no real story arc on this credit; avoid a bogus "cv-0" arc
		}
		if _, inLib := best[c.CVIssueID]; !inLib {
			continue
		}
		a := byArc[c.ArcID]
		if a == nil {
			a = &arcAgg{name: c.ArcName, seen: map[int64]bool{}}
			byArc[c.ArcID] = a
			arcOrder = append(arcOrder, c.ArcID)
		}
		if !a.seen[c.CVIssueID] {
			a.seen[c.CVIssueID] = true
			a.cvs = append(a.cvs, c.CVIssueID)
		}
	}

	keep := map[string]bool{}
	for _, arcID := range arcOrder {
		agg := byArc[arcID]
		cvArcID := strconv.FormatInt(arcID, 10)
		id := "cv-" + cvArcID
		if len(agg.cvs) < 3 || suppressed[cvArcID] {
			continue
		}
		if _, clash := mylar[id]; clash {
			continue // never overwrite a Mylar row that happens to share the id
		}
		copies := make([]arcCopy, len(agg.cvs))
		for i, cv := range agg.cvs {
			copies[i] = best[cv]
		}
		sort.Slice(copies, func(i, j int) bool { return readingOrderLess(copies[i], copies[j]) })
		want := arcState{name: agg.name, members: make([]string, len(copies))}
		pubs := make([]string, len(copies))
		dates := make([]string, len(copies))
		for i, c := range copies {
			want.members[i], pubs[i], dates[i] = c.issueID, c.publisher, c.coverDate
		}
		want.publisher, want.start, want.end = arcMeta(pubs, dates)
		keep[id] = true

		if cur, ok := existing[id]; ok && cur.name == want.name && cur.publisher == want.publisher &&
			cur.start == want.start && cur.end == want.end && slices.Equal(cur.members, want.members) {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO story_arcs (id, name, image_url, cv_arc_id, total_issues, source, publisher, start_year, end_year, updated_at)
			VALUES (?, ?, '', ?, 0, 'comicvine', ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(id) DO UPDATE SET
				name=excluded.name, image_url='', cv_arc_id=excluded.cv_arc_id, total_issues=0,
				publisher=excluded.publisher, start_year=excluded.start_year, end_year=excluded.end_year,
				updated_at=CURRENT_TIMESTAMP
			WHERE story_arcs.source = 'comicvine'`,
			id, want.name, cvArcID, want.publisher, want.start, want.end); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM story_arc_issues WHERE arc_id = ?`, id); err != nil {
			return 0, err
		}
		for pos, issueID := range want.members {
			if _, err := tx.Exec(`INSERT INTO story_arc_issues (arc_id, issue_id, position) VALUES (?, ?, ?)`,
				id, issueID, pos); err != nil {
				return 0, err
			}
		}
		changed++
	}

	// Prune ComicVine arcs no longer derived. Scoped to source='comicvine' only.
	for id := range existing {
		if keep[id] {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM story_arcs WHERE id = ? AND source = 'comicvine'`, id); err != nil {
			return 0, err
		}
		changed++
	}

	// Refresh publisher/years on Mylar arcs from their members.
	type metaIn struct{ pubs, dates []string }
	in := map[string]*metaIn{}
	rows, err = tx.Query(`
		SELECT sai.arc_id, COALESCE(se.publisher, ''), COALESCE(ie.cover_date, '')
		FROM story_arc_issues sai
		JOIN story_arcs a ON a.id = sai.arc_id AND a.source = 'mylar'
		LEFT JOIN issues i ON i.id = sai.issue_id
		LEFT JOIN series_enrichment se ON se.series_id = i.series_id
		LEFT JOIN issue_enrichment ie ON ie.issue_id = sai.issue_id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var arcID, pub, date string
		if err := rows.Scan(&arcID, &pub, &date); err != nil {
			rows.Close()
			return 0, err
		}
		m := in[arcID]
		if m == nil {
			m = &metaIn{}
			in[arcID] = m
		}
		m.pubs, m.dates = append(m.pubs, pub), append(m.dates, date)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for id, cur := range mylar {
		var pub string
		var start, end int
		if m := in[id]; m != nil {
			pub, start, end = arcMeta(m.pubs, m.dates)
		}
		if pub == cur.publisher && start == cur.start && end == cur.end {
			continue
		}
		if _, err := tx.Exec(`UPDATE story_arcs SET publisher = ?, start_year = ?, end_year = ?,
			updated_at = CURRENT_TIMESTAMP WHERE id = ? AND source = 'mylar'`, pub, start, end, id); err != nil {
			return 0, err
		}
		changed++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}
