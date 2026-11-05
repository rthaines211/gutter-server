package server

import (
	"encoding/json"
	"testing"
)

func TestFlagPageListsWithPathAndReason(t *testing.T) {
	srv, id := setupBubblesTest(t)
	for _, c := range []struct{ num, body string }{
		{"2", `{"reason":"panel_cut","zoom":"panels"}`},
		{"0", `{"reason":"order","zoom":"bubbles"}`},
		{"2", `{"reason":"balloon_missing","zoom":"bubbles"}`}, // re-flag replaces, no duplicate
	} {
		if w := callBubbles(srv.HandleFlagPage, "PUT", id, c.num, c.body); w.Code != 204 {
			t.Fatalf("flag %s: %d %s", c.num, w.Code, w.Body.String())
		}
	}
	var got []struct {
		ComicID            string `json:"comic_id"`
		Path, Reason, Zoom string
		Page               int
	}
	json.NewDecoder(callBubbles(srv.HandleListPageFlags, "GET", "", "", "").Body).Decode(&got)
	if len(got) != 2 {
		t.Fatalf("flags = %+v", got)
	}
	byPage := map[int]string{}
	for _, f := range got {
		if f.Path != "b.cbz" || f.ComicID != id {
			t.Errorf("flag %+v: wrong comic", f)
		}
		byPage[f.Page] = f.Zoom + "/" + f.Reason
	}
	if byPage[2] != "bubbles/balloon_missing" || byPage[0] != "bubbles/order" {
		t.Errorf("reasons = %v", byPage)
	}
}

func TestFlagPageRejects(t *testing.T) {
	srv, id := setupBubblesTest(t)
	ok := `{"reason":"order","zoom":"panels"}`
	for _, c := range []struct {
		id, num, body string
		want          int
	}{
		{id, "3", ok, 404}, {id, "-1", ok, 400}, {id, "x", ok, 400}, {pathToID("nope.cbz"), "0", ok, 404},
		{id, "0", `{"reason":"balloon_cut","zoom":"panels"}`, 400}, // not offered in panel zoom
		{id, "0", `{"reason":"order","zoom":"off"}`, 400},
		{id, "0", ``, 400},
	} {
		if w := callBubbles(srv.HandleFlagPage, "PUT", c.id, c.num, c.body); w.Code != c.want {
			t.Errorf("%s/%s %s: got %d, want %d", c.id, c.num, c.body, w.Code, c.want)
		}
	}
	if flags, _ := srv.Store.ListPageFlags(); len(flags) != 0 {
		t.Errorf("rejected flags were stored: %+v", flags)
	}
}
