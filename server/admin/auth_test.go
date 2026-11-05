package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rthaines211/gutter-server/config"
)

func TestRequireAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	cfg := &config.Config{}
	h := (&Admin{Config: cfg}).requireAuth(ok)
	do := func(method, pass, site string) int {
		r := httptest.NewRequest(method, "/_admin/", nil)
		if pass != "" {
			r.SetBasicAuth("x", pass)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	if c := do("GET", "", ""); c != 200 {
		t.Fatalf("auth off: got %d, want 200", c)
	}
	cfg.AdminPassword = "s3cret"
	for _, tc := range []struct {
		method, pass, site string
		want               int
	}{
		{"GET", "", "", 401},
		{"GET", "wrong", "", 401},
		{"GET", "s3cret", "", 200},
		{"POST", "s3cret", "same-origin", 200},
		{"POST", "s3cret", "cross-site", 403},
	} {
		if c := do(tc.method, tc.pass, tc.site); c != tc.want {
			t.Errorf("%s pass=%q site=%q: got %d, want %d", tc.method, tc.pass, tc.site, c, tc.want)
		}
	}
}
