package server

import (
	"net/http/httptest"
	"testing"
)

func TestParsePageLimit(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantPage  int
		wantLimit int
	}{
		{"defaults", "", 1, 20},
		{"explicit", "?page=3&limit=50", 3, 50},
		{"zero page", "?page=0", 1, 20},
		{"negative", "?page=-2&limit=-5", 1, 20},
		{"garbage", "?page=abc&limit=xyz", 1, 20},
		{"limit clamped", "?limit=99999", 1, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/stats/history"+c.query, nil)
			page, limit := parsePageLimit(r, 20, 100)
			if page != c.wantPage || limit != c.wantLimit {
				t.Fatalf("got page=%d limit=%d, want %d/%d", page, limit, c.wantPage, c.wantLimit)
			}
		})
	}
}
