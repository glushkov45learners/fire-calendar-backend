package mail

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendGridSend(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	sg := &SendGrid{APIKey: "SG.test", From: "digest@example.org", FromName: "Fire Calendar", Endpoint: srv.URL}
	err := sg.Send(t.Context(), Message{
		To: "a@example.org", Subject: "Hi", Text: "plain", HTML: "<p>html</p>",
		UnsubscribeURL: "https://example.org/u?token=t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer SG.test" {
		t.Errorf("Authorization: got %q", auth)
	}
	want := `{"content":[{"type":"text/plain","value":"plain"},{"type":"text/html","value":"<p>html</p>"}],` +
		`"from":{"email":"digest@example.org","name":"Fire Calendar"},` +
		`"headers":{"List-Unsubscribe":"<https://example.org/u?token=t>","List-Unsubscribe-Post":"List-Unsubscribe=One-Click"},` +
		`"personalizations":[{"to":[{"email":"a@example.org"}]}],"subject":"Hi"}`
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // keep < and > readable in the expectation above
	_ = enc.Encode(got)
	if strings.TrimSpace(b.String()) != want {
		t.Errorf("payload:\n got %s\nwant %s", b.String(), want)
	}
}

func TestSendGridReportsRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"message":"The from address does not match a verified Sender Identity."}]}`, http.StatusForbidden)
	}))
	defer srv.Close()

	err := (&SendGrid{APIKey: "k", From: "x@example.org", Endpoint: srv.URL}).Send(t.Context(), Message{To: "a@example.org"})
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "verified Sender Identity") {
		t.Errorf("got %v, want the 403 and SendGrid's message", err)
	}
}
