package match

import (
	"encoding/json"
	"log"
	"path/filepath"
	"strconv"

	"github.com/rthaines211/gutter-server/match/localcv"
	"github.com/rthaines211/gutter-server/match/parse"
	"github.com/rthaines211/gutter-server/store"
)

// cvCredit is one entry of the dump's person_credits JSON array.
type cvCredit struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// Apply writes a confirmed match's upstream metadata into the enrichment
// tables the app reads, then makes one opportunistic attempt to supplement it
// from the OTHER offline dump. It touches no files on disk and is fully
// reversible by deleting the enrichment rows.
func (r *Runner) Apply(seriesID string) error {
	if err := r.applyPrimary(seriesID); err != nil {
		return err
	}
	return r.mergeSecondaryEnrichment(seriesID)
}

// applyPrimary writes a confirmed match's upstream metadata into the
// enrichment tables the app reads. It touches no files on disk and is fully
// reversible by deleting the enrichment rows.
func (r *Runner) applyPrimary(seriesID string) error {
	m, err := r.st.GetSeriesMatch(seriesID)
	if err != nil {
		return err
	}
	if m.VolumeID == "" {
		// confirm() calls Apply before flipping the state to confirmed, so a
		// collection folder is still "collection" here; "confirmed" covers a
		// later re-apply once it has flipped. no_match stays a no-op.
		if m.State == store.MatchStateCollection || m.State == store.MatchStateConfirmed {
			return r.applyCollection(seriesID)
		}
		return nil // nothing resolved (no_match)
	}

	issueMatches, err := r.st.ListIssueMatchesForSeries(seriesID)
	if err != nil {
		return err
	}

	// Series-level fields describe the volume, so they are written once. Prefer
	// an issue that actually belongs to the majority volume: an ambiguous series
	// is precisely the volume-split case, and the console offers Confirm on those
	// rows, so taking them from whichever issue happened to sort first could pair
	// the majority volume's id with a minority volume's publisher and description.
	majorityVolumeID, _ := strconv.ParseInt(m.VolumeID, 10, 64)
	var seriesSrc *localcv.Issue
	applied := 0
	var metronCredits []store.Creator
	metronSeen := map[string]bool{}
	metronApplied := false
	var gcdCredits []store.Creator
	gcdSeen := map[string]bool{}
	var gcdSrc *localcv.Issue // series fields for a GCD-matched folder
	genreTally := map[string]int{}

	for _, im := range issueMatches {
		if im.State == store.MatchStateCollection {
			continue
		}
		upstreamID, err := strconv.ParseInt(im.UpstreamID, 10, 64)
		if err != nil {
			continue
		}
		if im.Source == store.SourceGCD {
			if r.gcd == nil {
				continue
			}
			gi, credits, err := r.gcd.IssueByID(upstreamID)
			if err != nil {
				log.Printf("match: apply %s: gcd issue %d: %v", seriesID, upstreamID, err)
				continue
			}
			if err := r.st.UpsertIssueEnrichment(store.IssueEnrichment{
				IssueID:    im.IssueID,
				VineID:     0, // GCD has no ComicVine id; same as a Metron row without one
				Title:      gi.IssueName,
				Summary:    gi.Description, // GCD synopsis; was hardcoded ""
				CoverDate:  gi.CoverDate,
				PageCount:  gi.PageCount,
				Characters: charactersJSON(gi.CharacterNames),
				EnrichedAt: r.nowMs(),
			}); err != nil {
				return err
			}
			if gi.Genre != "" {
				genreTally[gi.Genre]++
			}
			for _, c := range credits {
				key := c.Name + "\x00" + c.Role
				if gcdSeen[key] {
					continue
				}
				gcdSeen[key] = true
				gcdCredits = append(gcdCredits, c)
			}
			if gcdSrc == nil {
				gcdSrc = gi
			}
			applied++
			continue
		}
		if im.Source == store.SourceMetron {
			if r.metron == nil {
				continue
			}
			// Unlike the dump path, this is a (cached, rate-limited) network call per issue — fine for a human-triggered Apply.
			mi, err := r.metron.GetIssue(int(upstreamID))
			if err != nil {
				log.Printf("match: apply %s: metron issue %d: %v", seriesID, upstreamID, err)
				continue
			}
			if err := r.st.UpsertIssueEnrichment(store.IssueEnrichment{
				IssueID:    im.IssueID,
				VineID:     mi.CVID, // 0 when Metron has no CV cross-reference
				Title:      mi.Title,
				Summary:    mi.Desc,
				CoverDate:  mi.CoverDate,
				EnrichedAt: r.nowMs(),
			}); err != nil {
				return err
			}
			// Collapse per-issue credits into a series roster: one entry per
			// (name, role), first-seen order, roles already lowercased.
			for _, cr := range mi.Credits {
				for _, role := range cr.Roles {
					key := cr.Creator + "\x00" + role
					if cr.Creator == "" || metronSeen[key] {
						continue
					}
					metronSeen[key] = true
					metronCredits = append(metronCredits, store.Creator{Name: cr.Creator, Role: role})
				}
			}
			metronApplied = true
			applied++
			continue
		}
		iss, err := r.cv.IssueByID(upstreamID)
		if err != nil {
			continue
		}

		// Take the first majority-volume issue; fall back to the first resolved
		// issue of any volume so a series row still gets written when nothing
		// resolved to the majority volume.
		if seriesSrc == nil || (seriesSrc.VolumeID != majorityVolumeID && iss.VolumeID == majorityVolumeID) {
			seriesSrc = iss
		}

		if err := r.st.UpsertIssueEnrichment(store.IssueEnrichment{
			IssueID:    im.IssueID,
			VineID:     int(upstreamID), // store.IssueEnrichment.VineID is int
			Title:      iss.IssueName,
			Summary:    iss.Description,
			CoverDate:  iss.CoverDate,
			Characters: charactersJSON(iss.CharacterNames),
			EnrichedAt: r.nowMs(),
		}); err != nil {
			return err
		}
		applied++
	}

	if applied == 0 {
		// Not an error: a human already confirmed this match and a resolution
		// failure must not roll that back. But it must not be invisible either.
		if len(issueMatches) > 0 {
			log.Printf("match: apply %s: none of %d issue matches resolved, no metadata written",
				seriesID, len(issueMatches))
		}
		return nil
	}

	// A GCD-matched folder: series fields come from GCD, the ComicVine
	// volume id is 0, and the dump-resolved path below is never reached.
	if seriesSrc == nil && gcdSrc != nil {
		if gcdCredits == nil {
			gcdCredits = []store.Creator{}
		}
		creatorsJSON, err := json.Marshal(gcdCredits)
		if err != nil {
			creatorsJSON = []byte("[]")
		}
		startYear, _ := strconv.Atoi(gcdSrc.VolumeStartYear)
		gcdSeriesID, _ := strconv.Atoi(m.VolumeID)
		return r.st.UpsertSeriesEnrichment(store.SeriesEnrichment{
			SeriesID:    seriesID,
			VolumeID:    0,
			Description: "",
			Publisher:   gcdSrc.Publisher,
			StartYear:   startYear,
			Creators:    string(creatorsJSON),
			Genre:       modeGenre(genreTally),
			GCDSeriesID: gcdSeriesID,
			Status:      "confirmed",
			VolumeName:  gcdSrc.VolumeName,
			EnrichedAt:  r.nowMs(),
		})
	}

	// store.SeriesEnrichment.VolumeID is int, not int64. m.VolumeID stays the
	// authority for the id even when seriesSrc fell back to another volume.
	volumeID, _ := strconv.Atoi(m.VolumeID)

	// No dump-resolved issue to source series fields from — every applied row
	// came through Metron. Take the series fields from Metron too.
	if seriesSrc == nil {
		if !metronApplied || r.metron == nil {
			return nil
		}
		ms, err := r.metron.SeriesByCVID(volumeID)
		if err != nil {
			log.Printf("match: apply %s: metron series cv_id=%d: %v", seriesID, volumeID, err)
			return nil // issue rows are already written; series row can wait for a re-apply
		}
		if metronCredits == nil {
			metronCredits = []store.Creator{}
		}
		creatorsJSON, err := json.Marshal(metronCredits)
		if err != nil {
			creatorsJSON = []byte("[]")
		}
		return r.st.UpsertSeriesEnrichment(store.SeriesEnrichment{
			SeriesID:    seriesID,
			VolumeID:    volumeID,
			Description: ms.Desc,
			Publisher:   ms.Publisher,
			StartYear:   ms.YearBegan,
			Creators:    string(creatorsJSON),
			Status:      "confirmed",
			VolumeName:  ms.Name,
			EnrichedAt:  r.nowMs(),
		})
	}

	startYear, _ := strconv.Atoi(seriesSrc.VolumeStartYear)
	return r.st.UpsertSeriesEnrichment(store.SeriesEnrichment{
		SeriesID:    seriesID,
		VolumeID:    volumeID,
		Description: seriesSrc.Description,
		Publisher:   seriesSrc.Publisher,
		StartYear:   startYear,
		Creators:    convertCredits(seriesSrc.PersonCredits),
		Genre:       modeGenre(genreTally), // empty for pure-CV; populated later by the merge
		Status:      "confirmed",           // existing vocabulary is 'auto' | 'confirmed'
		VolumeName:  seriesSrc.VolumeName,
		EnrichedAt:  r.nowMs(),
	})
}

// mergeSecondaryEnrichment does ONE opportunistic lookup in the other offline
// dump by canonical name+year and merges fields into the already-written
// enrichment rows — only on an exact, unique hit. Primary stays authoritative
// for identity; this only supplements metadata. Metron-primary series and
// missing dump handles are skipped.
func (r *Runner) mergeSecondaryEnrichment(seriesID string) error {
	// confirm() calls Apply (and so this) BEFORE flipping the row to
	// confirmed — see server/admin/match.go's confirm() doc comment — so
	// this must work off the pre-confirm state, exactly like applyPrimary
	// does off m.Source/m.VolumeID. No state check here.
	m, err := r.st.GetSeriesMatch(seriesID)
	if err != nil {
		return nil
	}
	var secondary string
	switch m.Source {
	case store.SourceComicVine:
		secondary = store.SourceGCD
	case store.SourceGCD:
		secondary = store.SourceComicVine
	default:
		return nil // Metron etc. — out of scope
	}
	if (secondary == store.SourceGCD && r.gcd == nil) ||
		(secondary == store.SourceComicVine && r.cv == nil) {
		return nil
	}

	series, err := r.st.GetSeries(seriesID)
	if err != nil {
		return nil
	}
	folder := parse.ParseFolder(filepath.Base(series.FolderPath))
	secID := r.pinByName(folder, nil, secondary) // "" unless exact-unique
	if secID == "" {
		return nil
	}
	secIDn, err := strconv.ParseInt(secID, 10, 64)
	if err != nil {
		return nil
	}

	primaryIsCV := m.Source == store.SourceComicVine

	// Build the secondary series row + issue rows from the secondary dump.
	secSeries, secIssues, err := r.secondaryEnrichment(secondary, secIDn)
	if err != nil || secSeries == nil {
		return nil // best-effort supplement; never fail the confirm
	}

	// Merge the series row.
	prim, err := r.st.GetSeriesEnrichment(seriesID)
	if err != nil {
		return nil
	}
	merged := mergeSeriesFields(*prim, *secSeries, primaryIsCV)
	if err := r.st.UpsertSeriesEnrichment(merged); err != nil {
		// Best-effort supplement: the primary rows already wrote successfully,
		// so a transient write error here must not fail the confirm.
		log.Printf("match: merge %s: upsert series enrichment: %v", seriesID, err)
		return nil
	}

	// Merge issue rows, joining local issues to secondary issues by number.
	locals, err := r.st.ListIssues(seriesID)
	if err != nil {
		return nil
	}
	primIssues, err := r.st.ListIssueEnrichmentForSeries(seriesID)
	if err != nil {
		return nil
	}
	for _, li := range locals {
		pe, ok := primIssues[li.ID]
		if !ok {
			continue // unmatched primary issue — nothing to supplement
		}
		se, ok := secIssues[parse.NormalizeGCDNumber(li.Number)]
		if !ok {
			continue
		}
		mi := mergeIssueFields(pe, se, primaryIsCV)
		if err := r.st.UpsertIssueEnrichment(mi); err != nil {
			// Best-effort supplement, same as above — abandon the merge for
			// this series rather than fail the confirm.
			log.Printf("match: merge %s: upsert issue enrichment %s: %v", seriesID, li.ID, err)
			return nil
		}
	}
	if err := r.st.TouchSeries(seriesID); err != nil {
		log.Printf("match: merge %s: touch series: %v", seriesID, err)
	}
	return nil
}

// secondaryEnrichment reads a series' fields + per-issue fields from one dump,
// shaped as store enrichment rows keyed by normalized issue number.
//
// Both branches key their issue map with parse.NormalizeGCDNumber, not just
// the GCD branch: the join in mergeSecondaryEnrichment above normalizes the
// local issue number the same way, and a raw ComicVine issue_number ("1 (of
// 6)"-shaped dumps aside, even a plain "1" needs the same normalization path
// applied consistently) must match it exactly for a GCD-primary/CV-secondary
// series to join at all.
func (r *Runner) secondaryEnrichment(source string, id int64) (*store.SeriesEnrichment, map[string]store.IssueEnrichment, error) {
	issues := map[string]store.IssueEnrichment{}
	se := &store.SeriesEnrichment{}
	if source == store.SourceGCD {
		vol, err := r.gcd.SeriesByID(id)
		if err != nil {
			return nil, nil, err
		}
		yr, _ := strconv.Atoi(vol.StartYear)
		se.Publisher, se.StartYear, se.GCDSeriesID = vol.Publisher, yr, int(id)
		gis, err := r.gcd.IssuesBySeries(id)
		if err != nil {
			return nil, nil, err
		}
		genreTally := map[string]int{}
		for _, gi := range gis {
			issues[parse.NormalizeGCDNumber(gi.IssueNumber)] = store.IssueEnrichment{
				Title: gi.IssueName, Summary: gi.Description, CoverDate: gi.CoverDate,
				PageCount: gi.PageCount, Characters: charactersJSON(gi.CharacterNames),
			}
			if gi.Genre != "" {
				genreTally[gi.Genre]++
			}
		}
		se.Genre = modeGenre(genreTally)
		return se, issues, nil
	}
	// ComicVine dump.
	vol, err := r.cv.VolumeByID(id)
	if err != nil {
		return nil, nil, err
	}
	yr, _ := strconv.Atoi(vol.StartYear)
	se.Description, se.Publisher, se.StartYear = vol.Description, vol.Publisher, yr
	cis, err := r.cv.IssuesByVolume(id)
	if err != nil {
		return nil, nil, err
	}
	for i, ci := range cis {
		issues[parse.NormalizeGCDNumber(ci.IssueNumber)] = store.IssueEnrichment{
			Title: ci.IssueName, Summary: ci.Description, CoverDate: ci.CoverDate,
			Characters: charactersJSON(ci.CharacterNames),
		}
		if i == 0 { // representative issue → CV series creators
			se.Creators = convertCredits(ci.PersonCredits)
		}
	}
	return se, issues, nil
}

// applyCollection is Apply for a confirmed collection folder: no folder
// volume, so each file's metadata comes from its own trade volume. The
// trade's volume name becomes the file title; the series keeps its local name
// and year and takes only a publisher from the files.
func (r *Runner) applyCollection(seriesID string) error {
	issueMatches, err := r.st.ListIssueMatchesForSeries(seriesID)
	if err != nil {
		return err
	}
	s, err := r.st.GetSeries(seriesID)
	if err != nil {
		return err
	}

	publishers := map[string]int{}
	applied := 0
	for _, im := range issueMatches {
		if im.Source != store.SourceComicVine {
			continue // ponytail: Metron-bound trades are rare; add when one shows up
		}
		upstreamID, err := strconv.ParseInt(im.UpstreamID, 10, 64)
		if err != nil {
			continue
		}
		iss, err := r.cv.IssueByID(upstreamID)
		if err != nil {
			log.Printf("match: apply %s: issue %d: %v", seriesID, upstreamID, err)
			continue
		}
		summary := iss.Description
		if summary == "" {
			if vol, verr := r.cv.VolumeByID(iss.VolumeID); verr == nil {
				summary = vol.Description
			}
		}
		if err := r.st.UpsertIssueEnrichment(store.IssueEnrichment{
			IssueID:    im.IssueID,
			VineID:     int(upstreamID),
			Title:      iss.VolumeName,
			Summary:    summary,
			CoverDate:  iss.CoverDate,
			EnrichedAt: r.nowMs(),
		}); err != nil {
			return err
		}
		if iss.Publisher != "" {
			publishers[iss.Publisher]++
		}
		applied++
	}
	if applied == 0 {
		if len(issueMatches) > 0 {
			log.Printf("match: apply %s: none of %d issue matches resolved, no metadata written",
				seriesID, len(issueMatches))
		}
		return nil
	}

	publisher, best := "", 0
	for p, n := range publishers {
		if n > best || (n == best && p < publisher) {
			publisher, best = p, n
		}
	}
	return r.st.UpsertSeriesEnrichment(store.SeriesEnrichment{
		SeriesID:   seriesID,
		VolumeID:   0,
		Publisher:  publisher,
		StartYear:  s.Year,
		Creators:   "[]",
		Status:     "confirmed",
		EnrichedAt: r.nowMs(),
	})
}

// charactersJSON marshals character names into the [{"name":…}] JSON stored in
// issue_enrichment.characters. Empty input yields "[]".
func charactersJSON(names []string) string {
	arr := make([]struct {
		Name string `json:"name"`
	}, 0, len(names))
	for _, n := range names {
		arr = append(arr, struct {
			Name string `json:"name"`
		}{n})
	}
	b, err := json.Marshal(arr)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// modeGenre returns the most frequent genre; ties resolve arbitrarily (map
// iteration order), which is fine for a best-effort badge — or "" when the
// tally is empty.
func modeGenre(tally map[string]int) string {
	best, n := "", 0
	for g, c := range tally {
		if c > n {
			best, n = g, c
		}
	}
	return best
}

// convertCredits reshapes the dump's person_credits array into the JSON the
// store expects. An unparseable value yields an empty array rather than an
// error: bad credits must not block an otherwise good match.
func convertCredits(raw string) string {
	if raw == "" {
		return "[]"
	}
	var in []cvCredit
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return "[]"
	}
	out := make([]store.Creator, 0, len(in))
	for _, c := range in {
		if c.Name == "" {
			continue
		}
		out = append(out, store.Creator{ID: c.ID, Name: c.Name, Role: c.Role})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}
