package admin

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ryanhaines/comic-reader/cache"
	"github.com/ryanhaines/comic-reader/config"
	"github.com/ryanhaines/comic-reader/match"
	"github.com/ryanhaines/comic-reader/store"
)

//go:embed static/*
var staticFS embed.FS

//go:embed templates/*
var templateFS embed.FS

// Admin holds the state for the admin console.
type Admin struct {
	Store      *store.Store
	Cache      *cache.LRU
	ComicDirs  []string
	CoverDir   string
	DBPath     string
	Port       int
	StartedAt  time.Time
	RequestLog *RequestLog
	ScanState  *ScanState
	Config     *config.Config
	Match      *match.Runner
	Scheduler  *Scheduler
	// OnRun, when set, is called (in its own goroutine) with every recorded
	// pipeline run — the push-notification hook.
	OnRun      func(store.PipelineRun)
	RouteCount int
	Routes     []EndpointInfo
	templates  *template.Template

	// runMatch is the match pass handleMatchRun launches; nil wraps
	// Match.Run. A seam so tests can observe the detached run without a
	// real matcher.
	runMatch func(context.Context) error

	// matchRunning guards handleMatchRun. A full pass is ~4 minutes of I/O
	// against an external drive, so a double-click or a second tab must not
	// start a concurrent one.
	// ponytail: in-memory, single-process. A run cannot outlive the process
	// holding the flag, so there is nothing to recover on restart.
	matchRunning atomic.Bool
	// organizeRunning guards handleOrganizeApply. It also mutually excludes
	// matchRunning: applying moves/renames to disk while a match pass reads
	// the same folders would race against the scanner's own file access.
	organizeRunning atomic.Bool
	// writebackRunning guards the background writeback worker's per-job archive
	// rewrite. It joins the same exclusion as match/organize/scan: no two
	// subsystems mutate library files at once.
	writebackRunning atomic.Bool
}

// canStartMatchRun/canStartOrganize express the mutual exclusion between the
// matcher and the organizer: each CompareAndSwap acquires its own flag only
// if the other subsystem is idle, so a plain Load() (not the CAS itself)
// gates the other flag rather than acquiring it as a side effect of a check.
func (a *Admin) canStartMatchRun() bool {
	if a.organizeRunning.Load() || a.writebackRunning.Load() {
		return false
	}
	return a.matchRunning.CompareAndSwap(false, true)
}

// busyReason names whichever file-mutating subsystem currently holds the gate,
// for the "try again" banner. Only called after an acquire has already failed,
// so it is a message and not a lock — an inherently racy read is fine here, and
// the generic fallback covers the case where the holder finished in between.
func (a *Admin) busyReason() string {
	switch {
	case a.matchRunning.Load():
		return "a match run is in progress"
	case a.writebackRunning.Load():
		return "metadata writeback is in progress"
	case a.ScanState != nil && a.ScanState.IsScanning():
		return "a library scan is in progress"
	case a.organizeRunning.Load():
		return "an organize apply is already running"
	}
	return "another file operation is in progress"
}

func (a *Admin) canStartOrganize() bool {
	if a.matchRunning.Load() || a.writebackRunning.Load() {
		return false
	}
	if a.ScanState != nil && a.ScanState.IsScanning() {
		return false
	}
	return a.organizeRunning.CompareAndSwap(false, true)
}

// BeginWriteback acquires the writeback flag iff no other file-mutating
// subsystem is active. Implements writeback.FileMutationGate.
func (a *Admin) BeginWriteback() bool {
	if a.matchRunning.Load() || a.organizeRunning.Load() {
		return false
	}
	if a.ScanState != nil && a.ScanState.IsScanning() {
		return false
	}
	return a.writebackRunning.CompareAndSwap(false, true)
}

// EndWriteback releases the writeback flag.
func (a *Admin) EndWriteback() { a.writebackRunning.Store(false) }

// PageData is passed to every admin template.
type PageData struct {
	ActivePage    string
	Title         string
	Port          int
	SeriesCount   int
	EndpointCount int
	ReviewCount   int
	Uptime        string
	Version       string
	Content       interface{}
}

// New creates an Admin instance and parses the layout template.
func New(st *store.Store, c *cache.LRU, comicDirs []string, coverDir string, dbPath string, port int, rl *RequestLog, ss *ScanState) (*Admin, error) {
	funcMap := template.FuncMap{
		"icon": func(name string, size int) template.HTML {
			return template.HTML(fmt.Sprintf(
				`<svg width="%d" height="%d" class="icon"><use href="/_admin/static/icons.svg#i-%s"/></svg>`,
				size, size, name))
		},
		"pct": func(a, b int) float64 {
			if b == 0 {
				return 0
			}
			return float64(a) / float64(b) * 100
		},
		// template.CSS for the same reason as stateColor: this builds a
		// gradient from a hash of the name, and a plain string is escaped to
		// ZgotmplZ inside a style attribute.
		"coverGradient": func(name string, idx int) template.CSS {
			seed := (int(name[0]) + idx) * 13
			hue := (seed * 9) % 360
			return template.CSS(fmt.Sprintf("linear-gradient(155deg, oklch(0.62 0.10 %d), oklch(0.38 0.07 %d))", hue, (hue+40)%360))
		},
		// brandMark is the Longbox app icon: three shelved spines plus one
		// pulled out and tipped in accent, over a shelf line. Geometry is on
		// the design's 120×120 grid, so it scales to any tile size.
		"brandMark": func() template.HTML {
			return template.HTML(`<svg viewBox="0 0 120 120" width="100%" height="100%" style="display:block;background:#1B1720">` +
				`<rect x="22" y="26" width="16" height="68" rx="2" fill="#F8F0DD"/>` +
				`<rect x="66" y="30" width="16" height="64" rx="2" fill="#F8F0DD"/>` +
				`<rect x="88" y="36" width="12" height="58" rx="2" fill="#F8F0DD"/>` +
				`<path d="M44 18h17v76H44z" fill="#0A84FF" transform="rotate(-8 52 56)"/>` +
				`<path d="M18 100h84" stroke="#F8F0DD" stroke-width="7" stroke-linecap="round"/>` +
				`</svg>`)
		},
		// stateColor/stateLabel keep the match-state palette in one place —
		// the queue chips, the row dots and the Overview card all read from
		// here, so a state never renders green in one view and grey in another.
		// Returns template.CSS: these are fixed var() references chosen from a
		// closed set here, never caller data, and a plain string would be
		// escaped to ZgotmplZ in a style attribute.
		"stateColor": func(state string) template.CSS {
			switch state {
			case store.MatchStateConfirmed:
				return "var(--green)"
			case store.MatchStateHighConfidence:
				return "var(--accent)"
			case store.MatchStateAmbiguous:
				return "var(--amber)"
			case store.MatchStateCollection:
				return "var(--violet)"
			default:
				return "var(--text-disabled)"
			}
		},
		"stateLabel": func(state string) string {
			switch state {
			case store.MatchStateConfirmed:
				return "Confirmed"
			case store.MatchStateHighConfidence:
				return "High confidence"
			case store.MatchStateAmbiguous:
				return "Ambiguous"
			case store.MatchStateLowConfidence:
				return "Low confidence"
			case store.MatchStateNoMatch:
				return "No match"
			case store.MatchStateCollection:
				return "Collection"
			default:
				return state
			}
		},
		// if1 keeps query-string assembly on one line in the templates:
		// emit the fragment only when the filter is actually set.
		"if1": func(cond string, then string) string {
			if cond == "" {
				return ""
			}
			return then
		},
		"sub":        func(a, b int) int { return a - b },
		"durSec":     func(ms int64) string { return fmt.Sprintf("%.1fs", float64(ms)/1000.0) },
		"pathescape": pathEscape,
	}
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("parse layout template: %w", err)
	}

	return &Admin{
		Store:      st,
		Cache:      c,
		ComicDirs:  comicDirs,
		CoverDir:   coverDir,
		DBPath:     dbPath,
		Port:       port,
		StartedAt:  time.Now(),
		RequestLog: rl,
		ScanState:  ss,
		templates:  tmpl,
	}, nil
}

// Register mounts the admin routes onto the given mux.
func (a *Admin) Register(root *http.ServeMux) {
	mux := http.NewServeMux()
	root.Handle("/_admin/", a.requireAuth(mux))
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /_admin/static/", http.StripPrefix("/_admin/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("GET /_admin/library/{id}", a.handleSeries)
	mux.HandleFunc("GET /_admin/library", a.handleLibrary)
	mux.HandleFunc("GET /_admin/scanner", a.handleScanner)
	mux.HandleFunc("POST /_admin/scanner/schedule", a.handleSetSchedule)
	mux.HandleFunc("GET /_admin/config", a.handleConfig)
	mux.HandleFunc("POST /_admin/config/add-dir", a.handleAddDir)
	mux.HandleFunc("POST /_admin/config/remove-dir", a.handleRemoveDir)
	mux.HandleFunc("GET /_admin/endpoints", a.handleEndpoints)
	mux.HandleFunc("GET /_admin/logs", a.handleLogs)
	mux.HandleFunc("GET /_admin/activity", a.handleActivity)
	mux.HandleFunc("GET /_admin/match/series/{id}", a.handleMatchSeriesDetail)
	mux.HandleFunc("GET /_admin/match/review", a.handleMatchReview)
	mux.HandleFunc("POST /_admin/match/review/skip", a.handleMatchReviewSkip)
	mux.HandleFunc("GET /_admin/match", a.handleMatch)
	mux.HandleFunc("POST /_admin/match/run", a.handleMatchRun)
	mux.HandleFunc("POST /_admin/match/confirm", a.handleMatchConfirm)
	mux.HandleFunc("POST /_admin/match/confirm-all", a.handleMatchConfirmAll)
	mux.HandleFunc("POST /_admin/match/dont-match", a.handleMatchDontMatch)
	mux.HandleFunc("POST /_admin/match/unconfirm", a.handleMatchUnconfirm)
	mux.HandleFunc("POST /_admin/match/writeback", a.handleMatchWriteback)
	mux.HandleFunc("POST /_admin/match/set-volume", a.handleMatchSetVolume)
	mux.HandleFunc("POST /_admin/match/mark-collection", a.handleMatchMarkCollection)
	mux.HandleFunc("POST /_admin/match/repin", a.handleMatchRepin)
	mux.HandleFunc("GET /_admin/organize", a.handleOrganize)
	mux.HandleFunc("POST /_admin/organize/apply", a.handleOrganizeApply)
	mux.HandleFunc("POST /_admin/organize/templates", a.handleOrganizeTemplates)
	mux.HandleFunc("GET /_admin/{$}", a.handleOverview)
}

func (a *Admin) render(w http.ResponseWriter, page string, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl, err := a.templates.Clone()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl, err = tmpl.ParseFS(templateFS, "templates/"+page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *Admin) baseData(activePage string) PageData {
	allSeries, _ := a.Store.ListSeries()
	return PageData{
		ActivePage:    activePage,
		Port:          a.Port,
		SeriesCount:   len(allSeries),
		EndpointCount: a.RouteCount,
		ReviewCount:   a.reviewCount(),
		Uptime:        formatDuration(time.Since(a.StartedAt)),
		Version:       "0.4.2",
	}
}

// reviewCount is the sidebar's Matching badge: everything a human still has to
// look at. Best-effort — the badge is decoration, so a failed count renders as
// no badge rather than failing the page.
func (a *Admin) reviewCount() int {
	q, err := a.Store.ListReviewQueue(store.ReviewStates...)
	if err != nil {
		return 0
	}
	return len(q)
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
}
