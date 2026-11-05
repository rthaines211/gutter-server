package admin

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerStartStop(t *testing.T) {
	var count int64
	scanFn := func() { atomic.AddInt64(&count, 1) }

	sched := NewScheduler(scanFn)
	sched.Start("100ms")
	time.Sleep(350 * time.Millisecond)
	sched.Stop()

	got := atomic.LoadInt64(&count)
	if got < 2 || got > 5 {
		t.Errorf("scan count = %d, expected 2-5 in 350ms at 100ms interval", got)
	}
}

func TestSchedulerManualOnly(t *testing.T) {
	var count int64
	scanFn := func() { atomic.AddInt64(&count, 1) }

	sched := NewScheduler(scanFn)
	sched.Start("")
	time.Sleep(200 * time.Millisecond)
	sched.Stop()

	if atomic.LoadInt64(&count) != 0 {
		t.Error("scheduler should not run scans with empty interval")
	}
}

func TestSchedulerUpdateInterval(t *testing.T) {
	var count int64
	scanFn := func() { atomic.AddInt64(&count, 1) }

	sched := NewScheduler(scanFn)
	sched.Start("")
	time.Sleep(150 * time.Millisecond)
	if atomic.LoadInt64(&count) != 0 {
		t.Fatal("should not scan in manual mode")
	}

	sched.UpdateInterval("100ms")
	time.Sleep(350 * time.Millisecond)
	sched.Stop()

	got := atomic.LoadInt64(&count)
	if got < 2 {
		t.Errorf("after switching to 100ms interval, got %d scans, expected >= 2", got)
	}
}
