package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

const recapRecentLimit = 3

// RecapResponse is the JSON payload for GET /library/{id}/recap.
type RecapResponse struct {
	Series       RecapSeries  `json:"series"`
	RecentIssues []RecapIssue `json:"recent_issues"`
	History      RecapHistory `json:"history"`
}

// RecapSeries carries the minimal series metadata needed by the UI.
type RecapSeries struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RecapIssue describes one recently-finished issue.
type RecapIssue struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Number     string    `json:"number"`
	CoverURL   string    `json:"cover_url"`
	Summary    string    `json:"summary"`
	FinishedAt time.Time `json:"finished_at"`
}

// RecapHistory holds aggregate reading statistics for the series.
type RecapHistory struct {
	LastOpenedAt     *time.Time `json:"last_opened_at"`
	IssuesFinished   int        `json:"issues_finished"`
	IssuesTotal      int        `json:"issues_total"`
	TotalMinutesRead int        `json:"total_minutes_read"`
}

// HandleLibraryRecap handles GET /library/{id}/recap.
// It returns a "previously on" summary: the most-recently finished issues
// (capped to recapRecentLimit), per-issue ComicInfo summaries, and aggregate
// reading stats for the whole series.
func (s *Server) HandleLibraryRecap(w http.ResponseWriter, r *http.Request) {
	seriesID := r.PathValue("id")
	if seriesID == "" {
		writeJSONError(w, http.StatusBadRequest, "missing series id")
		return
	}

	series, err := s.Store.GetSeries(seriesID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "series not found")
			return
		}
		writeInternalError(w, err)
		return
	}

	// Recent finished readings, capped to recapRecentLimit.
	readings, err := s.Store.RecentFinishedReadings(seriesID, recapRecentLimit)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	dbIssues, err := s.Store.ListIssues(seriesID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	issueByID := make(map[string]store.Issue, len(dbIssues))
	for _, iss := range dbIssues {
		issueByID[iss.ID] = iss
	}

	recent := make([]RecapIssue, 0, len(readings))
	for _, rd := range readings {
		iss, ok := issueByID[rd.ComicID]
		if !ok {
			continue
		}
		summary := ""
		info, err := s.readComicInfo(rd.ComicID)
		if err != nil {
			log.Printf("recap: readComicInfo(%s) failed: %v — emitting empty summary", rd.ComicID, err)
		} else if info != nil {
			summary = info.Summary
		}
		recent = append(recent, RecapIssue{
			ID:         rd.ComicID,
			Name:       trimComicExt(iss.Filename),
			Number:     iss.Number,
			CoverURL:   "/comics/" + rd.ComicID + "/cover",
			Summary:    summary,
			FinishedAt: rd.UpdatedAt,
		})
	}

	// History facts: use a wide query for finished count and last-opened time.
	history := RecapHistory{
		IssuesTotal: len(dbIssues),
	}
	allFinished, err := s.Store.RecentFinishedReadings(seriesID, 1_000_000)
	if err == nil {
		history.IssuesFinished = len(allFinished)
		if len(allFinished) > 0 {
			t := allFinished[0].UpdatedAt
			history.LastOpenedAt = &t
		}
	}
	totalDuration, err := s.Store.SeriesTotalDurationS(seriesID)
	if err == nil {
		history.TotalMinutesRead = totalDuration / 60
	}

	resp := RecapResponse{
		Series:       RecapSeries{ID: series.ID, Name: series.Name},
		RecentIssues: recent,
		History:      history,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("recap: encode failed: %v", err)
	}
}

// trimComicExt strips the trailing file extension (.cbz, .cbr, .pdf, etc.)
// from a filename for display purposes.
func trimComicExt(filename string) string {
	idx := strings.LastIndex(filename, ".")
	if idx < 0 {
		return filename
	}
	return filename[:idx]
}
