// server/detect_job.go
package server

import (
	"sync"

	"github.com/ryanhaines/comic-reader/metadata"
)

type detectState string

const (
	detectRunning detectState = "running"
	detectDone    detectState = "done"
	detectFailed  detectState = "failed"
	detectUnknown detectState = "unknown"
)

// detectJob is one chapter-detection OCR job. scanned/total drive the client's
// determinate progress bar; candidates are set on completion.
type detectJob struct {
	id      string
	comicID string

	mu         sync.Mutex
	state      detectState
	scanned    int
	total      int
	candidates []metadata.Candidate
}

func (j *detectJob) setTotal(n int) {
	j.mu.Lock()
	j.total = n
	j.mu.Unlock()
}

func (j *detectJob) incScanned() {
	j.mu.Lock()
	j.scanned++
	j.mu.Unlock()
}

func (j *detectJob) finishDone(c []metadata.Candidate) {
	j.mu.Lock()
	j.state = detectDone
	j.candidates = c
	j.mu.Unlock()
}

func (j *detectJob) finishFailed() {
	j.mu.Lock()
	j.state = detectFailed
	j.mu.Unlock()
}

func (j *detectJob) snapshot() (detectState, int, int, []metadata.Candidate) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state, j.scanned, j.total, j.candidates
}

// detectRegistry tracks at most one active detection job. It retains only the
// current/last job; a status poll for any other id returns "unknown" (which is
// exactly what happens to an in-flight job after a server restart).
type detectRegistry struct {
	mu     sync.Mutex
	active *detectJob
	jobs   map[string]*detectJob
}

func newDetectRegistry() *detectRegistry {
	return &detectRegistry{jobs: make(map[string]*detectJob)}
}

// start registers a new running job, or returns (nil, false) if one is already
// running ("one at a time").
func (r *detectRegistry) start(id, comicID string) (*detectJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		r.active.mu.Lock()
		running := r.active.state == detectRunning
		r.active.mu.Unlock()
		if running {
			return nil, false
		}
	}
	j := &detectJob{id: id, comicID: comicID, state: detectRunning}
	r.active = j
	r.jobs = map[string]*detectJob{id: j} // bound memory: keep only the current job
	return j, true
}

// isBusy reports whether a job is currently running.
func (r *detectRegistry) isBusy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return false
	}
	r.active.mu.Lock()
	running := r.active.state == detectRunning
	r.active.mu.Unlock()
	return running
}

func (r *detectRegistry) clearActive(j *detectJob) {
	r.mu.Lock()
	if r.active == j {
		r.active = nil
	}
	r.mu.Unlock()
}

func (r *detectRegistry) lookup(jobID string) *detectJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[jobID]
}
