package creators

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ryanhaines/comic-reader/comicvine"
	"github.com/ryanhaines/comic-reader/store"
)

// ErrNotReady is returned by queries that read cv_credit (Bibliography,
// LookupByName) when Build has never completed.
var ErrNotReady = errors.New("creators: index not ready")

// Volume is one entry in a person's bibliography: a ComicVine volume they
// have an in-scope (writer/art/covers) credit on, with ownership info
// joined in from the caller.
type Volume struct {
	VolumeID     int64    `json:"volumeId"`
	Name         string   `json:"name"`
	StartYear    int      `json:"startYear"`
	Publisher    string   `json:"publisher"`
	CoverURL     string   `json:"coverUrl"`
	Roles        []string `json:"roles"`
	PersonIssues int      `json:"personIssues"`
	TotalIssues  int      `json:"totalIssues"`
	OwnedIssues  int      `json:"ownedIssues"`
	Owned        string   `json:"owned"`
	SeriesID     string   `json:"seriesId,omitempty"`
}

// PersonMatch is one cv_person row matched by LookupByName, with the
// in-scope volume count a creator-page search result shows.
type PersonMatch struct {
	ID      int64
	Name    string
	Volumes int
}

// VolumeDetail is the volume info shown above a bibliography entry's issue
// list.
type VolumeDetail struct {
	VolumeID    int64  `json:"volumeId"`
	Name        string `json:"name"`
	StartYear   int    `json:"startYear"`
	Publisher   string `json:"publisher"`
	CoverURL    string `json:"coverUrl"`
	TotalIssues int    `json:"totalIssues"`
	Description string `json:"description"`
}

// Bibliography returns every ComicVine volume the given person has an
// in-scope credit (RoleGroup != "") on, sorted newest first (then by name).
// own supplies per-volume ownership, keyed by ComicVine volume ID; a volume
// missing from own has no owned issues.
func (x *Index) Bibliography(personID int64, own map[int64]VolumeOwnership) ([]Volume, error) {
	if !x.Ready() {
		return nil, ErrNotReady
	}

	rows, err := x.db.Query(`
		SELECT cc.volume_id, cc.issue_id, cc.role, COALESCE(v.name, ''), COALESCE(v.start_year, ''),
		       COALESCE(v.count_of_issues, 0), COALESCE(v.image_url, ''), COALESCE(p.name, '')
		FROM cv_credit cc
		JOIN cv.cv_volume v ON v.id = cc.volume_id
		LEFT JOIN cv.cv_publisher p ON p.id = v.publisher_id
		WHERE cc.person_id = ?
	`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type agg struct {
		Volume
		roles  map[string]bool
		issues map[int64]bool
	}
	byVolume := make(map[int64]*agg)
	order := make([]int64, 0)

	for rows.Next() {
		var volumeID, issueID int64
		var role, name, startYearText, coverURL, publisher string
		var totalIssues int
		if err := rows.Scan(&volumeID, &issueID, &role, &name, &startYearText,
			&totalIssues, &coverURL, &publisher); err != nil {
			return nil, err
		}
		if RoleGroup(role) == "" {
			continue
		}
		a, ok := byVolume[volumeID]
		if !ok {
			a = &agg{
				Volume: Volume{
					VolumeID:    volumeID,
					Name:        name,
					StartYear:   parseYear(startYearText),
					Publisher:   publisher,
					CoverURL:    coverURL,
					TotalIssues: totalIssues,
				},
				roles:  make(map[string]bool),
				issues: make(map[int64]bool),
			}
			byVolume[volumeID] = a
			order = append(order, volumeID)
		}
		a.roles[role] = true
		a.issues[issueID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Volume, 0, len(order))
	for _, volumeID := range order {
		a := byVolume[volumeID]
		roles := make([]string, 0, len(a.roles))
		for r := range a.roles {
			roles = append(roles, r)
		}
		sort.Strings(roles)
		a.Roles = roles
		a.PersonIssues = len(a.issues)
		if o, ok := own[volumeID]; ok {
			a.OwnedIssues = o.OwnedIssues
			a.SeriesID = o.SeriesID
		}
		a.Owned = OwnedState(a.OwnedIssues, a.TotalIssues)
		out = append(out, a.Volume)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].StartYear != out[j].StartYear {
			return out[i].StartYear > out[j].StartYear
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// PersonName looks up a ComicVine person's display name. Returns an error
// wrapping sql.ErrNoRows when the person is unknown.
func (x *Index) PersonName(id int64) (string, error) {
	var name string
	if err := x.db.QueryRow(`SELECT name FROM cv.cv_person WHERE id = ?`, id).Scan(&name); err != nil {
		return "", fmt.Errorf("person %d: %w", id, err)
	}
	return name, nil
}

// LookupByName case-insensitively exact-matches cv_person by name, trimmed.
// Multiple people can share a name (ComicVine doesn't dedupe), so this
// returns every match with its in-scope volume count.
func (x *Index) LookupByName(name string) ([]PersonMatch, error) {
	if !x.Ready() {
		return nil, ErrNotReady
	}
	name = strings.TrimSpace(name)

	rows, err := x.db.Query(`SELECT id, name FROM cv.cv_person WHERE name = ? COLLATE NOCASE`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []PersonMatch
	for rows.Next() {
		var m PersonMatch
		if err := rows.Scan(&m.ID, &m.Name); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range matches {
		n, err := x.inScopeVolumeCount(matches[i].ID)
		if err != nil {
			return nil, err
		}
		matches[i].Volumes = n
	}
	return matches, nil
}

// inScopeVolumeCount counts the distinct volumes a person has an in-scope
// (RoleGroup != "") credit on.
func (x *Index) inScopeVolumeCount(personID int64) (int, error) {
	rows, err := x.db.Query(`SELECT DISTINCT volume_id, role FROM cv_credit WHERE person_id = ?`, personID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	volumes := make(map[int64]bool)
	for rows.Next() {
		var volumeID int64
		var role string
		if err := rows.Scan(&volumeID, &role); err != nil {
			return 0, err
		}
		if RoleGroup(role) == "" {
			continue
		}
		volumes[volumeID] = true
	}
	return len(volumes), rows.Err()
}

// VolumeInfo looks up a ComicVine volume's detail. Returns an error
// wrapping sql.ErrNoRows when the volume is unknown, so the HTTP layer can
// 404.
func (x *Index) VolumeInfo(id int64) (VolumeDetail, error) {
	var d VolumeDetail
	var startYearText string
	var coverURL, description sql.NullString
	var totalIssues sql.NullInt64
	err := x.db.QueryRow(`
		SELECT v.id, COALESCE(v.name, ''), COALESCE(v.start_year, ''), COALESCE(p.name, ''),
		       v.image_url, v.count_of_issues, v.description
		FROM cv.cv_volume v
		LEFT JOIN cv.cv_publisher p ON p.id = v.publisher_id
		WHERE v.id = ?
	`, id).Scan(&d.VolumeID, &d.Name, &startYearText, &d.Publisher, &coverURL,
		&totalIssues, &description)
	if err != nil {
		return VolumeDetail{}, fmt.Errorf("volume %d: %w", id, err)
	}
	d.StartYear = parseYear(startYearText)
	d.CoverURL = coverURL.String
	d.TotalIssues = int(totalIssues.Int64)
	d.Description = comicvine.StripHTML(description.String)
	return d, nil
}

// issueCredit mirrors one entry of ComicVine's person_credits JSON array,
// including the display name IssueCredits needs (unlike the ID-only
// personCredit used by the index build in index.go).
type issueCredit struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// IssueCredits returns every (person, role) credit on a ComicVine issue,
// including out-of-scope roles like letterer and editor: the caller
// decides what's tappable. It reads cv.cv_issue.person_credits directly by
// issue ID, so it works even without a built index.
func (x *Index) IssueCredits(cvIssueID int64) ([]store.Creator, error) {
	var raw sql.NullString
	if err := x.db.QueryRow(`SELECT person_credits FROM cv.cv_issue WHERE id = ?`, cvIssueID).
		Scan(&raw); err != nil {
		return nil, fmt.Errorf("issue %d: %w", cvIssueID, err)
	}
	if !raw.Valid || raw.String == "" || raw.String == "[]" {
		return nil, nil
	}

	var credits []issueCredit
	if err := json.Unmarshal([]byte(raw.String), &credits); err != nil {
		return nil, fmt.Errorf("issue %d: unmarshal person_credits: %w", cvIssueID, err)
	}

	var out []store.Creator
	for _, c := range credits {
		for _, role := range SplitRoles(c.Role) {
			out = append(out, store.Creator{ID: c.ID, Name: c.Name, Role: role})
		}
	}
	return out, nil
}

// parseYear parses cv_volume.start_year (stored as TEXT), returning 0 when
// it's empty or not a valid integer.
func parseYear(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
