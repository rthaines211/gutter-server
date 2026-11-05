package match

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/ryanhaines/comic-reader/match/localcv"
	"github.com/ryanhaines/comic-reader/match/pagehash"
	"github.com/ryanhaines/comic-reader/match/parse"
	"github.com/ryanhaines/comic-reader/store"
)

// Misfiled is a file in a confirmed folder whose first-page hash belongs to
// a different (non-annual) volume: probably the wrong folder, a human's call.
type Misfiled struct{ IssueID, Filename, VolumeID string }

// FillResult reports what FillConfirmed did to a confirmed series' new files.
type FillResult struct {
	Bound    []string
	Unbound  []string
	Misfiled []Misfiled
}

// FillConfirmed binds the issues of a confirmed series that have no match
// row yet: new pulls into a known folder. It binds through the series'
// confirmed source, offline dump first, and never touches the series' own
// confirmed state.
func (r *Runner) FillConfirmed(ctx context.Context, s store.Series) (FillResult, error) {
	var res FillResult
	m, err := r.st.GetSeriesMatch(s.ID)
	if err != nil {
		return res, err
	}
	if m.State != store.MatchStateConfirmed || m.DontMatch || m.VolumeID == "" {
		return res, nil
	}
	vid, err := strconv.ParseInt(m.VolumeID, 10, 64)
	if err != nil {
		return res, fmt.Errorf("series %s: bad volume id %q", s.ID, m.VolumeID)
	}

	issues, err := r.st.ListIssues(s.ID)
	if err != nil {
		return res, err
	}
	matches, err := r.st.ListIssueMatchesForSeries(s.ID)
	if err != nil {
		return res, err
	}
	has := make(map[string]bool, len(matches))
	for _, im := range matches {
		has[im.IssueID] = true
	}
	folder := parse.ParseFolder(baseName(s.FolderPath, s.Name))

	// items is every unmatched issue; numbered is the subset the number
	// passes may bind. Annuals bind only by first-page hash (spec §2).
	var items, numbered []unmatchedIssue
	for _, iss := range issues {
		// Every issue, matched or not (Ruling E): a touched series then
		// renames consistently instead of mixing year/no-year templates.
		f := parse.ParseFile(filepath.Base(iss.Filename))
		if err := r.recordParsedFormat(iss.ID, f); err != nil {
			return res, err
		}
		if has[iss.ID] {
			continue
		}
		items = append(items, unmatchedIssue{iss, f})
		if f.Format != "annual" {
			numbered = append(numbered, unmatchedIssue{iss, f})
		}
	}
	if len(items) == 0 {
		return res, nil
	}

	bound := map[string]bool{}
	switch m.Source {
	case store.SourceGCD:
		if r.gcd != nil {
			up, err := r.gcd.IssuesBySeries(vid)
			if err != nil {
				return res, err
			}
			if _, _, _, err := r.bindByNumber(ctx, folder, numbered, up, store.SourceGCD, map[string]string{}); err != nil {
				return res, err
			}
		}
	case store.SourceMetron:
		r.metronBind(ctx, folder, numbered, vid)
	default: // comicvine
		var rest []unmatchedIssue
		for _, u := range items {
			hit, mis, err := r.fillByHash(s, u, vid)
			if err != nil {
				return res, err
			}
			if mis != nil {
				res.Misfiled = append(res.Misfiled, *mis)
				bound[u.iss.ID] = true // settled: not unbound, not retried below
				continue
			}
			if !hit && u.file.Format != "annual" {
				rest = append(rest, u)
			}
		}
		if len(rest) > 0 {
			up, err := r.cv.IssuesByVolume(vid)
			if err != nil {
				return res, err
			}
			_, _, remaining, err := r.bindByNumber(ctx, folder, rest, up, store.SourceComicVine, map[string]string{})
			if err != nil {
				return res, err
			}
			if r.live != nil && len(remaining) > 0 {
				_, remaining, _ = r.liveBind(ctx, folder, remaining, vid)
			}
			r.metronBind(ctx, folder, remaining, vid)
		}
	}

	// Re-read: the bind helpers write rows rather than returning ids.
	after, err := r.st.ListIssueMatchesForSeries(s.ID)
	if err != nil {
		return res, err
	}
	now := make(map[string]bool, len(after))
	for _, im := range after {
		now[im.IssueID] = true
	}
	for _, u := range items {
		switch {
		case now[u.iss.ID]:
			res.Bound = append(res.Bound, u.iss.ID)
		case !bound[u.iss.ID]:
			res.Unbound = append(res.Unbound, u.iss.Filename)
		}
	}
	return res, nil
}

// fillByHash tries the first-page hash for one new file. hit means a match
// row was written (into the confirmed volume, or an annual's own volume);
// mis is set when the hash belongs to another volume.
func (r *Runner) fillByHash(s store.Series, u unmatchedIssue, vid int64) (bool, *Misfiled, error) {
	sum, err := pagehash.FirstPageMD5(filepath.Join(store.IssueDir(s.FolderPath, u.iss.ID, u.iss.Filename), u.iss.Filename))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && r.libraryGone() {
			return false, nil, err
		}
		return false, nil, nil // unreadable file: leave it to the number passes
	}
	up, err := r.cv.IssueByFirstPageMD5(sum)
	if errors.Is(err, localcv.ErrNotFound) || (err == nil && isReprintPublisher(up.Publisher)) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if up.VolumeID != vid && u.file.Format != "annual" {
		return false, &Misfiled{IssueID: u.iss.ID, Filename: u.iss.Filename,
			VolumeID: strconv.FormatInt(up.VolumeID, 10)}, nil
	}
	folder := parse.ParseFolder(baseName(s.FolderPath, s.Name))
	v := Verify(folder, u.file, up)
	return true, nil, r.st.UpsertIssueMatch(store.IssueMatch{
		IssueID:    u.iss.ID,
		Source:     store.SourceComicVine,
		UpstreamID: strconv.FormatInt(up.IssueID, 10),
		State:      v.State,
		Method:     MethodFirstPageMD5,
	})
}
