package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type SeriesDetailData struct {
	ID         string
	Name       string
	FolderPath string
	IssueCount int
	ReadCount  int
	CoverURL   string
	Writer     string
	Publisher  string
	Genres     []string
	Year       int
	Progress   float64
	Issues     []IssueItem
	IssueView  string // "covers" or "list"
}

type IssueItem struct {
	ID          string
	Name        string
	Number      string
	CoverURL    string
	Status      string // "unread", "in_progress", "completed"
	CurrentPage int
	TotalPages  int
	Progress    float64
	Index       int
}

func (a *Admin) handleSeries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	series, err := a.Store.GetSeries(id)
	if err != nil {
		http.Error(w, "series not found", http.StatusNotFound)
		return
	}

	data := a.baseData("library")
	data.Title = series.Name

	issueView := r.URL.Query().Get("issues")
	if issueView != "list" {
		issueView = "covers"
	}

	genres, _ := a.Store.ListSeriesGenres(id)
	if genres == nil {
		genres = []string{}
	}

	coverURL := ""
	if series.CoverPath != "" {
		coverURL = "/library/" + series.ID + "/cover"
	}

	// Fetch issue list from the existing API endpoint
	apiURL := fmt.Sprintf("http://localhost:%d/library/%s", a.Port, id)
	var issues []IssueItem
	readCount := 0

	resp, err := http.Get(apiURL)
	if err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()
		var detail struct {
			Issues []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Number      string `json:"number"`
				CoverURL    string `json:"cover_url"`
				Status      string `json:"status"`
				CurrentPage *int   `json:"current_page"`
				TotalPages  *int   `json:"total_pages"`
			} `json:"issues"`
			ReadCount int `json:"read_count"`
		}
		if json.NewDecoder(resp.Body).Decode(&detail) == nil {
			readCount = detail.ReadCount
			for i, iss := range detail.Issues {
				cp, tp := 0, 0
				if iss.CurrentPage != nil {
					cp = *iss.CurrentPage
				}
				if iss.TotalPages != nil {
					tp = *iss.TotalPages
				}
				pct := float64(0)
				if tp > 0 {
					pct = float64(cp) / float64(tp) * 100
				}

				name := iss.Name
				for _, ext := range []string{".cbz", ".cbr", ".CBZ", ".CBR"} {
					name = strings.TrimSuffix(name, ext)
				}

				issues = append(issues, IssueItem{
					ID:          iss.ID,
					Name:        name,
					Number:      iss.Number,
					CoverURL:    iss.CoverURL,
					Status:      iss.Status,
					CurrentPage: cp,
					TotalPages:  tp,
					Progress:    pct,
					Index:       i,
				})
			}
		}
	}
	if resp != nil && resp.Body != nil {
		// Body already deferred above when status was 200
		// For non-200 responses, close body
		if resp.StatusCode != 200 {
			resp.Body.Close()
		}
	}

	progress := float64(0)
	if series.IssueCount > 0 {
		progress = float64(readCount) / float64(series.IssueCount) * 100
	}

	data.Content = SeriesDetailData{
		ID:         series.ID,
		Name:       series.Name,
		FolderPath: series.FolderPath,
		IssueCount: series.IssueCount,
		ReadCount:  readCount,
		CoverURL:   coverURL,
		Writer:     series.Writer,
		Publisher:  series.Publisher,
		Genres:     genres,
		Year:       series.Year,
		Progress:   progress,
		Issues:     issues,
		IssueView:  issueView,
	}

	a.render(w, "series.html", data)
}
