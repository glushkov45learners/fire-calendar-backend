package api

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/mail"
	"strings"

	"github.com/glushkov45learners/fire-calendar-backend/internal/digest"
)

const maxEmailLen = 254

// subscribe handles POST /api/v1/subscriptions: an address plus explicit
// consent to the monthly digest.
func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email   string `json:"email"`
		Consent bool   `json:"consent"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		s.fail(w, r, badRequest("invalid_request", "malformed JSON body: %v", err))
		return
	}
	email, ok := normalizeEmail(body.Email)
	if !ok {
		s.fail(w, r, badRequest("invalid_email", "email is not a valid address"))
		return
	}
	if !body.Consent {
		s.fail(w, r, badRequest("consent_required", "consent to the monthly digest must be true"))
		return
	}
	if err := s.store.Subscribe(r.Context(), email); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// normalizeEmail accepts a bare address (no display name) and lower-cases it.
func normalizeEmail(in string) (string, bool) {
	in = strings.TrimSpace(in)
	a, err := mail.ParseAddress(in)
	if err != nil || a.Address != in || len(in) > maxEmailLen {
		return "", false
	}
	_, domain, _ := strings.Cut(in, "@")
	if !strings.Contains(domain, ".") || strings.ContainsAny(in, " \t\r\n") {
		return "", false
	}
	return strings.ToLower(in), true
}

//go:embed pages/unsubscribe.html
var unsubscribeHTML string

//go:embed pages/digest.html
var digestHTML string

var (
	unsubscribeTmpl = template.Must(template.New("unsubscribe").Parse(unsubscribeHTML))
	digestTmpl      = template.Must(template.New("digest").Parse(digestHTML))
)

func writeHTML(w http.ResponseWriter, status int, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = t.Execute(w, data)
}

// unsubscribe serves the link at the bottom of every letter. GET only asks
// for confirmation, because mail scanners follow links on their own; POST
// (the page's button, or a mail client's one-click unsubscribe) does it.
func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeHTML(w, http.StatusNotFound, unsubscribeTmpl, map[string]any{"State": "unknown"})
		return
	}
	if r.Method == http.MethodGet {
		writeHTML(w, http.StatusOK, unsubscribeTmpl, map[string]any{"State": "confirm", "Token": token})
		return
	}
	found, err := s.store.Unsubscribe(r.Context(), token)
	switch {
	case err != nil:
		s.log.Error("unsubscribe failed", "err", err)
		writeHTML(w, http.StatusInternalServerError, unsubscribeTmpl, map[string]any{"State": "error"})
	case !found:
		writeHTML(w, http.StatusNotFound, unsubscribeTmpl, map[string]any{"State": "unknown"})
	default:
		writeHTML(w, http.StatusOK, unsubscribeTmpl, map[string]any{"State": "done"})
	}
}

// digestGuard protects the demo pages with the shared key from DIGEST_KEY.
// Without a configured key the pages do not exist.
func (s *Server) digestGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.digest == nil || s.digestKey == "" {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("key")), []byte(s.digestKey)) != 1 {
			http.Error(w, "wrong or missing key", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// digestPage is the demo page: what will be mailed, to how many people, and
// the button that mails it.
func (s *Server) digestPage(w http.ResponseWriter, r *http.Request) {
	view := map[string]any{"Key": s.digestKey, "MailOn": s.digest.MailOn()}
	d, err := s.digest.Latest(r.Context())
	if err != nil {
		if !errors.Is(err, digest.ErrNotReady) {
			s.log.Error("digest page", "err", err)
		}
		view["Problem"] = digestProblem(err)
	} else {
		view["Digest"] = d
	}
	if subs, err := s.digest.Store.Subscribers(r.Context()); err == nil {
		view["Subscribers"] = len(subs)
	} else {
		s.log.Error("digest page", "err", err)
		view["Subscribers"] = "?"
	}
	writeHTML(w, http.StatusOK, digestTmpl, view)
}

func digestProblem(err error) string {
	if errors.Is(err, digest.ErrNotReady) {
		return "The archive does not reach the end of last month yet."
	}
	return "Could not build the digest. See the API log."
}

// digestPreview serves the letter itself, for the iframe of the demo page.
func (s *Server) digestPreview(w http.ResponseWriter, r *http.Request) {
	html, err := s.digest.Preview(r.Context())
	if err != nil {
		http.Error(w, digestProblem(err), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(html))
}

// digestSend mails last month's digest to every subscriber right now.
func (s *Server) digestSend(w http.ResponseWriter, r *http.Request) {
	res, err := s.digest.SendAll(r.Context())
	switch {
	case errors.Is(err, digest.ErrMailOff):
		s.fail(w, r, &apiError{status: http.StatusServiceUnavailable, Code: "mail_off",
			Message: "SENDGRID_API_KEY is not set on the server"})
	case errors.Is(err, digest.ErrNotReady):
		s.fail(w, r, &apiError{status: http.StatusServiceUnavailable, Code: "no_data", Message: digestProblem(err)})
	case err != nil:
		s.fail(w, r, err)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}
