package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ryanhaines/comic-reader/archive"
	"github.com/ryanhaines/comic-reader/comicvine"
	"github.com/ryanhaines/comic-reader/metadata"
	"github.com/ryanhaines/comic-reader/server/admin"
	"github.com/ryanhaines/comic-reader/store"
)

type LibrarySeriesResponse struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	FolderPath       string          `json:"folder_path"`
	IssueCount       int             `json:"issue_count"`
	ReadCount        int             `json:"read_count"`
	CoverURL         string          `json:"cover_url"`
	Writer           string          `json:"writer"`
	Penciller        string          `json:"penciller"`
	Publisher        string          `json:"publisher"`
	Genres           []string        `json:"genres"`
	Year             int             `json:"year"`
	UpdatedAt        string          `json:"updated_at"`
	Description      string          `json:"description,omitempty"`
	StartYear        int             `json:"start_year,omitempty"`
	Creators         []store.Creator `json:"creators,omitempty"`
	EnrichmentStatus string          `json:"enrichment_status,omitempty"`
	lastReadAt       string
}

type IssueResponse struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Number          string   `json:"number"`
	CoverURL        string   `json:"cover_url"`
	Status          string   `json:"status"`
	CurrentPage     *int     `json:"current_page"`
	TotalPages      *int     `json:"total_pages"`
	Title           string   `json:"title,omitempty"`
	Summary         string   `json:"summary,omitempty"`
	CoverDate       string   `json:"cover_date,omitempty"`
	Format          string   `json:"format,omitempty"`
	Writer          string   `json:"writer,omitempty"`
	Penciller       string   `json:"penciller,omitempty"`
	WritebackStatus string   `json:"writeback_status,omitempty"`
	WritebackError  string   `json:"writeback_error,omitempty"`
	PageCount       float64  `json:"page_count,omitempty"`
	Characters      []string `json:"characters,omitempty"`
}

type SeriesDetailResponse struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	FolderPath       string          `json:"folder_path"`
	IssueCount       int             `json:"issue_count"`
	ReadCount        int             `json:"read_count"`
	CoverURL         string          `json:"cover_url"`
	Writer           string          `json:"writer"`
	Penciller        string          `json:"penciller"`
	Publisher        string          `json:"publisher"`
	Genres           []string        `json:"genres"`
	Year             int             `json:"year"`
	Description      string          `json:"description,omitempty"`
	StartYear        int             `json:"start_year,omitempty"`
	Creators         []store.Creator `json:"creators,omitempty"`
	EnrichmentStatus string          `json:"enrichment_status,omitempty"`
	Issues           []IssueResponse `json:"issues"`
}

func (s *Server) HandleListLibrary(w http.ResponseWriter, r *http.Request) {
	allSeries, err := s.Store.ListSeries()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	q := strings.ToLower(r.URL.Query().Get("q"))
	statusFilter := r.URL.Query().Get("status")
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "name"
	}
	order := r.URL.Query().Get("order")
	if order == "" {
		order = "asc"
	}

	publisherFilter := r.URL.Query().Get("publisher")
	genreFilter := r.URL.Query().Get("genre")
	writerFilter := r.URL.Query().Get("writer")
	pencillerFilter := r.URL.Query().Get("penciller")
	var yearMin, yearMax int
	if v := r.URL.Query().Get("year_min"); v != "" {
		fmt.Sscanf(v, "%d", &yearMin)
	}
	if v := r.URL.Query().Get("year_max"); v != "" {
		fmt.Sscanf(v, "%d", &yearMax)
	}

	extras := s.enrichmentExtras()
	var result []LibrarySeriesResponse
	for _, series := range allSeries {
		// Apply enrichment overrides before any filter/sort logic reads these fields.
		var seriesExtra seriesExtras
		if e, ok := extras[series.ID]; ok {
			seriesExtra = e
			series = mergeSeries(series, e)
		}

		if q != "" && !strings.Contains(strings.ToLower(series.Name), q) {
			continue
		}

		readCount := s.countReadIssues(series)

		if statusFilter != "" && statusFilter != "all" {
			match := false
			switch statusFilter {
			case "unread":
				match = readCount == 0
			case "in_progress":
				match = readCount > 0 && readCount < series.IssueCount
			case "completed":
				match = readCount >= series.IssueCount && series.IssueCount > 0
			}
			if !match {
				continue
			}
		}

		if publisherFilter != "" && series.Publisher != publisherFilter {
			continue
		}
		if writerFilter != "" && !creatorMatches(series.Writer, writerFilter) {
			continue
		}
		if pencillerFilter != "" && !creatorMatches(series.Penciller, pencillerFilter) {
			continue
		}
		if yearMin > 0 && series.Year < yearMin {
			continue
		}
		if yearMax > 0 && series.Year > yearMax {
			continue
		}

		genres, err := s.Store.ListSeriesGenres(series.ID)
		if err != nil || genres == nil {
			genres = []string{}
		}

		if genreFilter != "" {
			found := false
			for _, g := range genres {
				if g == genreFilter {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		coverURL := ""
		if series.CoverPath != "" {
			coverURL = "/library/" + series.ID + "/cover"
		}

		lastRead := s.lastReadTime(series)

		entry := LibrarySeriesResponse{
			ID:         series.ID,
			Name:       series.Name,
			FolderPath: series.FolderPath,
			IssueCount: series.IssueCount,
			ReadCount:  readCount,
			CoverURL:   coverURL,
			Writer:     series.Writer,
			Penciller:  series.Penciller,
			Publisher:  series.Publisher,
			Genres:     genres,
			Year:       series.Year,
			UpdatedAt:  series.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			lastReadAt: lastRead,
		}
		if seriesExtra.Status != "" {
			entry.Description = comicvine.StripHTML(seriesExtra.Description)
			entry.StartYear = seriesExtra.StartYear
			entry.Creators = seriesExtra.Creators
			entry.EnrichmentStatus = seriesExtra.Status
		}
		result = append(result, entry)
	}

	sort.Slice(result, func(i, j int) bool {
		var less bool
		switch sortBy {
		case "unread":
			unreadI := result[i].IssueCount - result[i].ReadCount
			unreadJ := result[j].IssueCount - result[j].ReadCount
			less = unreadI > unreadJ
		case "recent":
			less = result[i].UpdatedAt > result[j].UpdatedAt
		case "recently_read":
			less = result[i].lastReadAt > result[j].lastReadAt
		case "publisher":
			less = strings.ToLower(result[i].Publisher) < strings.ToLower(result[j].Publisher)
		case "year":
			less = result[i].Year < result[j].Year
		case "issue_count":
			less = result[i].IssueCount < result[j].IssueCount
		case "unread_count":
			unreadI := result[i].IssueCount - result[i].ReadCount
			unreadJ := result[j].IssueCount - result[j].ReadCount
			less = unreadI < unreadJ
		case "progress":
			progI := float64(0)
			if result[i].IssueCount > 0 {
				progI = float64(result[i].ReadCount) / float64(result[i].IssueCount)
			}
			progJ := float64(0)
			if result[j].IssueCount > 0 {
				progJ = float64(result[j].ReadCount) / float64(result[j].IssueCount)
			}
			less = progI < progJ
		default:
			less = strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
		}
		if order == "desc" {
			return !less
		}
		return less
	})

	if result == nil {
		result = []LibrarySeriesResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) HandleGetSeries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	series, err := s.Store.GetSeries(id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}

	issues := s.buildIssueList(series)
	readCount := 0
	for _, iss := range issues {
		if iss.Status == "completed" {
			readCount++
		}
	}

	coverURL := ""
	if series.CoverPath != "" {
		coverURL = "/library/" + series.ID + "/cover"
	}

	genres, err := s.Store.ListSeriesGenres(series.ID)
	if err != nil || genres == nil {
		genres = []string{}
	}

	detail := SeriesDetailResponse{
		ID:         series.ID,
		Name:       series.Name,
		FolderPath: series.FolderPath,
		IssueCount: series.IssueCount,
		ReadCount:  readCount,
		CoverURL:   coverURL,
		Writer:     series.Writer,
		Penciller:  series.Penciller,
		Publisher:  series.Publisher,
		Genres:     genres,
		Year:       series.Year,
		Issues:     issues,
	}

	// Overlay enrichment if available; ErrNotFound is silently ignored.
	if enrichment, err := s.Store.GetSeriesEnrichment(id); err == nil {
		var creators []store.Creator
		if err2 := json.Unmarshal([]byte(enrichment.Creators), &creators); err2 != nil {
			creators = nil
		}
		detail.Description = comicvine.StripHTML(enrichment.Description)
		detail.StartYear = enrichment.StartYear
		detail.Creators = creators
		detail.EnrichmentStatus = enrichment.Status
		if p := metadata.CanonicalPublisher(enrichment.Publisher); p != "" {
			detail.Publisher = p
		}
		if w2 := creatorsByRole(creators, "writer"); w2 != "" {
			detail.Writer = w2
		}
		if p := creatorsByRole(creators, "pencil", "artist"); p != "" {
			detail.Penciller = p
		}
		if enrichment.StartYear > 0 {
			detail.Year = enrichment.StartYear
		}
		if enrichment.Genre != "" {
			has := false
			for _, g := range detail.Genres {
				if strings.EqualFold(g, enrichment.Genre) {
					has = true
					break
				}
			}
			if !has {
				detail.Genres = append(detail.Genres, enrichment.Genre)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(detail)
}

func (s *Server) HandleSeriesCover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Check for cached thumbnail
	thumbPath := filepath.Join(s.CoverDir, "series_"+id+".jpg")
	if _, err := os.Stat(thumbPath); err == nil {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, thumbPath)
		return
	}

	// Fallback to archive extraction
	series, err := s.Store.GetSeries(id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "series not found")
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}

	if series.CoverPath == "" {
		writeJSONError(w, http.StatusNotFound, "no cover")
		return
	}

	coverComicID := pathToID(series.CoverPath)
	absPath, err := s.resolveComicPath(coverComicID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "cover comic not found")
		return
	}

	cached, err := getArchive(absPath)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	cached.mu.Lock()
	rc, contentType, err := cached.reader.PageAt(0)
	cached.mu.Unlock()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	buf := make([]byte, 32*1024)
	for {
		n, readErr := rc.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
}

func (s *Server) HandleRefreshLibrary(w http.ResponseWriter, r *http.Request) {
	var ss *admin.ScanState
	if s.Admin != nil {
		ss = s.Admin.ScanState
	}

	// Reject if scan already running
	if ss != nil {
		snap := ss.Snapshot()
		if snap.IsScanning || snap.PipelineRunning {
			writeJSONError(w, http.StatusConflict, "scan already in progress")
			return
		}
	}

	// Launch scan in background
	if s.Rescan != nil {
		go s.Rescan()
		w.WriteHeader(http.StatusAccepted)
		return
	}
	go func() {
		if err := ScanLibrary(s.ComicDirs, s.Store, s.CoverDir, ss); err != nil {
			log.Printf("background scan failed: %v", err)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
}

// seriesProgress is the stored progress of every issue in a series. It
// lists the series' issue rows, not its folder, so a twin-folder issue
// (ScanLibrary) counts too.
func (s *Server) seriesProgress(series store.Series) map[string]store.Progress {
	ids, err := s.Store.IssueIDsForSeries(series.ID)
	if err != nil {
		return nil
	}
	progress, _ := s.Store.GetProgressForComics(ids)
	return progress
}

func (s *Server) lastReadTime(series store.Series) string {
	var latest string
	for _, progress := range s.seriesProgress(series) {
		ts := progress.UpdatedAt.Format("2006-01-02T15:04:05Z")
		if ts > latest {
			latest = ts
		}
	}
	return latest
}

func (s *Server) countReadIssues(series store.Series) int {
	count := 0
	for _, progress := range s.seriesProgress(series) {
		if progress.CurrentPage >= progress.TotalPages {
			count++
		}
	}
	return count
}

func (s *Server) buildIssueList(series *store.Series) []IssueResponse {
	// Load issue enrichment once; ignore error — enrichment is optional.
	issueExtras, err := s.Store.ListIssueEnrichmentForSeries(series.ID)
	if err != nil {
		issueExtras = map[string]store.IssueEnrichment{}
	}

	// Load writeback status once for all issues in this series.
	// Only "error" status is surfaced to clients; pending/done stay internal.
	writebackStatus, _ := s.Store.WritebackStatusForSeries(series.ID)

	// Try DB first (no filesystem access needed)
	dbIssues, err := s.Store.ListIssues(series.ID)
	if err == nil && len(dbIssues) > 0 {
		issueIDs := make([]string, len(dbIssues))
		for i, iss := range dbIssues {
			issueIDs[i] = iss.ID
		}
		// One batched query for every issue's progress instead of one query
		// per issue. Ignore error like the enrichment/writeback lookups
		// above — progress is best-effort here, a failure just means every
		// issue reports "unread" instead of failing the whole series.
		progressByIssue, err := s.Store.GetProgressForComics(issueIDs)
		if err != nil {
			progressByIssue = map[string]store.Progress{}
		}

		issues := make([]IssueResponse, 0, len(dbIssues))
		for _, iss := range dbIssues {
			status := "unread"
			var currentPage, totalPages *int
			if progress, ok := progressByIssue[iss.ID]; ok {
				cp := progress.CurrentPage
				tp := progress.TotalPages
				currentPage = &cp
				totalPages = &tp
				if progress.CurrentPage >= progress.TotalPages {
					status = "completed"
				} else {
					status = "in_progress"
				}
			}
			ir := IssueResponse{
				ID:          iss.ID,
				Name:        iss.Filename,
				Number:      iss.Number,
				CoverURL:    "/comics/" + iss.ID + "/cover",
				Status:      status,
				CurrentPage: currentPage,
				TotalPages:  totalPages,
				Format:      iss.Format,
				Writer:      iss.Writer,
				Penciller:   iss.Penciller,
			}
			if ie, ok := issueExtras[iss.ID]; ok {
				ir.Title = ie.Title
				ir.Summary = comicvine.StripHTML(ie.Summary)
				ir.CoverDate = ie.CoverDate
				ir.PageCount = ie.PageCount
				if ie.Characters != "" && ie.Characters != "[]" {
					var chars []struct {
						Name string `json:"name"`
					}
					if json.Unmarshal([]byte(ie.Characters), &chars) == nil {
						for _, c := range chars {
							ir.Characters = append(ir.Characters, c.Name)
						}
					}
				}
			}
			if wb, ok := writebackStatus[iss.ID]; ok && wb[0] == "error" {
				ir.WritebackStatus = wb[0]
				ir.WritebackError = wb[1]
			}
			issues = append(issues, ir)
		}
		return issues
	}

	// Fallback to filesystem
	comics := listComics(series.FolderPath)
	sort.Strings(comics)

	comicIDs := make([]string, 0, len(comics))
	for _, comic := range comics {
		relPath, err := filepath.Rel(filepath.Dir(series.FolderPath), filepath.Join(series.FolderPath, comic))
		if err != nil {
			continue
		}
		comicIDs = append(comicIDs, pathToID(relPath))
	}
	// Same batching as the DB-issues path above: one query for every
	// comic's progress instead of one per comic.
	progressByComic, err := s.Store.GetProgressForComics(comicIDs)
	if err != nil {
		progressByComic = map[string]store.Progress{}
	}

	issues := make([]IssueResponse, 0, len(comics))
	for _, comic := range comics {
		relPath, err := filepath.Rel(filepath.Dir(series.FolderPath), filepath.Join(series.FolderPath, comic))
		if err != nil {
			continue
		}
		comicID := pathToID(relPath)

		status := "unread"
		var currentPage, totalPages *int
		if progress, ok := progressByComic[comicID]; ok {
			cp := progress.CurrentPage
			tp := progress.TotalPages
			currentPage = &cp
			totalPages = &tp
			if progress.CurrentPage >= progress.TotalPages {
				status = "completed"
			} else {
				status = "in_progress"
			}
		}

		number := extractIssueNumber(filepath.Join(series.FolderPath, comic))

		issues = append(issues, IssueResponse{
			ID:          comicID,
			Name:        comic,
			Number:      number,
			CoverURL:    "/comics/" + comicID + "/cover",
			Status:      status,
			CurrentPage: currentPage,
			TotalPages:  totalPages,
		})
	}
	return issues
}

func extractIssueNumber(comicPath string) string {
	ext := strings.ToLower(filepath.Ext(comicPath))
	if ext != ".cbz" && ext != ".cbr" {
		return ""
	}

	if _, err := os.Stat(comicPath); err != nil {
		return ""
	}

	ch := make(chan string, 1)
	go func() {
		ar, err := archive.Open(comicPath)
		if err != nil {
			ch <- ""
			return
		}
		defer ar.Close()

		info, err := metadata.ParseComicInfo(ar.ComicInfoXML())
		if err == nil && info.Number != "" {
			ch <- info.Number
			return
		}
		ch <- ""
	}()
	select {
	case num := <-ch:
		return num
	case <-time.After(scanFileTimeout):
		log.Printf("scanner: timeout reading issue number from %s (>%s), skipping", comicPath, scanFileTimeout)
		return ""
	}
}

func (s *Server) HandleScanStatus(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil || s.Admin.ScanState == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "scan state not available")
		return
	}
	snap := s.Admin.ScanState.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}
