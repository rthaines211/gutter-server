package match

import "github.com/ryanhaines/comic-reader/store"

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// firstNonEmptyList treats "" and "[]" as empty (JSON arrays).
func firstNonEmptyList(a, b string) string {
	if a != "" && a != "[]" {
		return a
	}
	return b
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func firstNonZeroF(a, b float64) float64 {
	if a != 0 {
		return a
	}
	return b
}

// mergeSeriesFields combines a primary and a secondary series row per the field
// policy: shared fields prefer ComicVine (fall back to GCD), genre prefers GCD.
// primaryIsCV says which of the two rows came from ComicVine. Provenance
// (gcd_series_id, secondary_source) records the supplement. Blank secondary
// values never clobber a populated primary one.
func mergeSeriesFields(primary, secondary store.SeriesEnrichment, primaryIsCV bool) store.SeriesEnrichment {
	cv, gcd := primary, secondary
	secSource := "gcd"
	if !primaryIsCV {
		cv, gcd = secondary, primary
		secSource = "comicvine"
	}
	out := primary // keep primary's id/status/enriched_at as the base
	out.Description = firstNonEmpty(cv.Description, gcd.Description)
	out.Publisher = firstNonEmpty(cv.Publisher, gcd.Publisher)
	out.StartYear = firstNonZero(cv.StartYear, gcd.StartYear)
	out.Creators = firstNonEmptyList(cv.Creators, gcd.Creators)
	out.Genre = firstNonEmpty(gcd.Genre, cv.Genre)
	if gcd.GCDSeriesID != 0 {
		out.GCDSeriesID = gcd.GCDSeriesID
	}
	out.SecondarySource = secSource
	return out
}

// mergeIssueFields combines a primary and secondary issue row: title/summary/
// cover_date/characters prefer ComicVine, page_count is GCD-only. Keeps the
// primary row's issue id + ComicVine issue id.
func mergeIssueFields(primary, secondary store.IssueEnrichment, primaryIsCV bool) store.IssueEnrichment {
	cv, gcd := primary, secondary
	if !primaryIsCV {
		cv, gcd = secondary, primary
	}
	out := primary
	out.Title = firstNonEmpty(cv.Title, gcd.Title)
	out.Summary = firstNonEmpty(cv.Summary, gcd.Summary)
	out.CoverDate = firstNonEmpty(cv.CoverDate, gcd.CoverDate)
	out.Characters = firstNonEmptyList(cv.Characters, gcd.Characters)
	out.PageCount = firstNonZeroF(gcd.PageCount, cv.PageCount)
	return out
}
