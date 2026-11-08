package server

// OPDS 1.2 catalog with OPDS-PSE page streaming, under /opds/. Feed
// generation lives in the opds package; this file maps the library onto it.
//
// Feeds:
//
//	/opds/                 navigation root
//	/opds/series?page=N    all series (navigation, 100 per page)
//	/opds/series/{id}      issues of one series (acquisition)
//	/opds/recent           recently added series (navigation)
//	/opds/reading          in-progress issues (acquisition)
//	/opds/search?q=        series search (navigation)
//	/opds/opensearch.xml   OpenSearch description for /opds/search
//	/opds/download/{id}    raw CBZ/CBR download (records the KOReader digest)
//
// Pages stream from the existing /comics/{id}/pages/{num} endpoint (0-based,
// matching OPDS-PSE's {pageNumber}); covers from /comics/{id}/cover and
// /library/{id}/cover. Links are absolute paths, so a reverse proxy that
// mounts Gutter under a sub-path is not supported.

import (
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rthaines211/gutter-server/archive"
	"github.com/rthaines211/gutter-server/comicvine"
	"github.com/rthaines211/gutter-server/opds"
	"github.com/rthaines211/gutter-server/store"
)

const (
	opdsPageSize    = 100
	opdsRecentLimit = 50
)

func writeOPDS(w http.ResponseWriter, f opds.Feed) {
	out, err := f.Marshal()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", opds.ContentType)
	w.Write(out)
}

// opdsCommonLinks are the start/search links every feed carries.
func opdsCommonLinks(self, selfType string) []opds.Link {
	return []opds.Link{
		{Rel: opds.RelSelf, Href: self, Type: selfType},
		{Rel: opds.RelStart, Href: "/opds/", Type: opds.TypeNavigation, Title: "Gutter"},
		{Rel: opds.RelSearch, Href: "/opds/opensearch.xml", Type: opds.TypeOpenSearch, Title: "Search"},
	}
}

// HandleOPDSRoot serves the navigation root.
func (s *Server) HandleOPDSRoot(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	nav := func(id, title, content, href, typ string, rel string) opds.Entry {
		return opds.Entry{
			ID: "urn:gutter:" + id, Title: title, Updated: now, Content: content,
			Links: []opds.Link{{Rel: rel, Href: href, Type: typ}},
		}
	}
	writeOPDS(w, opds.Feed{
		ID:      "urn:gutter:root",
		Title:   "Gutter",
		Updated: now,
		Links:   opdsCommonLinks("/opds/", opds.TypeNavigation),
		Entries: []opds.Entry{
			nav("reading", "Continue reading", "Issues you have started", "/opds/reading", opds.TypeAcquisition, opds.RelSubsection),
			nav("recent", "Recently added", "Newest series in the library", "/opds/recent", opds.TypeNavigation, opds.RelSortNew),
			nav("series", "All series", "Every series, A to Z", "/opds/series", opds.TypeNavigation, opds.RelSubsection),
		},
	})
}

// opdsSeriesEntry is a navigation entry pointing at a series' acquisition feed.
func opdsSeriesEntry(sr store.Series) opds.Entry {
	content := strconv.Itoa(sr.IssueCount) + " issue"
	if sr.IssueCount != 1 {
		content += "s"
	}
	if sr.Publisher != "" {
		content += " · " + sr.Publisher
	}
	if sr.Year > 0 {
		content += " · " + strconv.Itoa(sr.Year)
	}
	return opds.Entry{
		ID:      "urn:gutter:series:" + sr.ID,
		Title:   sr.Name,
		Updated: sr.UpdatedAt,
		Authors: []string{sr.Writer},
		Content: content,
		Links: []opds.Link{
			{Rel: opds.RelSubsection, Href: "/opds/series/" + sr.ID, Type: opds.TypeAcquisition},
			{Rel: opds.RelImage, Href: "/library/" + sr.ID + "/cover", Type: opds.TypeJPEG},
			{Rel: opds.RelThumbnail, Href: "/library/" + sr.ID + "/cover", Type: opds.TypeJPEG},
		},
	}
}

// HandleOPDSSeriesList serves every series, paged.
func (s *Server) HandleOPDSSeriesList(w http.ResponseWriter, r *http.Request) {
	all, err := s.Store.ListSeries()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start := min((page-1)*opdsPageSize, len(all))
	end := min(start+opdsPageSize, len(all))

	self := "/opds/series"
	if page > 1 {
		self += "?page=" + strconv.Itoa(page)
	}
	f := opds.Feed{
		ID:           "urn:gutter:series",
		Title:        "All series",
		Updated:      latestSeriesUpdate(all),
		Links:        opdsCommonLinks(self, opds.TypeNavigation),
		TotalResults: len(all),
		ItemsPerPage: opdsPageSize,
		StartIndex:   start + 1,
	}
	f.Links = append(f.Links, opds.Link{Rel: opds.RelUp, Href: "/opds/", Type: opds.TypeNavigation})
	if page > 1 {
		f.Links = append(f.Links, opds.Link{Rel: opds.RelPrevious, Href: "/opds/series?page=" + strconv.Itoa(page-1), Type: opds.TypeNavigation})
	}
	if end < len(all) {
		f.Links = append(f.Links, opds.Link{Rel: opds.RelNext, Href: "/opds/series?page=" + strconv.Itoa(page+1), Type: opds.TypeNavigation})
	}
	for _, sr := range all[start:end] {
		f.Entries = append(f.Entries, opdsSeriesEntry(sr))
	}
	writeOPDS(w, f)
}

func latestSeriesUpdate(list []store.Series) time.Time {
	var t time.Time
	for _, sr := range list {
		if sr.UpdatedAt.After(t) {
			t = sr.UpdatedAt
		}
	}
	return t
}

// HandleOPDSRecent serves the newest series by add date.
func (s *Server) HandleOPDSRecent(w http.ResponseWriter, r *http.Request) {
	all, err := s.Store.ListSeries()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if len(all) > opdsRecentLimit {
		all = all[:opdsRecentLimit]
	}
	f := opds.Feed{
		ID:      "urn:gutter:recent",
		Title:   "Recently added",
		Updated: latestSeriesUpdate(all),
		Links:   append(opdsCommonLinks("/opds/recent", opds.TypeNavigation), opds.Link{Rel: opds.RelUp, Href: "/opds/", Type: opds.TypeNavigation}),
	}
	for _, sr := range all {
		e := opdsSeriesEntry(sr)
		if !sr.CreatedAt.IsZero() {
			e.Updated = sr.CreatedAt
		}
		f.Entries = append(f.Entries, e)
	}
	writeOPDS(w, f)
}

// HandleOPDSSearch matches series by name, writer or publisher.
func (s *Server) HandleOPDSSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	all, err := s.Store.ListSeries()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	matches := opdsFilterSeries(all, q)
	f := opds.Feed{
		ID:      "urn:gutter:search:" + q,
		Title:   "Search: " + q,
		Updated: latestSeriesUpdate(matches),
		Links: append(opdsCommonLinks("/opds/search?q="+url.QueryEscape(q), opds.TypeNavigation),
			opds.Link{Rel: opds.RelUp, Href: "/opds/", Type: opds.TypeNavigation}),
		TotalResults: len(matches),
		ItemsPerPage: max(len(matches), 1),
		StartIndex:   1,
	}
	for _, sr := range matches {
		f.Entries = append(f.Entries, opdsSeriesEntry(sr))
	}
	writeOPDS(w, f)
}

// opdsFilterSeries returns series where every query word appears in the name,
// writer or publisher (case-insensitive). An empty query matches nothing.
func opdsFilterSeries(all []store.Series, q string) []store.Series {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return nil
	}
	var out []store.Series
	for _, sr := range all {
		hay := strings.ToLower(sr.Name + " " + sr.Writer + " " + sr.Publisher)
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, sr)
		}
	}
	return out
}

// HandleOPDSOpenSearch serves the OpenSearch description.
func (s *Server) HandleOPDSOpenSearch(w http.ResponseWriter, r *http.Request) {
	out, err := opds.OpenSearchDescription("Gutter", "Search Gutter series",
		"/opds/search?q="+opds.SearchTermsTemplate, opds.TypeNavigation)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", opds.OpenSearchContentType)
	w.Write(out)
}

// HandleOPDSSeries serves one series' issues as publications.
func (s *Server) HandleOPDSSeries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sr, err := s.Store.GetSeries(id)
	if err != nil || sr == nil {
		writeJSONError(w, http.StatusNotFound, "series not found")
		return
	}
	issues, err := s.Store.ListIssues(id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	ids := make([]string, len(issues))
	for i, iss := range issues {
		ids[i] = iss.ID
	}
	progress, err := s.Store.GetProgressForComics(ids)
	if err != nil {
		progress = map[string]store.Progress{}
	}
	extras, err := s.Store.ListIssueEnrichmentForSeries(id)
	if err != nil {
		extras = map[string]store.IssueEnrichment{}
	}
	f := opds.Feed{
		ID:      "urn:gutter:series:" + id,
		Title:   sr.Name,
		Updated: sr.UpdatedAt,
		Links: append(opdsCommonLinks("/opds/series/"+id, opds.TypeAcquisition),
			opds.Link{Rel: opds.RelUp, Href: "/opds/series", Type: opds.TypeNavigation}),
	}
	for _, iss := range issues {
		var p *store.Progress
		if pr, ok := progress[iss.ID]; ok {
			p = &pr
		}
		var ie *store.IssueEnrichment
		if e, ok := extras[iss.ID]; ok {
			ie = &e
		}
		f.Entries = append(f.Entries, s.opdsIssueEntry(*sr, iss, ie, p))
	}
	writeOPDS(w, f)
}

// HandleOPDSReading serves in-progress issues, most recent first.
func (s *Server) HandleOPDSReading(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListInProgress()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	f := opds.Feed{
		ID:    "urn:gutter:reading",
		Title: "Continue reading",
		Links: append(opdsCommonLinks("/opds/reading", opds.TypeAcquisition),
			opds.Link{Rel: opds.RelUp, Href: "/opds/", Type: opds.TypeNavigation}),
	}
	seriesCache := map[string]*store.Series{}
	issueCache := map[string]map[string]store.Issue{}
	for _, p := range list {
		seriesID, err := s.Store.SeriesIDForComic(p.ComicID)
		if err != nil {
			continue // progress for a comic no longer in the library
		}
		sr, ok := seriesCache[seriesID]
		if !ok {
			sr, _ = s.Store.GetSeries(seriesID)
			seriesCache[seriesID] = sr
			byID := map[string]store.Issue{}
			if issues, err := s.Store.ListIssues(seriesID); err == nil {
				for _, iss := range issues {
					byID[iss.ID] = iss
				}
			}
			issueCache[seriesID] = byID
		}
		iss, ok := issueCache[seriesID][p.ComicID]
		if sr == nil || !ok {
			continue
		}
		if p.UpdatedAt.After(f.Updated) {
			f.Updated = p.UpdatedAt
		}
		pc := p
		f.Entries = append(f.Entries, s.opdsIssueEntry(*sr, iss, nil, &pc))
	}
	writeOPDS(w, f)
}

// opdsIssueTitle is "Series #N", falling back to the file name.
func opdsIssueTitle(sr store.Series, iss store.Issue) string {
	if iss.Number != "" {
		return sr.Name + " #" + iss.Number
	}
	return strings.TrimSuffix(iss.Filename, filepath.Ext(iss.Filename))
}

// opdsIssueEntry builds a publication entry: acquisition, cover, and (when the
// page count is known) an OPDS-PSE stream link.
func (s *Server) opdsIssueEntry(sr store.Series, iss store.Issue, ie *store.IssueEnrichment, p *store.Progress) opds.Entry {
	e := opds.Entry{
		ID:      "urn:gutter:comic:" + iss.ID,
		Title:   opdsIssueTitle(sr, iss),
		Updated: sr.UpdatedAt,
		Authors: []string{opdsFirstNonEmpty(iss.Writer, sr.Writer)},
		Links: []opds.Link{
			{Rel: opds.RelAcquisition, Href: "/opds/download/" + iss.ID, Type: opds.MimeForExt(filepath.Ext(iss.Filename))},
			{Rel: opds.RelImage, Href: "/comics/" + iss.ID + "/cover", Type: opds.TypeJPEG},
			{Rel: opds.RelThumbnail, Href: "/comics/" + iss.ID + "/cover", Type: opds.TypeJPEG},
		},
	}
	if sr.Year > 0 {
		e.Issued = strconv.Itoa(sr.Year)
	}
	if ie != nil {
		if ie.Title != "" {
			e.Title += ": " + ie.Title
		}
		e.Summary = comicvine.StripHTML(ie.Summary)
		if ie.CoverDate != "" {
			e.Issued = ie.CoverDate
		}
	}
	var lastRead *int
	var lastReadDate time.Time
	if p != nil {
		if p.UpdatedAt.After(e.Updated) {
			e.Updated = p.UpdatedAt
		}
		lastReadDate = p.UpdatedAt
	}
	if count := s.opdsPageCount(iss.ID, iss.Filename, p); count > 0 {
		if p != nil {
			lr := min(max(p.CurrentPage, 0), count-1)
			lastRead = &lr
		}
		e.Links = append(e.Links, opds.StreamLink(
			"/comics/"+iss.ID+"/pages/"+opds.PageNumberTemplate, count, lastRead, lastReadDate))
	}
	return e
}

func opdsFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func opdsPageCountKey(id string) string { return "opds:pages:" + id }

// opdsPageCount returns an issue's page count without slow work: a cached
// count, then the reader's own total from reading progress, then (CBZ only)
// opening the zip's central directory. CBR counts aren't computed here
// because opening a CBR may extract the whole archive; those issues get a
// PSE link once they've been opened (progress or cache), and are always
// downloadable.
func (s *Server) opdsPageCount(id, filename string, p *store.Progress) int {
	if s.Cache != nil {
		if v, ok := s.Cache.Get(opdsPageCountKey(id)); ok {
			if n, ok := v.(int); ok {
				return n
			}
		}
	}
	if p != nil && p.TotalPages > 0 {
		return p.TotalPages
	}
	if !strings.EqualFold(filepath.Ext(filename), ".cbz") {
		return 0
	}
	abs, err := s.resolveComicPath(id)
	if err != nil {
		return 0
	}
	zr, err := archive.OpenCBZ(abs)
	if err != nil {
		return 0
	}
	n := zr.PageCount()
	zr.Close()
	if s.Cache != nil && n > 0 {
		s.Cache.Set(opdsPageCountKey(id), n)
	}
	return n
}

// HandleOPDSDownload serves the raw archive and, on the way, records the
// file's KOReader digests so kosync can match it without a library scan.
func (s *Server) HandleOPDSDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if abs, err := s.resolveComicPath(id); err == nil {
		if doc, err := kosyncDocumentFor(id, abs); err == nil {
			if err := s.Store.UpsertKosyncDocument(doc); err != nil {
				log.Printf("opds: recording kosync digest for %s: %v", id, err)
			}
		}
	}
	s.HandleDownloadComic(w, r)
}
