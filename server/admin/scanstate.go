package admin

import (
	"sync"
	"sync/atomic"
	"time"
)

type ScanLogEntry struct {
	Timestamp time.Time
	Level     string
	Message   string
}

type ScanResult struct {
	SeriesCount  int
	NewCount     int
	RemovedCount int
	WarningCount int
}

type ScanSnapshot struct {
	IsScanning      bool           `json:"is_scanning"`
	PipelineRunning bool           `json:"pipeline_running"`
	Scanned         int            `json:"scanned"`
	Total           int            `json:"total"`
	StartedAt       time.Time      `json:"started_at"`
	DurationMs      int64          `json:"duration_ms"`
	LastScanAt      time.Time      `json:"last_scan_at"`
	NewCount        int            `json:"new_count"`
	RemovedCount    int            `json:"removed_count"`
	WarningCount    int            `json:"warning_count"`
	LogEntries      []ScanLogEntry `json:"log_entries"`
}

type ScanState struct {
	mu               sync.RWMutex
	isScanning       bool
	scanned          int
	total            int
	scanStartedAt    time.Time
	lastScanAt       time.Time
	lastScanDuration time.Duration
	newCount         int
	removedCount     int
	warningCount     int
	logEntries       []ScanLogEntry

	// organizeRunning mirrors Admin.organizeRunning so the scanner (which has
	// no reference to Admin) can refuse to start while an organize apply is
	// mutating the same folders on disk. See canStartOrganize for the
	// reverse direction of this guard.
	organizeRunning atomic.Bool

	// pipelineRunning is true for the whole matching-pipeline run that
	// follows a scan, so clients keep polling past is_scanning=false.
	pipelineRunning atomic.Bool
}

// IsScanning reports whether a scan is currently in progress, without the
// cost of copying log entries the way Snapshot does.
func (ss *ScanState) IsScanning() bool {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.isScanning
}

// SetOrganizeRunning records whether an organize apply is in flight.
func (ss *ScanState) SetOrganizeRunning(running bool) {
	ss.organizeRunning.Store(running)
}

// IsOrganizeRunning reports whether an organize apply is in flight.
func (ss *ScanState) IsOrganizeRunning() bool {
	return ss.organizeRunning.Load()
}

// SetPipelineRunning records whether the matching pipeline is in flight.
func (ss *ScanState) SetPipelineRunning(running bool) {
	ss.pipelineRunning.Store(running)
}

// IsPipelineRunning reports whether the matching pipeline is in flight.
func (ss *ScanState) IsPipelineRunning() bool {
	return ss.pipelineRunning.Load()
}

func NewScanState() *ScanState {
	return &ScanState{}
}

func (ss *ScanState) StartScan() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.isScanning = true
	ss.scanStartedAt = time.Now()
	ss.scanned = 0
	ss.total = 0
	ss.logEntries = nil
}

func (ss *ScanState) SetTotal(n int) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.total = n
}

func (ss *ScanState) IncrScanned() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.scanned++
}

func (ss *ScanState) AddLog(level, message string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if len(ss.logEntries) < 100 {
		ss.logEntries = append(ss.logEntries, ScanLogEntry{
			Timestamp: time.Now(),
			Level:     level,
			Message:   message,
		})
	}
}

func (ss *ScanState) FinishScan(result ScanResult) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.isScanning = false
	ss.lastScanAt = time.Now()
	ss.lastScanDuration = time.Since(ss.scanStartedAt)
	ss.newCount = result.NewCount
	ss.removedCount = result.RemovedCount
	ss.warningCount = result.WarningCount
}

func (ss *ScanState) Snapshot() ScanSnapshot {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	entries := make([]ScanLogEntry, len(ss.logEntries))
	copy(entries, ss.logEntries)
	return ScanSnapshot{
		IsScanning:      ss.isScanning,
		PipelineRunning: ss.pipelineRunning.Load(),
		Scanned:         ss.scanned,
		Total:           ss.total,
		StartedAt:       ss.scanStartedAt,
		DurationMs:      int64(ss.lastScanDuration / time.Millisecond),
		LastScanAt:      ss.lastScanAt,
		NewCount:        ss.newCount,
		RemovedCount:    ss.removedCount,
		WarningCount:    ss.warningCount,
		LogEntries:      entries,
	}
}
