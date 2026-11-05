package tagger

import (
	"log"
	"sync"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

type DetectedChapter struct {
	Page       int
	Title      string
	Confidence string
}

// Detector is the archive+OCR surface the worker needs; implemented by the
// server, faked in tests.
type Detector interface {
	PageMeta(comicID string) (pageCount int, pageNames []string, err error)
	Detect(comicID string) ([]DetectedChapter, error)
	RecentlyActive() bool // true while the reader is serving pages — worker yields
}

type Options struct {
	MinPages     int
	QuietHours   string
	PauseBetween time.Duration
	Now          func() time.Time
}

type Worker struct {
	st       *store.Store
	d        Detector
	opts     Options
	stop     chan struct{}
	stopOnce sync.Once
}

func NewWorker(st *store.Store, d Detector, opts Options) *Worker {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Worker{st: st, d: d, opts: opts, stop: make(chan struct{})}
}

func (w *Worker) Start() {
	go func() {
		for {
			did, err := w.processOne()
			if err != nil {
				log.Printf("tagger: %v", err)
			}
			if did {
				select {
				case <-time.After(w.opts.PauseBetween):
				case <-w.stop:
					return
				}
				continue
			}
			select {
			case <-time.After(5 * time.Minute):
			case <-w.stop:
				return
			}
		}
	}()
}

func (w *Worker) Stop() { w.stopOnce.Do(func() { close(w.stop) }) }

// processOne handles a single comic. didWork=false means "nothing to do right
// now" (queue empty, reader active, or outside the quiet window) — the caller
// sleeps. Governance checks happen BEFORE any drive read.
func (w *Worker) processOne() (bool, error) {
	// Yield to the reader.
	if w.d.RecentlyActive() {
		return false, nil
	}
	// Quiet-hours gate (if configured).
	if start, end, ok := ParseQuietHours(w.opts.QuietHours); ok {
		if !InQuietWindow(w.opts.Now().Hour(), start, end) {
			return false, nil
		}
	}

	comicID, ok, err := w.st.NextTaggableComic()
	if err != nil || !ok {
		return false, err
	}

	// Cheap pre-check: central-directory metadata only, no image reads.
	pageCount, pageNames, err := w.d.PageMeta(comicID)
	if err != nil {
		return true, w.st.MarkTagged(comicID, "failed")
	}
	if !IsLikelyCollection(pageCount, pageNames, w.opts.MinPages) {
		return true, w.st.MarkTagged(comicID, "skipped")
	}

	// Expensive: full OCR/structural detection (minutes).
	chapters, err := w.d.Detect(comicID)
	if err != nil {
		return true, w.st.MarkTagged(comicID, "failed")
	}

	chs := make([]store.Chapter, 0, len(chapters))
	conf := "medium"
	for _, c := range chapters {
		chs = append(chs, store.Chapter{ComicID: comicID, PageNum: c.Page, Title: c.Title})
		if c.Confidence != "" {
			conf = c.Confidence
		}
	}
	if err := w.st.WriteAutoChapters(comicID, chs, conf); err != nil {
		return true, err
	}
	return true, w.st.MarkTagged(comicID, "processed")
}
