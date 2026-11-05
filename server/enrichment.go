package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ryanhaines/comic-reader/enrichment"
	"github.com/ryanhaines/comic-reader/store"
)

type EnrichmentStatusResponse struct {
	Worker          string         `json:"worker"` // "running" | "no-key"
	Counts          map[string]int `json:"counts"`
	BudgetRemaining int            `json:"budget_remaining"`
}

type PendingMatchResponse struct {
	SeriesID   string                 `json:"series_id"`
	Name       string                 `json:"name"`
	IssueCount int                    `json:"issue_count"`
	CoverURL   string                 `json:"cover_url"`
	Candidates []enrichment.Candidate `json:"candidates"`
}

func (s *Server) HandleEnrichmentStatus(w http.ResponseWriter, r *http.Request) {
	counts, err := s.Store.CountEnrichmentByStatus()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	resp := EnrichmentStatusResponse{Worker: "no-key", Counts: counts}
	if s.Enrichment != nil {
		resp.Worker = "running"
		resp.BudgetRemaining = s.Enrichment.Client.Available()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) HandleEnrichmentPending(w http.ResponseWriter, r *http.Request) {
	pending, err := s.Store.ListPendingReview()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	out := make([]PendingMatchResponse, 0, len(pending))
	for _, p := range pending {
		series, err := s.Store.GetSeries(p.SeriesID)
		if err != nil {
			continue // series deleted since; skip
		}
		var cands []enrichment.Candidate
		if err := json.Unmarshal([]byte(p.Candidates), &cands); err != nil {
			cands = []enrichment.Candidate{}
		}
		coverURL := ""
		if series.CoverPath != "" {
			coverURL = "/library/" + series.ID + "/cover"
		}
		out = append(out, PendingMatchResponse{
			SeriesID:   series.ID,
			Name:       series.Name,
			IssueCount: series.IssueCount,
			CoverURL:   coverURL,
			Candidates: cands,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// HandleEnrichmentMatch resolves a review: volume_id 0 = "none of these"
// (skip), any other value re-queues the series with that volume chosen.
// The body also accepts manually-entered volume IDs not in the candidate
// list (power-user escape hatch).
func (s *Server) HandleEnrichmentMatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Store.GetEnrichmentState(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "no enrichment state for series")
			return
		}
		writeInternalError(w, err)
		return
	}
	var body struct {
		VolumeID int `json:"volume_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.VolumeID < 0 {
		writeJSONError(w, http.StatusBadRequest, "volume_id must be >= 0")
		return
	}
	// Note: the two writes below (DeleteSeriesEnrichment + SetEnrichmentStatus)
	// are not atomic, but the partial state is self-healing — a series with
	// deleted enrichment but no status update will simply reappear in review.
	if body.VolumeID == 0 {
		if err := s.Store.DeleteSeriesEnrichment(id); err != nil {
			writeInternalError(w, err)
			return
		}
		if err := s.Store.SetEnrichmentStatus(id, "skipped"); err != nil {
			writeInternalError(w, err)
			return
		}
	} else {
		if err := s.Store.SetEnrichmentChosen(id, body.VolumeID); err != nil {
			writeInternalError(w, err)
			return
		}
		if s.Enrichment != nil {
			s.Enrichment.Kick()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// HandleEnrichmentRequeue clears applied enrichment and re-matches from
// scratch (recovery from a bad auto match). Note: a concurrent worker apply
// can win the race between DeleteSeriesEnrichment and RequeueEnrichment; the
// user can simply requeue again if that happens.
func (s *Server) HandleEnrichmentRequeue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Store.GetSeries(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "series not found")
			return
		}
		writeInternalError(w, err)
		return
	}
	if err := s.Store.DeleteSeriesEnrichment(id); err != nil {
		writeInternalError(w, err)
		return
	}
	if err := s.Store.RequeueEnrichment(id); err != nil {
		writeInternalError(w, err)
		return
	}
	if s.Enrichment != nil {
		s.Enrichment.Kick()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
