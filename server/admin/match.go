package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

// MatchRow is one series as shown in the review list.
type MatchRow struct {
	SeriesID  string
	Name      string
	State     string
	Method    string
	VolumeID  string
	Penalties string
	// PenaltyRows is Penalties unmarshaled into field/message pairs so the
	// template can render a table instead of a raw JSON blob. Empty when
	// Penalties is "{}" or unparseable.
	PenaltyRows []PenaltyRow
	DontMatch   bool
	// HasVolume gates the Confirm button: confirming a row with no matched
	// volume writes no metadata, so the template must not offer it.
	HasVolume bool
	// Confirmable gates the Confirm button: true when there's a folder
	// volume, or the row is a collection folder (per-file volumes, no
	// folder volume — Apply still writes metadata from those).
	Confirmable bool
	// LocalCoverURL/CVImageURL back the row's cover-pair thumbnail, taken from
	// the series' first matched issue. Both are blank when the series has no
	// matched issue (or CV lookup failed), and the template renders nothing.
	LocalCoverURL string
	CVImageURL    string
	ConfirmedBy   string
}

// PenaltyRow is one field/message pair from series_match.penalties, for
// rendering as a table row rather than a raw JSON blob.
type PenaltyRow struct {
	Field   string
	Message string
}

// parsePenalties unmarshals series_match.penalties (a JSON object like
// {"year":"local 2016 vs volume start 2020"}) into rows sorted by field name
// for stable rendering. The message text is shown verbatim — its format is
// not stable enough to parse into further columns. Returns nil for "{}",
// empty, or unparseable input, which templates treat as "nothing to show".
func parsePenalties(raw string) []PenaltyRow {
	if raw == "" || raw == "{}" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil || len(m) == 0 {
		return nil
	}
	fields := make([]string, 0, len(m))
	for k := range m {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	rows := make([]PenaltyRow, 0, len(fields))
	for _, f := range fields {
		rows = append(rows, PenaltyRow{Field: f, Message: m[f]})
	}
	return rows
}

// pathEscape is exposed to templates for building links/URLs from series and
// issue ids, which can contain characters ('/', '=') that need escaping —
// issue ids are base64 of a relative path.
func pathEscape(s string) string { return url.PathEscape(s) }

type MatchData struct {
	Counts    map[string]int
	State     string // the state currently being viewed
	Rows      []MatchRow
	Enabled   bool
	Running   bool // a full pass is in flight; the run button is disabled
	LastRun   string
	AllState  []string
	Writeback map[string]int // status -> count (pending/done/error)
	// Busy explains a refused run, for the same reason as OrganizeData.Busy:
	// the bare redirect looked like a dead button.
	Busy string
}

func (a *Admin) handleMatch(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = store.MatchStateHighConfidence
	}

	counts, err := a.Store.CountSeriesMatchesByState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	matches, err := a.Store.ListSeriesMatchesByState(state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// One query for the whole queue's representative covers, not one per row.
	firstIssues, err := a.Store.ListFirstMatchedIssuesByState(state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rows := make([]MatchRow, 0, len(matches))
	for _, m := range matches {
		name := m.SeriesID
		if s, err := a.Store.GetSeries(m.SeriesID); err == nil {
			name = s.Name
		}
		row := MatchRow{
			SeriesID:    m.SeriesID,
			Name:        name,
			State:       m.State,
			Method:      m.Method,
			VolumeID:    m.VolumeID,
			Penalties:   m.Penalties,
			PenaltyRows: parsePenalties(m.Penalties),
			DontMatch:   m.DontMatch,
			HasVolume:   m.VolumeID != "",
			Confirmable: m.VolumeID != "" || m.State == store.MatchStateCollection,
			ConfirmedBy: m.ConfirmedBy,
		}
		if fm, ok := firstIssues[m.SeriesID]; ok {
			row.LocalCoverURL = "/comics/" + pathEscape(fm.IssueID) + "/cover"
			if a.Match != nil {
				if cvIssue, lookupErr := a.Match.LookupIssue(fm.Source, fm.UpstreamID); lookupErr == nil {
					row.CVImageURL = cvIssue.ImageURL
				}
			}
		}
		rows = append(rows, row)
	}

	// Best-effort — a nil map renders as zeros in the template.
	writebackCounts, _ := a.Store.CountWritebackByStatus()

	data := a.baseData("match")
	data.Title = "Metadata Matching"
	data.Content = MatchData{
		Counts:    counts,
		State:     state,
		Rows:      rows,
		Enabled:   a.Match != nil,
		Running:   a.matchRunning.Load(),
		Busy:      r.URL.Query().Get("busy"),
		Writeback: writebackCounts,
		AllState: []string{
			store.MatchStateHighConfidence,
			store.MatchStateAmbiguous,
			store.MatchStateLowConfidence,
			store.MatchStateNoMatch,
			store.MatchStateCollection,
			store.MatchStateConfirmed,
		},
	}
	a.render(w, "match.html", data)
}

func (a *Admin) handleMatchRun(w http.ResponseWriter, r *http.Request) {
	if a.Match == nil {
		http.Error(w, "matching is disabled: set localcv_db_path", http.StatusPreconditionFailed)
		return
	}
	// A run is already in flight (double-click, refresh, second tab), or an
	// organize apply is running against the same library. Not an error
	// condition — show the page rather than a 4xx.
	if !a.canStartMatchRun() {
		http.Redirect(w, r, "/_admin/match?busy="+url.QueryEscape(a.busyReason()), http.StatusSeeOther)
		return
	}
	run := a.runMatch
	if run == nil {
		run = func(ctx context.Context) error {
			_, err := a.Match.Run(ctx)
			return err
		}
	}

	// Detached from the request: a pass is ~4 minutes and the browser hanging
	// up (navigation, timeout) must not cancel it. The page's Running flag is
	// the progress UI; errors go to the server log.
	go func() {
		defer a.matchRunning.Store(false)
		// The library is on an external drive and a full pass reads every
		// archive's first page, so give it room but do not let it run forever.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := run(ctx); err != nil {
			log.Printf("match run: %v", err)
		}
	}()
	http.Redirect(w, r, "/_admin/match", http.StatusSeeOther)
}

func (a *Admin) handleMatchConfirm(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("series_id")
	if id == "" {
		http.Error(w, "series_id required", http.StatusBadRequest)
		return
	}
	if err := a.confirm(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, matchRedirectTarget(r), http.StatusSeeOther)
}

// matchRedirectTarget sends the browser back where the form was submitted
// from: the detail page passes its own URL via a hidden "redirect" field, the
// list page relies on the "state" fallback it already sends.
func matchRedirectTarget(r *http.Request) string {
	if redirect := r.FormValue("redirect"); redirect != "" {
		return redirect
	}
	return "/_admin/match?state=" + r.FormValue("state")
}

// handleMatchConfirmAll is the headline action: accept every series currently
// sitting in high_confidence in one click. It does not abort on the first
// failure — with 139 rows in play, stopping early would leave the user unable
// to tell how many landed. It keeps going and reports a count instead.
func (a *Admin) handleMatchConfirmAll(w http.ResponseWriter, r *http.Request) {
	matches, err := a.Store.ListReviewQueue(store.MatchStateHighConfidence) // skips dont_match
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var ok, failed int
	var firstErr error
	for _, m := range matches {
		if err := a.confirm(m.SeriesID); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok++
	}
	if failed > 0 {
		http.Error(w, fmt.Sprintf("confirmed %d of %d; %d failed (first: %v)",
			ok, len(matches), failed, firstErr), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/_admin/match", http.StatusSeeOther)
}

// Sentinel errors for the volume pick and confirm paths, so the JSON API can
// map them to statuses while the HTML handlers keep their messages.
var (
	ErrAlreadyConfirmed = errors.New("unconfirm first")
	ErrBadSource        = errors.New("unknown or unconfigured source")
	ErrNoVolume         = errors.New("no volume matched")
)

// handleMatchSetVolume records a human's choice from the volume picker, then
// recomputes penalties/state against it. Refused on confirmed rows — the
// picker isn't rendered there, and flipping the volume under applied metadata
// would desync what the app shows from what the match says.
func (a *Admin) handleMatchSetVolume(w http.ResponseWriter, r *http.Request) {
	if a.Match == nil {
		http.Error(w, "matching is disabled: set localcv_db_path", http.StatusPreconditionFailed)
		return
	}
	id, vol := r.FormValue("series_id"), r.FormValue("volume_id")
	if id == "" || vol == "" {
		http.Error(w, "series_id and volume_id required", http.StatusBadRequest)
		return
	}
	// Which dump the human picked from. The id spaces overlap, so a pick
	// bound against the wrong dump is a wrong volume, not an error.
	source := r.FormValue("source")
	if source == "" {
		source = store.SourceComicVine
	}
	if source != store.SourceComicVine && source != store.SourceGCD {
		http.Error(w, "unknown source", http.StatusBadRequest)
		return
	}
	if source == store.SourceGCD && !a.Match.HasGCD() {
		http.Error(w, "gcd pick refused: gcd_db_path is not configured", http.StatusInternalServerError)
		return
	}
	if err := a.setVolume(r.Context(), id, source, vol); err != nil {
		if errors.Is(err, ErrAlreadyConfirmed) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, matchRedirectTarget(r), http.StatusSeeOther)
}

// setVolume pins a series to a human-chosen volume and rescores it.
// ErrAlreadyConfirmed for confirmed rows, ErrBadSource for an unknown source
// or a GCD pick with no GCD dump; store.ErrNotFound for an unknown series.
func (a *Admin) setVolume(ctx context.Context, id, source, vol string) error {
	if source != store.SourceComicVine && source != store.SourceGCD {
		return ErrBadSource
	}
	if source == store.SourceGCD && !a.Match.HasGCD() {
		return ErrBadSource
	}
	m, err := a.Store.GetSeriesMatch(id)
	if err != nil {
		return err
	}
	if m.State == store.MatchStateConfirmed {
		return ErrAlreadyConfirmed
	}
	ims, err := a.Store.ListIssueMatchesForSeries(id)
	if err != nil {
		return err
	}
	if len(ims) == 0 || source != m.Source {
		// A cross-source pin invalidates the existing issue rows: the id
		// spaces overlap, so a comicvine issue id is meaningless under a gcd
		// series (and vice-versa). Drop them so the folder rebinds cleanly
		// under the new source, the same way the zero-match case already
		// does. There is then nothing for Reverify to rescore: bind the
		// folder by number into the chosen volume instead.
		if source != m.Source {
			if err := a.Store.DeleteIssueMatchesForSeries(id); err != nil {
				return err
			}
		}
		return a.Match.Rematch(ctx, id, source, vol)
	}
	if err := a.Store.SetSeriesMatchVolume(id, source, vol, time.Now().UnixMilli()); err != nil {
		return err
	}
	return a.Match.Reverify(id)
}

// handleMatchMarkCollection is the manual counterpart to the runner's
// collection detection: it clears the folder volume and puts the row in the
// collection state, where "Confirm as collection" is offered.
//
// Auto-detection (match/runner.go) only fires when every matched file pins a
// distinct volume. A folder holding two trades from the same volume — Black
// Science, whose v01 and v05 are both in volume 74320 — misses that test by
// one, falls through to the majority vote, and confirms as an ordinary series
// against one trade's volume. Organize then flags every other trade in it as a
// homeless stray. Nothing else recovers such a row: set-volume cannot clear a
// volume, and unconfirm re-derives collection only from an already-empty one.
//
// The state is set directly rather than via Reverify, which would re-run the
// same detection that missed this folder in the first place.
func (a *Admin) handleMatchMarkCollection(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("series_id")
	if id == "" {
		http.Error(w, "series_id required", http.StatusBadRequest)
		return
	}
	m, err := a.Store.GetSeriesMatch(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Same order of operations set-volume and repin enforce: applied metadata
	// must be withdrawn before the match beneath it moves.
	if m.State == store.MatchStateConfirmed {
		http.Error(w, "unconfirm first", http.StatusBadRequest)
		return
	}
	ims, err := a.Store.ListIssueMatchesForSeries(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A collection folder is per-file volumes; with no per-file matches there
	// are none, and an empty volume would read as a no-match instead. Same
	// distinction deriveUnconfirmState draws.
	if len(ims) == 0 {
		http.Error(w, "no issue matches: this is a no-match, not a collection", http.StatusBadRequest)
		return
	}
	if err := a.Store.UpdateSeriesMatchReview(id, "", store.MatchStateCollection, "{}", time.Now().UnixMilli()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, matchRedirectTarget(r), http.StatusSeeOther)
}

// decodeRepinVolume splits the picker's "<source>:<id>" value. The two dumps'
// id spaces overlap, so the pick has to carry the dump the human was looking
// at rather than assume the folder's. A bare id (no prefix) is read as the
// folder's own source, which is what an API caller would send. Reports false
// for a source that is neither dump.
func decodeRepinVolume(raw, fallbackSource string) (source, volumeID string, ok bool) {
	source, volumeID = fallbackSource, raw
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		source, volumeID = raw[:i], raw[i+1:]
	}
	if source != store.SourceComicVine && source != store.SourceGCD {
		return "", "", false
	}
	if volumeID == "" {
		return "", "", false
	}
	return source, volumeID, true
}

// handleMatchRepin rebinds one issue to a human-chosen upstream issue (the
// template only offers same-number issues in the series' chosen volume), then
// recomputes the series' penalties/state.
func (a *Admin) handleMatchRepin(w http.ResponseWriter, r *http.Request) {
	if a.Match == nil {
		http.Error(w, "matching is disabled: set localcv_db_path", http.StatusPreconditionFailed)
		return
	}
	seriesID := r.FormValue("series_id")
	issueID := r.FormValue("issue_id")
	upstreamID := r.FormValue("upstream_id")
	if seriesID == "" || issueID == "" {
		http.Error(w, "series_id and issue_id required", http.StatusBadRequest)
		return
	}
	if upstreamID == "" && r.FormValue("repin_volume") == "" {
		http.Error(w, "upstream_id or repin_volume required", http.StatusBadRequest)
		return
	}
	m, err := a.Store.GetSeriesMatch(seriesID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if m.State == store.MatchStateConfirmed {
		http.Error(w, "unconfirm first", http.StatusBadRequest)
		return
	}
	// A collection folder has no folder volume, so the detail page cannot
	// precompute a re-pin target and sends the chosen volume instead: resolve
	// this file's local number inside it. Without this a mis-pinned file in a
	// collection has no fix at all — the per-row button is gated on a folder
	// volume that a collection never has.
	if volID := r.FormValue("repin_volume"); volID != "" {
		src, volID, ok := decodeRepinVolume(volID, m.Source)
		if !ok {
			http.Error(w, "unknown source", http.StatusBadRequest)
			return
		}
		issues, err := a.Store.ListIssues(seriesID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		number := ""
		for _, iss := range issues {
			if iss.ID == issueID {
				number = iss.Number
				break
			}
		}
		if number == "" {
			http.Error(w, "issue has no parsed number to match on", http.StatusBadRequest)
			return
		}
		// Number first, then the standalone-trade fallback: ComicVine files a
		// collected edition as a one-issue volume numbered 1, so a file
		// numbered 5 never matches by number into the volume that actually
		// holds it. With exactly one issue there is nothing to disambiguate.
		target, err := a.Match.LookupIssueInVolume(src, volID, number)
		if err != nil {
			target, err = a.Match.SoleIssueInVolume(src, volID)
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("volume %s has no issue #%s and is not a single-issue volume", volID, number), http.StatusBadRequest)
			return
		}
		if err := a.Match.RepinIssue(issueID, src, strconv.FormatInt(target.IssueID, 10)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := a.Match.Reverify(seriesID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, matchRedirectTarget(r), http.StatusSeeOther)
		return
	}
	if err := a.Match.RepinIssue(issueID, m.Source, upstreamID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Match.Reverify(seriesID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, matchRedirectTarget(r), http.StatusSeeOther)
}

func (a *Admin) handleMatchDontMatch(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("series_id")
	if id == "" {
		http.Error(w, "series_id required", http.StatusBadRequest)
		return
	}
	if err := a.Store.SetDontMatch(id, true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, matchRedirectTarget(r), http.StatusSeeOther)
}

// handleMatchUnconfirm reverses a confirmation: it clears the metadata Apply
// wrote (so the app stops showing it) and puts the series back in a review
// queue. Unlike confirm, a series with no matched volume can still be
// unconfirmed — Don't match is what put dont_match rows here, and unconfirm
// doesn't touch that flag.
func (a *Admin) handleMatchUnconfirm(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("series_id")
	if id == "" {
		http.Error(w, "series_id required", http.StatusBadRequest)
		return
	}
	if err := a.unconfirm(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, matchRedirectTarget(r), http.StatusSeeOther)
}

// unconfirm clears the metadata Apply wrote and puts a confirmed series back
// in a review queue, re-deriving that queue with deriveUnconfirmState.
func (a *Admin) unconfirm(seriesID string) error {
	m, err := a.Store.GetSeriesMatch(seriesID)
	if err != nil {
		return err
	}
	if err := a.Store.DeleteSeriesEnrichment(seriesID); err != nil {
		return err
	}
	issueMatches, err := a.Store.ListIssueMatchesForSeries(seriesID)
	if err != nil {
		return err
	}
	state := deriveUnconfirmState(m.VolumeID, m.Penalties, len(issueMatches) > 0)
	return a.Store.SetSeriesMatchState(seriesID, state, time.Now().UnixMilli())
}

// deriveUnconfirmState re-derives which queue an unconfirmed series should
// land in, rather than storing "the state before confirm" as a column: the
// schema has no prior_state slot, confirming happens from exactly one state
// per row today, and this mirrors what a fresh matcher pass would compute
// from the same VolumeID/Penalties anyway (see dominantState/Verify).
//
// An empty volumeID is ambiguous on its own: a collection folder (per-file
// volumes, no folder volume) and a genuine no-match both have VolumeID "".
// hasIssueMatches disambiguates them the same way the matcher does — a
// collection folder keeps its per-file issue_match rows, a no-match has none.
func deriveUnconfirmState(volumeID, penalties string, hasIssueMatches bool) string {
	if volumeID == "" {
		if hasIssueMatches {
			return store.MatchStateCollection
		}
		return store.MatchStateNoMatch
	}
	if penalties != "" && penalties != "{}" {
		return store.MatchStateAmbiguous
	}
	return store.MatchStateHighConfidence
}

// confirm applies a match's metadata to the enrichment tables the app reads,
// then marks it confirmed.
//
// A series with no matched volume is refused rather than confirmed: flipping it
// would write no metadata, so the user would get a success redirect and an empty
// app. "Don't match" is the way to dispose of those rows.
//
// Apply runs before the state flips to confirmed: if Apply fails (dump
// unavailable, sqlite busy, anything transient) the series stays out of
// confirmed rather than being stranded there with no metadata and no way to
// retry — match/runner.go skips confirmed series on every future run, and the
// console hides all action buttons on confirmed rows.
func (a *Admin) confirm(seriesID string) error { return a.confirmBy(seriesID, "human") }

// confirmBy is confirm with attribution: "human" from the console,
// "pipeline" from the auto pipeline.
func (a *Admin) confirmBy(seriesID, by string) error {
	m, err := a.Store.GetSeriesMatch(seriesID)
	if err != nil {
		return err
	}
	if m.VolumeID == "" && m.State != store.MatchStateCollection {
		return fmt.Errorf("cannot confirm %s: %w — use Don't match to exclude it", seriesID, ErrNoVolume)
	}
	if a.Match != nil {
		if err := a.Match.Apply(seriesID); err != nil {
			return err
		}
	}
	if err := a.Store.ConfirmSeriesMatchBy(seriesID, by, time.Now().UnixMilli()); err != nil {
		return err
	}
	// Push the confirmed metadata toward the files. Best-effort: the match is
	// already confirmed, so a queue hiccup must not roll it back.
	if ids, err := a.Store.IssueIDsForSeries(seriesID); err != nil {
		log.Printf("writeback enqueue %s: %v", seriesID, err)
	} else if err := a.Store.EnqueueWriteback(ids...); err != nil {
		log.Printf("writeback enqueue %s: %v", seriesID, err)
	}
	return nil
}

// handleMatchWriteback enqueues a writeback job for every confirmed series'
// issues — the one-time backfill for series confirmed before writeback was
// wired, plus a manual re-push button.
//
// Series confirmed before Apply stored the canonical volume_name, or before
// Apply backfilled ComicVine ids onto stored creators, are re-applied first
// — re-running the matcher alone skips confirmed series (match/runner.go),
// so this is the only path that fixes already-confirmed rows.
func (a *Admin) handleMatchWriteback(w http.ResponseWriter, r *http.Request) {
	if a.Match != nil {
		ids, err := a.Store.ConfirmedSeriesMissingVolumeName()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, id := range ids {
			if err := a.Match.Apply(id); err != nil {
				log.Printf("writeback backfill: apply %s: %v", id, err)
			}
		}

		creatorIDs, err := a.Store.ConfirmedSeriesMissingCreatorIDs()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, id := range creatorIDs {
			if err := a.Match.Apply(id); err != nil {
				log.Printf("writeback backfill: apply (creator ids) %s: %v", id, err)
			}
		}
	}
	if _, err := a.Store.EnqueueWritebackForConfirmed(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/_admin/match", http.StatusSeeOther)
}

// MatchSeriesDetailData is the review page for a single series: enough to
// actually judge the match — resolved ComicVine volume info and a
// cover-vs-cover comparison per issue — rather than a name and a JSON blob.
type MatchSeriesDetailData struct {
	SeriesID    string
	Name        string
	FolderPath  string
	FolderName  string
	State       string
	Method      string
	Source      string
	VolumeID    string
	HasVolume   bool
	Confirmable bool
	DontMatch   bool

	// CV* fields are read off the first issue that resolves successfully
	// (they're joined onto every issue row in the dump alike), so they stay
	// blank if matching is disabled or no issue in this folder resolves.
	CVVolumeName string
	CVStartYear  string
	CVPublisher  string

	PenaltyRows []PenaltyRow
	// Volumes lists every volume this folder's issues resolved to, with vote
	// counts, so a split can be resolved by picking one. Only rendered when
	// there is more than one.
	Volumes []VolumeOption
	// Query/SearchResults: a human's dump search for a volume to pin — the
	// way in for a folder nothing matched, which has no Volumes to offer.
	Query         string
	SearchResults []VolumeOption
	Searchable    bool
	// Issues is the full per-issue cover-vs-cover list. VisibleIssues is what
	// the template actually renders — capped to issueCap unless ShowAll is
	// set, so a 60-issue folder doesn't render 60 cover pairs by default.
	Issues        []MatchIssueRow
	VisibleIssues []MatchIssueRow
	ShowAll       bool
	MoreCount     int
}

// issueCap is how many issue rows render by default; ?all=1 shows the rest.
const issueCap = 6

// applyIssueCap fills VisibleIssues/MoreCount from Issues. Kept separate from
// buildMatchSeriesDetail because "how many to show" is a rendering decision
// (driven by the request's ?all= param), not part of the view model itself.
func applyIssueCap(content *MatchSeriesDetailData, showAll bool) {
	content.ShowAll = showAll || len(content.Issues) <= issueCap
	if content.ShowAll {
		content.VisibleIssues = content.Issues
	} else {
		content.VisibleIssues = content.Issues[:issueCap]
	}
	content.MoreCount = len(content.Issues) - len(content.VisibleIssues)
}

// srcVol keys a volume by the dump it lives in; the two id spaces overlap, so
// a bare id would merge two different volumes into one option row.
func srcVol(source, volumeID string) string { return source + "\x00" + volumeID }

// VolumeOption is one candidate volume in a split, with how many of the
// folder's issues resolved to it.
type VolumeOption struct {
	ID string
	// Source is the dump ID came from; the set-volume form carries it back
	// so the pick binds against the dump the human was looking at.
	Source    string
	Name      string
	StartYear string
	Publisher string
	Count     int
	Current   bool
}

// MatchIssueRow is one local issue plus whatever ComicVine data resolved for
// it. HasCV is false — and the CV* fields stay blank — when there is no
// recorded issue match, matching is disabled, or the upstream lookup failed;
// the row still renders either way.
type MatchIssueRow struct {
	IssueID       string
	Filename      string
	LocalNumber   string
	LocalCoverURL string
	Method        string

	HasCV      bool
	CVNumber   string
	CVTitle    string
	CVImageURL string

	// OtherVolume flags a row whose match belongs to a different volume than
	// the series' chosen one; CVVolumeName/Year label which.
	OtherVolume  bool
	CVVolumeName string
	CVVolumeYear string
	// RepinID/RepinNumber offer "re-pin to #N of the chosen volume" when that
	// volume has an issue with this row's local number. Set for unmatched rows
	// and other-volume rows alike; blank when there is no such issue.
	RepinID     string
	RepinNumber string
}

// buildMatchSeriesDetail gathers everything needed to review one series: the
// local folder, the resolved ComicVine volume, and a cover-vs-cover row per
// issue. It is the single place that assembly happens — both the full-page
// detail handler and the one-at-a-time review handler call it, so neither
// duplicates the issue/cover assembly logic.
func (a *Admin) buildMatchSeriesDetail(id string) (*MatchSeriesDetailData, error) {
	series, err := a.Store.GetSeries(id)
	if err != nil {
		return nil, fmt.Errorf("series not found: %w", err)
	}
	m, err := a.Store.GetSeriesMatch(id)
	if err != nil {
		return nil, fmt.Errorf("no match record for this series: %w", err)
	}

	issues, err := a.Store.ListIssues(id)
	if err != nil {
		return nil, err
	}
	issueMatches, err := a.Store.ListIssueMatchesForSeries(id)
	if err != nil {
		return nil, err
	}
	upstreamByIssue := make(map[string]string, len(issueMatches))
	// Which dump each issue's upstream id belongs to; the id spaces overlap,
	// so it has to travel with the id into every lookup below.
	sourceByIssue := make(map[string]string, len(issueMatches))
	methodByIssue := make(map[string]string, len(issueMatches))
	for _, im := range issueMatches {
		upstreamByIssue[im.IssueID] = im.UpstreamID
		sourceByIssue[im.IssueID] = im.Source
		methodByIssue[im.IssueID] = im.Method
	}

	content := &MatchSeriesDetailData{
		SeriesID:    series.ID,
		Name:        series.Name,
		FolderPath:  series.FolderPath,
		FolderName:  filepath.Base(series.FolderPath),
		State:       m.State,
		Method:      m.Method,
		Source:      m.Source,
		VolumeID:    m.VolumeID,
		HasVolume:   m.VolumeID != "",
		Confirmable: m.VolumeID != "" || m.State == store.MatchStateCollection,
		DontMatch:   m.DontMatch,
		PenaltyRows: parsePenalties(m.Penalties),
	}

	volsSeen := map[string]*VolumeOption{}
	for _, iss := range issues {
		row := MatchIssueRow{
			IssueID:       iss.ID,
			Filename:      iss.Filename,
			LocalNumber:   iss.Number,
			LocalCoverURL: "/comics/" + pathEscape(iss.ID) + "/cover",
			Method:        methodByIssue[iss.ID],
		}
		if up, ok := upstreamByIssue[iss.ID]; ok && a.Match != nil {
			src := sourceByIssue[iss.ID]
			if cvIssue, lookupErr := a.Match.LookupIssue(src, up); lookupErr == nil {
				row.HasCV = true
				row.CVNumber = cvIssue.IssueNumber
				row.CVTitle = cvIssue.IssueName
				row.CVImageURL = cvIssue.ImageURL

				volID := strconv.FormatInt(cvIssue.VolumeID, 10)
				// The same number in the other dump is a different volume, so
				// "is this the chosen one" has to agree on source too.
				chosen := volID == m.VolumeID && src == m.Source
				row.OtherVolume = m.VolumeID != "" && !chosen
				row.CVVolumeName = cvIssue.VolumeName
				row.CVVolumeYear = cvIssue.VolumeStartYear
				if opt, seen := volsSeen[srcVol(src, volID)]; seen {
					opt.Count++
				} else {
					volsSeen[srcVol(src, volID)] = &VolumeOption{
						ID: volID, Source: src, Name: cvIssue.VolumeName,
						StartYear: cvIssue.VolumeStartYear, Publisher: cvIssue.Publisher,
						Count: 1, Current: chosen,
					}
				}
			}
			// A failed lookup (bad id, dump doesn't have it) leaves HasCV
			// false — the row still renders with local data only.
		}
		// Offer a re-pin target for rows that are unmatched or matched into
		// another volume, when the chosen volume has this local number.
		if a.Match != nil && m.VolumeID != "" && iss.Number != "" && (!row.HasCV || row.OtherVolume) {
			if target, err := a.Match.LookupIssueInVolume(m.Source, m.VolumeID, iss.Number); err == nil {
				row.RepinID = strconv.FormatInt(target.IssueID, 10)
				row.RepinNumber = target.IssueNumber
			}
		}
		content.Issues = append(content.Issues, row)
	}

	for _, opt := range volsSeen {
		content.Volumes = append(content.Volumes, *opt)
	}
	sort.Slice(content.Volumes, func(i, j int) bool {
		if content.Volumes[i].Count != content.Volumes[j].Count {
			return content.Volumes[i].Count > content.Volumes[j].Count
		}
		return content.Volumes[i].ID < content.Volumes[j].ID
	})

	// The header should describe the chosen volume; falling back to whichever
	// issue resolved first would show a minority volume's name on a split.
	if opt, ok := volsSeen[srcVol(m.Source, m.VolumeID)]; ok {
		content.CVVolumeName = opt.Name
		content.CVStartYear = opt.StartYear
		content.CVPublisher = opt.Publisher
	} else if len(content.Volumes) > 0 {
		content.CVVolumeName = content.Volumes[0].Name
		content.CVStartYear = content.Volumes[0].StartYear
		content.CVPublisher = content.Volumes[0].Publisher
	}

	return content, nil
}

func (a *Admin) handleMatchSeriesDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	content, err := a.buildMatchSeriesDetail(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	applyIssueCap(content, r.URL.Query().Get("all") == "1")
	content.Searchable = a.Match != nil
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" && a.Match != nil {
		content.Query = q
		vols, err := a.Match.SearchVolumes(q, 25)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, v := range vols {
			content.SearchResults = append(content.SearchResults, VolumeOption{
				ID: strconv.FormatInt(v.VolumeID, 10), Source: v.Source, Name: v.Name, StartYear: v.StartYear,
				Publisher: v.Publisher, Count: v.IssueCount,
				// Same id in the other dump is a different volume, so
				// "current" has to agree on both.
				Current: strconv.FormatInt(v.VolumeID, 10) == content.VolumeID && v.Source == content.Source,
			})
		}
	}

	data := a.baseData("match")
	data.Title = "Match: " + content.Name
	data.Content = content
	a.render(w, "match_detail.html", data)
}

// MatchReviewData is the view model for the one-at-a-time review screen: a
// single series (Detail) plus enough queue-position context to drive the
// progress indicator and the "Queue clear" end state.
type MatchReviewData struct {
	State    string
	Cursor   int // zero-based index into the queue, already clamped
	Position int // Cursor+1, for "Series X of Y" display
	Total    int // series currently in this queue

	// Empty is true once Cursor is past the end of the (possibly shrunk)
	// queue — the normal way a review session ends, not an error.
	Empty  bool
	Detail *MatchSeriesDetailData
}

// clampCursor parses the "i" query/form param. A negative or non-integer
// value is treated as the start of the queue rather than an error — the
// review screen is meant to be linkable/bookmarkable without fuss.
func clampCursor(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// handleMatchReview renders one series from a queue for review, with
// Confirm/Skip/Don't-match actions that auto-advance to the next one.
//
// Confirm and Don't match remove the series from the current queue, so they
// redirect back to the SAME cursor — the next unreviewed series has slid
// into that index. Skip leaves the series in place and redirects to i+1.
// See handleMatchConfirm/handleMatchDontMatch (redirect via matchRedirectTarget)
// and handleMatchReviewSkip.
func (a *Admin) handleMatchReview(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = store.MatchStateHighConfidence
	}
	cursor := clampCursor(r.URL.Query().Get("i"))

	matches, err := a.Store.ListReviewQueue(state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := a.baseData("match")
	data.Title = "Review: " + state

	if cursor >= len(matches) {
		data.Content = MatchReviewData{State: state, Cursor: cursor, Total: len(matches), Empty: true}
		a.render(w, "match_review.html", data)
		return
	}

	content, err := a.buildMatchSeriesDetail(matches[cursor].SeriesID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	applyIssueCap(content, r.URL.Query().Get("all") == "1")

	data.Content = MatchReviewData{
		State:    state,
		Cursor:   cursor,
		Position: cursor + 1,
		Total:    len(matches),
		Detail:   content,
	}
	a.render(w, "match_review.html", data)
}

// handleMatchReviewSkip advances the review cursor without acting on the
// current series — it leaves the queue untouched, so unlike confirm/don't
// match it must increment i rather than resubmit the same index.
func (a *Admin) handleMatchReviewSkip(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("state")
	if state == "" {
		state = store.MatchStateHighConfidence
	}
	cursor := clampCursor(r.FormValue("i"))
	http.Redirect(w, r,
		fmt.Sprintf("/_admin/match/review?state=%s&i=%d", url.QueryEscape(state), cursor+1),
		http.StatusSeeOther)
}
