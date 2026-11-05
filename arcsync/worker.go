// Package arcsync pulls captured story arcs from a Mylar instance and imports
// them into the local store on an hourly cadence.
package arcsync

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

type mylarArc struct {
	Key           string          `json:"key"`
	Name          string          `json:"name"`
	Image         string          `json:"image"`
	CVArcID       string          `json:"cv_arc_id"`
	SelfContained *bool           `json:"self_contained"`
	TotalIssues   int             `json:"total_issues"`
	Issues        []mylarArcIssue `json:"issues"`
}

type mylarArcIssue struct {
	CVIssueID  string `json:"cv_issue_id"`
	CVVolumeID string `json:"cv_volume_id"`
	Number     string `json:"number"`
	Order      int    `json:"order"`
}

type Worker struct {
	store    *store.Store
	url      string // Mylar base URL, e.g. http://host:8090
	client   *http.Client
	stop     chan struct{}
	stopOnce sync.Once
}

func New(st *store.Store, mylarURL string) *Worker {
	return &Worker{
		store:  st,
		url:    mylarURL,
		client: &http.Client{Timeout: 30 * time.Second},
		stop:   make(chan struct{}),
	}
}

func (w *Worker) Start() {
	go func() {
		w.runOnce()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				w.runOnce()
			case <-w.stop:
				return
			}
		}
	}()
}

func (w *Worker) Stop() { w.stopOnce.Do(func() { close(w.stop) }) }

// runOnce fetches and imports; on any failure it logs and keeps the last import
// (the store import is atomic, so a bad fetch never partially wipes arcs).
func (w *Worker) runOnce() {
	arcs, err := w.fetch()
	if err != nil {
		log.Printf("arcsync: %v", err)
		return
	}
	in := make([]store.ArcInput, 0, len(arcs))
	for _, a := range arcs {
		issues := make([]store.ArcIssueInput, 0, len(a.Issues))
		for _, iss := range a.Issues {
			issues = append(issues, store.ArcIssueInput{
				CVIssueID:  iss.CVIssueID,
				CVVolumeID: iss.CVVolumeID,
				Number:     iss.Number,
				Order:      iss.Order,
			})
		}
		in = append(in, store.ArcInput{
			Key: a.Key, Name: a.Name, Image: a.Image, CVArcID: a.CVArcID,
			SelfContained: a.SelfContained, TotalIssues: a.TotalIssues, Issues: issues,
		})
	}
	if err := w.store.ImportArcs(in); err != nil {
		log.Printf("arcsync: import: %v", err)
		return
	}
	log.Printf("arcsync: imported %d arcs from %s", len(in), w.url)
}

func (w *Worker) fetch() ([]mylarArc, error) {
	resp, err := w.client.Get(w.url + "/arcExport")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{resp.StatusCode}
	}
	var arcs []mylarArc
	if err := json.NewDecoder(resp.Body).Decode(&arcs); err != nil {
		return nil, err
	}
	return arcs, nil
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return "arcExport HTTP " + http.StatusText(e.code) }
