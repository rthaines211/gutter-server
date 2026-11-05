package admin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/rthaines211/gutter-server/match"
	"github.com/rthaines211/gutter-server/store"
)

const (
	pipelineGateWait  = 2 * time.Minute
	pipelineRunCap    = 30 * time.Minute
	pipelineRetention = 90 * 24 * time.Hour
)

// waitFor polls a non-blocking gate try-lock until it succeeds or d passes.
// The writeback worker holds its flag per file, so a single attempt during
// a long writeback pass would almost always lose.
func waitFor(ctx context.Context, try func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if try() {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// RunPipeline processes the series the scanner queued: match or fill,
// auto-confirm when the rule allows, enrich, queue writeback, organize.
// One run row records what happened. Safe to call when nothing is pending.
func (a *Admin) RunPipeline(ctx context.Context) error {
	if a.Match == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, pipelineRunCap)
	defer cancel()

	now := time.Now()
	if err := a.Store.PrunePipelineRuns(now.Add(-pipelineRetention).UnixMilli()); err != nil {
		log.Printf("pipeline: prune: %v", err)
	}
	ids, err := a.Store.ListPipelinePending()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil // nothing arrived: no run row, no noise
	}

	run := store.PipelineRun{StartedAt: now.UnixMilli(), SeriesCount: len(ids)}
	left := 0 // series the time cap cut off; they stay pending
	defer func() {
		if left > 0 {
			reason := fmt.Sprintf("time cap reached; %d series left pending", left)
			if run.SkippedReason != "" {
				reason += "; " + run.SkippedReason
			}
			run.SkippedReason = reason
		}
		// The only "ready" news: comic ids the library has never had. Claimed
		// last, so this run's organize renames are already folded in.
		fresh, err := a.Store.ClaimNewIssues()
		if err != nil {
			log.Printf("pipeline: claim new issues: %v", err)
		}
		for _, n := range fresh {
			run.Events = append(run.Events, store.PipelineEvent{SeriesID: n.SeriesID, SeriesName: n.SeriesName,
				Kind: "new_issues", Detail: fmt.Sprintf("%d new issue(s)", n.Count)})
		}
		run.FinishedAt = time.Now().UnixMilli()
		if err := a.Store.InsertPipelineRun(run); err != nil {
			log.Printf("pipeline: record run: %v", err)
		}
		if a.OnRun != nil {
			go a.OnRun(run)
		}
	}()
	event := func(s *store.Series, id, kind, detail string) {
		name := id
		if s != nil {
			name = s.Name
		}
		run.Events = append(run.Events, store.PipelineEvent{SeriesID: id, SeriesName: name, Kind: kind, Detail: detail})
	}

	if !waitFor(ctx, a.canStartMatchRun, pipelineGateWait) {
		run.SkippedReason = a.busyReason()
		return nil
	}
	var toOrganize []string
	func() {
		defer a.matchRunning.Store(false)
		for i, id := range ids {
			if ctx.Err() != nil {
				left = len(ids) - i
				return // unfinished series stay pending for the next scan
			}
			s, err := a.Store.GetSeries(id)
			if errors.Is(err, store.ErrNotFound) {
				_ = a.Store.RemovePipelinePending(id) // deleted or renamed away
				continue
			}
			if err != nil {
				event(nil, id, "error", err.Error())
				continue
			}
			if a.pipelineMatch(ctx, s, event) {
				toOrganize = append(toOrganize, id)
			}
		}
	}()

	if len(toOrganize) == 0 {
		return nil
	}
	if !waitFor(ctx, a.canStartOrganize, pipelineGateWait) {
		run.SkippedReason = "organize: " + a.busyReason()
		return nil // still pending; organized next time
	}
	defer a.organizeRunning.Store(false)
	if a.ScanState != nil {
		a.ScanState.SetOrganizeRunning(true)
		defer a.ScanState.SetOrganizeRunning(false)
	}
	// ponytail: Organizer.Apply re-plans the whole library 3x per call; fine
	// for a weekly pull's dozen series. Batch into one Apply if pulls grow.
	for i, id := range toOrganize {
		if ctx.Err() != nil {
			left += len(toOrganize) - i
			break // matched but unorganized: still pending, organized next time
		}
		s, _ := a.Store.GetSeries(id)
		rep, err := a.organizer().Apply(id)
		if err != nil {
			event(s, id, "error", "organize: "+err.Error())
			continue
		}
		for _, line := range rep.Errors {
			event(s, id, "flagged", line)
		}
		if rep.Applied > 0 {
			event(s, id, "organized", fmt.Sprintf("%d renamed/moved", rep.Applied))
		}
		_ = a.Store.RemovePipelinePending(id)
	}
	return nil
}

// pipelineMatch runs steps 1–3 for one series. It reports whether the
// series is confirmed and should be organized. Series it settles without
// organizing (sent to review, don't-match) leave the pending set here.
func (a *Admin) pipelineMatch(ctx context.Context, s *store.Series, event func(*store.Series, string, string, string)) bool {
	id := s.ID
	m, err := a.Store.GetSeriesMatch(id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		event(s, id, "error", err.Error())
		return false
	}
	if m != nil && m.DontMatch {
		_ = a.Store.RemovePipelinePending(id)
		return false
	}

	if m != nil && m.State == store.MatchStateConfirmed {
		fr, err := a.Match.FillConfirmed(ctx, *s)
		if err != nil {
			event(s, id, "error", "fill: "+err.Error())
			return false
		}
		for _, f := range fr.Unbound {
			event(s, id, "unbound", f)
		}
		for _, mf := range fr.Misfiled {
			event(s, id, "misfiled", fmt.Sprintf("%s → volume %s", mf.Filename, mf.VolumeID))
		}
		if len(fr.Bound) > 0 {
			if err := a.Match.Apply(id); err != nil {
				event(s, id, "error", "apply: "+err.Error())
				return false
			}
			if err := a.Store.EnqueueWriteback(fr.Bound...); err != nil {
				log.Printf("pipeline: writeback enqueue %s: %v", id, err)
			}
			event(s, id, "filled", fmt.Sprintf("%d issue(s)", len(fr.Bound)))
		}
		return true
	}

	if _, _, err := a.Match.MatchSeries(ctx, *s); err != nil {
		event(s, id, "error", "match: "+err.Error())
		return false
	}
	nm, err := a.Store.GetSeriesMatch(id)
	if err != nil {
		event(s, id, "error", err.Error())
		return false
	}
	if !match.AutoConfirmable(nm) {
		event(s, id, "sent_to_review", fmt.Sprintf("%s via %s %s", nm.State, nm.Method, nm.Penalties))
		_ = a.Store.RemovePipelinePending(id)
		return false
	}
	if err := a.confirmBy(id, "pipeline"); err != nil {
		event(s, id, "error", "confirm: "+err.Error())
		return false
	}
	event(s, id, "auto_confirmed", fmt.Sprintf("volume %s via %s", nm.VolumeID, nm.Method))
	return true
}
