// server/detect_job_test.go
package server

import (
	"testing"

	"github.com/ryanhaines/comic-reader/metadata"
)

func TestDetectRegistrySingleActive(t *testing.T) {
	r := newDetectRegistry()

	j1, ok := r.start("job1", "comicA")
	if !ok || j1 == nil {
		t.Fatal("first start should succeed")
	}
	// Second start while job1 runs is rejected.
	if _, ok := r.start("job2", "comicB"); ok {
		t.Fatal("second start must be rejected while one is running")
	}

	// Progress is reflected in the snapshot.
	j1.setTotal(10)
	j1.incScanned()
	j1.incScanned()
	state, scanned, total, _ := j1.snapshot()
	if state != detectRunning || scanned != 2 || total != 10 {
		t.Fatalf("got state=%s scanned=%d total=%d", state, scanned, total)
	}

	// Finishing + clearing the active slot lets a new job start.
	j1.finishDone([]metadata.Candidate{{Page: 0, Title: "Ch1"}})
	r.clearActive(j1)
	j3, ok := r.start("job3", "comicC")
	if !ok || j3 == nil {
		t.Fatal("start should succeed after the active job is cleared")
	}

	if got, _, _, _ := j1.snapshot(); got != detectDone {
		t.Fatalf("expected done, got %s", got)
	}
}

func TestDetectRegistryLookupUnknown(t *testing.T) {
	r := newDetectRegistry()
	if r.lookup("nope") != nil {
		t.Fatal("unknown job id must return nil")
	}
	j, _ := r.start("jobX", "comicX")
	if r.lookup("jobX") != j {
		t.Fatal("lookup must return the registered job")
	}
}
