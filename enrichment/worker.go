package enrichment

import (
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/ryanhaines/comic-reader/comicvine"
	"github.com/ryanhaines/comic-reader/store"
)

// VineClient is the subset of the ComicVine client the worker needs;
// faked in tests.
type VineClient interface {
	SearchVolumes(name string) ([]comicvine.Volume, error)
	GetVolume(id int) (*comicvine.VolumeDetail, error)
	ListVolumeIssues(volumeID int) ([]comicvine.Issue, error)
	Available() int
}

// Candidate is one review-queue entry, stored as JSON in
// enrichment_state.candidates and returned by GET /enrichment/pending.
type Candidate struct {
	VolumeID   int    `json:"volume_id"`
	Name       string `json:"name"`
	StartYear  int    `json:"start_year"`
	Publisher  string `json:"publisher"`
	IssueCount int    `json:"issue_count"`
	CoverURL   string `json:"cover_url"`
	Snippet    string `json:"description_snippet"`
}

// Worker drains queued enrichment_state rows one at a time. All state
// lives in SQLite, so restarts resume cleanly. Kick() wakes it early
// (after a confirmed review); otherwise it polls every 5 minutes, which
// also picks up series added by later scans.
type Worker struct {
	Store    *store.Store
	Client   VineClient
	kick     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
}

func NewWorker(st *store.Store, c VineClient) *Worker {
	return &Worker{Store: st, Client: c, kick: make(chan struct{}, 1), stop: make(chan struct{})}
}

func (w *Worker) Start() {
	go func() {
		for {
			did, err := w.processOne()
			if err != nil {
				log.Printf("enrichment: %v", err)
			}
			if did {
				continue
			}
			select {
			case <-w.kick:
			case <-time.After(5 * time.Minute):
			case <-w.stop:
				return
			}
		}
	}()
}

func (w *Worker) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

func (w *Worker) Stop() { w.stopOnce.Do(func() { close(w.stop) }) }

// processOne handles a single queued series. Returns true when it did
// work (caller should loop immediately), false when idle.
func (w *Worker) processOne() (bool, error) {
	now := time.Now().UnixMilli()
	state, err := w.Store.NextQueuedEnrichment(now)
	if err != nil {
		return false, err
	}
	if state == nil {
		// Idle: sweep for series that don't have a state row yet
		// (covers startup and series added by later scans).
		added, err := w.Store.EnqueueMissingEnrichment()
		if err != nil {
			return false, err
		}
		return added > 0, nil
	}

	// Record the attempt before any fallible work so transient failures
	// are subject to back-off rather than spinning at full speed.
	if err := w.Store.RecordEnrichmentAttempt(state.SeriesID, now); err != nil {
		return true, err
	}

	series, err := w.Store.GetSeries(state.SeriesID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Series was deleted; FK cascade usually removes the state row,
			// but mark it skipped just in case. If that row is also gone, treat as success.
			if serr := w.Store.SetEnrichmentStatus(state.SeriesID, "skipped"); serr != nil && !errors.Is(serr, store.ErrNotFound) {
				return true, serr
			}
			return true, nil
		}
		// Transient error — stay queued; back-off applies since attempt was recorded.
		return true, err
	}

	// A series confirmed from another source (GCD) has no ComicVine volume
	// to enrich from, and searching live for one would overwrite its
	// confirmed row with an 'auto' guess. Leave it alone.
	if m, err := w.Store.GetSeriesMatch(state.SeriesID); err == nil &&
		m.State == store.MatchStateConfirmed && m.Source != store.SourceComicVine {
		return true, w.Store.SetEnrichmentStatus(state.SeriesID, "skipped")
	}

	if state.ChosenVolumeID != 0 {
		return true, w.apply(series, state.ChosenVolumeID, "confirmed")
	}

	results, err := w.Client.SearchVolumes(series.Name)
	if err != nil {
		return true, err // stays queued; backoff via recorded attempt
	}
	d := Match(series.Name, series.Year, series.IssueCount, results)
	switch {
	case d.Auto:
		return true, w.apply(series, d.Volume.ID, "auto")
	case len(d.Candidates) == 0:
		return true, w.Store.SetEnrichmentStatus(series.ID, "unmatched")
	default:
		cands := make([]Candidate, 0, len(d.Candidates))
		for _, v := range d.Candidates {
			cands = append(cands, Candidate{
				VolumeID:   v.ID,
				Name:       v.Name,
				StartYear:  v.StartYear,
				Publisher:  v.Publisher,
				IssueCount: v.CountOfIssues,
				CoverURL:   v.CoverURL,
				Snippet:    snippet(v.Description, 200),
			})
		}
		buf, err := json.Marshal(cands)
		if err != nil {
			return true, err
		}
		return true, w.Store.SetEnrichmentCandidates(series.ID, string(buf))
	}
}

// apply fetches volume detail + issues and writes enrichment, then bumps
// the series updated_at so delta sync carries the new fields to the app.
//
// Policy: if the series already has a series_enrichment row with status='manual'
// (set by UpdateSeriesMetadata when the user edits creator/description fields),
// the worker must not overwrite it. The series is marked 'skipped_manual' in
// enrichment_state so it does not loop back into the queue.
func (w *Worker) apply(series *store.Series, volumeID int, status string) error {
	// Guard: never overwrite manual enrichment.
	existing, err := w.Store.GetSeriesEnrichment(series.ID)
	if err == nil && existing.Status == "manual" {
		// Mark state so the worker doesn't keep retrying this series.
		if serr := w.Store.SetEnrichmentStatus(series.ID, "skipped_manual"); serr != nil && !errors.Is(serr, store.ErrNotFound) {
			return serr
		}
		log.Printf("enrichment: skipping %s — manual enrichment row preserved", series.ID)
		return nil
	}

	vd, err := w.Client.GetVolume(volumeID)
	if err != nil {
		return err
	}
	vines, err := w.Client.ListVolumeIssues(volumeID)
	if err != nil {
		return err
	}
	creators := make([]store.Creator, 0, len(vd.Creators))
	for _, c := range vd.Creators {
		creators = append(creators, store.Creator{ID: c.ID, Name: c.Name, Role: c.Role})
	}
	creatorsJSON, err := json.Marshal(creators)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if err := w.Store.UpsertSeriesEnrichment(store.SeriesEnrichment{
		SeriesID:    series.ID,
		VolumeID:    vd.ID,
		Description: vd.Description,
		Publisher:   vd.Publisher,
		StartYear:   vd.StartYear,
		Creators:    string(creatorsJSON),
		Status:      status,
		EnrichedAt:  now,
	}); err != nil {
		return err
	}

	byNumber := make(map[string]comicvine.Issue, len(vines))
	for _, vi := range vines {
		byNumber[NormalizeIssueNumber(vi.Number)] = vi
	}
	dbIssues, err := w.Store.ListIssues(series.ID)
	if err != nil {
		return err
	}
	for _, di := range dbIssues {
		vi, ok := byNumber[NormalizeIssueNumber(di.Number)]
		if !ok {
			continue // unmatched issues stay un-enriched, by design
		}
		if err := w.Store.UpsertIssueEnrichment(store.IssueEnrichment{
			IssueID:    di.ID,
			VineID:     vi.ID,
			Title:      vi.Title,
			Summary:    vi.Summary,
			CoverDate:  vi.CoverDate,
			EnrichedAt: now,
		}); err != nil {
			return err
		}
	}

	if err := w.Store.SetEnrichmentStatus(series.ID, "enriched"); err != nil {
		return err
	}
	return w.Store.TouchSeries(series.ID)
}

func snippet(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
