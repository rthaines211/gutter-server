package server

import (
	"encoding/json"
	"net/http"
	"strconv"
)

type CreateCollectionRequest struct {
	Name  string `json:"name"`
	Icon  string `json:"icon"`
	Color string `json:"color"`
}

type UpdateCollectionRequest struct {
	Name  string `json:"name"`
	Icon  string `json:"icon"`
	Color string `json:"color"`
}

type AddSeriesRequest struct {
	SeriesID string `json:"series_id"`
}

type ReorderSeriesRequest struct {
	SeriesIDs []string `json:"series_ids"`
}

type CollectionResponse struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Icon      string   `json:"icon"`
	Color     string   `json:"color"`
	Position  int      `json:"position"`
	SeriesIDs []string `json:"series_ids"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

func (s *Server) HandleListCollections(w http.ResponseWriter, r *http.Request) {
	collections, err := s.Store.ListCollections()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	result := make([]CollectionResponse, 0, len(collections))
	for _, c := range collections {
		ids, _ := s.Store.ListCollectionSeriesIDs(c.ID)
		if ids == nil {
			ids = []string{}
		}
		result = append(result, CollectionResponse{
			ID:        c.ID,
			Name:      c.Name,
			Icon:      c.Icon,
			Color:     c.Color,
			Position:  c.Position,
			SeriesIDs: ids,
			CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: c.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) HandleCreateCollection(w http.ResponseWriter, r *http.Request) {
	var req CreateCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Icon == "" {
		req.Icon = "bookmark"
	}
	if req.Color == "" {
		req.Color = "#FFB74D"
	}
	id, err := s.Store.CreateCollection(req.Name, req.Icon, req.Color)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]int64{"id": id})
}

func (s *Server) HandleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req UpdateCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.Store.UpdateCollection(id, req.Name, req.Icon, req.Color); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) HandleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.Store.DeleteCollection(id); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) HandleAddSeriesToCollection(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req AddSeriesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.Store.AddSeriesToCollection(id, req.SeriesID); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) HandleRemoveSeriesFromCollection(w http.ResponseWriter, r *http.Request) {
	colID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid collection id")
		return
	}
	seriesID := r.PathValue("seriesId")
	if err := s.Store.RemoveSeriesFromCollection(colID, seriesID); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) HandleReorderCollectionSeries(w http.ResponseWriter, r *http.Request) {
	colID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req ReorderSeriesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.Store.ReorderCollectionSeries(colID, req.SeriesIDs); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
