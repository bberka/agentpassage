package gateway

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
)

// barePaths are the API paths a client reaches when its base URL lacks /v1.
var barePaths = []string{
	"/models", "/chat/completions", "/responses", "/messages", "/embeddings",
	"/rerank", "/images/generations", "/audio/speech", "/audio/transcriptions",
}

// withBarePaths serves each of barePaths as its /v1 path.
func withBarePaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slices.Contains(barePaths, r.URL.Path) {
			r = r.Clone(r.Context())
			r.URL.Path, r.URL.RawPath = "/v1"+r.URL.Path, ""
		}
		next.ServeHTTP(w, r)
	})
}

// withCORS lets web apps on any origin call the API under /v1/. Requests are
// authorized only by a key the app sends itself, never by a cookie, so another
// site gains nothing without one. Admin, usage and management paths stay closed
// to other origins.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Access-Control-Expose-Headers", "*")
			// The preflight carries no key, so it is answered before auth.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				// "*" would not cover Authorization, so echo what was asked for.
				h.Set("Access-Control-Allow-Headers", cmp.Or(r.Header.Get("Access-Control-Request-Headers"), "Authorization, Content-Type"))
				h.Set("Access-Control-Max-Age", "86400")
				h.Add("Vary", "Access-Control-Request-Headers")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
