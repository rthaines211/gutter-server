package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScanStateLifecycle(t *testing.T) {
	ss := NewScanState()
	snap := ss.Snapshot()
	if snap.IsScanning {
		t.Error("new ScanState should not be scanning")
	}
	if len(snap.LogEntries) != 0 {
		t.Error("new ScanState should have no log entries")
	}

	ss.StartScan()
	snap = ss.Snapshot()
	if !snap.IsScanning {
		t.Error("should be scanning after StartScan")
	}

	ss.AddLog("INFO", "scan starting · 2 directories")
	ss.AddLog("OK", "series Iron Comet · 24 issues")
	ss.AddLog("WARN", "comicinfo.xml malformed")

	snap = ss.Snapshot()
	if len(snap.LogEntries) != 3 {
		t.Fatalf("expected 3 log entries, got %d", len(snap.LogEntries))
	}
	if snap.LogEntries[0].Level != "INFO" {
		t.Errorf("first entry level = %q, want INFO", snap.LogEntries[0].Level)
	}
	if snap.LogEntries[0].Timestamp.IsZero() {
		t.Error("log entry should have a timestamp")
	}

	ss.FinishScan(ScanResult{
		SeriesCount:  142,
		NewCount:     3,
		RemovedCount: 0,
		WarningCount: 1,
	})

	snap = ss.Snapshot()
	if snap.IsScanning {
		t.Error("should not be scanning after FinishScan")
	}
	if snap.NewCount != 3 {
		t.Errorf("new count = %d, want 3", snap.NewCount)
	}
	if snap.LastScanAt.IsZero() {
		t.Error("LastScanAt should be set")
	}
	if snap.DurationMs < 0 {
		t.Error("DurationMs should be non-negative")
	}
}

func TestScanStateStartClearsLog(t *testing.T) {
	ss := NewScanState()
	ss.AddLog("INFO", "old entry")
	ss.StartScan()
	snap := ss.Snapshot()
	if len(snap.LogEntries) != 0 {
		t.Errorf("StartScan should clear log entries, got %d", len(snap.LogEntries))
	}
}

func TestScanStateProgressCounters(t *testing.T) {
	ss := NewScanState()
	ss.StartScan()

	ss.SetTotal(50)
	snap := ss.Snapshot()
	if snap.Total != 50 {
		t.Errorf("total = %d, want 50", snap.Total)
	}
	if snap.Scanned != 0 {
		t.Errorf("scanned = %d, want 0", snap.Scanned)
	}

	ss.IncrScanned()
	ss.IncrScanned()
	ss.IncrScanned()
	snap = ss.Snapshot()
	if snap.Scanned != 3 {
		t.Errorf("scanned = %d, want 3", snap.Scanned)
	}

	// StartScan should reset counters
	ss.StartScan()
	snap = ss.Snapshot()
	if snap.Scanned != 0 {
		t.Errorf("scanned after StartScan = %d, want 0", snap.Scanned)
	}
	if snap.Total != 0 {
		t.Errorf("total after StartScan = %d, want 0", snap.Total)
	}
}

func TestSnapshotReportsPipelineRunning(t *testing.T) {
	ss := NewScanState()
	ss.SetPipelineRunning(true)
	snap := ss.Snapshot()
	if !snap.PipelineRunning || !ss.IsPipelineRunning() {
		t.Fatal("pipeline should be reported running")
	}
	b, _ := json.Marshal(snap)
	if !strings.Contains(string(b), `"pipeline_running":true`) {
		t.Fatalf("json missing pipeline_running: %s", b)
	}
}
