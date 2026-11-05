package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

type FacetItem struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

var validFacets = map[string]bool{
	"publisher": true,
	"writer":    true,
	"penciller": true,
	"genre":     true,
	"year":      true,
}

func (s *Server) HandleListFacets(w http.ResponseWriter, r *http.Request) {
	facet := r.PathValue("facet")
	if !validFacets[facet] {
		writeJSONError(w, http.StatusBadRequest, "invalid facet: must be one of publisher, writer, penciller, genre, year")
		return
	}

	allSeries, err := s.Store.ListSeries()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	q := strings.ToLower(r.URL.Query().Get("q"))
	statusFilter := r.URL.Query().Get("status")
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
	counts := make(map[string]int)

	for _, series := range allSeries {
		if e, ok := extras[series.ID]; ok {
			series = mergeSeries(series, e)
		}

		if q != "" && !strings.Contains(strings.ToLower(series.Name), q) {
			continue
		}

		if statusFilter != "" && statusFilter != "all" {
			readCount := s.countReadIssues(series)
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

		// Tally the requested facet.
		// Writer and penciller are split on commas like genres,
		// since ComicInfo.xml often lists multiple creators.
		switch facet {
		case "publisher":
			if series.Publisher != "" {
				counts[series.Publisher]++
			}
		case "writer":
			for _, w := range splitCreators(series.Writer) {
				counts[w]++
			}
		case "penciller":
			for _, p := range splitCreators(series.Penciller) {
				counts[p]++
			}
		case "genre":
			for _, g := range genres {
				if g != "" {
					counts[g]++
				}
			}
		case "year":
			if series.Year != 0 {
				counts[fmt.Sprintf("%d", series.Year)]++
			}
		}
	}

	result := make([]FacetItem, 0, len(counts))
	for value, count := range counts {
		result = append(result, FacetItem{Value: value, Count: count})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Value < result[j].Value
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// creatorMatches checks if a specific creator name appears in a
// comma-separated creator string. "Tom King" matches "Tom King, Scott Snyder".
func creatorMatches(raw, name string) bool {
	for _, c := range splitCreators(raw) {
		if c == name {
			return true
		}
	}
	return false
}

// splitCreators splits a comma-separated creator string into trimmed,
// non-empty individual names. "Tom King, Scott Snyder" → ["Tom King", "Scott Snyder"]
// stripComicVineID removes trailing bracketed IDs from creator names.
// "Mark Waid [40724]" → "Mark Waid"
var comicVineIDSuffix = regexp.MustCompile(`\s*\[\d+\]\s*$`)

func stripComicVineID(name string) string {
	return comicVineIDSuffix.ReplaceAllString(name, "")
}

func splitCreators(raw string) []string {
	if raw == "" {
		return nil
	}
	var result []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		s = stripComicVineID(s)
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}
