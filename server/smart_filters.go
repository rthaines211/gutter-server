package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

type smartFilterCondition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type createSmartFilterRequest struct {
	Name       string                 `json:"name"`
	Icon       string                 `json:"icon"`
	Color      string                 `json:"color"`
	Conditions []smartFilterCondition `json:"conditions"`
}

func (s *Server) HandleListSmartFilters(w http.ResponseWriter, r *http.Request) {
	filters, err := s.Store.ListSmartFilters()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if filters == nil {
		filters = []store.SmartFilter{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(filters)
}

func (s *Server) HandleCreateSmartFilter(w http.ResponseWriter, r *http.Request) {
	var req createSmartFilterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Icon == "" {
		req.Icon = "filter_list"
	}
	if req.Color == "" {
		req.Color = "#64B5F6"
	}
	if req.Conditions == nil {
		req.Conditions = []smartFilterCondition{}
	}
	condJSON, err := json.Marshal(req.Conditions)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conditions")
		return
	}
	id, err := s.Store.CreateSmartFilter(req.Name, req.Icon, req.Color, string(condJSON))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]int64{"id": id})
}

func (s *Server) HandleUpdateSmartFilter(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req createSmartFilterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Conditions == nil {
		req.Conditions = []smartFilterCondition{}
	}
	condJSON, err := json.Marshal(req.Conditions)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conditions")
		return
	}
	if err := s.Store.UpdateSmartFilter(id, req.Name, req.Icon, req.Color, string(condJSON)); err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) HandleDeleteSmartFilter(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.Store.DeleteSmartFilter(id); err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) HandleSmartFilterSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	sf, err := s.Store.GetSmartFilter(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "smart filter not found")
		return
	}

	var conditions []smartFilterCondition
	if err := json.Unmarshal([]byte(sf.Conditions), &conditions); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "invalid filter conditions")
		return
	}

	allSeries, err := s.Store.ListSeries()
	if err != nil {
		writeInternalError(w, err)
		return
	}

	var result []LibrarySeriesResponse
	for _, series := range allSeries {
		readCount := s.countReadIssues(series)
		genres, err := s.Store.ListSeriesGenres(series.ID)
		if err != nil || genres == nil {
			genres = []string{}
		}

		if !matchesConditions(series, readCount, genres, conditions) {
			continue
		}

		coverURL := ""
		if series.CoverPath != "" {
			coverURL = "/library/" + series.ID + "/cover"
		}

		result = append(result, LibrarySeriesResponse{
			ID:         series.ID,
			Name:       series.Name,
			FolderPath: series.FolderPath,
			IssueCount: series.IssueCount,
			ReadCount:  readCount,
			CoverURL:   coverURL,
			Writer:     series.Writer,
			Penciller:  series.Penciller,
			Publisher:  series.Publisher,
			Genres:     genres,
			Year:       series.Year,
			UpdatedAt:  series.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})

	if result == nil {
		result = []LibrarySeriesResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func matchesConditions(series store.Series, readCount int, genres []string, conditions []smartFilterCondition) bool {
	for _, c := range conditions {
		if !matchCondition(series, readCount, genres, c) {
			return false
		}
	}
	return true
}

func matchCondition(series store.Series, readCount int, genres []string, c smartFilterCondition) bool {
	switch c.Field {
	case "status":
		status := computeStatus(series.IssueCount, readCount)
		return matchString(status, c.Op, c.Value)
	case "issue_count":
		return matchInt(series.IssueCount, c.Op, c.Value)
	case "publisher":
		return matchString(series.Publisher, c.Op, c.Value)
	case "genre":
		return matchGenre(genres, c.Op, c.Value)
	case "writer":
		return matchString(series.Writer, c.Op, c.Value)
	case "penciller":
		return matchString(series.Penciller, c.Op, c.Value)
	case "year":
		return matchInt(series.Year, c.Op, c.Value)
	case "date_added":
		return matchDateAdded(series, c.Op, c.Value)
	default:
		return false
	}
}

func computeStatus(issueCount, readCount int) string {
	if readCount == 0 {
		return "unread"
	}
	if readCount >= issueCount && issueCount > 0 {
		return "completed"
	}
	return "in_progress"
}

func matchString(actual, op, expected string) bool {
	switch op {
	case "eq":
		return strings.EqualFold(actual, expected)
	case "neq":
		return !strings.EqualFold(actual, expected)
	case "contains":
		return strings.Contains(strings.ToLower(actual), strings.ToLower(expected))
	default:
		return false
	}
}

func matchInt(actual int, op, valueStr string) bool {
	expected, err := strconv.Atoi(valueStr)
	if err != nil {
		return false
	}
	switch op {
	case "eq":
		return actual == expected
	case "neq":
		return actual != expected
	case "gt":
		return actual > expected
	case "gte":
		return actual >= expected
	case "lt":
		return actual < expected
	case "lte":
		return actual <= expected
	default:
		return false
	}
}

func matchGenre(genres []string, op, value string) bool {
	found := false
	for _, g := range genres {
		if strings.EqualFold(g, value) {
			found = true
			break
		}
	}
	switch op {
	case "eq":
		return found
	case "neq":
		return !found
	default:
		return false
	}
}

func matchDateAdded(series store.Series, op, valueStr string) bool {
	switch op {
	case "within_days":
		days, err := strconv.Atoi(valueStr)
		if err != nil {
			return false
		}
		return time.Since(series.UpdatedAt) <= time.Duration(days)*24*time.Hour
	default:
		return false
	}
}
