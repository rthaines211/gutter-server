package server

import (
	"log"
	"os"

	"github.com/rthaines211/gutter-server/archive"
)

// pageOrderMigration names the one-time pass that moved stored page numbers
// when pages switched from byte order to natural order.
const pageOrderMigration = "natural_page_order"

// MigratePageOrder moves stored page numbers (progress, bookmarks, chapters,
// page flags) to each page's new natural-order index, once per database. It
// opens only comics with stored page numbers and reads entry names only.
// Comics whose file is gone are skipped; if the library is unavailable, or a
// comic can't be read or written, the pass stays unfinished and the next
// start retries it with the first start's cutover (finished comics carry
// their own marker and are not redone).
func (s *Server) MigratePageOrder() {
	if done, err := s.Store.MigrationDone(pageOrderMigration); err != nil || done {
		if err != nil {
			log.Printf("page order: %v", err)
		}
		return
	}
	ids, err := s.Store.PageDataComicIDs()
	if err != nil {
		log.Printf("page order: %v", err)
		return
	}
	if len(ids) > 0 {
		for _, root := range s.ComicDirs {
			if entries, err := os.ReadDir(root); err != nil || len(entries) == 0 {
				log.Printf("page order: library %s not available, will retry next start", root)
				return
			}
		}
	}
	cutover, err := s.Store.MigrationStart(pageOrderMigration)
	if err != nil {
		log.Printf("page order: %v", err)
		return
	}
	remapped, skipped, failed := 0, 0, 0
	for _, id := range ids {
		path, err := s.resolveComicPath(id)
		if err != nil {
			log.Printf("page order: skip %s: %v", id, err)
			skipped++
			continue
		}
		names, err := archive.ImageEntryNames(path)
		if err != nil {
			log.Printf("page order: %s: %v (will retry next start)", path, err)
			failed++
			continue
		}
		m := archive.PageOrderMap(names)
		if m == nil {
			continue
		}
		if err := s.Store.RemapPages(id, m, cutover, pageOrderMigration+":"+id); err != nil {
			log.Printf("page order: %s: %v (will retry next start)", path, err)
			failed++
			continue
		}
		remapped++
	}
	log.Printf("page order: remapped %d comics (%d gone, %d failed)", remapped, skipped, failed)
	if failed > 0 {
		return
	}
	if err := s.Store.MarkMigrationDone(pageOrderMigration); err != nil {
		log.Printf("page order: %v", err)
	}
}
