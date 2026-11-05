package server

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rthaines211/gutter-server/archive"
	"github.com/rthaines211/gutter-server/match/parse"
	"github.com/rthaines211/gutter-server/metadata"
	"github.com/rthaines211/gutter-server/server/admin"
	"github.com/rthaines211/gutter-server/store"
)

const scanFileTimeout = 30 * time.Second
const maxConsecutiveScanFailures = 5

var (
	fnExtRe   = regexp.MustCompile(`(?i)\.(cbz|cbr)$`)
	fnGroupRe = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)
	fnVolRe   = regexp.MustCompile(`(?i)\b(?:v|vol\.?|volume)\s*\d+\b`)
	fnHashRe  = regexp.MustCompile(`#\s*(\d+(?:\.\d+)?)`)
	fnNumRe   = regexp.MustCompile(`\d{1,4}(?:\.\d+)?`)
)

// scanFileDelay paces archive opens during a scan. Zero (default) disables
// throttling. Set via SetScanFileDelay from main at startup.
var scanFileDelay time.Duration

// SetScanFileDelay configures the inter-open delay used during scans.
func SetScanFileDelay(d time.Duration) { scanFileDelay = d }

func slugify(name string) string { return store.SlugifyName(name) }

// issueNumberFromFilename derives an issue number from a comic filename
// without opening the archive. It strips the extension, parenthesized and
// bracketed groups (years, scan tags) and volume markers, then takes the
// number after '#' if present, else the sole remaining numeric token.
// Returns "" when ambiguous (multiple numbers remain) or none is found, so
// the caller can fall back to the authoritative ComicInfo.xml.
func issueNumberFromFilename(name string) string {
	s := fnExtRe.ReplaceAllString(name, "")
	s = fnGroupRe.ReplaceAllString(s, " ")
	s = fnVolRe.ReplaceAllString(s, " ")
	if m := fnHashRe.FindStringSubmatch(s); m != nil {
		return normalizeIssueNum(m[1])
	}
	nums := fnNumRe.FindAllString(s, -1)
	if len(nums) != 1 {
		return "" // zero = none, >1 = ambiguous
	}
	return normalizeIssueNum(nums[0])
}

// normalizeIssueNum strips leading zeros while keeping decimals intact
// ("004" → "4", "012.5" → "12.5", "0" → "0").
func normalizeIssueNum(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intp := strings.TrimLeft(s[:i], "0")
		if intp == "" {
			intp = "0"
		}
		return intp + s[i:]
	}
	t := strings.TrimLeft(s, "0")
	if t == "" {
		return "0"
	}
	return t
}

func ScanLibrary(comicDirs []string, st *store.Store, coverDir string, ss *admin.ScanState) error {
	// An organize apply renames/moves folders on disk; a scan racing it would
	// see a stale directory listing and CASCADE-delete series whose folders
	// just moved out from under it. Refuse until organize finishes.
	if ss != nil && ss.IsOrganizeRunning() {
		ss.AddLog("WARN", "scan skipped: organize apply is in progress")
		log.Printf("scanner: skipped, organize apply in progress")
		return nil
	}
	if ss != nil {
		ss.StartScan()
	}

	// Phase 1: enumerate all series directories
	var entries []seriesEntry

	for _, root := range comicDirs {
		if ss != nil {
			ss.AddLog("INFO", "reading "+root)
		}
		dirEntries, err := os.ReadDir(root)
		if err != nil {
			// A root we cannot read usually means the drive is unmounted.
			// Abort without pruning so a disconnected drive never wipes
			// the library rows.
			if ss != nil {
				ss.AddLog("ERROR", fmt.Sprintf("aborting: cannot read %s: %v", root, err))
				ss.FinishScan(admin.ScanResult{})
			}
			return fmt.Errorf("scanner: root %s unreadable, aborting scan: %w", root, err)
		}
		for _, entry := range dirEntries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			seriesDir := filepath.Join(root, entry.Name())
			comics := listComics(seriesDir)
			if len(comics) == 0 {
				continue
			}
			sort.Slice(comics, func(i, j int) bool { return naturalLess(comics[i], comics[j]) })
			entries = append(entries, seriesEntry{
				name:   entry.Name(),
				dir:    seriesDir,
				comics: comics,
				root:   root,
			})
		}
	}

	// Two folders can slugify to one series id: Mylar's folder_format strips
	// colons, so new issues land in "Rook Exodus (2024)" beside the organized
	// "Rook: Exodus (2024)". The series row holds one primary folder — the one
	// the row already points at, else the better twin (betterTwin) — and its
	// issues are the union of every same-root twin's comics, each keeping its
	// own path-derived id. Twin folders are Mylar's: nothing here moves them.
	// (Processing each twin as its own series replaced the issue rows twice
	// per scan and CASCADE-wiped their matches — the PR #20 loop.)
	keep := map[string]string{} // slug -> dir
	owned := map[string]bool{}  // slug whose kept dir is the row's own folder
	sizes := map[string]int{}   // dir -> comic count
	for _, e := range entries {
		sizes[e.dir] = len(e.comics)
	}
	for _, e := range entries {
		slug := slugify(e.name)
		if owned[slug] {
			continue
		}
		if s, err := st.GetSeries(slug); err == nil && strings.EqualFold(s.FolderPath, e.dir) {
			keep[slug], owned[slug] = e.dir, true
			continue
		}
		if k, ok := keep[slug]; !ok || betterTwin(filepath.Base(e.dir), len(e.comics), filepath.Base(k), sizes[k]) {
			keep[slug] = e.dir
		}
	}
	primary := map[string]int{} // slug -> index in deduped
	deduped := entries[:0:0]
	var twins []seriesEntry
	for _, e := range entries {
		if keep[slugify(e.name)] == e.dir {
			primary[slugify(e.name)] = len(deduped)
			deduped = append(deduped, e)
		} else {
			twins = append(twins, e)
		}
	}
	for _, tw := range twins { // entries are in root, then name order
		p := &deduped[primary[slugify(tw.name)]]
		var msg string
		if tw.root != p.root {
			// Issue ids resolve against the primary's root (store.IssueDir).
			msg = fmt.Sprintf("skipping %s: same series id as %s in another root", tw.dir, p.dir)
		} else {
			p.twins = append(p.twins, tw)
			msg = fmt.Sprintf("merging twin folder %s into series %s", tw.dir, p.dir)
		}
		log.Printf("scanner: %s", msg)
		if ss != nil {
			ss.AddLog("INFO", msg)
		}
	}
	entries = deduped

	if ss != nil {
		ss.SetTotal(len(entries))
	}

	// Phase 2: process each series, skipping unchanged
	var allIDs []string
	var newCount int
	consecutiveFailures := 0

	for _, e := range entries {
		seriesID := slugify(e.name)
		allIDs = append(allIDs, seriesID)

		// Check if series exists with same issue count AND same folder — skip if
		// unchanged. The path check matters: a folder that moved roots (or had a
		// same-slug twin deleted) keeps its issue count, and skipping it would
		// leave folder_path pointing at a directory that no longer exists.
		//
		// hasMetadata is the staleness term: the file-identity checks below all
		// pass for a row that was indexed by a run where extraction failed (scan
		// timeout, unavailable drive, older scanner), so without it such a row is
		// skipped forever and never retried — its files never change again.
		existing, err := st.GetSeries(seriesID)
		isNew := err != nil // store.ErrNotFound means new series
		if !isNew && existing.FolderPath != e.dir && strings.EqualFold(existing.FolderPath, e.dir) {
			// Case-only drift (a case-insensitive volume kept the old
			// spelling): carry every issue's rows to the disk spelling's ids
			// instead of replacing them, which would CASCADE away matches
			// and orphan progress.
			// On failure leave the rows alone: the replace below would CASCADE
			// them away. The next scan retries.
			if err := adoptCase(st, existing, e.dir, e.name, e.comics, coverDir); err != nil {
				log.Printf("scanner: adopting folder case %s: %v", e.dir, err)
				if ss != nil {
					ss.IncrScanned()
				}
				continue
			}
			if existing, err = st.GetSeries(seriesID); err != nil {
				log.Printf("scanner: re-reading %s: %v", seriesID, err)
				continue
			}
		}
		files := e.files(func(msg string) {
			log.Printf("scanner: %s", msg)
			if ss != nil {
				ss.AddLog("INFO", msg)
			}
		})
		changed := isNew || existing.IssueCount != len(files) || existing.FolderPath != e.dir || !sameIssueIDs(st, seriesID, files)
		if !changed && hasMetadata(existing) {
			if ss != nil {
				ss.IncrScanned()
			}
			continue
		}

		// Full processing: metadata, cover, upsert, issues
		thumbPath := filepath.Join(coverDir, "series_"+seriesID+".jpg")
		m, ok := extractMetadataAndCover(filepath.Join(e.dir, e.comics[0]), thumbPath)
		if ok {
			consecutiveFailures = 0
		} else {
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveScanFailures {
				if ss != nil {
					ss.AddLog("ERROR", "aborting: drive appears unavailable (repeated read failures)")
					ss.FinishScan(admin.ScanResult{})
				}
				return fmt.Errorf("scanner: %d consecutive archive-open failures, aborting scan", consecutiveFailures)
			}
		}

		if scanFileDelay > 0 {
			time.Sleep(scanFileDelay)
		}

		coverPath, _ := filepath.Rel(e.root, filepath.Join(e.dir, e.comics[0]))

		series := store.Series{
			ID:         seriesID,
			Name:       e.name,
			FolderPath: e.dir,
			IssueCount: len(files),
			CoverPath:  coverPath,
			Writer:     m.Writer,
			Penciller:  m.Penciller,
			Publisher:  m.Publisher,
			Year:       m.Year,
			CreatedAt:  oldestMtime(e.dir, e.comics), // ignored on re-scan (INSERT-only)
		}

		if err := st.UpsertSeries(series); err != nil {
			log.Printf("scanner: failed to upsert series %s: %v", seriesID, err)
			if ss != nil {
				ss.IncrScanned()
			}
			continue
		}

		if genres := collectGenres([]string{m.Genre}); len(genres) > 0 {
			if err := st.ReplaceSeriesGenres(seriesID, genres); err != nil {
				log.Printf("scanner: failed to replace genres for %s: %v", seriesID, err)
			}
		}

		var dbIssues []store.Issue
		for i, f := range files {
			number := issueNumberFromFilename(f.name)
			if number == "" {
				number = extractIssueNumber(filepath.Join(f.dir, f.name)) // archive fallback, rare
			}
			dbIssues = append(dbIssues, store.Issue{
				ID:       f.id(),
				SeriesID: seriesID,
				Filename: f.name,
				Number:   number,
				Position: i,
			})
		}
		if err := st.ReplaceIssues(seriesID, dbIssues); err != nil {
			log.Printf("scanner: failed to save issues for %s: %v", seriesID, err)
		}

		// Queue for the auto pipeline only what the scan created or whose
		// files changed (spec §1) — a metadata-only re-read is not a change.
		// Recorded even when auto_pipeline is off, so enabling it later
		// catches up on what arrived meanwhile.
		if changed {
			if err := st.AddPipelinePending(seriesID); err != nil {
				log.Printf("scanner: pipeline pending %s: %v", seriesID, err)
			}
		}

		if isNew {
			newCount++
		}

		if ss != nil {
			ss.AddLog("OK", fmt.Sprintf("series %s · %d issues", e.name, len(files)))
			ss.IncrScanned()
		}
	}

	// Count removals
	removedCount := 0
	allSeries, _ := st.ListSeries()
	allIDSet := make(map[string]bool, len(allIDs))
	for _, id := range allIDs {
		allIDSet[id] = true
	}
	for _, s := range allSeries {
		if !allIDSet[s.ID] {
			removedCount++
		}
	}

	if ss != nil {
		ss.FinishScan(admin.ScanResult{
			SeriesCount:  len(allIDs),
			NewCount:     newCount,
			RemovedCount: removedCount,
		})
		ss.AddLog("DONE", fmt.Sprintf("scan complete · %d series · %d new · %d removed", len(allIDs), newCount, removedCount))
	}

	return st.DeleteSeriesNotIn(allIDs)
}

// naturalLess compares two strings with embedded digit runs treated as
// numbers (case-insensitive), so "2.cbz" sorts before "10.cbz".
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ad := isDigit(a[i])
		bd := isDigit(b[j])
		if ad && bd {
			ai, an := i, 0
			for i < len(a) && isDigit(a[i]) {
				i++
				an++
			}
			bj, bn := j, 0
			for j < len(b) && isDigit(b[j]) {
				j++
				bn++
			}
			as := trimLeadingZeros(a[ai : ai+an])
			bs := trimLeadingZeros(b[bj : bj+bn])
			if len(as) != len(bs) {
				return len(as) < len(bs)
			}
			if as != bs {
				return as < bs
			}
			// Numbers equal; padded variant (more leading zeros) sorts first.
			if an != bn {
				return an > bn
			}
			continue
		}
		ac := toLower(a[i])
		bc := toLower(b[j])
		if ac != bc {
			return ac < bc
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func toLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

func trimLeadingZeros(s string) string {
	k := 0
	for k < len(s)-1 && s[k] == '0' {
		k++
	}
	return s[k:]
}

// oldestMtime returns the earliest file-modification time among a series'
// comic files — the best on-disk proxy for "when was this added". Returns the
// zero Time when nothing is stat-able (caller falls back to now). Note: a bulk
// copy/convert rewrites mtimes, so this only reflects true add-date for files
// left untouched since import.
func oldestMtime(dir string, comics []string) time.Time {
	var oldest time.Time
	for _, name := range comics {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if oldest.IsZero() || fi.ModTime().Before(oldest) {
			oldest = fi.ModTime()
		}
	}
	return oldest
}

func listComics(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var comics []string
	for _, e := range entries {
		// Dotfiles are writeback temp files (.rewrite-*, .convert-*) or macOS
		// AppleDouble shadows (._*), never comics.
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".cbz" || ext == ".cbr" {
			comics = append(comics, e.Name())
		}
	}
	return comics
}

// extractMetadataAndCover opens comicPath once, parses series metadata from
// ComicInfo.xml and writes a cover thumbnail from page 0. Returns the
// metadata and ok=false when the archive could not be opened (a corruption
// or drive-disconnect signal). Bounded by scanFileTimeout.
// scanTimeoutFor scales the archive-open budget by file size so multi-GB
// omnibus editions don't hit the 30s floor before their cover reads. Opening a
// large cbz reads the central directory at EOF then decodes page 0; both grow
// with size. ponytail: integer GB steps, cap at 5min — upgrade to a real
// bytes/sec budget only if the cap starts biting.
func scanTimeoutFor(comicPath string) time.Duration {
	fi, err := os.Stat(comicPath)
	if err != nil {
		return scanFileTimeout
	}
	gb := fi.Size() / (1 << 30)
	d := scanFileTimeout + time.Duration(gb)*60*time.Second
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

func extractMetadataAndCover(comicPath, thumbPath string) (issueMetadata, bool) {
	type result struct {
		m  issueMetadata
		ok bool
	}
	ch := make(chan result, 1)
	go func() {
		ar, err := archive.Open(comicPath)
		if err != nil {
			ch <- result{ok: false}
			return
		}
		defer ar.Close()

		var m issueMetadata
		if info, err := metadata.ParseComicInfo(ar.ComicInfoXML()); err == nil {
			m = issueMetadata{
				Writer:    info.Writer,
				Penciller: info.Penciller,
				Publisher: metadata.CanonicalPublisher(info.Publisher),
				Genre:     info.Genre,
				Year:      info.Year,
			}
		}
		if rc, _, err := ar.PageAt(0); err == nil {
			if terr := GenerateThumbnail(rc, thumbPath); terr != nil {
				log.Printf("scanner: failed to generate thumbnail for %s: %v", comicPath, terr)
			}
			rc.Close()
		} else {
			log.Printf("scanner: failed to read page 0 from %s: %v", comicPath, err)
		}
		ch <- result{m: m, ok: true}
	}()
	timeout := scanTimeoutFor(comicPath)
	select {
	case r := <-ch:
		return r.m, r.ok
	case <-time.After(timeout):
		log.Printf("scanner: timeout reading %s (>%s), skipping", comicPath, timeout)
		return issueMetadata{}, false
	}
}

type issueMetadata struct {
	Writer    string
	Penciller string
	Publisher string
	Genre     string
	Year      int
}

// hasMetadata reports whether a stored series row actually carries scanned
// metadata. A row with none was written by a run whose extraction failed, and
// must not be skipped by the unchanged-files check — see the call site.
// Publisher and Writer are the two fields nearly every ComicInfo.xml sets, so
// both being empty means the whole issueMetadata struct went unwritten rather
// than one tag being absent from the file.
func hasMetadata(s *store.Series) bool {
	if s == nil {
		return false
	}
	return strings.TrimSpace(s.Publisher) != "" || strings.TrimSpace(s.Writer) != ""
}

func mostCommon(values []string) string {
	counts := make(map[string]int)
	for _, v := range values {
		if v != "" {
			counts[v]++
		}
	}
	var best string
	var bestCount int
	for v, c := range counts {
		if c > bestCount {
			best = v
			bestCount = c
		}
	}
	return best
}

func earliestYear(years []int) int {
	var min int
	for _, y := range years {
		if y > 0 && (min == 0 || y < min) {
			min = y
		}
	}
	return min
}

func collectGenres(rawGenres []string) []string {
	seen := make(map[string]bool)
	for _, raw := range rawGenres {
		for _, g := range strings.Split(raw, ",") {
			g = strings.TrimSpace(g)
			if g != "" && !seen[g] {
				seen[g] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for g := range seen {
		result = append(result, g)
	}
	sort.Strings(result)
	return result
}

// seriesEntry is one series folder found on disk, plus the same-root twin
// folders whose comics it also lists (see ScanLibrary).
type seriesEntry struct {
	name   string
	dir    string
	comics []string // naturally sorted
	root   string
	twins  []seriesEntry
}

// comicFile is one comic of a series: its folder and filename.
type comicFile struct{ dir, name string }

// id is the comic's path-derived id, relative to the library root.
func (f comicFile) id() string { return pathToID(filepath.Join(filepath.Base(f.dir), f.name)) }

// files lists the series' comics: the primary folder's, then each twin's,
// in a fixed order so an unchanged pair of folders yields the same list
// every scan. An issue a twin shares with an earlier folder — the same
// filename, or the same parsed number and format — is listed once, from the
// primary folder (or the earlier twin); skip is told about each dropped copy.
// Within one folder nothing is dropped (two #1 variants both stay).
func (e seriesEntry) files(skip func(string)) []comicFile {
	var out []comicFile
	seen := map[string]string{} // dedup key -> path of the kept copy, earlier folders only
	add := func(dir string, comics []string) {
		mine := map[string]string{}
		for _, c := range comics {
			keys := []string{"name:" + strings.ToLower(c)}
			if p := parse.ParseFile(c); p.Number != "" {
				keys = append(keys, "num:"+p.Format+"#"+normalizeIssueNum(p.Number))
			}
			dup := ""
			for _, k := range keys {
				if kept, ok := seen[k]; ok {
					dup = kept
					break
				}
			}
			if dup != "" {
				skip(fmt.Sprintf("skipping %s: same issue as %s", filepath.Join(dir, c), dup))
				continue
			}
			for _, k := range keys {
				mine[k] = filepath.Join(dir, c)
			}
			out = insertByNumber(out, comicFile{dir, c}, dir != e.dir)
		}
		for k, v := range mine {
			if _, ok := seen[k]; !ok {
				seen[k] = v
			}
		}
	}
	add(e.dir, e.comics)
	for _, tw := range e.twins {
		add(tw.dir, tw.comics)
	}
	return out
}

// insertByNumber appends f, or — for a twin's comic — inserts it before the
// first listed comic of the same format with a higher parsed number, so a
// twin issue that fills a gap lands in number order. The listed comics keep
// their relative (natural filename) order; a single-folder series is
// untouched. A twin comic with no parsed number goes last.
func insertByNumber(out []comicFile, f comicFile, twin bool) []comicFile {
	p := parse.ParseFile(f.name)
	if twin && p.Number != "" {
		n := normalizeIssueNum(p.Number)
		for i, o := range out {
			q := parse.ParseFile(o.name)
			if q.Number != "" && q.Format == p.Format && naturalLess(n, normalizeIssueNum(q.Number)) {
				return append(out[:i], append([]comicFile{f}, out[i:]...)...)
			}
		}
	}
	return append(out, f)
}

// sameIssueIDs reports whether the stored issues are exactly the comics on
// disk. Ids are path-derived (folder + filename), so this catches in-place
// renames (e.g. an organize run against another DB) and a file moving
// between twin folders, either of which would leave ids pointing at files
// that no longer exist.
func sameIssueIDs(st *store.Store, seriesID string, files []comicFile) bool {
	issues, err := st.ListIssues(seriesID)
	if err != nil || len(issues) != len(files) {
		return false
	}
	have := make(map[string]bool, len(issues))
	for _, iss := range issues {
		have[iss.ID] = true
	}
	for _, f := range files {
		if !have[f.id()] {
			return false
		}
	}
	return true
}

// adoptCase is adoptFolderCase; a var so a test can make it fail.
var adoptCase = adoptFolderCase

// adoptFolderCase migrates a series whose stored folder differs from disk
// only by case onto the disk spelling, keeping every issue-keyed row. Each
// issue is paired with its disk file case-insensitively (an organize
// case-only file rename can have drifted too) and takes the disk filename,
// so the scan's exact filename check then passes instead of replacing rows.
func adoptFolderCase(st *store.Store, s *store.Series, dir, name string, comics []string, coverDir string) error {
	issues, err := st.ListIssues(s.ID)
	if err != nil {
		return err
	}
	pairs := make([]store.IDPair, 0, len(issues))
	for _, iss := range issues {
		if store.InTwinFolder(s.FolderPath, iss.ID, iss.Filename) {
			continue // a twin folder's issue keeps its id; only the primary drifted
		}
		fn := iss.Filename
		for _, c := range comics {
			if c == iss.Filename {
				fn = c
				break
			}
			if strings.EqualFold(c, iss.Filename) {
				fn = c
			}
		}
		pairs = append(pairs, store.IDPair{Old: iss.ID, New: pathToID(filepath.Join(name, fn)), Filename: fn})
	}
	if err := st.MigrateSeriesFolder(s.ID, s.ID, dir, name, pairs); err != nil {
		return err
	}
	// /sync must re-send the series with its new inline issue ids, or the
	// app keeps the old ids and writes orphan progress.
	if err := st.TouchSeries(s.ID); err != nil {
		log.Printf("scanner: touch %s: %v", s.ID, err)
	}
	for _, p := range pairs {
		if p.Old != p.New && coverDir != "" {
			os.Remove(filepath.Join(coverDir, "comic_"+p.Old+".jpg"))
		}
	}
	return nil
}

// betterTwin reports whether folder a (n comics) should hold a series id
// over same-slug folder b (m comics) when no row decides: more comics wins,
// then the spelling with a colon (organize's canonical form, which
// downloaders strip), then the bytewise-first name.
func betterTwin(a string, n int, b string, m int) bool {
	if n != m {
		return n > m
	}
	if ca, cb := strings.Contains(a, ":"), strings.Contains(b, ":"); ca != cb {
		return ca
	}
	return a < b
}
