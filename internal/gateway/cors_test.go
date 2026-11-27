package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSAnswersPreflightWithoutAKeyOnAPIPathsOnly(t *testing.T) {
	reached := false
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusUnauthorized)
	}))
	call := func(method, path string) *httptest.ResponseRecorder {
		reached = false
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Origin", "https://app.example")
		if method == http.MethodOptions {
			r.Header.Set("Access-Control-Request-Method", "POST")
			r.Header.Set("Access-Control-Request-Headers", "authorization,x-api-key")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := call(http.MethodOptions, "/v1/chat/completions")
	if w.Code != http.StatusNoContent || reached ||
		w.Header().Get("Access-Control-Allow-Origin") != "*" ||
		w.Header().Get("Access-Control-Allow-Headers") != "authorization,x-api-key" {
		t.Fatalf("preflight: code %d, reached %v, headers %v", w.Code, reached, w.Header())
	}
	// A rejected request still needs the header, or the app can't read why.
	if w := call(http.MethodGet, "/v1/models"); !reached || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("GET /v1/models: reached %v, headers %v", reached, w.Header())
	}
	for _, path := range []string{"/admin/state", "/v0/management/auth-files", "/healthz"} {
		for _, method := range []string{http.MethodOptions, http.MethodGet} {
			if w := call(method, path); !reached || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("%s %s opened to other origins: %v", method, path, w.Header())
			}
		}
	}
}
