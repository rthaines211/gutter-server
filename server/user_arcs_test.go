package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/rthaines211/gutter-server/store"
)

func putArc(t *testing.T, srv *Server, id, body string) int {
	t.Helper()
	req := httptest.NewRequest("PUT", "/arcs/"+id, bytes.NewBufferString(body))
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.HandlePutUserArc(w, req)
	return w.Code
}

func syncArcs(t *testing.T, srv *Server) SyncResponse {
	t.Helper()
	w := httptest.NewRecorder()
	srv.HandleSync(w, httptest.NewRequest("GET", "/sync?since=0", nil))
	var resp SyncResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUserArcPutSyncDelete(t *testing.T) {
	srv := newTestServer(t)

	if c := putArc(t, srv, "u1", `{"name":"Endgame","issue_ids":["c","a","b"]}`); c != 200 {
		t.Fatalf("PUT = %d", c)
	}
	resp := syncArcs(t, srv)
	if len(resp.Arcs) != 1 || resp.Arcs[0].Source != "user" || resp.Arcs[0].Total != 3 {
		t.Fatalf("arc not synced: %+v", resp.Arcs)
	}
	if got := resp.Arcs[0].IssueIDs; len(got) != 3 || got[0] != "c" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("order lost: %v", got)
	}

	// Replace keeps the new order.
	putArc(t, srv, "u1", `{"name":"Endgame","issue_ids":["b","c"]}`)
	if got := syncArcs(t, srv).Arcs[0].IssueIDs; len(got) != 2 || got[0] != "b" {
		t.Fatalf("replace failed: %v", got)
	}

	req := httptest.NewRequest("DELETE", "/arcs/u1", nil)
	req.SetPathValue("id", "u1")
	w := httptest.NewRecorder()
	srv.HandleDeleteUserArc(w, req)
	if w.Code != 200 || len(syncArcs(t, srv).ArcIDs) != 0 {
		t.Fatalf("delete failed: %d", w.Code)
	}
}

func TestUserArcRefusesImportedArc(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.Store.ImportArcs([]store.ArcInput{{Key: "m1", Name: "Mylar"}}); err != nil {
		t.Fatal(err)
	}
	if c := putArc(t, srv, "m1", `{"name":"x","issue_ids":[]}`); c != 409 {
		t.Fatalf("PUT on imported arc = %d, want 409", c)
	}
	req := httptest.NewRequest("DELETE", "/arcs/m1", nil)
	req.SetPathValue("id", "m1")
	w := httptest.NewRecorder()
	srv.HandleDeleteUserArc(w, req)
	if w.Code != 409 {
		t.Fatalf("DELETE on imported arc = %d, want 409", w.Code)
	}
	// A Mylar re-import must not prune user arcs.
	putArc(t, srv, "u2", `{"name":"Mine","issue_ids":[]}`)
	if err := srv.Store.ImportArcs(nil); err != nil {
		t.Fatal(err)
	}
	if ids := syncArcs(t, srv).ArcIDs; len(ids) != 1 || ids[0] != "u2" {
		t.Fatalf("user arc pruned by import: %v", ids)
	}
}
