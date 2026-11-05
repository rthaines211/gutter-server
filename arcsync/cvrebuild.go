package arcsync

import (
	"log"
	"sync"
	"time"

	"github.com/rthaines211/gutter-server/comicvine"
	"github.com/rthaines211/gutter-server/match/localcv"
	"github.com/rthaines211/gutter-server/store"
)

// maxArcDescriptionsPerRun caps how many ComicVine description fetches a
// single RunOnce makes, so an hourly rebuild never burns the whole API budget.
const maxArcDescriptionsPerRun = 25

// arcDescVersion tags ArcDescriptionPlainText's algorithm version. Bump it
// when the parsing logic changes materially (v2: section-aware synopsis
// extraction, dropping wiki boilerplate like "Creative Team"/"Collected
// Editions") so ArcsNeedingDescription re-derives already-fetched arcs from
// their ComicVine response — free, since GetStoryArc hits the disk cache
// for an id it has already fetched and never touches the API budget.
const arcDescVersion = 2

// CVRebuilder rebuilds the derived (source='comicvine') story arcs from the
// localcv dump's story_arc_credits, on start and hourly. Independent of
// Worker/MylarURL: it only needs the localcv dump to be open.
type CVRebuilder struct {
	store    *store.Store
	cv       *localcv.DB
	descSrc  *comicvine.Client // optional; nil = description fetch off
	stop     chan struct{}
	stopOnce sync.Once
}

func NewCVRebuilder(st *store.Store, cv *localcv.DB) *CVRebuilder {
	return &CVRebuilder{store: st, cv: cv, stop: make(chan struct{})}
}

// SetDescriptionSource enables fetching ComicVine story-arc descriptions
// after each rebuild. Pass nil to turn the feature back off.
func (r *CVRebuilder) SetDescriptionSource(c *comicvine.Client) { r.descSrc = c }

func (r *CVRebuilder) Start() {
	go func() {
		r.RunOnce()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.RunOnce()
			case <-r.stop:
				return
			}
		}
	}()
}

func (r *CVRebuilder) Stop() { r.stopOnce.Do(func() { close(r.stop) }) }

// RunOnce rebuilds the comicvine-derived arcs from the current library and
// localcv dump. On error it logs and returns without touching story_arcs —
// RebuildComicVineArcs is transactional, so a failed rebuild just keeps the
// arcs from the last successful one.
func (r *CVRebuilder) RunOnce() error {
	issueIDs, err := r.store.EnrichedCVIssueIDs()
	if err != nil {
		log.Printf("arcsync: comicvine arc rebuild: %v", err)
		return err
	}
	credits, err := r.cv.ArcCredits(issueIDs)
	if err != nil {
		log.Printf("arcsync: comicvine arc rebuild: %v", err)
		return err
	}
	in := make([]store.CVArcCredit, len(credits))
	for i, c := range credits {
		in[i] = store.CVArcCredit{CVIssueID: c.IssueID, ArcID: c.ArcID, ArcName: c.ArcName}
	}
	changed, err := r.store.RebuildComicVineArcs(in)
	if err != nil {
		log.Printf("arcsync: comicvine arc rebuild: %v", err)
		return err
	}
	log.Printf("arcsync: comicvine arcs rebuilt (%d changed)", changed)

	r.fetchArcDescriptions()
	return nil
}

// fetchArcDescriptions fills in plain-text ComicVine descriptions for arcs
// that don't have one yet. Best-effort: a per-arc fetch error is logged and
// skipped (fetched_at stays 0, so it retries next hour); it never fails
// RunOnce or touches the arcs rebuilt above.
func (r *CVRebuilder) fetchArcDescriptions() {
	// The Available() > 30 check is advisory, not a hard reservation: the
	// enrichment worker shares the same token bucket and can drain it
	// between this check and any GetStoryArc call below, at which point
	// Client.get's limiter.wait() just blocks this background goroutine
	// until the bucket refills. That's intended — it only delays the
	// descriptions backfill, it never errors or drops work.
	if r.descSrc == nil || r.descSrc.Available() <= 30 {
		return
	}
	targets, err := r.store.ArcsNeedingDescription(maxArcDescriptionsPerRun, arcDescVersion)
	if err != nil {
		log.Printf("arcsync: arc descriptions: %v", err)
		return
	}
	fetched := 0
	for _, target := range targets {
		sa, err := r.descSrc.GetStoryArc(target.CVArcID)
		if err != nil {
			log.Printf("arcsync: arc description fetch failed for %s: %v", target.ArcID, err)
			continue
		}
		text := comicvine.ArcDescriptionPlainText(sa.Description, sa.Deck)
		if err := r.store.SetArcDescription(target.ArcID, text, time.Now().UnixMilli(), arcDescVersion); err != nil {
			log.Printf("arcsync: arc description fetch failed for %s: %v", target.ArcID, err)
			continue
		}
		fetched++
	}
	log.Printf("arcsync: arc descriptions fetched (%d)", fetched)
}
