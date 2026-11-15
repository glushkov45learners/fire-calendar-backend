// Package mail sends e-mail through the SendGrid v3 Mail Send API.
package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Message is one e-mail to one recipient.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	// UnsubscribeURL, if set, goes into List-Unsubscribe as a one-click link,
	// which is what puts the "Unsubscribe" button next to the sender in Gmail.
	UnsubscribeURL string
}

// SendGrid is a minimal client: one POST per message, no SDK.
type SendGrid struct {
	APIKey   string
	From     string // a sender verified in SendGrid
	FromName string
	Endpoint string       // default https://api.sendgrid.com/v3/mail/send
	Client   *http.Client // default: 20 s timeout
}

type address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type content struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type payload struct {
	Personalizations []struct {
		To []address `json:"to"`
	} `json:"personalizations"`
	From    address           `json:"from"`
	Subject string            `json:"subject"`
	Content []content         `json:"content"`
	Headers map[string]string `json:"headers,omitempty"`
}

var defaultClient = &http.Client{Timeout: 20 * time.Second}

// Send delivers m, or returns SendGrid's own explanation of why it would not.
func (s *SendGrid) Send(ctx context.Context, m Message) error {
	p := payload{
		From:    address{Email: s.From, Name: s.FromName},
		Subject: m.Subject,
		// SendGrid requires text/plain before text/html.
		Content: []content{{"text/plain", m.Text}, {"text/html", m.HTML}},
	}
	p.Personalizations = []struct {
		To []address `json:"to"`
	}{{To: []address{{Email: m.To}}}}
	if m.UnsubscribeURL != "" {
		p.Headers = map[string]string{
			"List-Unsubscribe":      "<" + m.UnsubscribeURL + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		}
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}

	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = "https://api.sendgrid.com/v3/mail/send"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := s.Client
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("sendgrid: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return nil
}
