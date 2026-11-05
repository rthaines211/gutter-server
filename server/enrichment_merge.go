package server

import (
	"encoding/json"
	"strings"

	"github.com/ryanhaines/comic-reader/metadata"
	"github.com/ryanhaines/comic-reader/store"
)

// seriesExtras carries enrichment-derived response fields for one series.
type seriesExtras struct {
	Description string
	StartYear   int
	Creators    []store.Creator
	Status      string // "auto" | "confirmed"
	Publisher   string // override when non-empty
	Writer      string // derived from creators (role contains "writer")
	Penciller   string // derived (role contains "pencil" or "artist")
}

// enrichmentExtras loads all series enrichment as response-ready extras.
// Returns an empty map on error — enrichment must never break listings.
func (s *Server) enrichmentExtras() map[string]seriesExtras {
	m, err := s.Store.ListSeriesEnrichmentMap()
	if err != nil {
		return map[string]seriesExtras{}
	}
	out := make(map[string]seriesExtras, len(m))
	for id, e := range m {
		var creators []store.Creator
		if err := json.Unmarshal([]byte(e.Creators), &creators); err != nil {
			creators = nil
		}
		out[id] = seriesExtras{
			Description: e.Description,
			StartYear:   e.StartYear,
			Creators:    creators,
			Status:      e.Status,
			Publisher:   metadata.CanonicalPublisher(e.Publisher),
			Writer:      creatorsByRole(creators, "writer"),
			Penciller:   creatorsByRole(creators, "pencil", "artist"),
		}
	}
	return out
}

// creatorsByRole joins names whose comma-separated role list contains any
// of the given substrings ("pencil" matches both "penciler" and
// "penciller" spellings).
func creatorsByRole(creators []store.Creator, roleSubstrings ...string) string {
	var names []string
	for _, c := range creators {
		role := strings.ToLower(c.Role)
		for _, sub := range roleSubstrings {
			if strings.Contains(role, sub) {
				names = append(names, c.Name)
				break
			}
		}
	}
	return strings.Join(names, ", ")
}

// mergeSeries overlays enriched values onto a scanned series row, used by
// the facet computation so filters/counts reflect enriched data.
func mergeSeries(series store.Series, e seriesExtras) store.Series {
	if e.Publisher != "" {
		series.Publisher = e.Publisher
	}
	if e.Writer != "" {
		series.Writer = e.Writer
	}
	if e.Penciller != "" {
		series.Penciller = e.Penciller
	}
	if e.StartYear > 0 {
		series.Year = e.StartYear
	}
	return series
}
