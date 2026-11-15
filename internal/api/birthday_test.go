package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBirthday(t *testing.T) {
	h := New(nil, nil, Config{})
	for path, want := range map[string]string{"/api/birthday/": "text/html", "/api/birthday/andriyko.jpg": "image/jpeg"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), want) {
			t.Errorf("%s: %d %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
}
