package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Bad parameters are rejected before the database is touched, so no store is needed.
func TestGridRejectsBadParameters(t *testing.T) {
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	for query, code := range map[string]string{
		"from=2020-01-01&to=2020-02-01&record=frp": "invalid_request",
		"from=2020-01-01&record=modis":             "invalid_range",
		"from=01.01.2020&to=2020-02-01":            "invalid_range",
		"from=2021-01-01&to=2020-01-01":            "invalid_range",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/grid?"+query, nil))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Errorf("%s: got %d %s, want 400 %s", query, w.Code, strings.TrimSpace(w.Body.String()), code)
		}
	}
}
