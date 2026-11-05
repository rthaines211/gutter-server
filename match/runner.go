package match

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ryanhaines/comic-reader/comicvine"
	"github.com/ryanhaines/comic-reader/match/gcd"
	"github.com/ryanhaines/comic-reader/match/localcv"
	"github.com/ryanhaines/comic-reader/match/pagehash"
	"github.com/ryanhaines/comic-reader/match/parse"
	"github.com/ryanhaines/comic-reader/metron"
	"github.com/ryanhaines/comic-reader/store"
)

// MethodFirstPageMD5 records how a match was reached, so the console can show
// provenance and Phase 1B can tell its own matches apart from these.
const MethodFirstPageMD5 = "first_page_md5"

// MethodVolumeNumber records an issue bound by its number inside an
// already-pinned volume, rather than by an exact first-page hash.
const MethodVolumeNumber = "volume_number"

// MethodNameYear marks a series whose volume was pinned by exact folder name
// and start year because nothing in it hashed, then bound by issue number.
// The issue rows under it carry MethodVolumeNumber as usual.
const MethodNameYear = "name_year"

// MethodHumanVolume marks a series whose volume a human chose in the console
// for a folder nothing had matched; its issues were then bound by number.
const MethodHumanVolume = "human_volume"

// MethodLiveVolumeNumber is MethodVolumeNumber resolved against the live
// ComicVine API instead of the offline dump, whose hash and issue data end
// at Feb 2026 — the only path for comics newer than that.
const MethodLiveVolumeNumber = "live_volume_number"

// MethodMetronVolumeNumber is a number-bind against Metron's issue list for
// the series with the matching ComicVine cross-reference id — the fallback
// for issues neither the dump nor live ComicVine could bind.
const MethodMetronVolumeNumber = "metron_volume_number"

type Runner struct {
	st     *store.Store
	cv     *localcv.DB
	live   *comicvine.Client // nil = offline only
	metron *metron.Client    // nil = no fallback
	roots  []string
	nowMs  func() int64

	gcd *gcd.DB // nil = GCD pass off

	// Volume indexes by canonical name, one per dump, built on first use and
	// dropped at the start of each Run so a replaced dump is picked up.
	// Guarded by volsMu: the console can search while a run goes.
	volsMu  sync.Mutex
	cvVols  *volumeIndex
	gcdVols *volumeIndex
}

type volumeIndex struct {
	all   []localcv.Volume
	byKey map[string][]localcv.Volume
}

// volumes returns the canonical-name index for one dump, building it on
// first use. GCD returns an empty index when not configured.
func (r *Runner) volumes(source string) (*volumeIndex, error) {
	r.volsMu.Lock()
	defer r.volsMu.Unlock()
	slot := &r.cvVols
	if source == store.SourceGCD {
		slot = &r.gcdVols
	}
	if *slot != nil {
		return *slot, nil
	}
	var all []localcv.Volume
	var err error
	switch source {
	case store.SourceGCD:
		if r.gcd != nil {
			all, err = r.gcd.AllSeries()
		}
	default:
		all, err = r.cv.AllVolumes()
	}
	if err != nil {
		return nil, err
	}
	idx := &volumeIndex{all: all, byKey: make(map[string][]localcv.Volume, len(all))}
	for _, v := range all {
		k := parse.CanonicalKey(v.Name)
		idx.byKey[k] = append(idx.byKey[k], v)
	}
	*slot = idx
	return idx, nil
}

// issuesFor lists a volume's (ComicVine) or series' (GCD) issues.
func (r *Runner) issuesFor(source string, id int64) ([]localcv.Issue, error) {
	if source == store.SourceGCD {
		if r.gcd == nil {
			return nil, gcd.ErrNotFound
		}
		return r.gcd.IssuesBySeries(id)
	}
	return r.cv.IssuesByVolume(id)
}

// SearchHit is a console search result: a volume and which dump it is from.
type SearchHit struct {
	localcv.Volume
	Source string
}

// SearchVolumes finds volumes in both dumps whose canonical name contains
// every word of q, ComicVine first, for the console's manual pin. A linear
// scan over ~250k names is a few milliseconds.
func (r *Runner) SearchVolumes(q string, limit int) ([]SearchHit, error) {
	var words []string
	for _, w := range strings.Fields(q) {
		if k := parse.CanonicalKey(w); k != "" {
			words = append(words, k)
		}
	}
	if len(words) == 0 {
		return nil, nil
	}
	var out []SearchHit
	for _, source := range []string{store.SourceComicVine, store.SourceGCD} {
		idx, err := r.volumes(source)
		if err != nil {
			return nil, err
		}
		var hits []localcv.Volume
		for _, v := range idx.all {
			k := parse.CanonicalKey(v.Name)
			ok := true
			for _, w := range words {
				if !strings.Contains(k, w) {
					ok = false
					break
				}
			}
			if ok {
				hits = append(hits, v)
			}
		}
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].StartYear != hits[j].StartYear {
				return hits[i].StartYear > hits[j].StartYear
			}
			return hits[i].Name < hits[j].Name
		})
		if len(hits) > limit {
			hits = hits[:limit]
		}
		for _, v := range hits {
			out = append(out, SearchHit{Volume: v, Source: source})
		}
	}
	return out, nil
}

// SetLive enables live ComicVine lookups for issues the offline dump cannot
// match. The client is rate-limited and disk-cached, so re-runs are free.
func (r *Runner) SetLive(c *comicvine.Client) { r.live = c }

// SetMetron enables the Metron fallback for issues and series ComicVine
// cannot match. The client is rate-limited and disk-cached like the CV one.
func (r *Runner) SetMetron(c *metron.Client) { r.metron = c }

// SetGCD enables the Grand Comics Database pass for folders no ComicVine
// pass could pin, and GCD results in the console search.
func (r *Runner) SetGCD(db *gcd.DB) { r.gcd = db }

// HasGCD reports whether the GCD pass and search are configured.
func (r *Runner) HasGCD() bool { return r.gcd != nil }

// unmatchedIssue pairs an issue that missed a matching pass with its already
// parsed filename, so later passes don't have to reparse it.
type unmatchedIssue struct {
	iss  store.Issue
	file parse.File
}

// NewRunner takes the configured library roots so it can tell an unmounted
// drive from a renamed folder. See libraryGone.
func NewRunner(st *store.Store, cv *localcv.DB, roots []string, nowMs func() int64) *Runner {
	return &Runner{st: st, cv: cv, roots: roots, nowMs: nowMs}
}

type Result struct {
	Series int            `json:"series"`
	Issues int            `json:"issues"`
	States map[string]int `json:"states"`
	Errors int            `json:"errors"`
}

// Run matches every series that has no human decision recorded against it.
func (r *Runner) Run(ctx context.Context) (Result, error) {
	res := Result{States: map[string]int{}}

	all, err := r.st.ListSeries()
	if err != nil {
		return res, err
	}
	r.volsMu.Lock()
	r.cvVols, r.gcdVols = nil, nil // a dump may have been replaced since the last run
	r.volsMu.Unlock()

	for _, s := range all {
		if err := ctx.Err(); err != nil {
			return res, err
		}

		existing, err := r.st.GetSeriesMatch(s.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return res, err
		}
		if existing != nil && (existing.DontMatch || existing.State == store.MatchStateConfirmed) {
			// Confirmed series are settled, but issues bought after
			// confirmation have no match row and the offline passes will
			// never see them again. Fill those from the live volume.
			if existing.State == store.MatchStateConfirmed && !existing.DontMatch {
				fr, err := r.FillConfirmed(ctx, s)
				if err != nil {
					log.Printf("match: fill %s: %v", s.ID, err)
					res.Errors++
				}
				res.Issues += len(fr.Bound)
			}
			continue
		}

		n, state, err := r.matchSeries(ctx, s, "", "")
		if err != nil {
			// A vanished path means the external drive went away. Stop the
			// whole run rather than marking every remaining series no_match.
			if errors.Is(err, fs.ErrNotExist) {
				return res, err
			}
			log.Printf("match: series %s: %v", s.ID, err)
			res.Errors++
			continue
		}

		res.Series++
		res.Issues += n
		res.States[state]++
	}
	return res, nil
}

// Rematch runs the passes for one series with the volume a human chose in
// the console, in the dump they chose it from: the folder is bound by number
// into that volume, then scored like any other. For a no_match folder this is
// the only way in — Reverify only rescores issue matches that already exist.
func (r *Runner) Rematch(ctx context.Context, seriesID, source, volumeID string) error {
	s, err := r.st.GetSeries(seriesID)
	if err != nil {
		return err
	}
	_, _, err = r.matchSeries(ctx, *s, source, volumeID)
	return err
}

// MatchSeries runs the unforced passes for one series: the auto pipeline's
// entry point for a new or changed folder.
func (r *Runner) MatchSeries(ctx context.Context, s store.Series) (int, string, error) {
	return r.matchSeries(ctx, s, "", "")
}

// AutoConfirmable is the only rule under which the pipeline confirms without
// a human: a first-page hash anchored the volume, nothing disagreed, and the
// folder is a single series. Name+year pins (the 5% the user corrects by
// hand) and collection folders always go to review.
func AutoConfirmable(m *store.SeriesMatch) bool {
	return m != nil &&
		m.State == store.MatchStateHighConfidence &&
		m.Method == MethodFirstPageMD5 &&
		(m.Penalties == "" || m.Penalties == "{}") &&
		m.VolumeID != ""
}

// recordParsedFormat stores what the filename says the file is: annual or
// single. Collections are left blank here; Verify supplies their format
// once matched.
func (r *Runner) recordParsedFormat(issueID string, f parse.File) error {
	switch {
	case f.IsCollection:
		return nil
	case f.Format == "annual":
		return r.st.SetIssueFormatIfBlank(issueID, "annual")
	default:
		return r.st.SetIssueFormatIfBlank(issueID, "single")
	}
}

// matchSeries runs the passes for one series. forced, when set, is a volume
// id a human chose in forcedSource; it replaces the hash/name anchor.
func (r *Runner) matchSeries(ctx context.Context, s store.Series, forcedSource, forced string) (int, string, error) {
	issues, err := r.st.ListIssues(s.ID)
	if err != nil {
		return 0, "", err
	}

	folder := parse.ParseFolder(baseName(s.FolderPath, s.Name))

	var (
		matched     int
		stateVote   = map[string]int{}
		volumeVotes = map[string]int{}
		penalties   = map[string]string{}
		// Collection-folder detection: how many MD5 hits were collections.
		collectionHits int
	)

	var unmatched []unmatchedIssue

	for _, iss := range issues {
		if err := ctx.Err(); err != nil {
			return matched, "", err
		}

		file := parse.ParseFile(filepath.Base(iss.Filename))
		if err := r.recordParsedFormat(iss.ID, file); err != nil {
			return matched, "", err
		}

		// issues.filename is a bare basename; the folder comes from the issue
		// id, since a series can span twin folders (store.IssueDir).
		archivePath := filepath.Join(store.IssueDir(s.FolderPath, iss.ID, iss.Filename), iss.Filename)

		sum, err := pagehash.FirstPageMD5(archivePath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && r.libraryGone() {
				// The drive is unmounted. Abort the batch — see libraryGone.
				return matched, "", err
			}
			// Either a stale or renamed DB row (the file moved but the library
			// is still mounted) or a corrupt/unreadable archive — 28 are
			// already known in this library. Count it as unmatched, keep going.
			// An annual binds only by hash (spec §2): it stays unbound and,
			// like an annual hit (Ruling B), casts no vote.
			if file.Format != "annual" {
				stateVote[store.MatchStateNoMatch]++
				unmatched = append(unmatched, unmatchedIssue{iss, file})
			}
			continue
		}

		up, err := r.cv.IssueByFirstPageMD5(sum)
		// A hash resolving to an international reprint is the same pages under
		// the wrong volume — treat it as no hit and let pass 2 bind by number.
		if err == nil && isReprintPublisher(up.Publisher) {
			err = localcv.ErrNotFound
		}
		if errors.Is(err, localcv.ErrNotFound) {
			if file.Format != "annual" { // see above: annuals bind only by hash
				stateVote[store.MatchStateNoMatch]++
				unmatched = append(unmatched, unmatchedIssue{iss, file})
			}
			continue
		}
		if err != nil {
			return matched, "", err
		}

		v := Verify(folder, file, up)
		// Ruling B: ComicVine files annuals as their own volumes. An annual
		// still records its own issue_match (with its own Verify state) and
		// counts toward matched, but its state, penalties and volume vote
		// must NOT reach the folder's vote — not just the volume vote, since
		// dominantState goes ambiguous if any vote is ambiguous, so one
		// annual could still poison its parent series. See spec Annuals (#6).
		if file.Format != "annual" {
			stateVote[v.State]++
			for k, msg := range v.Penalties {
				penalties[k] = msg
			}
			vol := strconv.FormatInt(up.VolumeID, 10)
			volumeVotes[vol]++
		}
		matched++
		if file.IsCollection {
			collectionHits++
			if err := r.st.SetIssueFormatIfBlank(iss.ID, v.Format); err != nil {
				return matched, "", err
			}
			// The scanner leaves number blank for trades (it strips "v01" as a
			// volume marker); without this the organizer skips them forever.
			if err := r.st.SetIssueNumberIfBlank(iss.ID, file.Number); err != nil {
				return matched, "", err
			}
		}

		if err := r.st.UpsertIssueMatch(store.IssueMatch{
			IssueID:    iss.ID,
			Source:     store.SourceComicVine,
			UpstreamID: strconv.FormatInt(up.IssueID, 10),
			State:      v.State,
			Confidence: 0,
			Method:     MethodFirstPageMD5,
		}); err != nil {
			return matched, "", err
		}
	}

	// Pass 2: bind by (volume, issue number) whatever pass 1 left unmatched,
	// once something has told us which volume this folder actually is.
	// Normally that anchor is an MD5 hit. With zero hits, the dump's volume
	// list is the fallback anchor: exact folder name, start year within
	// yearSlack, exactly one candidate — and it only sticks if issue numbers
	// then bind, so a wrong pin leaves the folder no_match just as before.
	// (The scene-release hash index covers ~3% of 2026 issues; the volume
	// and issue rows for those series are all present.)
	seriesMethod := MethodFirstPageMD5
	seriesSource := store.SourceComicVine
	// A human pinned this folder to a GCD series in the console. Every
	// ComicVine-side pass below is skipped for it: binding these files into a
	// ComicVine volume, or letting Metron place the folder, is precisely what
	// they overrode. Only the GCD pass at the bottom runs.
	forcedGCD := forced != "" && forcedSource == store.SourceGCD
	pinnedVolume, _ := majorityVolume(volumeVotes)
	switch {
	case forcedGCD:
		pinnedVolume = ""
	case forced != "":
		pinnedVolume, seriesMethod = forced, MethodHumanVolume
	case pinnedVolume == "" && len(unmatched) > 0:
		if pinnedVolume = r.pinByName(folder, unmatched, store.SourceComicVine); pinnedVolume != "" {
			seriesMethod = MethodNameYear
		}
	}
	if pinnedVolume != "" && len(unmatched) > 0 {
		if vid, err := strconv.ParseInt(pinnedVolume, 10, 64); err == nil {
			upIssues, err := r.cv.IssuesByVolume(vid)
			if err != nil {
				return matched, "", err
			}
			// What the dump's volume list can't bind goes to the live
			// pass: the dump ends at Feb 2026, new issues live past it.
			states, bound, remaining, err := r.bindByNumber(ctx, folder, unmatched, upIssues, store.SourceComicVine, penalties)
			if err != nil {
				return matched, "", err
			}
			for st, n := range states {
				stateVote[st] += n
			}
			if bound > 0 {
				volumeVotes[pinnedVolume] += bound
			}
			matched += bound

			// A name pin that bound nothing offline is a guess we don't
			// spend API budget on; the folder falls through to no_match.
			if seriesMethod == MethodNameYear && bound == 0 {
				remaining = nil
				seriesMethod = MethodFirstPageMD5 // the pin didn't stick; don't record it
			}
			// A human's choice is not second-guessed: bind what the dump
			// can, let live/Metron try the rest, and record the method.
			// Pass 3: live ComicVine, same volume, same rule — bind by
			// number. Only runs with a pinned volume, like pass 2.
			liveStates, liveLeft, err := r.liveBind(ctx, folder, remaining, vid)
			if err != nil {
				return matched, "", err
			}
			for st, n := range liveStates {
				stateVote[st] += n
				matched += n
			}
			// Pass 4: Metron, for what live ComicVine couldn't bind.
			for st, n := range r.metronBind(ctx, folder, liveLeft, vid) {
				stateVote[st] += n
				matched += n
			}
		}
	}

	// Metron fallback: a folder with zero volume votes is invisible to every
	// CV pass (nothing hashed, so nothing pins a volume). Let Metron's name
	// search place it — accepted only with a ComicVine cross-reference id.
	if pv, _ := majorityVolume(volumeVotes); pv == "" && len(unmatched) > 0 && !forcedGCD {
		if states, cvid := r.metronSeriesFallback(ctx, folder, unmatched); cvid != "" {
			total := 0
			for st, n := range states {
				stateVote[st] += n
				volumeVotes[cvid] += n
				matched += n
				total += n
			}
			// Each bound issue cast one no_match vote in pass 1; those votes are
			// superseded. Votes for issues Metron did NOT bind must survive, so a
			// partial bind can't promote the whole series.
			if v := stateVote[store.MatchStateNoMatch] - total; v > 0 {
				stateVote[store.MatchStateNoMatch] = v
			} else {
				delete(stateVote, store.MatchStateNoMatch)
			}
		}
	}

	// GCD pass: every ComicVine pass — hash, name pin, live, Metron — left
	// the folder with zero votes. Pin it in the Grand Comics Database by the
	// same name+year rules and bind by number; rows are tagged gcd and stay
	// GCD-keyed downstream (Apply reads GCD for them).
	//
	// A human's GCD choice is not a fallback but a replacement: it runs
	// whatever the ComicVine passes found (a folder whose files hashed to
	// some unrelated volume is exactly what the console reaches for GCD to
	// fix, so votes must not veto the pick), it drops every row those passes
	// wrote, and it rebinds every file in the folder — not just the leftovers
	// — so the folder's state is built from the human's series alone.
	if pv, _ := majorityVolume(volumeVotes); r.gcd != nil &&
		(forcedGCD || (forced == "" && pv == "" && len(unmatched) > 0)) {
		gid, method := forced, MethodHumanVolume
		if !forcedGCD {
			gid, method = r.pinByName(folder, unmatched, store.SourceGCD), MethodNameYear
		}
		if id, err := strconv.ParseInt(gid, 10, 64); gid != "" && err == nil {
			upIssues, err := r.gcd.IssuesBySeries(id)
			if err != nil {
				return matched, "", err
			}
			items := unmatched
			if forcedGCD {
				if err := r.st.DeleteIssueMatchesForSeries(s.ID); err != nil {
					return matched, "", err
				}
				items = make([]unmatchedIssue, 0, len(issues))
				for _, iss := range issues {
					if f := parse.ParseFile(filepath.Base(iss.Filename)); f.Format != "annual" {
						items = append(items, unmatchedIssue{iss, f})
					}
				}
				// The pass-1 penalties describe a volume that is no longer
				// this folder's; only the GCD binds' own may be recorded.
				penalties = map[string]string{}
			}
			states, bound, _, err := r.bindByNumber(ctx, folder, items, upIssues, store.SourceGCD, penalties)
			if err != nil {
				return matched, "", err
			}
			if bound > 0 || forcedGCD {
				seriesSource, seriesMethod = store.SourceGCD, method
				if forcedGCD {
					// A rebuild, not an adjustment: the folder's votes are
					// exactly what GCD bound, plus one no_match per file it
					// could not. bound == 0 still records the pick so the
					// console shows it; the state then falls to no_match.
					stateVote = map[string]int{}
					for st, n := range states {
						stateVote[st] = n
					}
					if missed := len(items) - bound; missed > 0 {
						stateVote[store.MatchStateNoMatch] = missed
					}
					volumeVotes = map[string]int{gid: bound}
					matched = bound
				} else {
					for st, n := range states {
						stateVote[st] += n
					}
					volumeVotes[gid] += bound
					matched += bound
					// Each bound issue cast one no_match vote in pass 1;
					// those votes are superseded, the rest survive.
					if v := stateVote[store.MatchStateNoMatch] - bound; v > 0 {
						stateVote[store.MatchStateNoMatch] = v
					} else {
						delete(stateVote, store.MatchStateNoMatch)
					}
				}
			}
		}
	}

	// Collection folder: every hit was a trade and each one pinned a different
	// volume — the dump files standalone trades as their own volumes, so no
	// folder-level volume exists. Keep the per-file matches, drop the folder
	// volume, and don't call the split a conflict. Compared against the
	// post-pass-2/3/4 total (matched), not just pass 1's count: anything bound
	// outside pass 1 means this folder isn't purely a collection of standalone
	// trades, so it should fall through to dominantState instead.
	//
	// One file per voted volume is the test, not VolumeIssueCount <= 1: a
	// multi-part trade set like "Green Lantern: The Sinestro Corps War" is one
	// ComicVine volume of 2, and requiring one-issue volumes left the Blackest
	// Night omnibus ambiguous over that single trade. Two volumes with one vote
	// each still can't be a folder-level series.
	// ponytail: a folder holding BOTH halves of such a set gives that volume 2
	// votes and falls through to dominantState. Revisit if one shows up.
	if collectionHits > 0 && collectionHits == matched && matched == len(volumeVotes) && len(volumeVotes) > 1 {
		penJSON, _ := json.Marshal(map[string]string{})
		if err := r.st.UpsertSeriesMatch(store.SeriesMatch{
			SeriesID:  s.ID,
			Source:    store.SourceComicVine,
			VolumeID:  "",
			State:     store.MatchStateCollection,
			Method:    MethodFirstPageMD5,
			Penalties: string(penJSON),
			UpdatedAt: r.nowMs(),
		}); err != nil {
			return matched, "", err
		}
		return matched, store.MatchStateCollection, nil
	}

	state := dominantState(stateVote)

	// The folder's volume is what most of its issues agree on. A split means
	// something is misfiled, which is exactly a human's job to look at.
	volumeID, split := majorityVolume(volumeVotes)
	if split != "" {
		penalties["volume"] = split
		state = store.MatchStateAmbiguous
	}

	penJSON, _ := json.Marshal(penalties)

	if err := r.st.UpsertSeriesMatch(store.SeriesMatch{
		SeriesID:   s.ID,
		Source:     seriesSource,
		VolumeID:   volumeID,
		State:      state,
		Confidence: 0,
		Method:     seriesMethod,
		Penalties:  string(penJSON),
		UpdatedAt:  r.nowMs(),
	}); err != nil {
		return matched, "", err
	}
	return matched, state, nil
}

// dominantState picks the series-level state from its issues' states.
// Ambiguity wins ties: one disagreeing issue is enough to warrant a look.
func dominantState(votes map[string]int) string {
	if len(votes) == 0 {
		return store.MatchStateNoMatch
	}
	if votes[store.MatchStateAmbiguous] > 0 {
		return store.MatchStateAmbiguous
	}
	best, bestN := store.MatchStateNoMatch, -1
	for _, s := range []string{
		store.MatchStateHighConfidence,
		store.MatchStateNoMatch,
	} {
		if votes[s] > bestN {
			best, bestN = s, votes[s]
		}
	}
	return best
}

// majorityVolume picks the volume the folder's issues mostly agree on, and
// describes the disagreement when there is more than one. Ties break on the
// numerically smallest id so a rerun over unchanged data gives the same answer.
func majorityVolume(votes map[string]int) (volumeID, split string) {
	if len(votes) == 0 {
		return "", ""
	}
	ids := make([]string, 0, len(votes))
	for id := range votes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if votes[ids[i]] != votes[ids[j]] {
			return votes[ids[i]] > votes[ids[j]]
		}
		a, _ := strconv.ParseInt(ids[i], 10, 64)
		b, _ := strconv.ParseInt(ids[j], 10, 64)
		return a < b
	})
	if len(ids) == 1 {
		return ids[0], ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%s (%d)", id, votes[id])
	}
	return ids[0], fmt.Sprintf("issues resolve to %d volumes: %s",
		len(ids), strings.Join(parts, ", "))
}

// pinByName finds the one volume in a dump a zero-hit folder can only be.
// Names compare by CanonicalKey — "Barbara Gordon Breakout" is "Barbara
// Gordon: Breakout" on ComicVine, and folder names never carry the colon.
// Start year within yearSlack when both are known, an exact year beating a
// neighbor. Same-name, same-year survivors (an ongoing beside its trade) are
// told apart by the files: numbered singles pick the volume that has their
// numbers, trades pick the one-issue volume. Anything still ambiguous
// returns "".
//
// A folder of collections ignores the year when the name is unique: the
// folder names the collected material's year ("Avengers vs. X-Men Omnibus
// (2012)"), the dumps the print's (2022). Uniqueness is the whole guard.
func (r *Runner) pinByName(folder parse.Folder, items []unmatchedIssue, source string) string {
	if folder.Series == "" {
		return ""
	}
	idx, err := r.volumes(source)
	if err != nil {
		log.Printf("match: %s volume index: %v", source, err)
		return ""
	}
	cands := idx.byKey[parse.CanonicalKey(folder.Series)]
	if len(cands) == 1 && allCollections(items) {
		return strconv.FormatInt(cands[0].VolumeID, 10)
	}
	var pick, exact []localcv.Volume
	for _, v := range cands {
		if folder.Year != 0 {
			y, err := strconv.Atoi(v.StartYear)
			if err != nil || y == 0 {
				continue // a volume with no year can't be told apart from its namesakes
			}
			if d := folder.Year - y; d > yearSlack || d < -yearSlack {
				continue
			}
			if y == folder.Year {
				exact = append(exact, v)
			}
		}
		pick = append(pick, v)
	}
	if len(exact) == 1 {
		pick = exact
	}
	if len(pick) > 1 {
		pick = r.tieBreakByFiles(pick, items, source)
	}
	// ponytail: two same-name volumes in the same year (an ongoing beside its
	// one-shot) with nothing in the files to separate them stay unpinned.
	if len(pick) != 1 {
		return ""
	}
	return strconv.FormatInt(pick[0].VolumeID, 10)
}

func allCollections(items []unmatchedIssue) bool {
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		if !it.file.IsCollection {
			return false
		}
	}
	return true
}

// tieBreakByFiles narrows same-name candidates using what is in the folder.
// Numbered singles: keep the volumes that carry the most of those numbers.
// Only trades: keep the one-issue volumes — that is how the dump files a
// standalone collection. Returns the input unchanged when nothing separates.
func (r *Runner) tieBreakByFiles(cands []localcv.Volume, items []unmatchedIssue, source string) []localcv.Volume {
	numbers := map[string]bool{}
	trades := 0
	for _, it := range items {
		switch {
		case it.file.IsCollection:
			trades++
		case it.file.Number != "":
			numbers[parse.NormalizeNumber(it.file.Number)] = true
		}
	}
	if len(numbers) > 0 {
		best, bestN := []localcv.Volume(nil), 0
		for _, v := range cands {
			issues, err := r.issuesFor(source, v.VolumeID)
			if err != nil {
				continue
			}
			n := 0
			for i := range issues {
				if numbers[parse.NormalizeNumber(issues[i].IssueNumber)] {
					n++
				}
			}
			switch {
			case n > bestN:
				best, bestN = []localcv.Volume{v}, n
			case n == bestN && n > 0:
				best = append(best, v)
			}
		}
		if bestN > 0 {
			return best
		}
		return cands
	}
	if trades > 0 && trades == len(items) {
		var ones []localcv.Volume
		for _, v := range cands {
			if v.IssueCount == 1 {
				ones = append(ones, v)
			}
		}
		if len(ones) > 0 {
			return ones
		}
	}
	return cands
}

// libraryGone reports whether a configured library root has disappeared, which
// is the only thing that means "the drive unmounted".
//
// This exists to tell two very different fs.ErrNotExist cases apart:
//
//   - A file, or an entire series folder, is missing while the roots are still
//     there. The DB is simply out of date — a quarter of this library's issue
//     rows name files that moved, and a reorg renamed six series folders
//     outright. Skipping the issue is correct; aborting would mean the first
//     stale row anywhere stops the whole batch.
//   - A root is missing. The library lives on an external drive, so this is an
//     unmount, and continuing would rewrite every remaining series as no_match
//     against files we simply cannot see.
//
// Testing the roots rather than the series folder is the point: a root vanishes
// only on unmount, and survives any rename underneath it. Do not "simplify"
// this back to statting the series folder — a renamed folder is
// filesystem-identical to an unmounted one, so that check cannot work.
//
// With no roots configured we never abort: an empty list means the caller gave
// us no unmount signal to test, and silently stopping the batch would be worse
// than carrying on.
func (r *Runner) libraryGone() bool {
	for _, root := range r.roots {
		// Anything other than "not there" (a permission error, say) counts as
		// present: the conservative choice is to skip one issue, not the run.
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// LookupIssue resolves an upstream issue id in its own source for display
// in the review console. A ComicVine id never resolves against GCD or vice
// versa — the id spaces overlap.
func (r *Runner) LookupIssue(source, upstreamID string) (*localcv.Issue, error) {
	id, err := strconv.ParseInt(upstreamID, 10, 64)
	if err != nil {
		return nil, err
	}
	if source == store.SourceGCD {
		if r.gcd == nil {
			return nil, gcd.ErrNotFound
		}
		iss, _, err := r.gcd.IssueByID(id)
		return iss, err
	}
	return r.cv.IssueByID(id)
}

func baseName(path, fallback string) string {
	if path == "" {
		return fallback
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// bindByNumber binds items into upIssues by issue number and writes one
// issue_match row per hit under source. A numberless file ("Devil's Reign
// TPB", a one-shot) in a one-issue list can only be that issue — Verify
// already ignores the number against such a volume. Numbered files nothing
// bound come back as remaining for the live/Metron passes; numberless ones
// don't, since those passes bind by number too. Votes are the caller's:
// every hit is a vote for the volume the caller pinned.
func (r *Runner) bindByNumber(ctx context.Context, folder parse.Folder, items []unmatchedIssue, upIssues []localcv.Issue, source string, penalties map[string]string) (map[string]int, int, []unmatchedIssue, error) {
	byNumber := make(map[string]*localcv.Issue, len(upIssues))
	for i := range upIssues {
		key := parse.NormalizeNumber(upIssues[i].IssueNumber)
		if _, exists := byNumber[key]; !exists {
			byNumber[key] = &upIssues[i]
		}
	}
	states := map[string]int{}
	bound := 0
	var remaining []unmatchedIssue
	for _, u := range items {
		if err := ctx.Err(); err != nil {
			return states, bound, remaining, err
		}
		// A one-file folder against a volume KNOWN to have exactly one issue can
		// only be that issue — bind it even if the file carries a stray number
		// token and isn't tagged a collection (e.g. a single "Life Story" book
		// vs a numberless GCD issue). Gated on VolumeIssueCount, not just
		// len(upIssues) == 1: a name-pinned volume's candidate list can be one
		// row deep only because the dump's coverage of it is sparse, not because
		// the volume itself is a one-shot — TestRunnerNamePinRejectedWhenNumbersDoNotBind
		// covers that case, where the real volume has 7 issues.
		soleFileSoleIssue := len(items) == 1 && len(upIssues) == 1 && upIssues[0].VolumeIssueCount == 1
		var up *localcv.Issue
		if u.file.Number != "" && !(u.file.IsCollection && len(upIssues) == 1) && !soleFileSoleIssue {
			up = byNumber[parse.NormalizeNumber(u.file.Number)]
		} else if len(upIssues) == 1 {
			up = &upIssues[0]
		}
		if up == nil {
			if u.file.Number != "" {
				remaining = append(remaining, u)
			}
			continue
		}
		v := Verify(folder, u.file, up)
		if u.file.IsCollection {
			if err := r.st.SetIssueFormatIfBlank(u.iss.ID, v.Format); err != nil {
				return states, bound, remaining, err
			}
			if err := r.st.SetIssueNumberIfBlank(u.iss.ID, u.file.Number); err != nil {
				return states, bound, remaining, err
			}
		}
		states[v.State]++
		for k, msg := range v.Penalties {
			penalties[k] = msg
		}
		bound++
		if err := r.st.UpsertIssueMatch(store.IssueMatch{
			IssueID:    u.iss.ID,
			Source:     source,
			UpstreamID: strconv.FormatInt(up.IssueID, 10),
			State:      v.State,
			Confidence: 0,
			Method:     MethodVolumeNumber,
		}); err != nil {
			return states, bound, remaining, err
		}
	}
	return states, bound, remaining, nil
}

// liveBind matches items by normalized issue number against the live
// ComicVine volume list and writes an issue_match row per hit. A live API
// failure is logged and skipped — the offline result already stands, and a
// downed API must not fail the series.
func (r *Runner) liveBind(ctx context.Context, folder parse.Folder, items []unmatchedIssue, volumeID int64) (map[string]int, []unmatchedIssue, error) {
	if r.live == nil || len(items) == 0 {
		return nil, items, nil // no live pass: everything is a leftover
	}
	upIssues, err := r.live.ListVolumeIssues(int(volumeID))
	if err != nil {
		log.Printf("match: live volume %d: %v", volumeID, err)
		return nil, items, nil // live down: everything is a leftover
	}
	byNumber := make(map[string]comicvine.Issue, len(upIssues))
	for _, u := range upIssues {
		key := parse.NormalizeNumber(u.Number)
		if _, exists := byNumber[key]; !exists {
			byNumber[key] = u
		}
	}

	states := map[string]int{}
	var leftover []unmatchedIssue
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return states, leftover, err
		}
		if it.file.Number == "" {
			continue // unnumbered: no provider can number-bind it
		}
		up, ok := byNumber[parse.NormalizeNumber(it.file.Number)]
		if !ok {
			leftover = append(leftover, it)
			continue
		}
		// The live response has no volume name/year, so Verify only checks
		// what it can — the number. The volume itself was already pinned.
		v := Verify(folder, it.file, &localcv.Issue{
			IssueID:     int64(up.ID),
			VolumeID:    volumeID,
			IssueNumber: up.Number,
			IssueName:   up.Title,
			CoverDate:   up.CoverDate,
		})
		if err := r.st.UpsertIssueMatch(store.IssueMatch{
			IssueID:    it.iss.ID,
			Source:     store.SourceComicVine,
			UpstreamID: strconv.Itoa(up.ID),
			State:      v.State,
			Confidence: 0,
			Method:     MethodLiveVolumeNumber,
		}); err != nil {
			return states, leftover, err
		}
		states[v.State]++
	}
	return states, leftover, nil
}

// metronBind is the last number-binding pass: Metron's issue list for the
// series carrying this ComicVine volume id as its cross-reference. Rows it
// writes carry Source=metron and the Metron issue id, so Apply resolves them
// against Metron, not the dump. Every failure is logged and swallowed — a
// downed fallback must never fail the series.
func (r *Runner) metronBind(ctx context.Context, folder parse.Folder, items []unmatchedIssue, cvVolumeID int64) map[string]int {
	if r.metron == nil || len(items) == 0 {
		return nil
	}
	ms, err := r.metron.SeriesByCVID(int(cvVolumeID))
	if err != nil {
		if !errors.Is(err, metron.ErrNotFound) {
			log.Printf("match: metron series cv_id=%d: %v", cvVolumeID, err)
		}
		return nil
	}
	upIssues, err := r.metron.ListSeriesIssues(ms.ID)
	if err != nil {
		log.Printf("match: metron issues series=%d: %v", ms.ID, err)
		return nil
	}
	byNumber := make(map[string]metron.IssueResult, len(upIssues))
	for _, u := range upIssues {
		key := parse.NormalizeNumber(u.Number)
		if _, exists := byNumber[key]; !exists {
			byNumber[key] = u
		}
	}

	states := map[string]int{}
	for _, it := range items {
		if ctx.Err() != nil {
			return states
		}
		if it.file.Number == "" {
			continue
		}
		up, ok := byNumber[parse.NormalizeNumber(it.file.Number)]
		if !ok {
			continue
		}
		// Same shape liveBind uses: Verify checks what it can (the number
		// plus the volume name/year Metron does give us).
		v := Verify(folder, it.file, &localcv.Issue{
			VolumeID:        cvVolumeID,
			IssueNumber:     up.Number,
			CoverDate:       up.CoverDate,
			VolumeName:      ms.Name,
			VolumeStartYear: strconv.Itoa(ms.YearBegan),
		})
		if err := r.st.UpsertIssueMatch(store.IssueMatch{
			IssueID:    it.iss.ID,
			Source:     store.SourceMetron,
			UpstreamID: strconv.Itoa(up.ID),
			State:      v.State,
			Confidence: 0,
			Method:     MethodMetronVolumeNumber,
		}); err != nil {
			log.Printf("match: metron upsert %s: %v", it.iss.ID, err)
			return states
		}
		states[v.State]++
	}
	return states
}

// metronSeriesFallback matches a folder that got zero volume votes from the
// CV passes (typically: every issue post-dates the dump, so nothing hashed).
// It searches Metron by the parsed folder name, requires a year agreement
// when the folder has one, and — critically — requires a ComicVine cv_id:
// our match and enrichment tables are CV-keyed, and a Metron-only series
// cannot feed them (spec defers provider-qualified ids until a real need).
// Returns per-state counts and the accepted CV volume id, or ("", nil).
func (r *Runner) metronSeriesFallback(ctx context.Context, folder parse.Folder, items []unmatchedIssue) (map[string]int, string) {
	if r.metron == nil || folder.Series == "" || len(items) == 0 {
		return nil, ""
	}
	results, err := r.metron.SearchSeries(folder.Series)
	if err != nil {
		log.Printf("match: metron search %q: %v", folder.Series, err)
		return nil, ""
	}
	// Check the top few candidates only: a broad name can return hundreds,
	// and each acceptance check costs a series-detail request.
	// ponytail: fixed top-5 cutoff; score candidates if it ever misses.
	const maxCandidates = 5
	for i, cand := range results {
		if i >= maxCandidates || ctx.Err() != nil {
			break
		}
		if folder.Year != 0 && cand.YearBegan != 0 && cand.YearBegan != folder.Year {
			continue
		}
		ms, err := r.metron.GetSeries(cand.ID)
		if err != nil {
			log.Printf("match: metron series %d: %v", cand.ID, err)
			continue
		}
		if ms.CVID == 0 {
			log.Printf("match: metron series %d (%s) has no cv_id, skipping", ms.ID, ms.Name)
			continue
		}
		states := r.metronBind(ctx, folder, items, int64(ms.CVID))
		bound := 0
		for _, n := range states {
			bound += n
		}
		if bound == 0 {
			continue // name matched but no issue numbers lined up — wrong series
		}
		return states, strconv.Itoa(ms.CVID)
	}
	return nil, ""
}
