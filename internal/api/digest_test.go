package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glushkov45learners/fire-calendar-backend/internal/analysis"
	"github.com/glushkov45learners/fire-calendar-backend/internal/digest"
	"github.com/glushkov45learners/fire-calendar-backend/internal/mail"
	"github.com/glushkov45learners/fire-calendar-backend/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// Bad bodies are rejected before the database is touched, so no store is needed.
func TestSubscribeRejectsBadBodies(t *testing.T) {
	h := New(nil, quiet, Config{})
	for body, code := range map[string]string{
		`{"email":"a@example.org"}`:                      "consent_required",
		`{"email":"a@example.org","consent":false}`:      "consent_required",
		`{"email":"not an address","consent":true}`:      "invalid_email",
		`{"email":"a@localhost","consent":true}`:         "invalid_email",
		`{"email":"Ann <a@example.org>","consent":true}`: "invalid_email",
		`{"consent":true}`:                               "invalid_email",
		`{"email":"a@example.org","consent":true,"x":1}`: "invalid_request",
		`not json`: "invalid_request",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Errorf("%s: got %d %s, want 400 %s", body, w.Code, strings.TrimSpace(w.Body.String()), code)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got, ok := normalizeEmail("  Ann.Lee@Example.ORG "); !ok || got != "ann.lee@example.org" {
		t.Errorf("got %q %v, want ann.lee@example.org", got, ok)
	}
}

func TestUnsubscribeAsksBeforeActing(t *testing.T) {
	h := New(nil, quiet, Config{}) // a nil store would panic if GET touched it
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/unsubscribe?token=abc", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="?token=abc"`) {
		t.Errorf("got %d, want the confirmation form:\n%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/unsubscribe", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("no token: got %d, want 404", w.Code)
	}
}

type digestStore struct{ subs []store.Subscriber }

func (digestStore) Coverage(context.Context) (store.Coverage, error) {
	return store.Coverage{
		First: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Last:  time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC),
	}, nil
}
func (digestStore) CountryDailySums(context.Context) ([]analysis.Day, error) {
	return []analysis.Day{{Date: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), CountH: 1234}}, nil
}
func (d digestStore) Subscribers(context.Context) ([]store.Subscriber, error) { return d.subs, nil }
func (digestStore) ClaimDigest(context.Context, string) (bool, error)         { return true, nil }
func (digestStore) FinishDigest(context.Context, string, int, int) error      { return nil }

type countingMailer struct{ n int }

func (c *countingMailer) Send(context.Context, mail.Message) error { c.n++; return nil }

func digestServer(m digest.Mailer, key string) http.Handler {
	svc := &digest.Service{
		Store:     digestStore{subs: []store.Subscriber{{Email: "a@example.org", Token: "t"}}},
		Mailer:    m,
		PublicURL: "https://example.org",
		Log:       quiet,
		Now:       func() time.Time { return time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC) },
	}
	return New(nil, quiet, Config{Digest: svc, DigestKey: key})
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestDigestPagesNeedTheKey(t *testing.T) {
	m := &countingMailer{}
	h := digestServer(m, "s3cret")
	for _, c := range []struct {
		method, target string
		want           int
	}{
		{http.MethodGet, "/api/digest", http.StatusForbidden},
		{http.MethodGet, "/api/digest?key=wrong", http.StatusForbidden},
		{http.MethodGet, "/api/digest/preview", http.StatusForbidden},
		{http.MethodPost, "/api/digest/send?key=", http.StatusForbidden},
	} {
		if w := do(h, c.method, c.target); w.Code != c.want {
			t.Errorf("%s %s: got %d, want %d", c.method, c.target, w.Code, c.want)
		}
	}
	// Without DIGEST_KEY the pages are off, even for an empty key.
	if w := do(digestServer(m, ""), http.MethodPost, "/api/digest/send?key="); w.Code != http.StatusNotFound {
		t.Errorf("no key configured: got %d, want 404", w.Code)
	}
	if m.n != 0 {
		t.Errorf("%d letters were sent without the key", m.n)
	}
}

func TestDigestPageAndSend(t *testing.T) {
	m := &countingMailer{}
	h := digestServer(m, "s3cret")

	page := do(h, http.MethodGet, "/api/digest?key=s3cret")
	for _, want := range []string{"October 2026", "1,234", "Start mailing", `src="/api/digest/preview?key=s3cret"`} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), want) {
			t.Errorf("page: got %d, lacks %q", page.Code, want)
		}
	}

	if w := do(h, http.MethodGet, "/api/digest/preview?key=s3cret"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "Harmonized fire detections") {
		t.Errorf("preview: got %d", w.Code)
	}

	w := do(h, http.MethodPost, "/api/digest/send?key=s3cret")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"period":"2026-10","sent":1,"failed":0}` || m.n != 1 {
		t.Errorf("send: got %d %s, %d letters", w.Code, strings.TrimSpace(w.Body.String()), m.n)
	}

	// A GET must never mail: link previews and crawlers issue GETs.
	if w := do(h, http.MethodGet, "/api/digest/send?key=s3cret"); w.Code != http.StatusMethodNotAllowed || m.n != 1 {
		t.Errorf("GET send: got %d, %d letters", w.Code, m.n)
	}
}

func TestDigestSendWithoutSendGrid(t *testing.T) {
	w := do(digestServer(nil, "s3cret"), http.MethodPost, "/api/digest/send?key=s3cret")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"mail_off"`) {
		t.Errorf("got %d %s, want 503 mail_off", w.Code, strings.TrimSpace(w.Body.String()))
	}
}
