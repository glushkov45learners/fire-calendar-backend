package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := withCORS([]string{"http://localhost:5173"}, ok)

	do := func(method, origin string, preflight bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/analyze", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if preflight {
			r.Header.Set("Access-Control-Request-Method", "POST")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	pre := do(http.MethodOptions, "http://localhost:5173", true)
	if pre.Code != http.StatusNoContent || pre.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		pre.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Fatalf("preflight: %d %v", pre.Code, pre.Header())
	}
	post := do(http.MethodPost, "http://localhost:5173", false)
	if post.Code != http.StatusTeapot || post.Header().Get("Access-Control-Expose-Headers") != "Content-Disposition" {
		t.Fatalf("simple request: %d %v", post.Code, post.Header())
	}
	if bad := do(http.MethodPost, "https://evil.example", false); bad.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("foreign origin allowed: %v", bad.Header())
	}

	star := withCORS([]string{"*"}, ok)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	r.Header.Set("Origin", "http://anything:3000")
	star.ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("wildcard: %v", w.Header())
	}
}
