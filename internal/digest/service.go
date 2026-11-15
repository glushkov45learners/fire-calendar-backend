package digest

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glushkov45learners/fire-calendar-backend/internal/analysis"
	"github.com/glushkov45learners/fire-calendar-backend/internal/mail"
	"github.com/glushkov45learners/fire-calendar-backend/internal/store"
)

var (
	// ErrMailOff: no SendGrid key is configured, nothing can be sent.
	ErrMailOff = errors.New("mail sending is not configured")
	// ErrNotReady: the archive does not reach the end of last month yet.
	ErrNotReady = errors.New("data for the previous month is not loaded yet")
)

// Store is what the digest needs from the database (*store.Store).
type Store interface {
	Coverage(context.Context) (store.Coverage, error)
	CountryDailySums(context.Context) ([]analysis.Day, error)
	Subscribers(context.Context) ([]store.Subscriber, error)
	ClaimDigest(ctx context.Context, period string) (bool, error)
	FinishDigest(ctx context.Context, period string, sent, failed int) error
}

// Mailer sends one letter (*mail.SendGrid).
type Mailer interface {
	Send(context.Context, mail.Message) error
}

// autoSendDays is how long after the 1st the scheduler still mails last
// month. It leaves room for late data, but a first deploy in the middle of a
// month does not surprise subscribers with an old digest.
const autoSendDays = 7

const sendWorkers = 8

// Service builds the digest of the previous calendar month and mails it.
type Service struct {
	Store     Store
	Mailer    Mailer // nil = sending is off
	PublicURL string // origin of the site, no trailing slash
	Log       *slog.Logger
	Now       func() time.Time // nil = time.Now

	mu     sync.Mutex
	cached Data
	cacheK string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// MailOn reports whether letters can be sent.
func (s *Service) MailOn() bool { return s.Mailer != nil }

// Latest returns the digest of the calendar month before the current one.
func (s *Service) Latest(ctx context.Context) (Data, error) {
	cov, err := s.Store.Coverage(ctx)
	if err != nil {
		return Data{}, err
	}
	now := s.now()
	prev := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)

	// The country-wide sum scans all of cell_day; keep it until new data arrives.
	key := prev.Format("2006-01") + "/" + cov.Last.Format(time.DateOnly)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheK == key {
		return s.cached, nil
	}
	if prev.AddDate(0, 1, -1).After(cov.Last) {
		return Data{}, ErrNotReady
	}
	days, err := s.Store.CountryDailySums(ctx)
	if err != nil {
		return Data{}, err
	}
	d, ok := Build(analysis.New(days, cov.First, cov.Last), cov.First, cov.Last, prev.Year(), prev.Month())
	if !ok {
		return Data{}, ErrNotReady
	}
	s.cached, s.cacheK = d, key
	return d, nil
}

func (s *Service) links(token string) Links {
	return Links{
		Site:        s.PublicURL + "/",
		Unsubscribe: s.PublicURL + "/api/v1/unsubscribe?token=" + url.QueryEscape(token),
	}
}

// Preview renders the letter as a subscriber would see it; its unsubscribe
// link leads nowhere.
func (s *Service) Preview(ctx context.Context) (string, error) {
	d, err := s.Latest(ctx)
	if err != nil {
		return "", err
	}
	html, _, err := Render(d, s.links("preview"))
	return html, err
}

// Result is the outcome of one mailing.
type Result struct {
	Period string `json:"period"`
	Sent   int    `json:"sent"`
	Failed int    `json:"failed"`
}

// SendAll mails last month's digest to every active subscriber, now. It does
// not look at or touch the scheduler's record, so it can be repeated.
func (s *Service) SendAll(ctx context.Context) (Result, error) {
	if s.Mailer == nil {
		return Result{}, ErrMailOff
	}
	d, err := s.Latest(ctx)
	if err != nil {
		return Result{}, err
	}
	subs, err := s.Store.Subscribers(ctx)
	if err != nil {
		return Result{}, err
	}

	var sent, failed atomic.Int64
	jobs := make(chan store.Subscriber)
	var wg sync.WaitGroup
	for range min(sendWorkers, len(subs)) {
		wg.Go(func() {
			for sub := range jobs {
				l := s.links(sub.Token)
				html, text, err := Render(d, l)
				if err == nil {
					err = s.Mailer.Send(ctx, mail.Message{
						To:             sub.Email,
						Subject:        d.Subject(),
						Text:           text,
						HTML:           html,
						UnsubscribeURL: l.Unsubscribe,
					})
				}
				if err != nil {
					failed.Add(1)
					// The address is personal data: log only which letter failed and why.
					s.Log.Error("digest letter failed", "period", d.Period, "err", err)
					continue
				}
				sent.Add(1)
			}
		})
	}
	for _, sub := range subs {
		jobs <- sub
	}
	close(jobs)
	wg.Wait()

	r := Result{Period: d.Period, Sent: int(sent.Load()), Failed: int(failed.Load())}
	s.Log.Info("digest mailed", "period", r.Period, "sent", r.Sent, "failed", r.Failed)
	return r, nil
}

// Tick is one scheduler step: in the first days of a month, mail the previous
// month once. The claim in the database is taken before sending, so a crash
// in the middle loses letters rather than doubling them.
func (s *Service) Tick(ctx context.Context) error {
	if s.Mailer == nil || s.now().Day() > autoSendDays {
		return nil
	}
	d, err := s.Latest(ctx)
	if errors.Is(err, ErrNotReady) || errors.Is(err, store.ErrNoData) {
		return nil // try again next hour
	}
	if err != nil {
		return err
	}
	claimed, err := s.Store.ClaimDigest(ctx, d.Period)
	if err != nil || !claimed {
		return err
	}
	r, err := s.SendAll(ctx)
	if err != nil {
		return err
	}
	return s.Store.FinishDigest(ctx, r.Period, r.Sent, r.Failed)
}

// Run ticks at start and then every hour until ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("digest scheduler", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
