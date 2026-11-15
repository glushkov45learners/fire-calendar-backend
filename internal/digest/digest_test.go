package digest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glushkov45learners/fire-calendar-backend/internal/analysis"
	"github.com/glushkov45learners/fire-calendar-backend/internal/mail"
	"github.com/glushkov45learners/fire-calendar-backend/internal/store"
)

func date(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

var (
	first = date("2020-01-01")
	last  = date("2026-11-14")
)

// Octobers 2020..2025 have 100, 200, ... 600 detections on the 10th;
// October 2026 has 900 on the 14th and 100 on the 20th.
func sampleDays() []analysis.Day {
	var days []analysis.Day
	for i, y := range []string{"2020", "2021", "2022", "2023", "2024", "2025"} {
		days = append(days, analysis.Day{Date: date(y + "-10-10"), CountH: float64(100 * (i + 1))})
	}
	return append(days,
		analysis.Day{Date: date("2026-10-14"), CountH: 900, Modis: 120, Viirs: 1234},
		analysis.Day{Date: date("2026-10-20"), CountH: 100},
		analysis.Day{Date: date("2026-07-01"), CountH: 2000},
	)
}

func TestBuild(t *testing.T) {
	s := analysis.New(sampleDays(), first, last)
	d, ok := Build(s, first, last, 2026, time.October)
	if !ok {
		t.Fatal("October 2026 is fully covered, want ok")
	}
	if d.Period != "2026-10" || d.MonthName != "October" || d.Year != 2026 {
		t.Errorf("period: got %q %q %d", d.Period, d.MonthName, d.Year)
	}
	if d.Count != "1,000" || d.Median != "350" {
		t.Errorf("count %q median %q, want 1,000 and 350", d.Count, d.Median)
	}
	if d.Versus != "2.9× the October norm" {
		t.Errorf("versus: got %q", d.Versus)
	}
	if d.Anomaly != analysis.Extreme || d.AnomalyLabel != "Extreme" || d.AnomalyColor != "#fcffa4" {
		t.Errorf("anomaly: got %q %q %q", d.Anomaly, d.AnomalyLabel, d.AnomalyColor)
	}
	if d.Rank != 1 || d.Years != 7 || d.Ordinal() != "1st" {
		t.Errorf("rank %d of %d (%s), want 1st of 7", d.Rank, d.Years, d.Ordinal())
	}
	if d.PeakDate != "14 October" || d.PeakCount != "900" {
		t.Errorf("peak: got %q %q", d.PeakDate, d.PeakCount)
	}
	if d.Modis != "120" || d.Viirs != "1,234" {
		t.Errorf("raw: got %q %q", d.Modis, d.Viirs)
	}
	if len(d.Bars) != 12 || d.Bars[0].Label != "Nov" || !d.Bars[11].Current || d.Bars[11].Label != "Oct" {
		t.Fatalf("bars: got %+v", d.Bars)
	}
	// July (2000) is the tallest bar; October (1000) is half of it; empty months keep a sliver.
	if d.Bars[8].Height != barMaxPx || d.Bars[11].Height != barMaxPx/2 || d.Bars[0].Height != 2 {
		t.Errorf("bar heights: got %d %d %d", d.Bars[8].Height, d.Bars[11].Height, d.Bars[0].Height)
	}
}

func TestBuildNeedsTheWholeMonth(t *testing.T) {
	s := analysis.New(sampleDays(), first, last)
	if _, ok := Build(s, first, last, 2026, time.November); ok {
		t.Error("November 2026 is partial, want !ok")
	}
	if _, ok := Build(s, first, last, 2019, time.December); ok {
		t.Error("December 2019 is before the data, want !ok")
	}
}

func TestVersusAndOrdinal(t *testing.T) {
	for _, c := range []struct {
		count, median float64
		want          string
	}{
		{130, 100, "30% above the May norm"},
		{60, 100, "40% below the May norm"},
		{101, 100, "In line with the May norm"},
		{0, 0, "No fires, as usual for May"},
		{5, 0, "May normally has no fires at all"},
	} {
		if got := versus(c.count, c.median, "May"); got != c.want {
			t.Errorf("versus(%v, %v) = %q, want %q", c.count, c.median, got, c.want)
		}
	}
	for rank, want := range map[int]string{2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 21: "21st"} {
		if got := (Data{Rank: rank}).Ordinal(); got != want {
			t.Errorf("Ordinal(%d) = %q, want %q", rank, got, want)
		}
	}
}

func TestRender(t *testing.T) {
	d, _ := Build(analysis.New(sampleDays(), first, last), first, last, 2026, time.October)
	l := Links{Site: "https://example.org/", Unsubscribe: "https://example.org/api/v1/unsubscribe?token=abc"}
	html, text, err := Render(d, l)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"October 2026", "1,000", "2.9× the October norm", "Extreme", "1st of 7 Octobers",
		"14 October", "color:#fcffa4", "background-color:#ed6925", "height:48px",
		`href="https://example.org/"`, `href="https://example.org/api/v1/unsubscribe?token=abc"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html lacks %q", want)
		}
	}
	// html/template replaces values it does not trust in CSS with this marker.
	if strings.Contains(html, "ZgotmplZ") {
		t.Error("html contains an escaped-away CSS value")
	}
	for _, want := range []string{"October 2026", "1,000", "Unsubscribe: " + l.Unsubscribe, "Oct  1,000  <-"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

type fakeStore struct {
	last     time.Time
	subs     []store.Subscriber
	claimed  map[string]bool
	finished string
	sums     int // how many times the country sum was computed
}

func (f *fakeStore) Coverage(context.Context) (store.Coverage, error) {
	return store.Coverage{First: first, Last: f.last}, nil
}
func (f *fakeStore) CountryDailySums(context.Context) ([]analysis.Day, error) {
	f.sums++
	return sampleDays(), nil
}
func (f *fakeStore) Subscribers(context.Context) ([]store.Subscriber, error) { return f.subs, nil }
func (f *fakeStore) ClaimDigest(_ context.Context, period string) (bool, error) {
	if f.claimed[period] {
		return false, nil
	}
	f.claimed[period] = true
	return true, nil
}
func (f *fakeStore) FinishDigest(_ context.Context, period string, sent, failed int) error {
	f.finished = period
	return nil
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []mail.Message
	bad  string // this recipient always fails
}

func (f *fakeMailer) Send(_ context.Context, m mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.To == f.bad {
		return errors.New("rejected")
	}
	f.sent = append(f.sent, m)
	return nil
}

func newService(now string, st *fakeStore, m Mailer) *Service {
	return &Service{
		Store:     st,
		Mailer:    m,
		PublicURL: "https://example.org",
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:       func() time.Time { return date(now) },
	}
}

func twoSubscribers() *fakeStore {
	return &fakeStore{
		last:    last,
		subs:    []store.Subscriber{{Email: "a@example.org", Token: "ta"}, {Email: "b@example.org", Token: "tb"}},
		claimed: map[string]bool{},
	}
}

func TestSendAll(t *testing.T) {
	st, m := twoSubscribers(), &fakeMailer{bad: "b@example.org"}
	svc := newService("2026-11-15", st, m)

	res, err := svc.SendAll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{Period: "2026-10", Sent: 1, Failed: 1}) {
		t.Errorf("result: got %+v", res)
	}
	if len(m.sent) != 1 {
		t.Fatalf("sent %d letters, want 1", len(m.sent))
	}
	got := m.sent[0]
	unsub := "https://example.org/api/v1/unsubscribe?token=ta"
	if got.To != "a@example.org" || got.Subject != "Fire Calendar: October 2026 in Ukraine" ||
		got.UnsubscribeURL != unsub || !strings.Contains(got.HTML, unsub) || !strings.Contains(got.Text, unsub) {
		t.Errorf("letter: to %q subject %q unsubscribe %q", got.To, got.Subject, got.UnsubscribeURL)
	}
	if len(st.claimed) != 0 {
		t.Error("a manual mailing must not claim the scheduler's period")
	}

	// The button can be pressed again, and the country sum is not recomputed.
	if _, err := svc.SendAll(t.Context()); err != nil || len(m.sent) != 2 || st.sums != 1 {
		t.Errorf("second mailing: err %v, %d letters, %d sums", err, len(m.sent), st.sums)
	}
}

func TestSendAllWithoutMailer(t *testing.T) {
	if _, err := newService("2026-11-15", twoSubscribers(), nil).SendAll(t.Context()); !errors.Is(err, ErrMailOff) {
		t.Errorf("got %v, want ErrMailOff", err)
	}
}

func TestLatestNeedsLastMonthLoaded(t *testing.T) {
	st := twoSubscribers()
	st.last = date("2026-10-30") // October is not complete
	if _, err := newService("2026-11-02", st, nil).Latest(t.Context()); !errors.Is(err, ErrNotReady) {
		t.Errorf("got %v, want ErrNotReady", err)
	}
}

func TestTick(t *testing.T) {
	t.Run("mails once at the start of the month", func(t *testing.T) {
		st, m := twoSubscribers(), &fakeMailer{}
		svc := newService("2026-11-01", st, m)
		for range 3 {
			if err := svc.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if len(m.sent) != 2 || st.finished != "2026-10" {
			t.Errorf("sent %d letters, finished %q; want 2 and 2026-10", len(m.sent), st.finished)
		}
	})
	t.Run("stays quiet in the middle of the month", func(t *testing.T) {
		st, m := twoSubscribers(), &fakeMailer{}
		if err := newService("2026-11-15", st, m).Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(m.sent) != 0 || len(st.claimed) != 0 {
			t.Errorf("sent %d letters, claimed %v; want nothing", len(m.sent), st.claimed)
		}
	})
	t.Run("waits for the data without claiming", func(t *testing.T) {
		st, m := twoSubscribers(), &fakeMailer{}
		st.last = date("2026-10-30")
		if err := newService("2026-11-01", st, m).Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(m.sent) != 0 || len(st.claimed) != 0 {
			t.Errorf("sent %d letters, claimed %v; want nothing", len(m.sent), st.claimed)
		}
	})
}
