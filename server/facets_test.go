package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

type FacetResponse struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

func getFacets(t *testing.T, srv *Server, facet string, query string) []FacetResponse {
	t.Helper()
	url := "/facets/" + facet
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest("GET", url, nil)
	req.SetPathValue("facet", facet)
	w := httptest.NewRecorder()
	srv.HandleListFacets(w, req)
	if w.Code != 200 {
		t.Fatalf("GET %s: got %d, body: %s", url, w.Code, w.Body.String())
	}
	var result []FacetResponse
	json.NewDecoder(w.Body).Decode(&result)
	return result
}

func TestFacetPublisher(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getFacets(t, srv, "publisher", "")
	if len(result) != 3 {
		t.Fatalf("publisher facets: got %d, want 3", len(result))
	}
	if result[0].Count != 1 {
		t.Errorf("first publisher count: got %d, want 1", result[0].Count)
	}
}

func TestFacetGenre(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getFacets(t, srv, "genre", "")
	if len(result) != 5 {
		t.Fatalf("genre facets: got %d, want 5", len(result))
	}
	if result[0].Count != 2 {
		t.Errorf("top genre count: got %d, want 2", result[0].Count)
	}
}

func TestFacetWriter(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getFacets(t, srv, "writer", "")
	if len(result) != 3 {
		t.Fatalf("writer facets: got %d, want 3", len(result))
	}
}

func TestFacetYear(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getFacets(t, srv, "year", "")
	if len(result) != 3 {
		t.Fatalf("year facets: got %d, want 3", len(result))
	}
}

func TestFacetRespectsFilters(t *testing.T) {
	srv := newTestServer(t)
	seedLibraryData(t, srv)
	result := getFacets(t, srv, "genre", "publisher=DC+Comics")
	if len(result) != 2 {
		t.Fatalf("filtered genre facets: got %d, want 2 (Action, Superhero)", len(result))
	}
}

func TestFacetInvalidFacet(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/facets/invalid", nil)
	req.SetPathValue("facet", "invalid")
	w := httptest.NewRecorder()
	srv.HandleListFacets(w, req)
	if w.Code != 400 {
		t.Errorf("invalid facet: got %d, want 400", w.Code)
	}
}
