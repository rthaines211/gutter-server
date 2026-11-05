package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestSmartFilterCRUDEndpoints(t *testing.T) {
	srv := newTestServer(t)

	// Create
	body := `{"name":"Unread Big","icon":"filter_list","color":"#64B5F6","conditions":[{"field":"status","op":"eq","value":"unread"},{"field":"issue_count","op":"gte","value":"5"}]}`
	req := httptest.NewRequest("POST", "/smart-filters", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateSmartFilter(w, req)
	if w.Code != 201 {
		t.Fatalf("POST: got %d, body: %s", w.Code, w.Body.String())
	}
	var created map[string]int64
	json.NewDecoder(w.Body).Decode(&created)
	sfID := strconv.FormatInt(created["id"], 10)

	// List
	req = httptest.NewRequest("GET", "/smart-filters", nil)
	w = httptest.NewRecorder()
	srv.HandleListSmartFilters(w, req)
	if w.Code != 200 {
		t.Fatalf("GET list: got %d", w.Code)
	}
	var list []json.RawMessage
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 1 {
		t.Errorf("list count: got %d, want 1", len(list))
	}

	// Update
	body = `{"name":"Updated","icon":"star","color":"#FF0000","conditions":[{"field":"genre","op":"eq","value":"Horror"}]}`
	req = httptest.NewRequest("PUT", "/smart-filters/"+sfID, bytes.NewBufferString(body))
	req.SetPathValue("id", sfID)
	w = httptest.NewRecorder()
	srv.HandleUpdateSmartFilter(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT: got %d, body: %s", w.Code, w.Body.String())
	}

	// Delete
	req = httptest.NewRequest("DELETE", "/smart-filters/"+sfID, nil)
	req.SetPathValue("id", sfID)
	w = httptest.NewRecorder()
	srv.HandleDeleteSmartFilter(w, req)
	if w.Code != 200 {
		t.Fatalf("DELETE: got %d", w.Code)
	}
}

func TestSmartFilterEvaluation(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)

	body := `{"name":"Sci-Fi","conditions":[{"field":"genre","op":"eq","value":"Sci-Fi"}]}`
	req := httptest.NewRequest("POST", "/smart-filters", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateSmartFilter(w, req)
	var created map[string]int64
	json.NewDecoder(w.Body).Decode(&created)
	sfID := strconv.FormatInt(created["id"], 10)

	req = httptest.NewRequest("GET", "/smart-filters/"+sfID+"/series", nil)
	req.SetPathValue("id", sfID)
	w = httptest.NewRecorder()
	srv.HandleSmartFilterSeries(w, req)
	if w.Code != 200 {
		t.Fatalf("evaluate: got %d, body: %s", w.Code, w.Body.String())
	}
	var result []LibrarySeriesResponse
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 2 {
		t.Errorf("sci-fi filter: got %d results, want 2 (saga, xmen)", len(result))
	}
}

func TestSmartFilterEvalIssueCount(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)

	body := `{"name":"Big Series","conditions":[{"field":"issue_count","op":"gte","value":"10"}]}`
	req := httptest.NewRequest("POST", "/smart-filters", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateSmartFilter(w, req)
	var created map[string]int64
	json.NewDecoder(w.Body).Decode(&created)
	sfID := strconv.FormatInt(created["id"], 10)

	req = httptest.NewRequest("GET", "/smart-filters/"+sfID+"/series", nil)
	req.SetPathValue("id", sfID)
	w = httptest.NewRecorder()
	srv.HandleSmartFilterSeries(w, req)
	var result []LibrarySeriesResponse
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 2 {
		t.Errorf("issue_count>=10: got %d results, want 2 (batman, saga)", len(result))
	}
}

func TestSmartFilterEvalMultipleConditions(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)

	body := `{"name":"DC Action","conditions":[{"field":"genre","op":"eq","value":"Action"},{"field":"publisher","op":"eq","value":"DC Comics"}]}`
	req := httptest.NewRequest("POST", "/smart-filters", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateSmartFilter(w, req)
	var created map[string]int64
	json.NewDecoder(w.Body).Decode(&created)
	sfID := strconv.FormatInt(created["id"], 10)

	req = httptest.NewRequest("GET", "/smart-filters/"+sfID+"/series", nil)
	req.SetPathValue("id", sfID)
	w = httptest.NewRecorder()
	srv.HandleSmartFilterSeries(w, req)
	var result []LibrarySeriesResponse
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 1 || result[0].ID != "batman" {
		t.Errorf("DC Action filter: got %d results, want 1 (batman)", len(result))
	}
}

func TestSmartFilterCreateNameRequired(t *testing.T) {
	srv := newTestServer(t)
	body := `{"conditions":[]}`
	req := httptest.NewRequest("POST", "/smart-filters", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateSmartFilter(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for missing name, got %d", w.Code)
	}
}
