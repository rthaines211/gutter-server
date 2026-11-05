package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

func basic(u, p string) string {
	r, _ := http.NewRequest("GET", "/", nil)
	r.SetBasicAuth(u, p)
	return r.Header.Get("Authorization")
}

func TestRequireAuth(t *testing.T) {
	h := RequireAuth(okHandler, "p:ss wörd", "key123")
	cases := []struct {
		name, method, path, authz, site string
		want                            int
	}{
		{"no creds", "GET", "/library", "", "", 401},
		{"healthz open", "GET", "/healthz", "", "", 200},
		{"options open", "OPTIONS", "/library", "", "", 200},
		{"basic ok any user", "GET", "/library", basic("whoever", "p:ss wörd"), "", 200},
		{"basic wrong", "GET", "/library", basic("x", "nope"), "", 401},
		{"bearer ok", "GET", "/library", "Bearer key123", "", 200},
		{"bearer wrong", "GET", "/library", "Bearer nope", "", 401},
		{"admin gated", "GET", "/_admin/", "", "", 401},
		{"status page gated", "GET", "/", "", "", 401},
		{"cross-site write refused", "POST", "/library/refresh", basic("x", "p:ss wörd"), "cross-site", 403},
		{"same-origin write ok", "POST", "/library/refresh", basic("x", "p:ss wörd"), "same-origin", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			if c.authz != "" {
				req.Header.Set("Authorization", c.authz)
			}
			if c.site != "" {
				req.Header.Set("Sec-Fetch-Site", c.site)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("got %d want %d", rec.Code, c.want)
			}
			if c.want == 401 && rec.Header().Get("WWW-Authenticate") != `Basic realm="Gutter", charset="UTF-8"` {
				t.Fatalf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestRequireAuthEmptyAPIKeyNeverMatches(t *testing.T) {
	h := RequireAuth(okHandler, "pw", "")
	for _, authz := range []string{"Bearer ", "Bearer"} {
		req := httptest.NewRequest("GET", "/library", nil)
		req.Header.Set("Authorization", authz)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("%q: got %d want 401", authz, rec.Code)
		}
	}
}

func TestRouterCORSAllowsAuthorization(t *testing.T) {
	s := &Server{Password: "pw"}
	rec := httptest.NewRecorder()
	s.NewRouter().ServeHTTP(rec, httptest.NewRequest("OPTIONS", "/library", nil))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("code=%d headers=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestRequireAuthEmptyPasswordNeverMatches(t *testing.T) {
	req := httptest.NewRequest("GET", "/library", nil)
	req.Header.Set("Authorization", basic("x", ""))
	rec := httptest.NewRecorder()
	RequireAuth(okHandler, "", "").ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("got %d want 401", rec.Code)
	}
}
