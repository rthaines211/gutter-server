package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/ryanhaines/comic-reader/comicvine"
	"github.com/ryanhaines/comic-reader/store"
)

// SyncSeriesResponse is a library series with its issues inline, as returned
// by GET /sync. It intentionally has no read_count: the client computes read
// counts from its own mirrored progress.
type SyncSeriesResponse struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	FolderPath       string          `json:"folder_path"`
	IssueCount       int             `json:"issue_count"`
	CoverURL         string          `json:"cover_url"`
	Writer           string          `json:"writer"`
	Penciller        string          `json:"penciller"`
	Publisher        string          `json:"publisher"`
	Genres           []string        `json:"genres"`
	Year             int             `json:"year"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	Description      string          `json:"description,omitempty"`
	StartYear        int             `json:"start_year,omitempty"`
	Creators         []store.Creator `json:"creators,omitempty"`
	EnrichmentStatus string          `json:"enrichment_status,omitempty"`
	Issues           []IssueResponse `json:"issues"`
}

type SyncResponse struct {
	ServerTime  int64                     `json:"server_time"`
	Series      []SyncSeriesResponse      `json:"series"`
	Progress    []ReadingProgressResponse `json:"progress"`
	SeriesIDs   []string                  `json:"series_ids"`
	ProgressIDs []string                  `json:"progress_ids"`
	Queues      []QueueResponse           `json:"queues"`
	QueueIDs    []string                  `json:"queue_ids"`
	Arcs        []ArcResponse             `json:"arcs"`
	ArcIDs      []string                  `json:"arc_ids"`
}

// ArcResponse is a story arc with its member issue ids, as returned by
// GET /sync.
type ArcResponse struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ImageURL      string   `json:"image_url"`
	SelfContained *bool    `json:"self_contained"`
	Total         int      `json:"total"`
	IssueIDs      []string `json:"issue_ids"`
	Source        string   `json:"source"`
	Publisher     string   `json:"publisher"`
	StartYear     int      `json:"start_year"`
	EndYear       int      `json:"end_year"`
	Description   string   `json:"description"`
}

// HandleSync returns all series/progress changed since the given watermark
// (unix milliseconds), plus full ID lists so the client can prune deletions.
func (s *Server) HandleSync(w http.ResponseWriter, r *http.Request) {
	sinceMs, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if err != nil || sinceMs < 0 {
		sinceMs = 0
	}
	since := sinceMs / 1000
	if since > 0 {
		// 1s overlap: updated_at has second resolution, so a write in the same
		// second as the previous server_time would otherwise be missed. Client
		// upserts are idempotent, so re-sending is harmless.
		since--
	}
	serverTime := time.Now().UnixMilli()

	changed, err := s.Store.ListSeriesUpdatedSince(since)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	extras := s.enrichmentExtras()
	seriesIDsForGenres := make([]string, len(changed))
	for i := range changed {
		seriesIDsForGenres[i] = changed[i].ID
	}
	genresBySeries, err := s.Store.ListGenresForSeries(seriesIDsForGenres)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	seriesOut := make([]SyncSeriesResponse, 0, len(changed))
	for i := range changed {
		series := &changed[i]
		genres := genresBySeries[series.ID]
		if genres == nil {
			genres = []string{}
		}
		coverURL := ""
		if series.CoverPath != "" {
			coverURL = "/library/" + series.ID + "/cover"
		}
		resp := SyncSeriesResponse{
			ID:         series.ID,
			Name:       series.Name,
			FolderPath: series.FolderPath,
			IssueCount: series.IssueCount,
			CoverURL:   coverURL,
			Writer:     series.Writer,
			Penciller:  series.Penciller,
			Publisher:  series.Publisher,
			Genres:     genres,
			Year:       series.Year,
			CreatedAt:  series.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:  series.UpdatedAt.UTC().Format(time.RFC3339),
			Issues:     s.buildIssueList(series),
		}
		if e, ok := extras[series.ID]; ok {
			resp.Description = comicvine.StripHTML(e.Description)
			resp.StartYear = e.StartYear
			resp.Creators = e.Creators
			resp.EnrichmentStatus = e.Status
			if e.Publisher != "" {
				resp.Publisher = e.Publisher
			}
			if e.Writer != "" {
				resp.Writer = e.Writer
			}
			if e.Penciller != "" {
				resp.Penciller = e.Penciller
			}
			if e.StartYear > 0 {
				resp.Year = e.StartYear
			}
		}
		seriesOut = append(seriesOut, resp)
	}

	progress, err := s.Store.ListProgressUpdatedSince(since)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	progressComicIDs := make([]string, len(progress))
	for i, p := range progress {
		progressComicIDs[i] = p.ComicID
	}
	chaptersByComic, err := s.Store.ListChaptersForComics(progressComicIDs)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	progressOut := make([]ReadingProgressResponse, 0, len(progress))
	for _, p := range progress {
		chapters := chaptersByComic[p.ComicID]
		progressOut = append(progressOut, ReadingProgressResponse{
			ComicID:     p.ComicID,
			CurrentPage: p.CurrentPage,
			TotalPages:  p.TotalPages,
			UpdatedAt:   p.UpdatedAt.UTC().Format(time.RFC3339),
			Chapters:    chapters,
		})
	}

	seriesIDs, err := s.Store.ListSeriesIDs()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	progressIDs, err := s.Store.ListProgressComicIDs()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	changedQueues, err := s.Store.ListQueuesUpdatedSince(since)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	queuesOut, err := s.buildQueueResponses(changedQueues)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	queueIDs, err := s.Store.ListQueueIDs()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	changedArcs, err := s.Store.ListArcsUpdatedSince(since)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	arcIDsForIssues := make([]string, len(changedArcs))
	for i, a := range changedArcs {
		arcIDsForIssues[i] = a.ID
	}
	issueIDsByArc, err := s.Store.ListIssueIDsForArcs(arcIDsForIssues)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	arcsOut := make([]ArcResponse, 0, len(changedArcs))
	for _, a := range changedArcs {
		issueIDs := issueIDsByArc[a.ID]
		if issueIDs == nil {
			issueIDs = []string{}
		}
		arcsOut = append(arcsOut, ArcResponse{
			ID: a.ID, Name: a.Name, ImageURL: a.ImageURL,
			SelfContained: a.SelfContained, Total: a.TotalIssues, IssueIDs: issueIDs,
			Source: a.Source, Publisher: a.Publisher, StartYear: a.StartYear, EndYear: a.EndYear,
			Description: a.Description,
		})
	}
	arcIDs, err := s.Store.ListArcIDs()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if arcIDs == nil {
		arcIDs = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(SyncResponse{
		ServerTime:  serverTime,
		Series:      seriesOut,
		Progress:    progressOut,
		SeriesIDs:   seriesIDs,
		ProgressIDs: progressIDs,
		Queues:      queuesOut,
		QueueIDs:    queueIDs,
		Arcs:        arcsOut,
		ArcIDs:      arcIDs,
	})
}
