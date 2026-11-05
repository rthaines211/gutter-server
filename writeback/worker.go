package writeback

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rthaines211/gutter-server/archive"
	"github.com/rthaines211/gutter-server/comicvine"
	"github.com/rthaines211/gutter-server/metadata"
	"github.com/rthaines211/gutter-server/store"
)

// FileMutationGate lets the worker cooperate with scan/match/organize so no two
// subsystems mutate library files at once. A nil gate means always-allowed.
type FileMutationGate interface {
	BeginWriteback() bool // true = acquired; false = scan/match/organize active
	EndWriteback()
}

// Worker is a single-goroutine async loop that writes ComicInfo.xml into
// archive files for queued writeback jobs.
type Worker struct {
	st           *store.Store
	libraryRoots []string // all configured comic directories; file paths are probed in order
	coverDir     string
	pause        time.Duration
	gate         FileMutationGate
	stop         chan struct{}
	stopOnce     sync.Once
}

// New constructs a Worker. libraryRoots must contain all configured comic directories
// (cfg.ComicDirs); the worker probes each root with os.Stat to find the actual file.
// Start() must be called to begin processing. gate coordinates with scan/match/organize
// so writeback doesn't rewrite a file another subsystem is mutating; nil means always-allowed.
func New(st *store.Store, libraryRoots []string, coverDir string, pause time.Duration, gate FileMutationGate) *Worker {
	return &Worker{
		st:           st,
		libraryRoots: libraryRoots,
		coverDir:     coverDir,
		pause:        pause,
		gate:         gate,
		stop:         make(chan struct{}),
	}
}

// Start launches the worker goroutine. Call Stop() to shut it down.
func (w *Worker) Start() {
	go func() {
		w.sweepTemps()
		for {
			select {
			case <-w.stop:
				return
			default:
			}
			worked := w.ProcessOne()
			d := w.pause
			if !worked {
				d = 5 * time.Minute
			}
			select {
			case <-w.stop:
				return
			case <-time.After(d):
			}
		}
	}()
}

// Stop signals the worker goroutine to exit. Safe to call multiple times.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
}

// ProcessOne claims and processes at most one writeback job. Returns true if a
// job was attempted (whether it succeeded or failed), false if the queue is empty.
func (w *Worker) ProcessOne() bool {
	if w.gate != nil {
		if !w.gate.BeginWriteback() {
			return false // another mutator is active; treat as "no work", back off
		}
		defer w.gate.EndWriteback()
	}
	comicID, ok, err := w.st.NextWritebackJob()
	if err != nil || !ok {
		return false
	}
	// process returns the effective comic ID (may differ if CBR was converted to CBZ).
	// If conversion succeeded but a later step failed, the writeback row already lives
	// under the new ID — we must use effectiveID in both branches so the row is found.
	// Guard: if effectiveID is empty (early failure before any work), fall back to comicID.
	effectiveID, err := w.process(comicID)
	if effectiveID == "" {
		effectiveID = comicID
	}
	if err != nil {
		log.Printf("writeback %s: %v", effectiveID, err)
		w.st.MarkWritebackError(effectiveID, err.Error())
	} else {
		w.st.MarkWritebackDone(effectiveID)
	}
	return true
}

// process handles one comic. Returns the effective comic ID (which may differ
// from comicID when a CBR is converted to CBZ) and any error.
func (w *Worker) process(comicID string) (string, error) {
	src, err := w.st.WritebackSource(comicID)
	if err != nil {
		return comicID, err
	}

	// Probe each library root to find the actual file — supports multi-root libraries.
	// Known limitation: concurrent folder-rename endpoints may move the file between
	// the Stat probe and the subsequent archive open (single-user deployment; revisit
	// if multi-user access is ever needed).
	path := ""
	for _, root := range w.libraryRoots {
		candidate := filepath.Join(root, src.FolderName, src.Filename)
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		// No root contained the file; fall back to first root so errors are useful.
		if len(w.libraryRoots) > 0 {
			path = filepath.Join(w.libraryRoots[0], src.FolderName, src.Filename)
		} else {
			path = filepath.Join(src.FolderName, src.Filename)
		}
	}

	// PDFs have no ComicInfo.xml — DB-only metadata is the whole story for PDFs.
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		return comicID, nil
	}

	// Non-CBZ files (e.g. CBR) must be converted first. IDs are path-derived so
	// the extension change requires a full ID migration.
	if !strings.EqualFold(filepath.Ext(path), ".cbz") {
		if src.InTwinFolder {
			// Converting renames the file; the downloader that owns a twin
			// folder would re-download it. DB-only, like a PDF.
			log.Printf("writeback %s: not converting %s in a twin folder", comicID, path)
			return comicID, nil
		}
		newPath, err := archive.ConvertToCbz(path)
		if err != nil {
			return comicID, err
		}
		newFilename := filepath.Base(newPath)
		newID := store.PathToID(filepath.Join(src.FolderName, newFilename))
		if err := w.st.MigrateComicIDFull(comicID, newID, newFilename); err != nil {
			return comicID, err
		}
		// Update locals to continue processing under the new identity.
		comicID = newID
		path = newPath
		src.Filename = newFilename
	}

	// Read existing ComicInfo.xml from the archive so it can be overlaid rather
	// than replaced. metadata.ComicInfo models the fields writeback manages
	// (Title, Series, credits, dates, …) plus Pages bookmarks; any other
	// element (Notes, Web, Characters, StoryArc, …) is captured by its
	// ",any" catch-all field and re-emitted unchanged on marshal.
	ci := &metadata.ComicInfo{}
	r, err := archive.Open(path)
	if err != nil {
		return comicID, err
	}
	if xmlData := r.ComicInfoXML(); len(xmlData) > 0 {
		if parsed, perr := metadata.ParseComicInfo(xmlData); perr == nil {
			ci = parsed
		}
	}
	r.Close()

	// Overlay DB values onto the ComicInfo struct. An empty DB value means
	// "unknown", not "clear it" — never blank a field the file already has.
	overlay(&ci.Series, src.SeriesName)
	overlay(&ci.Number, src.Number)
	overlay(&ci.Title, src.Title)
	// Enrichment keeps ComicVine HTML verbatim; files get plain text, as the API serves.
	overlay(&ci.Summary, comicvine.StripHTML(src.Summary))
	overlay(&ci.Format, canonicalFormat(src.Format))
	overlay(&ci.Publisher, src.Publisher)
	if src.Year > 0 {
		ci.Year = src.Year
	}
	// Typed credits win when confirmed enrichment supplied them; otherwise fall
	// back to the flat per-issue/series writer+penciller columns.
	if len(src.Creators) > 0 {
		applyCredits(ci, src.Creators)
	} else {
		ci.Writer = firstNonEmpty(src.IssueWriter, src.SeriesWriter)
		ci.Penciller = firstNonEmpty(src.IssuePenciller, src.SeriesPenciller)
	}
	// Per-issue cover date overrides the volume Year when known. CV cover
	// dates are often YYYY-only or missing, so Month/Day must be guarded the
	// same way Year is — otherwise an absent/partial cover date zeroes
	// pre-existing <Month>/<Day> values from the file.
	if src.CoverYear > 0 {
		ci.Year = src.CoverYear
	}
	if src.CoverMonth > 0 {
		ci.Month = src.CoverMonth
	}
	if src.CoverDay > 0 {
		ci.Day = src.CoverDay
	}
	if len(src.Genres) > 0 {
		ci.Genre = strings.Join(src.Genres, ", ")
	}

	xmlData, err := metadata.MarshalComicInfo(ci)
	if err != nil {
		return comicID, err
	}
	if err := archive.RewriteComicInfoXML(path, xmlData); err != nil {
		return comicID, err
	}

	// Invalidate cached page-derived thumbnail so the next request re-generates it.
	w.invalidateThumbnail(comicID)
	return comicID, nil
}

// invalidateThumbnail removes the cached cover thumbnail for a comic, if it exists.
func (w *Worker) invalidateThumbnail(comicID string) {
	if w.coverDir == "" {
		return
	}
	os.Remove(filepath.Join(w.coverDir, "comic_"+comicID+".jpg"))
}

// firstNonEmpty returns a if non-empty, otherwise b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// canonicalFormat maps internal format keys to ComicInfo-conventional strings.
// Unknown/already-canonical values are passed through unchanged so they are
// preserved rather than silently erased.
func canonicalFormat(f string) string {
	switch f {
	case "tpb":
		return "TPB"
	case "hc":
		return "HC"
	case "omnibus":
		return "Omnibus"
	case "annual":
		return "Annual"
	case "one-shot":
		return "One-Shot"
	case "single":
		return "Single Issue"
	default:
		return f
	}
}

// overlay sets *dst to v unless v is empty.
func overlay(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// sweepTemps deletes temp archives orphaned by an interrupted rewrite or
// conversion (a restart mid-write). Only files older than an hour, so a
// concurrent writer's in-flight temp is never touched.
// ponytail: globs <root>/<series>/ only — the library is flat by convention.
func (w *Worker) sweepTemps() {
	cutoff := time.Now().Add(-time.Hour)
	for _, root := range w.libraryRoots {
		for _, pat := range []string{".rewrite-*.cbz", ".convert-*.cbz"} {
			matches, _ := filepath.Glob(filepath.Join(root, "*", pat))
			for _, m := range matches {
				fi, err := os.Stat(m)
				if err != nil || fi.ModTime().After(cutoff) {
					continue
				}
				if err := os.Remove(m); err != nil {
					log.Printf("writeback: sweep %s: %v", m, err)
					continue
				}
				log.Printf("writeback: removed orphaned temp %s", m)
			}
		}
	}
}
