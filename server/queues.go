package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

type QueueResponse struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Icon      string   `json:"icon"`
	Color     string   `json:"color"`
	Position  int      `json:"position"`
	SeriesIDs []string `json:"series_ids"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type queuePutRequest struct {
	Name      string   `json:"name"`
	Icon      string   `json:"icon"`
	Color     string   `json:"color"`
	Position  int      `json:"position"`
	SeriesIDs []string `json:"series_ids"`
}

// buildQueueResponses attaches ordered series IDs to each queue.
func (s *Server) buildQueueResponses(queues []store.Queue) ([]QueueResponse, error) {
	out := make([]QueueResponse, 0, len(queues))
	for _, q := range queues {
		ids, err := s.Store.ListQueueSeriesIDs(q.ID)
		if err != nil {
			return nil, err
		}
		if ids == nil {
			ids = []string{}
		}
		out = append(out, QueueResponse{
			ID:        q.ID,
			Name:      q.Name,
			Icon:      q.Icon,
			Color:     q.Color,
			Position:  q.Position,
			SeriesIDs: ids,
			CreatedAt: q.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: q.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

func (s *Server) HandleListQueues(w http.ResponseWriter, r *http.Request) {
	queues, err := s.Store.ListQueues()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	resp, err := s.buildQueueResponses(queues)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) HandlePutQueue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing queue id")
		return
	}
	var body queuePutRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	q := store.Queue{ID: id, Name: body.Name, Icon: body.Icon, Color: body.Color, Position: body.Position}
	if err := s.Store.UpsertQueue(q, body.SeriesIDs); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) HandleDeleteQueue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing queue id")
		return
	}
	if err := s.Store.DeleteQueue(id); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
