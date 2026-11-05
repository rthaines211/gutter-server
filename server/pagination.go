package server

import (
	"net/http"
	"strconv"
)

// parsePageLimit reads 1-based ?page= and ?limit= query params.
// Invalid or missing values fall back to page 1 / defaultLimit; limit is
// clamped to maxLimit. This is the repo's one pagination convention
// (study rec 6) — new paged endpoints reuse it.
func parsePageLimit(r *http.Request, defaultLimit, maxLimit int) (page, limit int) {
	page, limit = 1, defaultLimit
	if v := r.URL.Query().Get("page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			page = p
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if l, err := strconv.Atoi(v); err == nil && l > 0 {
			if l > maxLimit {
				l = maxLimit
			}
			limit = l
		}
	}
	return page, limit
}
