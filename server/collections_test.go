package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestCollectionsCRUD(t *testing.T) {
	srv := newTestServer(t)

	// Create
	body := `{"name": "Favorites", "icon": "star", "color": "#FFD700"}`
	req := httptest.NewRequest("POST", "/collections", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateCollection(w, req)
	if w.Code != 201 {
		t.Fatalf("POST expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created map[string]int64
	json.NewDecoder(w.Body).Decode(&created)
	colID := strconv.FormatInt(created["id"], 10)

	// List
	req = httptest.NewRequest("GET", "/collections", nil)
	w = httptest.NewRecorder()
	srv.HandleListCollections(w, req)
	if w.Code != 200 {
		t.Fatalf("GET expected 200, got %d", w.Code)
	}
	var list []CollectionResponse
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 1 || list[0].Name != "Favorites" {
		t.Errorf("unexpected list: %+v", list)
	}
	if len(list[0].SeriesIDs) != 0 {
		t.Errorf("expected empty series_ids, got %v", list[0].SeriesIDs)
	}

	// Update
	body = `{"name": "Top Picks", "icon": "heart", "color": "#F00"}`
	req = httptest.NewRequest("PUT", "/collections/"+colID, bytes.NewBufferString(body))
	req.SetPathValue("id", colID)
	w = httptest.NewRecorder()
	srv.HandleUpdateCollection(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Delete
	req = httptest.NewRequest("DELETE", "/collections/"+colID, nil)
	req.SetPathValue("id", colID)
	w = httptest.NewRecorder()
	srv.HandleDeleteCollection(w, req)
	if w.Code != 200 {
		t.Fatalf("DELETE expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify empty
	req = httptest.NewRequest("GET", "/collections", nil)
	w = httptest.NewRecorder()
	srv.HandleListCollections(w, req)
	var empty []CollectionResponse
	json.NewDecoder(w.Body).Decode(&empty)
	if len(empty) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(empty))
	}
}

func TestCollectionSeriesEndpoints(t *testing.T) {
	srv := newTestServer(t)

	body := `{"name": "Test"}`
	req := httptest.NewRequest("POST", "/collections", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateCollection(w, req)
	var created map[string]int64
	json.NewDecoder(w.Body).Decode(&created)
	colID := strconv.FormatInt(created["id"], 10)

	// Add series
	body = `{"series_id": "batman"}`
	req = httptest.NewRequest("POST", "/collections/"+colID+"/series", bytes.NewBufferString(body))
	req.SetPathValue("id", colID)
	w = httptest.NewRecorder()
	srv.HandleAddSeriesToCollection(w, req)
	if w.Code != 200 {
		t.Fatalf("add series expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify via list
	req = httptest.NewRequest("GET", "/collections", nil)
	w = httptest.NewRecorder()
	srv.HandleListCollections(w, req)
	var list []CollectionResponse
	json.NewDecoder(w.Body).Decode(&list)
	if len(list[0].SeriesIDs) != 1 || list[0].SeriesIDs[0] != "batman" {
		t.Errorf("expected [batman], got %v", list[0].SeriesIDs)
	}

	// Remove series
	req = httptest.NewRequest("DELETE", "/collections/"+colID+"/series/batman", nil)
	req.SetPathValue("id", colID)
	req.SetPathValue("seriesId", "batman")
	w = httptest.NewRecorder()
	srv.HandleRemoveSeriesFromCollection(w, req)
	if w.Code != 200 {
		t.Fatalf("remove series expected 200, got %d", w.Code)
	}

	// Verify empty
	req = httptest.NewRequest("GET", "/collections", nil)
	w = httptest.NewRecorder()
	srv.HandleListCollections(w, req)
	json.NewDecoder(w.Body).Decode(&list)
	if len(list[0].SeriesIDs) != 0 {
		t.Errorf("expected empty after remove, got %v", list[0].SeriesIDs)
	}
}

func TestCreateCollectionNameRequired(t *testing.T) {
	srv := newTestServer(t)
	body := `{"icon": "star"}`
	req := httptest.NewRequest("POST", "/collections", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.HandleCreateCollection(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for missing name, got %d", w.Code)
	}
}
