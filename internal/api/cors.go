package api

import (
	"net/http"
	"slices"
)

// withCORS allows cross-origin calls from the given origins ("*" = any).
// In production the frontend is served from the same origin and the list is
// empty; it exists for a frontend dev server on another port or host.
// The API uses no cookies or auth headers, so credentials are never allowed.
func withCORS(origins []string, next http.Handler) http.Handler {
	if len(origins) == 0 {
		return next
	}
	any := slices.Contains(origins, "*")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (any || slices.Contains(origins, origin)) {
			h := w.Header()
			if any {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
			}
			h.Set("Access-Control-Expose-Headers", "Content-Disposition")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
