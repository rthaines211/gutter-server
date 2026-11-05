// Package notify turns pipeline runs and scan failures into push
// notifications and fans them out to every registered device.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/rthaines211/gutter-server/push"
	"github.com/rthaines211/gutter-server/store"
)

// failureWindow caps failure pushes to one per kind per window.
// ponytail: in-memory, so a restart resets it; fine for one process.
const failureWindow = 6 * time.Hour

// Summarize builds the single notification for a run. ok is false when the
// run has nothing worth a buzz.
func Summarize(run store.PipelineRun, includeErrors bool) (n push.Notification, ok bool) {
	var ready []string
	seenReady, seenReview := map[string]bool{}, map[string]bool{}
	review, errs := 0, 0
	var firstErr store.PipelineEvent
	for _, e := range run.Events {
		switch e.Kind {
		case "new_issues": // only first sightings; re-fills and renames are not news
			if !seenReady[e.SeriesID] {
				seenReady[e.SeriesID] = true
				ready = append(ready, e.SeriesName)
			}
		case "sent_to_review":
			if !seenReview[e.SeriesID] {
				seenReview[e.SeriesID] = true
				review++
			}
		case "error":
			if errs == 0 {
				firstErr = e
			}
			errs++
		}
	}
	if !includeErrors {
		errs = 0
	}
	if errs == 0 && review == 0 && len(ready) == 0 {
		return push.Notification{}, false
	}

	var parts []string
	if errs > 0 {
		parts = append(parts, firstErr.SeriesName+": "+firstErr.Detail)
	}
	reviewPart := ""
	if review > 0 {
		reviewPart = fmt.Sprintf("%d series need review", review)
		if review == 1 {
			reviewPart = "1 series needs review"
		}
		parts = append(parts, reviewPart)
	}
	if len(ready) > 0 {
		p := "New issues in " + strings.Join(ready[:min(2, len(ready))], ", ")
		if len(ready) > 2 {
			p += fmt.Sprintf(" +%d", len(ready)-2)
		}
		parts = append(parts, p)
	}

	switch {
	case errs == 1:
		n.Title = "Pipeline: 1 error"
	case errs > 1:
		n.Title = fmt.Sprintf("Pipeline: %d errors", errs)
	case review > 0:
		n.Title = reviewPart
	default:
		n.Title = "New issues ready"
	}
	n.Body = strings.Join(parts, " · ")
	switch {
	case review > 0:
		n.Screen = "review"
	case len(ready) > 0:
		n.Screen = "read"
	}
	return n, true
}

type Notifier struct {
	Store       *store.Store
	Send        func(context.Context, push.Device, push.Notification) error
	DevicesPath string
	Now         func() time.Time

	mu       sync.Mutex
	lastFail map[string]time.Time
}

// allowFailure reports whether a failure push of this kind may go out now,
// and if so starts its window.
func (n *Notifier) allowFailure(kind string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.Now()
	if last, ok := n.lastFail[kind]; ok && now.Sub(last) < failureWindow {
		return false
	}
	if n.lastFail == nil {
		n.lastFail = map[string]time.Time{}
	}
	n.lastFail[kind] = now
	return true
}

func (n *Notifier) PipelineRun(run store.PipelineRun) {
	if n == nil {
		return
	}
	hasErr := false
	for _, e := range run.Events {
		if e.Kind == "error" {
			hasErr = true
			break
		}
	}
	msg, ok := Summarize(run, hasErr && n.allowFailure("pipeline_error"))
	if ok {
		n.broadcast(msg)
	}
}

func (n *Notifier) ScanFailed(err error) {
	if n == nil || !n.allowFailure("scan_failed") {
		return
	}
	body := []rune(err.Error())
	if len(body) > 200 {
		body = body[:200]
	}
	n.broadcast(push.Notification{Title: "Scan failed", Body: string(body)})
}

// SyncDevices rewrites the token mirror the host watcher reads.
func (n *Notifier) SyncDevices() error {
	if n == nil {
		return nil
	}
	ds, err := n.Store.ListDeviceTokens()
	if err != nil {
		return err
	}
	return push.WriteDevices(n.DevicesPath, ds)
}

func (n *Notifier) broadcast(msg push.Notification) {
	ds, err := n.Store.ListDeviceTokens()
	if err != nil {
		log.Printf("notify: listing devices: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pruned := false
	for _, d := range ds {
		err := n.Send(ctx, d, msg)
		switch {
		case errors.Is(err, push.ErrUnregistered):
			log.Printf("notify: dropping unregistered device %.8s…", d.Token)
			if err := n.Store.DeleteDeviceToken(d.Token); err != nil {
				log.Printf("notify: deleting device: %v", err)
			}
			pruned = true
		case err != nil:
			log.Printf("notify: send to %.8s…: %v", d.Token, err)
		}
	}
	if pruned {
		if err := n.SyncDevices(); err != nil {
			log.Printf("notify: syncing device mirror: %v", err)
		}
	}
}
