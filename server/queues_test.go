package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestQueuePutGetDelete(t *testing.T) {
	srv := newTestServer(t)

	body := `{"name":"Weekend","icon":"star","color":"#FFD700","position":0,"series_ids":["batman","saga"]}`
	req := httptest.NewRequest("PUT", "/queues/q1", bytes.NewBufferString(body))
	req.SetPathValue("id", "q1")
	w := httptest.NewRecorder()
	srv.HandlePutQueue(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/queues", nil)
	w = httptest.NewRecorder()
	srv.HandleListQueues(w, req)
	if w.Code != 200 {
		t.Fatalf("GET expected 200, got %d", w.Code)
	}
	var list []QueueResponse
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 1 || list[0].ID != "q1" || list[0].Name != "Weekend" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if len(list[0].SeriesIDs) != 2 || list[0].SeriesIDs[0] != "batman" {
		t.Errorf("unexpected series_ids: %v", list[0].SeriesIDs)
	}

	body = `{"name":"Weekend","icon":"star","color":"#FFD700","position":0,"series_ids":["saga"]}`
	req = httptest.NewRequest("PUT", "/queues/q1", bytes.NewBufferString(body))
	req.SetPathValue("id", "q1")
	w = httptest.NewRecorder()
	srv.HandlePutQueue(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT(replace) expected 200, got %d", w.Code)
	}
	req = httptest.NewRequest("GET", "/queues", nil)
	w = httptest.NewRecorder()
	srv.HandleListQueues(w, req)
	json.NewDecoder(w.Body).Decode(&list)
	if len(list[0].SeriesIDs) != 1 || list[0].SeriesIDs[0] != "saga" {
		t.Errorf("expected [saga], got %v", list[0].SeriesIDs)
	}

	req = httptest.NewRequest("DELETE", "/queues/q1", nil)
	req.SetPathValue("id", "q1")
	w = httptest.NewRecorder()
	srv.HandleDeleteQueue(w, req)
	if w.Code != 200 {
		t.Fatalf("DELETE expected 200, got %d", w.Code)
	}
}
