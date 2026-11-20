package server

import (
	"log"
	"time"

	"github.com/rthaines211/gutter-server/archive"
)

// pageOrderMigration names the one-time pass that moved stored page numbers
// when pages switched from byte order to natural order.
const pageOrderMigration = "natural_page_order"

// MigratePageOrder moves stored page numbers (progress, bookmarks, chapters,
// page flags) to each page's new natural-order index, once per database. It
// opens only comics with stored page numbers and reads entry names only. A
// store error leaves the pass unfinished so the next start retries it.
func (s *Server) MigratePageOrder() {
	if done, err := s.Store.MigrationDone(pageOrderMigration); err != nil || done {
		if err != nil {
			log.Printf("page order: %v", err)
		}
		return
	}
	start := time.Now()
	ids, err := s.Store.PageDataComicIDs()
	if err != nil {
		log.Printf("page order: %v", err)
		return
	}
	remapped, skipped := 0, 0
	for _, id := range ids {
		path, err := s.resolveComicPath(id)
		if err != nil {
			skipped++
			continue
		}
		names, err := archive.ImageEntryNames(path)
		if err != nil {
			log.Printf("page order: skip %s: %v", path, err)
			skipped++
			continue
		}
		m := archive.PageOrderMap(names)
		if m == nil {
			continue
		}
		if err := s.Store.RemapPages(id, m, start, pageOrderMigration+":"+id); err != nil {
			log.Printf("page order: %s: %v (will retry next start)", path, err)
			return
		}
		remapped++
	}
	if err := s.Store.MarkMigrationDone(pageOrderMigration); err != nil {
		log.Printf("page order: %v", err)
		return
	}
	log.Printf("page order: remapped %d comics (%d skipped)", remapped, skipped)
}
