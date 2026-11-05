package organize

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ryanhaines/comic-reader/store"
)

// Report summarizes an Apply run.
type Report struct {
	Applied int
	Skipped int      // conflicts/flags encountered at apply time
	Errors  []string // one line per skipped/failed op: "old -> new: reason"
}

func recordSkip(report *Report, op Op, reason string) {
	report.Skipped++
	report.Errors = append(report.Errors, fmt.Sprintf("%s -> %s: %s", op.Old, op.New, reason))
}

// filterOps flattens a Plan to the ops of one kind, optionally restricted to
// one series (seriesID == "" means everything).
func filterOps(p *Plan, kind OpKind, seriesID string) []Op {
	var out []Op
	for _, sp := range p.Series {
		for _, op := range sp.Ops {
			if op.Kind != kind {
				continue
			}
			if seriesID != "" && op.SeriesID != seriesID {
				continue
			}
			out = append(out, op)
		}
	}
	return out
}

// caseOnlySame reports whether old and new are the same filesystem entry
// under different case — the only situation where the target-exists conflict
// checks below may be bypassed. EqualFold alone is not enough: on a
// case-SENSITIVE filesystem "a" and "A" are two distinct real entries that
// EqualFold conflates, and os.Rename would silently clobber the target.
// os.SameFile verifies identity (dev+inode), not just spelling.
func caseOnlySame(oldPath, newPath string) bool {
	if !strings.EqualFold(oldPath, newPath) {
		return false
	}
	oi, err := os.Stat(oldPath)
	if err != nil {
		return false
	}
	ni, err := os.Stat(newPath)
	if err != nil {
		return false
	}
	return os.SameFile(oi, ni)
}

// seriesFolderAbs is the authoritative on-disk location of a series: the
// absolute folder_path stored on its row, verified present. Never resolve a
// series folder by basename across roots — duplicate folder names in two
// roots (a real occurrence in this library: an empty leftover "Battleworld
// (2025)" in one root, the real one in the other) make basename search pick
// whichever root lists first.
func (o *Organizer) seriesFolderAbs(seriesID string) (string, error) {
	series, err := o.St.GetSeries(seriesID)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(series.FolderPath); err != nil {
		return "", err
	}
	return series.FolderPath, nil
}

func (o *Organizer) removeThumb(name string) {
	if o.CoverDir == "" {
		return
	}
	os.Remove(filepath.Join(o.CoverDir, name))
}

// Apply executes the current plan. seriesID == "" applies everything;
// otherwise only ops whose SeriesID matches (after each phase the id is
// re-derived, since a folder rename changes the slug).
//
// Apply re-plans before each of its three phases (folder renames, moves,
// file renames) because earlier phases change series IDs and folder names
// that later ops reference — the plan captured before phase 1 is stale by
// the time phase 2 runs.
func (o *Organizer) Apply(seriesID string) (Report, error) {
	var report Report

	// Unmount protection: every configured root must be reachable, or we do
	// zero work. Same idea as match.Runner.libraryGone.
	for _, root := range o.Roots {
		if _, err := os.Stat(root); err != nil {
			return Report{}, fmt.Errorf("library root missing: %s", root)
		}
	}

	// --- Phase 1: folder renames (plus the one-time flag accounting). ---
	plan1, err := o.Plan()
	if err != nil {
		return report, err
	}
	// Flags are stable planning facts (bad match data, sanitize failures,
	// duplicate targets) independent of what the other phases do, so they're
	// counted once here rather than once per phase.
	for _, op := range filterOps(plan1, OpFlag, seriesID) {
		recordSkip(&report, op, op.Reason)
	}
	for _, op := range filterOps(plan1, OpRenameFolder, seriesID) {
		if err := o.applyRenameFolder(&report, op); err != nil {
			return report, err
		}
		if seriesID != "" && op.SeriesID == seriesID {
			seriesID = store.SlugifyName(op.New)
		}
	}

	// --- Phase 2: moves, against a fresh plan reflecting phase 1's renames. ---
	plan2, err := o.Plan()
	if err != nil {
		return report, err
	}
	for _, op := range filterOps(plan2, OpMoveFile, seriesID) {
		if err := o.applyMoveFile(&report, op); err != nil {
			return report, err
		}
	}

	// --- Phase 3: file renames, against a fresh plan again. ---
	plan3, err := o.Plan()
	if err != nil {
		return report, err
	}
	for _, op := range filterOps(plan3, OpRenameFile, seriesID) {
		if err := o.applyRenameFile(&report, op); err != nil {
			return report, err
		}
	}

	return report, nil
}

// applyRenameFolder executes one OpRenameFolder: disk rename, then DB
// migration of the series and every issue in its folder, with rollback of
// the disk rename on DB failure. Store errors that indicate a broken DB
// (rather than an expected conflict) are returned to abort Apply.
func (o *Organizer) applyRenameFolder(report *Report, op Op) error {
	oldAbs, err := o.seriesFolderAbs(op.SeriesID)
	if err != nil {
		recordSkip(report, op, "series folder not found on disk at its recorded path")
		return nil
	}
	newAbs := filepath.Join(filepath.Dir(oldAbs), op.New)

	// On case-insensitive filesystems (macOS default), os.Stat(newAbs) finds
	// the source itself for a case-only rename ("invincible" -> "Invincible"),
	// so the conflict check would skip it forever and the plan would never
	// converge. Bypass only when old and new are verifiably the same file.
	if _, err := os.Stat(newAbs); err == nil && !caseOnlySame(oldAbs, newAbs) {
		recordSkip(report, op, "target folder already exists")
		return nil
	}

	newSlug := store.SlugifyName(op.New)
	if newSlug != op.SeriesID {
		exists, err := o.St.SeriesExists(newSlug)
		if err != nil {
			return err
		}
		if exists {
			recordSkip(report, op, "a series with that name already exists")
			return nil
		}
	}

	// Snapshot the folder's issues (old ids/filenames) before mutating
	// anything: MigrateSeriesFolder needs the full IDPair list — every issue
	// in the folder, not just the ones this op happens to reference — and
	// the thumbnail cleanup below needs the old ids.
	issues, err := o.St.ListIssues(op.SeriesID)
	if err != nil {
		return err
	}

	if err := os.Rename(oldAbs, newAbs); err != nil {
		recordSkip(report, op, err.Error())
		return nil
	}

	pairs := make([]store.IDPair, len(issues))
	for i, iss := range issues {
		pairs[i] = store.IDPair{Old: iss.ID, New: store.PathToID(filepath.Join(op.New, iss.Filename))}
	}

	if err := o.St.MigrateSeriesFolder(op.SeriesID, newSlug, newAbs, op.New, pairs); err != nil {
		if rerr := os.Rename(newAbs, oldAbs); rerr != nil {
			log.Printf("organize: rollback rename %s -> %s failed: %v", newAbs, oldAbs, rerr)
		}
		recordSkip(report, op, err.Error())
		return nil
	}

	o.removeThumb("series_" + op.SeriesID + ".jpg")
	for _, iss := range issues {
		o.removeThumb("comic_" + iss.ID + ".jpg")
	}

	// MigrateSeriesFolder doesn't bump series.updated_at itself; touch it so
	// delta sync picks up the rename (rename_folder.go step 11).
	if err := o.St.TouchSeries(newSlug); err != nil {
		log.Printf("organize: TouchSeries %s: %v", newSlug, err)
	}

	log.Printf("organize: %s %s -> %s", op.Kind, op.Old, op.New)
	report.Applied++
	return nil
}

// applyMoveFile executes one OpMoveFile: disk rename into the destination
// series' folder, then DB migration of series membership. Rolls the disk
// move back on DB failure.
func (o *Organizer) applyMoveFile(report *Report, op Op) error {
	filename := filepath.Base(op.Old)
	dstFolder := filepath.Dir(op.New)

	srcFolderAbs, err := o.seriesFolderAbs(op.SeriesID)
	if err != nil {
		recordSkip(report, op, "source series folder not found on disk at its recorded path")
		return nil
	}
	srcAbs := filepath.Join(srcFolderAbs, filename)
	if _, err := os.Stat(srcAbs); err != nil {
		recordSkip(report, op, "file not on disk under this name — DB is stale, run a library rescan first")
		return nil
	}
	// Series IDs are always the slug of their current folder basename
	// (scanner and rename_folder.go both maintain this invariant), so the
	// destination series' row — and its authoritative folder_path — can be
	// found from the planned folder name.
	targetSeriesID := store.SlugifyName(dstFolder)
	dstFolderAbs, err := o.seriesFolderAbs(targetSeriesID)
	if err != nil {
		recordSkip(report, op, "target series folder not found on disk")
		return nil
	}
	dstAbs := filepath.Join(dstFolderAbs, filename)
	if _, err := os.Stat(dstAbs); err == nil && !caseOnlySame(srcAbs, dstAbs) {
		recordSkip(report, op, "target file already exists")
		return nil
	}

	if err := os.Rename(srcAbs, dstAbs); err != nil {
		recordSkip(report, op, err.Error())
		return nil
	}

	newID := store.PathToID(filepath.Join(dstFolder, filename))

	if err := o.St.MigrateComicToSeries(op.IssueID, newID, targetSeriesID); err != nil {
		if rerr := os.Rename(dstAbs, srcAbs); rerr != nil {
			log.Printf("organize: rollback move %s -> %s failed: %v", dstAbs, srcAbs, rerr)
		}
		recordSkip(report, op, err.Error())
		return nil
	}

	o.removeThumb("comic_" + op.IssueID + ".jpg")

	log.Printf("organize: %s %s -> %s", op.Kind, op.Old, op.New)
	report.Applied++
	return nil
}

// applyRenameFile executes one OpRenameFile: disk rename within the series'
// current folder, then DB migration of the comic id, with rollback on DB
// failure and a cover_path fixup if the renamed file was the series cover.
func (o *Organizer) applyRenameFile(report *Report, op Op) error {
	series, err := o.St.GetSeries(op.SeriesID)
	if err != nil {
		return err
	}
	folderAbs, err := o.seriesFolderAbs(op.SeriesID)
	if err != nil {
		recordSkip(report, op, "series folder not found on disk at its recorded path")
		return nil
	}
	folderBase := filepath.Base(folderAbs)
	oldAbs := filepath.Join(folderAbs, op.Old)
	newAbs := filepath.Join(folderAbs, op.New)

	// A missing source means the DB row is stale (the file was renamed outside
	// the app). Raw ENOENT from os.Rename reads like an organize bug; name the
	// actual problem and its remedy instead.
	if _, err := os.Stat(oldAbs); err != nil {
		recordSkip(report, op, "file not on disk under this name — DB is stale, run a library rescan first")
		return nil
	}

	if _, err := os.Stat(newAbs); err == nil && !caseOnlySame(oldAbs, newAbs) {
		recordSkip(report, op, "target file already exists")
		return nil
	}

	if err := os.Rename(oldAbs, newAbs); err != nil {
		recordSkip(report, op, err.Error())
		return nil
	}

	newID := store.PathToID(filepath.Join(folderBase, op.New))
	if err := o.St.MigrateComicIDFull(op.IssueID, newID, op.New); err != nil {
		if rerr := os.Rename(newAbs, oldAbs); rerr != nil {
			log.Printf("organize: rollback rename %s -> %s failed: %v", newAbs, oldAbs, rerr)
		}
		recordSkip(report, op, err.Error())
		return nil
	}

	o.removeThumb("comic_" + op.IssueID + ".jpg")

	if filepath.Base(series.CoverPath) == op.Old {
		newCoverPath := filepath.Join(folderBase, op.New)
		if err := o.St.UpdateSeriesCoverPath(op.SeriesID, newCoverPath); err != nil {
			log.Printf("organize: UpdateSeriesCoverPath %s: %v", op.SeriesID, err)
		}
	}

	log.Printf("organize: %s %s -> %s", op.Kind, op.Old, op.New)
	report.Applied++
	return nil
}
