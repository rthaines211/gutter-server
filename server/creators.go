package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/rthaines211/gutter-server/creators"
	"github.com/rthaines211/gutter-server/store"
)

// CreatorListEntry is one entry of GET /creators: a library creator grouped
// by ComicVine person ID (or by lowercased name when the matcher hasn't
// backfilled an ID yet).
type CreatorListEntry struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Roles     []string `json:"roles"`
	SeriesIds []string `json:"seriesIds"`
}

// creatorAgg accumulates one CreatorListEntry's roles/series across the
// series_enrichment rows that credit them.
type creatorAgg struct {
	id        int64
	name      string
	roles     map[string]bool
	seriesIds map[string]bool
}

// libraryCreators walks every series_enrichment row's creators JSON and
// returns only in-scope (RoleGroup != "") credits, keyed by ComicVine person
// ID when it's set, otherwise by lowercased trimmed name.
func (s *Server) libraryCreators() (map[string]*creatorAgg, error) {
	enrichment, err := s.Store.ListSeriesEnrichmentMap()
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]*creatorAgg)
	for seriesID, e := range enrichment {
		var credits []store.Creator
		if e.Creators == "" {
			continue
		}
		if err := json.Unmarshal([]byte(e.Creators), &credits); err != nil {
			continue // malformed creators JSON is data, not a bug
		}
		for _, c := range credits {
			// c.Role is ComicVine's raw field and can carry several
			// comma-separated roles ("penciler, inker"); scope each one
			// individually, same as the cv_credit index build does for
			// issue-level credits (creators/index.go).
			for _, role := range creators.SplitRoles(c.Role) {
				if creators.RoleGroup(role) == "" {
					continue
				}
				key := creatorKey(c.ID, c.Name)
				a, ok := byKey[key]
				if !ok {
					a = &creatorAgg{id: c.ID, name: c.Name, roles: map[string]bool{}, seriesIds: map[string]bool{}}
					byKey[key] = a
				}
				a.roles[role] = true
				a.seriesIds[seriesID] = true
			}
		}
	}
	return byKey, nil
}

// creatorKey groups by ComicVine person ID when it's known (> 0), otherwise
// by lowercased trimmed name — the same rule for both /creators and
// /creators/lookup's library fallback.
func creatorKey(id int64, name string) string {
	if id > 0 {
		return "id:" + strconv.FormatInt(id, 10)
	}
	return "name:" + strings.ToLower(strings.TrimSpace(name))
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HandleCreatorsList handles GET /creators.
func (s *Server) HandleCreatorsList(w http.ResponseWriter, r *http.Request) {
	byKey, err := s.libraryCreators()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	entries := make([]CreatorListEntry, 0, len(byKey))
	for _, a := range byKey {
		entries = append(entries, CreatorListEntry{
			ID:        a.id,
			Name:      a.name,
			Roles:     sortedKeys(a.roles),
			SeriesIds: sortedKeys(a.seriesIds),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if len(entries[i].SeriesIds) != len(entries[j].SeriesIds) {
			return len(entries[i].SeriesIds) > len(entries[j].SeriesIds)
		}
		return entries[i].Name < entries[j].Name
	})

	writeJSON(w, entries)
}

// CreatorDetailResponse is the JSON payload for GET /creators/{personId}.
type CreatorDetailResponse struct {
	ID                  int64             `json:"id"`
	Name                string            `json:"name"`
	Roles               []string          `json:"roles"`
	SeriesIds           []string          `json:"seriesIds"`
	VolumesOwned        int               `json:"volumesOwned"`
	VolumesTotal        int               `json:"volumesTotal"`
	Bibliography        []creators.Volume `json:"bibliography"`
	BibliographyPending bool              `json:"bibliographyPending"`
}

// HandleCreatorDetail handles GET /creators/{personId}.
func (s *Server) HandleCreatorDetail(w http.ResponseWriter, r *http.Request) {
	personID, err := strconv.ParseInt(r.PathValue("personId"), 10, 64)
	if err != nil || personID <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid person id")
		return
	}

	byKey, err := s.libraryCreators()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	agg, inLibrary := byKey[creatorKey(personID, "")]

	name := ""
	knownToIndex := false
	// During a rebuild the single pinned connection PersonName reads from is
	// busy for ~15s; skip the index entirely rather than blocking the
	// request on it, and fall back to the library-derived name below.
	if s.Creators != nil && s.Creators.Ready() {
		if n, err := s.Creators.PersonName(personID); err == nil {
			name = n
			knownToIndex = true
		}
	}
	if name == "" && inLibrary {
		name = agg.name
	}
	if !knownToIndex && !inLibrary {
		writeJSONError(w, http.StatusNotFound, "creator not found")
		return
	}

	resp := CreatorDetailResponse{ID: personID, Name: name}
	if inLibrary {
		resp.Roles = sortedKeys(agg.roles)
		resp.SeriesIds = sortedKeys(agg.seriesIds)
	} else {
		resp.Roles = []string{}
		resp.SeriesIds = []string{}
	}

	if s.Creators != nil && s.Creators.Ready() {
		owned, err := s.Store.OwnedCVIssues()
		if err != nil {
			writeInternalError(w, err)
			return
		}
		ownership, err := s.Creators.Ownership(owned)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		bibliography, err := s.Creators.Bibliography(personID, ownership)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		resp.Bibliography = bibliography
		resp.VolumesTotal = len(bibliography)
		for _, v := range bibliography {
			if v.Owned != "none" {
				resp.VolumesOwned++
			}
		}
	} else {
		resp.VolumesOwned = len(resp.SeriesIds)
		resp.VolumesTotal = 0
		resp.BibliographyPending = s.Creators != nil && !s.Creators.Ready()
	}

	writeJSON(w, resp)
}

// creatorMatchJSON is the shape of every /creators/lookup entry, used both
// for a real index match and the no-index library fallback so the two paths
// never drift apart.
type creatorMatchJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Volumes int    `json:"volumes"`
}

// HandleCreatorLookup handles GET /creators/lookup?name=.
func (s *Server) HandleCreatorLookup(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "missing name")
		return
	}

	matches := []creatorMatchJSON{}
	if s.Creators != nil && s.Creators.Ready() {
		found, err := s.Creators.LookupByName(name)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		for _, m := range found {
			matches = append(matches, creatorMatchJSON{ID: m.ID, Name: m.Name, Volumes: m.Volumes})
		}
	} else {
		byKey, err := s.libraryCreators()
		if err != nil {
			writeInternalError(w, err)
			return
		}
		for _, a := range byKey {
			if strings.EqualFold(strings.TrimSpace(a.name), name) {
				matches = append(matches, creatorMatchJSON{ID: a.id, Name: a.name, Volumes: 0})
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })

	writeJSON(w, matches)
}

// HandleCreatorVolume handles GET /creators/volumes/{id}.
func (s *Server) HandleCreatorVolume(w http.ResponseWriter, r *http.Request) {
	if s.Creators == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "creator index unavailable")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid volume id")
		return
	}

	detail, err := s.Creators.VolumeInfo(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "volume not found")
			return
		}
		writeInternalError(w, err)
		return
	}
	writeJSON(w, detail)
}

// HandleCreatorIssueCredits handles GET /creators/issues/{comicId}.
func (s *Server) HandleCreatorIssueCredits(w http.ResponseWriter, r *http.Request) {
	comicID := r.PathValue("comicId")
	cvIssueID, err := s.Store.IssueComicVineID(comicID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "comic has no matched comicvine issue")
			return
		}
		writeInternalError(w, err)
		return
	}

	if s.Creators == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "creator index unavailable")
		return
	}

	credits, err := s.Creators.IssueCredits(cvIssueID)
	if err != nil {
		// A CV issue id stored on the comic that's missing from localcv
		// (stale sidecar, partial rebuild) surfaces as sql.ErrNoRows — same
		// "not found" outcome as a comic with no matched issue at all above.
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "comicvine issue not found")
			return
		}
		writeInternalError(w, err)
		return
	}
	if credits == nil {
		credits = []store.Creator{}
	}
	writeJSON(w, credits)
}
