package admin

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/ryanhaines/comic-reader/match"
	"github.com/ryanhaines/comic-reader/store"
)

// ReviewItem is one series awaiting a human match decision, with the matcher's
// best guess (all guess_* fields empty/0 when there is none).
type ReviewItem struct {
	SeriesID        string `json:"series_id"`
	Name            string `json:"name"`
	State           string `json:"state"`
	LocalCoverURL   string `json:"local_cover_url"`
	GuessVolumeID   string `json:"guess_volume_id"`
	GuessSource     string `json:"guess_source"`
	GuessName       string `json:"guess_name"`
	GuessPublisher  string `json:"guess_publisher"`
	GuessYear       string `json:"guess_year"`
	GuessIssueCount int    `json:"guess_issue_count"`
	GuessCoverURL   string `json:"guess_cover_url"`
}

// VolumeHit is one volume search result for pinning.
type VolumeHit struct {
	Source     string `json:"source"`
	VolumeID   string `json:"volume_id"`
	Name       string `json:"name"`
	Year       string `json:"year"`
	Publisher  string `json:"publisher"`
	IssueCount int    `json:"issue_count"`
	CoverURL   string `json:"cover_url"`
}

// RegisterAPI mounts the iPad review deck's JSON routes. Public like the rest
// of the app API: deliberately not behind requireAuth.
func (a *Admin) RegisterAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /review", a.apiReviewList)
	mux.HandleFunc("GET /review/search", a.apiReviewSearch)
	mux.HandleFunc("POST /review/{id}/confirm", a.apiReviewConfirm)
	mux.HandleFunc("POST /review/{id}/volume", a.apiReviewVolume)
	mux.HandleFunc("POST /review/{id}/dont-match", a.apiReviewDontMatch)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func apiJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// apiFail maps an action error to its status; unexpected errors are logged and
// returned generically.
func apiFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "series not found")
	case errors.Is(err, ErrAlreadyConfirmed):
		apiError(w, http.StatusConflict, "already confirmed")
	case errors.Is(err, ErrNoVolume), errors.Is(err, ErrBadSource):
		apiError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("review api: %v", err)
		apiError(w, http.StatusInternalServerError, "internal error")
	}
}

// matchingOn writes the 412 and returns false when matching is disabled.
func (a *Admin) matchingOn(w http.ResponseWriter) bool {
	if a.Match == nil {
		apiError(w, http.StatusPreconditionFailed, "matching is disabled: set localcv_db_path")
		return false
	}
	return true
}

func (a *Admin) apiReviewList(w http.ResponseWriter, r *http.Request) {
	if !a.matchingOn(w) {
		return
	}
	queue, err := a.Store.ListReviewQueue(store.ReviewStates...)
	if err != nil {
		apiFail(w, err)
		return
	}
	// One pass per state: the first matched issue of each series is the cheap
	// way to a guess (volume name, publisher, cover) without a dump scan.
	firsts := map[string]store.FirstMatchedIssue{}
	for _, st := range store.ReviewStates {
		m, err := a.Store.ListFirstMatchedIssuesByState(st)
		if err != nil {
			apiFail(w, err)
			return
		}
		for k, v := range m {
			firsts[k] = v
		}
	}
	items := make([]ReviewItem, 0, len(queue))
	for _, m := range queue {
		it := ReviewItem{SeriesID: m.SeriesID, State: m.State, LocalCoverURL: "/library/" + m.SeriesID + "/cover"}
		if s, err := a.Store.GetSeries(m.SeriesID); err == nil {
			it.Name = s.Name
		}
		if m.State != store.MatchStateCollection {
			a.fillGuess(&it, m, firsts[m.SeriesID])
		}
		items = append(items, it)
	}
	apiJSON(w, map[string]any{"count": len(items), "items": items})
}

// fillGuess resolves the series' chosen volume (or, absent one, its first
// matched issue's volume) into display fields. Lookup failures leave the guess
// empty rather than failing the whole list.
func (a *Admin) fillGuess(it *ReviewItem, m store.SeriesMatch, fm store.FirstMatchedIssue) {
	if fm.UpstreamID != "" {
		if iss, err := a.Match.LookupIssue(fm.Source, fm.UpstreamID); err == nil {
			vid := strconv.FormatInt(iss.VolumeID, 10)
			if m.VolumeID == "" || (m.VolumeID == vid && m.Source == fm.Source) {
				it.GuessVolumeID, it.GuessSource = vid, fm.Source
				it.GuessName, it.GuessPublisher = iss.VolumeName, iss.Publisher
				it.GuessYear, it.GuessIssueCount = iss.VolumeStartYear, iss.VolumeIssueCount
				it.GuessCoverURL = match.MediumCover(iss.ImageURL)
				return
			}
		}
	}
	if m.VolumeID == "" {
		return
	}
	v, err := a.Match.LookupVolume(m.Source, m.VolumeID)
	if err != nil {
		return
	}
	it.GuessVolumeID, it.GuessSource = m.VolumeID, m.Source
	it.GuessName, it.GuessPublisher, it.GuessYear = v.Name, v.Publisher, v.StartYear
	it.GuessIssueCount = v.IssueCount
	it.GuessCoverURL = a.Match.VolumeCoverURL(m.Source, m.VolumeID)
}

func (a *Admin) apiReviewSearch(w http.ResponseWriter, r *http.Request) {
	if !a.matchingOn(w) {
		return
	}
	hits := []VolumeHit{}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		found, err := a.Match.SearchVolumes(q, 25)
		if err != nil {
			apiFail(w, err)
			return
		}
		for _, h := range found {
			vid := strconv.FormatInt(h.VolumeID, 10)
			hits = append(hits, VolumeHit{
				Source: h.Source, VolumeID: vid, Name: h.Name, Year: h.StartYear,
				Publisher: h.Publisher, IssueCount: h.IssueCount,
				CoverURL: a.Match.VolumeCoverURL(h.Source, vid),
			})
		}
	}
	apiJSON(w, hits)
}

func (a *Admin) apiReviewConfirm(w http.ResponseWriter, r *http.Request) {
	if !a.matchingOn(w) {
		return
	}
	if err := a.confirm(r.PathValue("id")); err != nil {
		apiFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) apiReviewVolume(w http.ResponseWriter, r *http.Request) {
	if !a.matchingOn(w) {
		return
	}
	var body struct {
		Source   string `json:"source"`
		VolumeID string `json:"volume_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.VolumeID == "" {
		apiError(w, http.StatusBadRequest, "source and volume_id required")
		return
	}
	if body.Source == "" {
		body.Source = store.SourceComicVine
	}
	id := r.PathValue("id")
	if err := a.setVolume(r.Context(), id, body.Source, body.VolumeID); err != nil {
		apiFail(w, err)
		return
	}
	if err := a.confirm(id); err != nil {
		apiFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) apiReviewDontMatch(w http.ResponseWriter, r *http.Request) {
	if !a.matchingOn(w) {
		return
	}
	if err := a.Store.SetDontMatch(r.PathValue("id"), true); err != nil {
		apiFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
