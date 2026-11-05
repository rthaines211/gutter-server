package archive

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gen2brain/go-unarr"
)

// ExtractedCBRReader serves CBR pages from a pre-extracted cache directory.
// On first access, pages are extracted to disk. Subsequent opens reuse the cache.
type ExtractedCBRReader struct {
	pages    []extractedPage
	comicXML []byte
	cacheDir string
}

type extractedPage struct {
	path string // absolute path to extracted image file
	name string // original name in archive (for content-type)
}

// cbrCacheDir is the base directory for extracted CBR pages.
// Set by SetCBRCacheDir before opening any CBR files.
var cbrCacheDir string

// cbrCacheMaxBytes is the maximum total size of the CBR cache in bytes.
// Set by SetCBRCacheMaxGB. Zero means unlimited.
var cbrCacheMaxBytes int64

// SetCBRCacheDir sets the directory used for CBR page extraction cache.
func SetCBRCacheDir(dir string) {
	cbrCacheDir = dir
}

// SetCBRCacheMaxGB sets the maximum cache size in gigabytes.
func SetCBRCacheMaxGB(gb float64) {
	cbrCacheMaxBytes = int64(gb * 1024 * 1024 * 1024)
}

// openExtractedCBR extracts a CBR archive to a cache directory and returns
// a reader that serves pages from disk. If already extracted, reuses the cache.
func openExtractedCBR(archivePath string) (*ExtractedCBRReader, error) {
	if cbrCacheDir == "" {
		// Fallback to in-memory if no cache dir configured
		return nil, fmt.Errorf("no CBR cache dir configured")
	}

	cacheKey := cbrCacheKey(archivePath)
	extractDir := filepath.Join(cbrCacheDir, cacheKey)
	markerFile := filepath.Join(extractDir, ".extracted")

	// Check if already extracted
	if _, err := os.Stat(markerFile); err == nil {
		// Touch marker to update access time for LRU tracking
		now := time.Now()
		os.Chtimes(markerFile, now, now)
		return loadExtractedCBR(extractDir)
	}

	// Extract to disk
	if err := extractCBRToDisk(archivePath, extractDir); err != nil {
		// Clean up partial extraction
		os.RemoveAll(extractDir)
		return nil, fmt.Errorf("extracting cbr to disk: %w", err)
	}

	// Evict old entries if cache exceeds size limit
	go EvictCBRCache()

	return loadExtractedCBR(extractDir)
}

// cbrCacheKey generates a unique directory name from the archive path.
func cbrCacheKey(archivePath string) string {
	h := sha256.Sum256([]byte(archivePath))
	base := filepath.Base(archivePath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	// Use first 12 hex chars of hash + sanitized name for readability
	return fmt.Sprintf("%x_%s", h[:6], sanitizeName(base))
}

func sanitizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}

// extractCBRToDisk extracts all image pages and ComicInfo.xml from a CBR archive.
//
// It streams each entry straight from the archive to disk, one at a time, so peak
// memory is one page rather than the whole archive. (It used to go through the
// in-memory openCBR reader; a 370 MB trade paperback then needed ~1 GB of RSS and
// got the container OOM-killed at its 1 GiB cap, 2026-09-17.) Entries are written
// under temporary names first, then renamed to page_%04d in name order, which is
// what loadExtractedCBR expects.
func extractCBRToDisk(archivePath, extractDir string) error {
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return err
	}

	a, err := unarr.NewArchive(archivePath)
	if err != nil {
		return fmt.Errorf("opening cbr: %w", err)
	}
	defer a.Close()

	type rawPage struct{ name, tmp string }
	var pages []rawPage
	for i := 0; a.Entry() == nil; i++ {
		name := a.Name()
		lower := strings.ToLower(filepath.Base(name))
		ext := filepath.Ext(lower)
		var dst string
		switch {
		case lower == "comicinfo.xml":
			dst = filepath.Join(extractDir, "comicinfo.xml")
		case imageExts[ext] != "":
			dst = filepath.Join(extractDir, fmt.Sprintf(".raw_%06d%s", i, ext))
			pages = append(pages, rawPage{name: name, tmp: dst})
		default:
			continue
		}
		f, err := os.Create(dst)
		if err != nil {
			return err
		}
		// go-unarr's Read always reports len(b) bytes and only signals EOF on a
		// decoder failure, so an unbounded io.Copy over-reads past the entry.
		// Copy exactly the declared size, like unarr's own ReadAll does.
		_, err = io.CopyN(f, a, int64(a.Size()))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("extracting %s: %w", name, err)
		}
	}

	sort.Slice(pages, func(i, j int) bool { return pages[i].name < pages[j].name })
	for i, p := range pages {
		final := filepath.Join(extractDir, fmt.Sprintf("page_%04d%s", i, strings.ToLower(filepath.Ext(p.name))))
		if err := os.Rename(p.tmp, final); err != nil {
			return fmt.Errorf("naming page %d: %w", i, err)
		}
	}

	// Write marker file to signal extraction is complete
	marker := filepath.Join(extractDir, ".extracted")
	if err := os.WriteFile(marker, []byte("ok"), 0644); err != nil {
		return err
	}

	return nil
}

// loadExtractedCBR loads an already-extracted CBR from a cache directory.
func loadExtractedCBR(extractDir string) (*ExtractedCBRReader, error) {
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return nil, fmt.Errorf("reading extract dir: %w", err)
	}

	r := &ExtractedCBRReader{cacheDir: extractDir}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)

		if lower == "comicinfo.xml" {
			data, err := os.ReadFile(filepath.Join(extractDir, name))
			if err == nil {
				r.comicXML = data
			}
			continue
		}

		ext := filepath.Ext(lower)
		if _, ok := imageExts[ext]; ok && strings.HasPrefix(lower, "page_") {
			r.pages = append(r.pages, extractedPage{
				path: filepath.Join(extractDir, name),
				name: name,
			})
		}
	}

	sort.Slice(r.pages, func(i, j int) bool {
		return r.pages[i].name < r.pages[j].name
	})

	return r, nil
}

func (r *ExtractedCBRReader) PageCount() int { return len(r.pages) }

func (r *ExtractedCBRReader) PageNames() []string {
	out := make([]string, len(r.pages))
	for i, p := range r.pages {
		out[i] = p.name
	}
	return out
}

func (r *ExtractedCBRReader) PageAt(index int) (io.ReadCloser, string, error) {
	if index < 0 || index >= len(r.pages) {
		return nil, "", fmt.Errorf("page index %d out of range (0-%d)", index, len(r.pages)-1)
	}
	page := r.pages[index]
	ext := strings.ToLower(filepath.Ext(page.name))
	contentType := imageExts[ext]
	f, err := os.Open(page.path)
	if err != nil {
		return nil, "", fmt.Errorf("opening page %d: %w", index, err)
	}
	return f, contentType, nil
}

func (r *ExtractedCBRReader) ComicInfoXML() []byte { return r.comicXML }
func (r *ExtractedCBRReader) Close() error         { return nil }

// cacheEntry tracks a cached extraction for LRU eviction.
type cacheEntry struct {
	path       string
	size       int64
	accessTime time.Time
}

// StartCBRCacheEvictor runs eviction once immediately, then on every tick of interval.
// Safe to call with interval=0 to skip the periodic loop and only run startup eviction.
func StartCBRCacheEvictor(interval time.Duration) {
	go EvictCBRCache()
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			EvictCBRCache()
		}
	}()
}

// EvictCBRCache removes least-recently-used cache entries until total size is under the limit.
func EvictCBRCache() {
	if cbrCacheMaxBytes <= 0 || cbrCacheDir == "" {
		return
	}

	entries, err := os.ReadDir(cbrCacheDir)
	if err != nil {
		return
	}

	var cacheEntries []cacheEntry
	var totalSize int64

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirPath := filepath.Join(cbrCacheDir, e.Name())
		markerPath := filepath.Join(dirPath, ".extracted")
		markerInfo, err := os.Stat(markerPath)
		if err != nil {
			continue // not a valid cache entry
		}

		dirSize := dirSize(dirPath)
		totalSize += dirSize
		cacheEntries = append(cacheEntries, cacheEntry{
			path:       dirPath,
			size:       dirSize,
			accessTime: markerInfo.ModTime(),
		})
	}

	if totalSize <= cbrCacheMaxBytes {
		return
	}

	// Sort oldest-accessed first
	sort.Slice(cacheEntries, func(i, j int) bool {
		return cacheEntries[i].accessTime.Before(cacheEntries[j].accessTime)
	})

	for _, entry := range cacheEntries {
		if totalSize <= cbrCacheMaxBytes {
			break
		}
		log.Printf("cbr cache evicting %s (last accessed %s, %d MB)",
			filepath.Base(entry.path), entry.accessTime.Format("2006-01-02"), entry.size/(1024*1024))
		if err := os.RemoveAll(entry.path); err != nil {
			log.Printf("cbr cache eviction failed for %s: %v", entry.path, err)
			continue
		}
		totalSize -= entry.size
	}
}

// dirSize returns the total size of all files in a directory (non-recursive).
func dirSize(path string) int64 {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}
