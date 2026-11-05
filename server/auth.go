package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// RequireAuth gates every request except GET /healthz and OPTIONS. It accepts
// HTTP Basic (any username, password must match) or, when apiKey is set,
// "Authorization: Bearer <apiKey>".
func RequireAuth(next http.Handler, password, apiKey string) http.Handler {
	eq := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || (r.Method == http.MethodGet && r.URL.Path == "/healthz") {
			next.ServeHTTP(w, r)
			return
		}
		ok := false
		if _, pass, has := r.BasicAuth(); has {
			ok = eq(pass, password)
		} else if tok, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found {
			// An empty configured key must never match an empty token.
			ok = apiKey != "" && eq(tok, apiKey)
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="Gutter", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Browsers resend Basic credentials on any request, so a page on
		// another site could POST to us. Refuse cross-site writes.
		if r.Method != http.MethodGet && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
