// Package organize plans folder/file reorganization from confirmed matches.
// It is pure computation: no disk access. Conflict-with-disk detection is the
// applier's (Task 5) job; the planner only flags intra-plan target collisions
// and strays with no confirmed home.
package organize

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ryanhaines/comic-reader/match"
	"github.com/ryanhaines/comic-reader/match/gcd"
	"github.com/ryanhaines/comic-reader/match/localcv"
	"github.com/ryanhaines/comic-reader/match/parse"
	"github.com/ryanhaines/comic-reader/rename"
	"github.com/ryanhaines/comic-reader/store"
)

type OpKind string

const (
	OpRenameFolder OpKind = "rename_folder"
	OpMoveFile     OpKind = "move_file"
	OpRenameFile   OpKind = "rename_file"
	OpFlag         OpKind = "flag"
)

// Op is one planned operation. Paths are RELATIVE display forms:
//
//	rename_folder: Old/New are folder basenames.
//	move_file:     Old = "SrcFolder/name.cbz", New = "DstFolder/name.cbz".
//	rename_file:   Old/New are filenames within the op's series folder.
//	flag:          Old describes the item, Reason says why nothing will happen.
type Op struct {
	Kind     OpKind
	SeriesID string // series this op belongs to (for move_file: the SOURCE)
	IssueID  string // empty for rename_folder
	Old, New string
	Reason   string // flags only
}

type SeriesPlan struct {
	SeriesID   string
	SeriesName string
	Ops        []Op
}

type Plan struct {
	Series []SeriesPlan // only series with at least one op
	// Totals for the console header.
	FolderRenames, Moves, Renames, Flags int
}

type Organizer struct {
	St       *store.Store
	M        *match.Runner
	Roots    []string
	CoverDir string
}

func New(st *store.Store, m *match.Runner, roots []string, coverDir string) *Organizer {
	return &Organizer{St: st, M: m, Roots: roots, CoverDir: coverDir}
}

// Plan walks every confirmed series match and produces typed operations.
func (o *Organizer) Plan() (*Plan, error) {
	confirmed, err := o.St.ListSeriesMatchesByState(store.MatchStateConfirmed)
	if err != nil {
		return nil, err
	}

	// Keyed by source+volume, not the bare volume id: the ComicVine and GCD
	// id spaces overlap, so a bare id would hand a GCD stray to whichever
	// ComicVine series happens to share the number.
	volToSeries := map[string]store.SeriesMatch{}
	for _, m := range confirmed {
		if m.VolumeID == "" {
			continue
		}
		volToSeries[srcVol(m.Source, m.VolumeID)] = m
	}

	templates, err := o.St.ListRenameTemplates()
	if err != nil {
		return nil, err
	}

	var ops []Op
	seriesNames := map[string]string{}

	for _, cm := range confirmed {
		if cm.VolumeID == "" {
			// Confirmed collection folder: per-file volumes, no folder volume.
			// No canonical folder, no strays; just template renames.
			collOps, err := o.planCollectionFolder(cm, templates, seriesNames)
			if err != nil {
				return nil, err
			}
			ops = append(ops, collOps...)
			continue
		}
		series, err := o.St.GetSeries(cm.SeriesID)
		if err != nil {
			return nil, err
		}
		seriesNames[cm.SeriesID] = series.Name

		issues, err := o.St.IssuesForTemplateRename(cm.SeriesID)
		if err != nil {
			return nil, err
		}
		// Nothing to reorganize without at least one issue row.
		if len(issues) == 0 {
			continue
		}
		folderName := issues[0].FolderName

		vol, err := o.M.LookupVolume(cm.Source, cm.VolumeID)
		if isNotFound(err) {
			ops = append(ops, Op{Kind: OpFlag, SeriesID: cm.SeriesID, Old: folderName, Reason: "volume not in dump"})
			continue
		}
		if err != nil {
			return nil, err
		}

		// Only the rename_folder decision depends on canonical; a sanitize
		// failure flags it, but stray detection and file renames still run.
		canonical, cerr := canonicalFolder(vol)
		if cerr != nil || canonical == "" {
			ops = append(ops, Op{Kind: OpFlag, SeriesID: cm.SeriesID, Old: folderName, Reason: "cannot compute canonical folder name"})
		} else if !strings.EqualFold(canonical, folderName) {
			op, err := o.renameFolderOp(cm.SeriesID, folderName, canonical)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
		}

		issuesByID := make(map[string]store.TemplateRenameIssue, len(issues))
		for _, iss := range issues {
			issuesByID[iss.ID] = iss
		}

		ims, err := o.St.ListIssueMatchesForSeries(cm.SeriesID)
		if err != nil {
			return nil, err
		}

		// Issue IDs excluded from the rule-(d) rename pass: moved away, or
		// flagged homeless — renaming a homeless stray under the home series'
		// template would fabricate a name for content that isn't this series.
		strays := map[string]bool{}

		for _, im := range ims {
			iss, ok := issuesByID[im.IssueID]
			if !ok {
				continue // matched issue no longer has a row (stale)
			}
			// An annual's ComicVine volume is always "elsewhere" — that is how
			// CV files annuals. By convention it lives in its parent's folder,
			// so it is home here: never a move, never a homeless flag.
			if iss.Format == "annual" {
				continue
			}
			up, err := o.M.LookupIssue(im.Source, im.UpstreamID)
			if isNotFound(err) {
				// Upstream id no longer resolves (dump changed underneath a
				// confirmed match); leave the issue alone rather than guess.
				continue
			}
			if err != nil {
				return nil, err
			}
			upVol := strconv.FormatInt(up.VolumeID, 10)
			if im.Source == cm.Source && upVol == cm.VolumeID {
				continue // home
			}

			target, ok := volToSeries[srcVol(im.Source, upVol)]
			if ok && target.SeriesID != cm.SeriesID {
				strays[im.IssueID] = true

				targetFolder, tvol, terr := targetCanonicalFolder(o, target)
				if terr != nil {
					return nil, terr
				}

				ops = append(ops, Op{
					Kind: OpMoveFile, SeriesID: cm.SeriesID, IssueID: im.IssueID,
					Old: folderName + "/" + iss.Filename,
					New: targetFolder + "/" + iss.Filename,
				})

				if tvol != nil {
					fields := map[string]string{
						"series": trimTrailingFormatWord(tvol.Name, iss.Format),
						"number": iss.Number,
						"year":   issueYear(iss, tvol.StartYear),
						"title":  collectionSubtitle(iss.Title, tvol.Name),
						"format": iss.Format,
						"volume": tvol.Name,
					}
					if newName := resolveNewName(templates, iss.Format, filepath.Ext(iss.Filename), fields); newName != "" && !strings.EqualFold(newName, iss.Filename) {
						ops = append(ops, Op{Kind: OpRenameFile, SeriesID: target.SeriesID, IssueID: im.IssueID, Old: iss.Filename, New: newName})
					}
				}
			} else if !ok {
				strays[im.IssueID] = true
				ops = append(ops, Op{
					Kind: OpFlag, SeriesID: cm.SeriesID, IssueID: im.IssueID,
					Old:    folderName + "/" + iss.Filename,
					Reason: fmt.Sprintf("no confirmed series for volume %s (%s %s)", upVol, up.VolumeName, up.VolumeStartYear),
				})
			}
		}

		// A folder where most files are homeless is not a series with a few
		// strays — it is a run of trades, each its own volume, confirmed against
		// whichever volume the first file happened to match. The per-file flags
		// name the symptom; without this nothing points at the actual remedy,
		// which lives on a different page entirely.
		if len(issues) > 1 && len(strays)*2 > len(issues) {
			ops = append(ops, Op{
				Kind: OpFlag, SeriesID: cm.SeriesID,
				Old: folderName,
				Reason: fmt.Sprintf(
					"%d of %d files here are their own volume — this folder looks like a collection, "+
						"not one series. Fix: Matching → this series → Unconfirm → \"Not one series — mark as collection\" → Confirm as collection.",
					len(strays), len(issues)),
			})
		}

		// Step d: filename renames for every issue that isn't being moved away.
		for _, iss := range issues {
			if strays[iss.ID] {
				continue
			}
			// No parsed issue number means the numbered templates all collapse
			// to the same bare series name (the resolver drops empty tokens) —
			// a guaranteed pile-up of duplicate-target flags. Leave the file's
			// name alone rather than proposing a rename we can't number.
			if iss.Number == "" {
				continue
			}
			// Same title normalisation as planCollectionFolder: a trade can sit
			// in an ordinary confirmed folder too, and it resolves through the
			// very same tpb/hc/omnibus templates, so the raw title repeats the
			// series and volume here identically.
			fields := map[string]string{
				"series": trimTrailingFormatWord(vol.Name, iss.Format),
				"number": iss.Number,
				"year":   issueYear(iss, vol.StartYear),
				"title":  collectionSubtitle(iss.Title, vol.Name),
				"format": iss.Format,
				"volume": vol.Name,
			}
			if newName := resolveNewName(templates, iss.Format, filepath.Ext(iss.Filename), fields); newName != "" && !strings.EqualFold(newName, iss.Filename) {
				ops = append(ops, Op{Kind: OpRenameFile, SeriesID: cm.SeriesID, IssueID: iss.ID, Old: iss.Filename, New: newName})
			}
		}
	}

	return buildPlan(seriesNames, flagDuplicateTargets(ops)), nil
}

// planCollectionFolder plans a confirmed collection folder. The folder keeps
// its own name; each file is renamed from the folder's series name, its local
// number, and its own cover year (falling back to the folder year).
func (o *Organizer) planCollectionFolder(cm store.SeriesMatch, templates map[string]string, seriesNames map[string]string) ([]Op, error) {
	series, err := o.St.GetSeries(cm.SeriesID)
	if err != nil {
		return nil, err
	}
	seriesNames[cm.SeriesID] = series.Name

	issues, err := o.St.IssuesForTemplateRename(cm.SeriesID)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, nil
	}
	// The folder's parsed name is the *first* trade's subtitle ("Black
	// Science: How to Fall Forever" for a folder of v01-v09), so prefer the
	// parent series the trades collect. Falls back to the parsed name when
	// the back-link vote abstains.
	folderSeries := parse.ParseFolder(issues[0].FolderName).Series
	var ops []Op

	ims, err := o.St.ListIssueMatchesForSeries(cm.SeriesID)
	if err != nil {
		return nil, err
	}
	parent, err := resolveParentVolume(o, cm, ims)
	if err != nil {
		return nil, err
	}
	if parent != nil {
		folderSeries = parent.Name
		folderName := issues[0].FolderName
		if canonical, cerr := canonicalFolder(parent); cerr == nil && canonical != "" && !strings.EqualFold(canonical, folderName) {
			op, err := o.renameFolderOp(cm.SeriesID, folderName, canonical)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
		}
	}
	for _, iss := range issues {
		if iss.Number == "" {
			continue
		}
		year := iss.CoverYear
		if year == 0 {
			year = iss.Year
		}
		yearStr := ""
		if year > 0 {
			yearStr = strconv.Itoa(year)
		}
		format := iss.Format
		if format == "" {
			format = "tpb"
		}
		// {volume} is the parent series this trade collects, not a second
		// copy of {number} — see the token's other call sites.
		volumeName := ""
		if parent != nil {
			volumeName = parent.Name
		}
		fields := map[string]string{
			"series": trimTrailingFormatWord(folderSeries, format),
			"number": iss.Number,
			"year":   yearStr,
			"title":  collectionSubtitle(iss.Title, folderSeries),
			"format": format,
			"volume": volumeName,
		}
		if newName := resolveNewName(templates, format, filepath.Ext(iss.Filename), fields); newName != "" && !strings.EqualFold(newName, iss.Filename) {
			ops = append(ops, Op{Kind: OpRenameFile, SeriesID: cm.SeriesID, IssueID: iss.ID, Old: iss.Filename, New: newName})
		}
	}
	return ops, nil
}

// renameFolderOp plans renaming a series' primary folder, or flags it when
// the series also lists a downloader's twin folder: the rename could change
// the series id under the twin's issues, and the twin itself is not ours to
// move. Merging the folders by hand clears the flag.
func (o *Organizer) renameFolderOp(seriesID, old, canonical string) (Op, error) {
	twin, err := o.St.HasTwinFolder(seriesID)
	if err != nil {
		return Op{}, err
	}
	if twin {
		return Op{Kind: OpFlag, SeriesID: seriesID, Old: old,
			Reason: "folder rename to " + canonical + " skipped: series also reads from a downloader's twin folder"}, nil
	}
	return Op{Kind: OpRenameFolder, SeriesID: seriesID, Old: old, New: canonical}, nil
}

// Leading volume designator on an enrichment title: "Volume 1:", "Volume One -",
// "Vol. 3", "Book 2:". The template already emits "Vol. {number}", so this part
// of the title is pure repetition.
var volumeWordRe = regexp.MustCompile(`(?i)^(?:vol\.?|volume|book|part)\s*(?:\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\b`)

// Titles that survive prefix-stripping but say nothing: edition markers, not
// subtitles. "Fantastic Four - Doom 2099 Omnibus Vol. 1 - HC" is worse than
// having no subtitle at all.
var junkSubtitles = map[string]bool{
	"hc": true, "tpb": true, "sc": true, "gn": true,
	"hardcover": true, "paperback": true, "omnibus": true, "deluxe": true,
}

// collectionSubtitle reduces an enrichment title to the part that actually adds
// information, or "" when nothing does. rename.Resolve drops an empty token
// along with its separator, so a collection with no real subtitle simply keeps
// the plain "Series Vol. N (Year)" form rather than trailing a " - ".
//
// Three layers of repetition have to come off, because the title repeats what
// the template already emits: the series name ("Birthright: Homecoming"), the
// volume designator ("Volume 1: The Court of Owls"), and edition noise ("HC").
func collectionSubtitle(title, series string) string {
	t := strings.TrimSpace(title)
	s := strings.TrimSpace(series)
	if t == "" {
		return ""
	}
	if s != "" && len(t) >= len(s) && strings.EqualFold(t[:len(s)], s) {
		t = strings.TrimSpace(strings.TrimLeft(t[len(s):], " :-\u2013\u2014"))
	}
	// Repeat: "Volume 1: Volume One" is rare but the strip is cheap and the
	// loop terminates because each pass must shorten t.
	for {
		stripped := volumeWordRe.ReplaceAllString(t, "")
		stripped = strings.TrimSpace(strings.TrimLeft(stripped, " :-\u2013\u2014"))
		if stripped == t {
			break
		}
		t = stripped
	}
	if junkSubtitles[strings.ToLower(t)] {
		return ""
	}
	return t
}

// formatWord is the word a format's template already emits before the number,
// and which therefore must not be left on the end of {series} as well:
// "Superman ... Omnibus" + "Omnibus Vol. 1", "Nightwing 2024 Annual" +
// "Annual #001".
var formatWord = map[string]string{"omnibus": "Omnibus", "annual": "Annual"}

// trimTrailingFormatWord drops that word from the end of a series name when the
// format's template is about to add its own copy.
func trimTrailingFormatWord(series, format string) string {
	word, ok := formatWord[strings.ToLower(strings.TrimSpace(format))]
	if !ok {
		return series
	}
	trimmed := strings.TrimSpace(series)
	// Strictly longer, so a series called exactly "Annual" keeps its name
	// rather than collapsing to nothing.
	if len(trimmed) > len(word) && strings.EqualFold(trimmed[len(trimmed)-len(word):], word) {
		return strings.TrimSpace(strings.TrimRight(trimmed[:len(trimmed)-len(word)], " -:"))
	}
	return series
}

// canonicalFolder is the folder basename a volume's issues should live under.
func canonicalFolder(vol *localcv.Volume) (string, error) {
	canonical := vol.Name
	if vol.StartYear != "" {
		canonical += " (" + vol.StartYear + ")"
	}
	return rename.SanitizeFilename(canonical)
}

// srcVol keys a volume by the dump it lives in; the two id spaces overlap.
func srcVol(source, volumeID string) string { return source + "\x00" + volumeID }

// isNotFound reports the "no such id in the dump" sentinel of either dump.
func isNotFound(err error) bool {
	return errors.Is(err, localcv.ErrNotFound) || errors.Is(err, gcd.ErrNotFound)
}

// targetCanonicalFolder resolves a stray's destination folder name: the
// target series' canonical folder for its own confirmed volume, falling back
// to the target's current folder basename if that volume isn't in the dump.
func targetCanonicalFolder(o *Organizer, target store.SeriesMatch) (folder string, vol *localcv.Volume, err error) {
	targetSeries, err := o.St.GetSeries(target.SeriesID)
	if err != nil {
		return "", nil, err
	}
	folder = filepath.Base(targetSeries.FolderPath)

	tvol, terr := o.M.LookupVolume(target.Source, target.VolumeID)
	if terr != nil {
		return folder, nil, nil // fall back to current folder; nothing to retitle with either
	}
	if canonical, cerr := canonicalFolder(tvol); cerr == nil && canonical != "" {
		folder = canonical
	}
	return folder, tvol, nil
}

// resolveNewName applies the format-or-default template to fields, returning
// "" if the template resolves to nothing usable — apply_template.go's
// skip-silently behavior for an unrenameable issue.
func resolveNewName(templates map[string]string, format, ext string, fields map[string]string) string {
	tpl := templates[format]
	if tpl == "" {
		tpl = templates["default"]
	}
	resolved := rename.Resolve(tpl, fields)
	sanitized, err := rename.SanitizeFilename(resolved)
	if err != nil || sanitized == "" {
		return ""
	}
	return sanitized + ext
}

// flagDuplicateTargets converts any op whose planned target collides with
// another op's target into a flag instead. rename_file targets are scoped
// per series (two different series may legitimately want the same filename);
// move_file and rename_folder targets are compared across the whole plan,
// since those collisions land in the same folder.
func flagDuplicateTargets(ops []Op) []Op {
	type key struct{ series, target string }
	renameFileCount := map[key]int{}
	moveCount := map[string]int{}
	folderCount := map[string]int{}

	for _, op := range ops {
		switch op.Kind {
		case OpRenameFile:
			renameFileCount[key{op.SeriesID, op.New}]++
		case OpMoveFile:
			moveCount[op.New]++
		case OpRenameFolder:
			folderCount[op.New]++
		}
	}

	out := make([]Op, 0, len(ops))
	for _, op := range ops {
		dup := false
		switch op.Kind {
		case OpRenameFile:
			dup = renameFileCount[key{op.SeriesID, op.New}] > 1
		case OpMoveFile:
			dup = moveCount[op.New] > 1
		case OpRenameFolder:
			dup = folderCount[op.New] > 1
		}
		if dup {
			out = append(out, Op{Kind: OpFlag, SeriesID: op.SeriesID, IssueID: op.IssueID, Old: op.Old,
				Reason: fmt.Sprintf("duplicate target name %q", op.New)})
			continue
		}
		out = append(out, op)
	}
	return out
}

// buildPlan groups the flat op list into per-series plans, sorted by series
// id for stable output, and fills the console header totals.
func buildPlan(seriesNames map[string]string, ops []Op) *Plan {
	bySeries := map[string][]Op{}
	for _, op := range ops {
		bySeries[op.SeriesID] = append(bySeries[op.SeriesID], op)
	}
	ids := make([]string, 0, len(bySeries))
	for id := range bySeries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	p := &Plan{}
	for _, id := range ids {
		seriesOps := bySeries[id]
		for _, op := range seriesOps {
			switch op.Kind {
			case OpRenameFolder:
				p.FolderRenames++
			case OpMoveFile:
				p.Moves++
			case OpRenameFile:
				p.Renames++
			case OpFlag:
				p.Flags++
			}
		}
		p.Series = append(p.Series, SeriesPlan{SeriesID: id, SeriesName: seriesNames[id], Ops: seriesOps})
	}
	return p
}

// cvVolumeRef matches ComicVine's volume back-links in a description:
// data-ref-id="4050-69537", where 4050 is the volume type prefix.
var cvVolumeRef = regexp.MustCompile(`4050-(\d+)`)

// resolveParentVolume finds the ongoing series a collection folder's trades
// collect, so the folder can be named after it. ComicVine files each collected
// edition as its own volume, so a collection folder has no folder-level volume
// and nothing to name itself from — planCollectionFolder otherwise falls back
// to the folder's parsed name, which is the *first* trade's subtitle ("Black
// Science: How to Fall Forever" for a folder holding v01-v09).
//
// Each trade's description links its parent: every Black Science trade carries
// data-ref-id="4050-69537", which is "Black Science (2013)". Votes are tallied
// across the folder's trades and ids in the folder's own set are ignored, so a
// trade referencing a sibling trade cannot win.
//
// Returns nil (not an error) when the evidence is too thin — see parentVote.
// Callers keep their existing no-parent behavior on nil.
func resolveParentVolume(o *Organizer, cm store.SeriesMatch, issueMatches []store.IssueMatch) (*localcv.Volume, error) {
	own := map[int64]bool{}
	var vols []*localcv.Volume
	seen := map[string]bool{}

	for _, im := range issueMatches {
		up, err := o.M.LookupIssue(im.Source, im.UpstreamID)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		key := srcVol(im.Source, strconv.FormatInt(up.VolumeID, 10))
		if seen[key] {
			continue
		}
		seen[key] = true
		own[up.VolumeID] = true

		vol, err := o.M.LookupVolume(im.Source, strconv.FormatInt(up.VolumeID, 10))
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		vols = append(vols, vol)
	}

	winner, ok := parentVote(vols, own)
	if !ok {
		return nil, nil
	}
	// The vote only yields an id; the source is the folder's, since every
	// candidate came from a volume looked up in that same dump.
	parent, err := o.M.LookupVolume(cm.Source, strconv.FormatInt(winner, 10))
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parent, nil
}

// parentVote tallies back-links across a collection's volumes and returns the
// winner, requiring at least as many votes as there are volumes.
//
// That threshold is what separates a real parent from an incidental mention.
// Measured over the 14 collection folders in the live library, every correct
// resolution had votes >= len(volumes) and both wrong ones fell below it:
// "Countdown to Infinite Crisis" (2 votes / 5 volumes) is five unrelated
// Infinite Crisis one-shots with no common parent, and picked up "Identity
// Crisis" from a passing reference. With the threshold: 12 right, 2 abstain,
// 0 wrong.
//
// A tie abstains: two volumes equally referenced means the folder does not
// have one parent.
func parentVote(vols []*localcv.Volume, own map[int64]bool) (int64, bool) {
	if len(vols) < 2 {
		return 0, false // a single volume is not a collection
	}
	votes := map[int64]int{}
	for _, v := range vols {
		for _, m := range cvVolumeRef.FindAllStringSubmatch(v.Description, -1) {
			id, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil || own[id] {
				continue
			}
			votes[id]++
		}
	}
	var best int64
	bestN, tied := 0, false
	for id, n := range votes {
		switch {
		case n > bestN:
			best, bestN, tied = id, n, false
		case n == bestN:
			tied = true
		}
	}
	if bestN < len(vols) || tied {
		return 0, false
	}
	return best, true
}

// issueYear is the {year} token: the issue's own cover year when known,
// else the volume start year (the old behavior, kept as the fallback).
func issueYear(iss store.TemplateRenameIssue, volStart string) string {
	if iss.CoverYear > 0 {
		return strconv.Itoa(iss.CoverYear)
	}
	return volStart
}
