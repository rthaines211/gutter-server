package admin

import (
	"crypto/subtle"
	"net/http"
)

// requireAuth gates the console with HTTP Basic auth when admin_password is
// set; empty means open. Any username is accepted.
func (a *Admin) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Config == nil || a.Config.Password == "" {
			next.ServeHTTP(w, r)
			return
		}
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(a.Config.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Gutter", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Browsers resend Basic credentials on any request, so a page on
		// another site could POST to the console. Refuse cross-site writes.
		if r.Method != http.MethodGet && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
