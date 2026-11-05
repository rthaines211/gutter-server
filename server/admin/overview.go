package admin

import (
	"html/template"
	"net/http"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

type OverviewData struct {
	SeriesCount int
	IssueCount  int
	CacheSize   int
	CacheCap    int
	Volumes     []VolumeInfo
	MemoryMB    int
	Goroutines  int

	// The match queue breakdown the design surfaces on Overview, so the
	// operator sees what needs review without opening Matching.
	Queue        []QueueRow
	MatchedCount int
	CachePct     float64

	LastRun *ActivityRun
}

// QueueRow is one match state in the Overview queue card.
type QueueRow struct {
	State string
	Label string
	Count int
	Color template.CSS
}

type VolumeInfo struct {
	Path        string
	SeriesCount int
	Mounted     bool
	UsedGB      float64
	TotalGB     float64
	UsedPct     float64
}

func (a *Admin) handleOverview(w http.ResponseWriter, r *http.Request) {
	data := a.baseData("overview")
	data.Title = "Overview"

	allSeries, _ := a.Store.ListSeries()

	totalIssues := 0
	for _, s := range allSeries {
		totalIssues += s.IssueCount
	}

	cacheSize, cacheCap := a.Cache.Stats()

	var volumes []VolumeInfo
	for _, dir := range a.ComicDirs {
		vi := VolumeInfo{Path: dir}
		for _, s := range allSeries {
			if len(s.FolderPath) >= len(dir) && s.FolderPath[:len(dir)] == dir {
				vi.SeriesCount++
			}
		}
		if _, err := os.Stat(dir); err == nil {
			vi.Mounted = true
		}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(dir, &stat); err == nil {
			vi.TotalGB = float64(stat.Blocks*uint64(stat.Bsize)) / (1 << 30)
			freeBytes := stat.Bavail * uint64(stat.Bsize)
			vi.UsedGB = vi.TotalGB - float64(freeBytes)/(1<<30)
			if vi.TotalGB > 0 {
				vi.UsedPct = vi.UsedGB / vi.TotalGB * 100
			}
		}
		volumes = append(volumes, vi)
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	// Queue colors follow the design's state palette: green confirmed, accent
	// high-confidence, amber ambiguous, violet collection, grey the rest.
	counts, _ := a.Store.CountSeriesMatchesByState()
	queue := []QueueRow{
		{store.MatchStateConfirmed, "Confirmed", counts[store.MatchStateConfirmed], "var(--green)"},
		{store.MatchStateHighConfidence, "High confidence", counts[store.MatchStateHighConfidence], "var(--accent)"},
		{store.MatchStateAmbiguous, "Ambiguous", counts[store.MatchStateAmbiguous], "var(--amber)"},
		{store.MatchStateCollection, "Collection", counts[store.MatchStateCollection], "var(--violet)"},
		{store.MatchStateLowConfidence, "Low confidence", counts[store.MatchStateLowConfidence], "var(--text-disabled)"},
		{store.MatchStateNoMatch, "No match", counts[store.MatchStateNoMatch], "var(--text-disabled)"},
	}
	matched := 0
	for _, q := range queue {
		if q.State != store.MatchStateNoMatch && q.State != store.MatchStateLowConfidence {
			matched += q.Count
		}
	}

	cachePct := 0.0
	if cacheCap > 0 {
		cachePct = float64(cacheSize) / float64(cacheCap) * 100
	}

	ov := OverviewData{
		SeriesCount:  len(allSeries),
		IssueCount:   totalIssues,
		CacheSize:    cacheSize,
		CacheCap:     cacheCap,
		Volumes:      volumes,
		MemoryMB:     int(mem.Alloc / (1 << 20)),
		Goroutines:   runtime.NumGoroutine(),
		Queue:        queue,
		MatchedCount: matched,
		CachePct:     cachePct,
	}

	if runs, _ := a.Store.ListPipelineRuns(1); len(runs) > 0 {
		r := runs[0]
		ov.LastRun = &ActivityRun{PipelineRun: r,
			When: time.UnixMilli(r.StartedAt).Format("Jan 2 15:04"), Attention: attention(r)}
	}

	data.Content = ov

	a.render(w, "overview.html", data)
}
