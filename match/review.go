package match

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rthaines211/gutter-server/match/gcd"
	"github.com/rthaines211/gutter-server/match/localcv"
	"github.com/rthaines211/gutter-server/match/parse"
	"github.com/rthaines211/gutter-server/store"
)

// MethodHumanRepin records an issue match a human rebound in the console.
const MethodHumanRepin = "human_repin"

// RepinIssue replaces an issue's match with a human-chosen upstream issue,
// in the dump they chose it from. The id is validated against that same dump:
// ComicVine and GCD id spaces overlap, so a GCD id checked against ComicVine
// can "resolve" to an unrelated issue.
func (r *Runner) RepinIssue(issueID, source, upstreamID string) error {
	if _, err := r.LookupIssue(source, upstreamID); err != nil {
		return err
	}
	return r.st.ReplaceIssueMatch(store.IssueMatch{
		IssueID:    issueID,
		Source:     source,
		UpstreamID: upstreamID,
		State:      store.MatchStateHighConfidence,
		Confidence: 1,
		Method:     MethodHumanRepin,
	})
}

// LookupIssueInVolume finds the volume's issue whose number matches the local
// one, for offering a re-pin target in the console. localcv.ErrNotFound when
// the volume has no such number.
func (r *Runner) LookupIssueInVolume(source, volumeID, number string) (*localcv.Issue, error) {
	vid, err := strconv.ParseInt(volumeID, 10, 64)
	if err != nil {
		return nil, err
	}
	issues, err := r.issuesFor(source, vid)
	if err != nil {
		return nil, err
	}
	want := parse.NormalizeNumber(number)
	for i := range issues {
		if parse.NormalizeNumber(issues[i].IssueNumber) == want {
			return &issues[i], nil
		}
	}
	return nil, localcv.ErrNotFound
}

// Reverify recomputes a series' penalties and state from its stored issue
// matches, after a human changed something (re-pin, volume choice). It keeps
// the stored volume choice rather than recomputing the majority, and never
// touches a confirmed or dont_match row.
func (r *Runner) Reverify(seriesID string) error {
	m, err := r.st.GetSeriesMatch(seriesID)
	if err != nil {
		return err
	}
	if m.DontMatch || m.State == store.MatchStateConfirmed {
		return nil
	}
	s, err := r.st.GetSeries(seriesID)
	if err != nil {
		return err
	}
	issues, err := r.st.ListIssues(seriesID)
	if err != nil {
		return err
	}
	matches, err := r.st.ListIssueMatchesForSeries(seriesID)
	if err != nil {
		return err
	}
	byIssue := make(map[string]store.IssueMatch, len(matches))
	for _, im := range matches {
		byIssue[im.IssueID] = im
	}

	folder := parse.ParseFolder(baseName(s.FolderPath, s.Name))
	stateVote := map[string]int{}
	volumeVotes := map[string]int{}
	penalties := map[string]string{}
	// Collection-folder detection, mirroring the runner (match/runner.go):
	// how many resolved issues were collections.
	resolved := 0
	collectionHits := 0

	for _, iss := range issues {
		im, ok := byIssue[iss.ID]
		if !ok {
			stateVote[store.MatchStateNoMatch]++
			continue
		}
		up, err := r.LookupIssue(im.Source, im.UpstreamID)
		if err != nil {
			if errors.Is(err, localcv.ErrNotFound) || errors.Is(err, gcd.ErrNotFound) {
				stateVote[store.MatchStateNoMatch]++
				continue
			}
			return err
		}
		file := parse.ParseFile(filepath.Base(iss.Filename))
		v := Verify(folder, file, up)
		stateVote[v.State]++
		for k, msg := range v.Penalties {
			penalties[k] = msg
		}
		vol := strconv.FormatInt(up.VolumeID, 10)
		volumeVotes[vol]++
		resolved++
		if file.IsCollection {
			collectionHits++
		}
	}

	// Collection folder: every resolved issue was a trade and each one pinned a
	// different volume — no folder-level volume exists. Keep the per-file
	// matches (already stored) and clear the volume, same rule the runner uses.
	// A human's manual volume pin (m.VolumeID != "") overrides this — don't
	// silently reset it back to "".
	if m.VolumeID == "" && collectionHits > 0 && collectionHits == resolved && resolved == len(volumeVotes) && len(volumeVotes) > 1 {
		return r.st.UpdateSeriesMatchReview(seriesID, "", store.MatchStateCollection, "{}", r.nowMs())
	}

	state := dominantState(stateVote)
	majority, split := majorityVolume(volumeVotes)
	if split != "" {
		penalties["volume"] = split
		state = store.MatchStateAmbiguous
	}

	// A human's volume choice stands; the majority only fills a blank.
	volumeID := m.VolumeID
	if volumeID == "" {
		volumeID = majority
	}

	penJSON, _ := json.Marshal(penalties)
	return r.st.UpdateSeriesMatchReview(seriesID, volumeID, state, string(penJSON), r.nowMs())
}

// LookupVolume resolves a series_match.volume_id for display/planning.
func (r *Runner) LookupVolume(source, volumeID string) (*localcv.Volume, error) {
	id, err := strconv.ParseInt(volumeID, 10, 64)
	if err != nil {
		return nil, err
	}
	if source == store.SourceGCD {
		if r.gcd == nil {
			return nil, gcd.ErrNotFound
		}
		return r.gcd.SeriesByID(id)
	}
	return r.cv.VolumeByID(id)
}

// SoleIssueInVolume returns a volume's only issue, for pinning a standalone
// trade. ComicVine files each collected edition as its own one-issue volume
// whose issue is numbered 1 — "Black Science: True Atonement" is volume 96273,
// issue 563740, number "1", named "Volume 5". A local file numbered 5 can
// therefore never be matched into it by number, which is what
// LookupIssueInVolume does; with exactly one issue there is nothing to
// disambiguate, so the number is not needed.
//
// localcv.ErrNotFound when the volume holds more than one issue: there the
// number is the only thing that could pick between them, and guessing would
// pin the wrong issue silently.
func (r *Runner) SoleIssueInVolume(source, volumeID string) (*localcv.Issue, error) {
	vid, err := strconv.ParseInt(volumeID, 10, 64)
	if err != nil {
		return nil, err
	}
	issues, err := r.issuesFor(source, vid)
	if err != nil {
		return nil, err
	}
	if len(issues) != 1 {
		return nil, localcv.ErrNotFound
	}
	return &issues[0], nil
}

// VolumeCoverURL is the cover of a ComicVine volume's lowest-numbered issue,
// the picture a reviewer recognises a series by. "" for GCD (no cover URLs), a
// bad id, or a volume with no issues.
func (r *Runner) VolumeCoverURL(source, volumeID string) string {
	if source == store.SourceGCD {
		return ""
	}
	vid, err := strconv.ParseInt(volumeID, 10, 64)
	if err != nil {
		return ""
	}
	issues, err := r.issuesFor(source, vid)
	if err != nil {
		return ""
	}
	best, bestN, found := "", 0.0, false
	for _, iss := range issues {
		n, err := strconv.ParseFloat(parse.NormalizeNumber(iss.IssueNumber), 64)
		if err != nil {
			continue
		}
		if !found || n < bestN {
			best, bestN, found = iss.ImageURL, n, true
		}
	}
	return MediumCover(best)
}

// MediumCover swaps a ComicVine dump image URL's /original/ for the much
// smaller /scale_medium/ rendition.
func MediumCover(u string) string {
	return strings.Replace(u, "/original/", "/scale_medium/", 1)
}
