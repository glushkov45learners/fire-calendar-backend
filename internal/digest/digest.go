// Package digest builds and mails the monthly e-mail: last month's burning
// activity over Ukraine against the norm of that calendar month.
package digest

import (
	"bytes"
	_ "embed"
	"fmt"
	htmltemplate "html/template"
	"math"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/glushkov45learners/fire-calendar-backend/internal/analysis"
)

// Bar is one month of the "last 12 months" chart.
type Bar struct {
	Label   string // "Oct"
	Count   string // "12,345"
	Height  int    // px, 2..barMaxPx
	Current bool   // the month the digest is about
}

const barMaxPx = 96

// Data is everything the letter shows, already formatted for people.
type Data struct {
	Period    string // "2026-10"
	MonthName string // "October"
	Year      int

	Count  string // harmonized detections in the month
	Median string // norm: median of this calendar month over the baseline years
	Versus string // "2.3× the October norm"

	Anomaly      string // low | normal | high | extreme
	AnomalyLabel string // "Extreme"
	AnomalyColor string
	Pct          *int // percentile among the baseline, nil without a baseline

	Rank  int // 1 = the most active such month on record
	Years int // how many such months the archive has

	PeakDate  string // "14 October", empty if the month had no detections
	PeakCount string

	Modis string // raw MODIS detections
	Viirs string // raw VIIRS S-NPP detections

	Bars []Bar
}

// Subject is the e-mail subject line.
func (d Data) Subject() string {
	return fmt.Sprintf("Fire Calendar: %s %d in Ukraine", d.MonthName, d.Year)
}

// Colours of the frontend's anomaly classes (index.css, .cls-*).
var anomalyColors = map[string]string{
	analysis.Low:     "#9ecae8",
	analysis.Normal:  "#f0f0fa",
	analysis.High:    "#ed6925",
	analysis.Extreme: "#fcffa4",
}

// Build describes the calendar month (year, month) of a series over
// [first, last]. ok is false if the data does not cover the whole month.
func Build(s *analysis.Series, first, last time.Time, year int, month time.Month) (d Data, ok bool) {
	start := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, -1)
	if start.Before(first) || end.After(last) {
		return d, false
	}
	months := s.Monthly(start, end)
	if len(months) != 1 {
		return d, false
	}
	m := months[0]

	d = Data{
		Period:       m.Period,
		MonthName:    month.String(),
		Year:         year,
		Count:        group(m.CountH),
		Median:       group(m.Median),
		Versus:       versus(m.CountH, m.Median, month.String()),
		Anomaly:      m.Anomaly,
		AnomalyLabel: strings.ToUpper(m.Anomaly[:1]) + m.Anomaly[1:],
		AnomalyColor: anomalyColors[m.Anomaly],
		Pct:          m.Pct,
		Modis:        group(float64(m.ModisRaw)),
		Viirs:        group(float64(m.ViirsRaw)),
	}

	// Rank among the same calendar month of every year. The only partial
	// month of the archive is the current one, which is never this one.
	suffix := m.Period[4:]
	d.Rank = 1
	for _, o := range s.Monthly(first, last) {
		if !strings.HasSuffix(o.Period, suffix) || o.Period > m.Period {
			continue
		}
		d.Years++
		if o.CountH > m.CountH {
			d.Rank++
		}
	}

	var peak analysis.DayOut
	for _, day := range s.Daily(start, end) {
		if day.CountH > peak.CountH {
			peak = day
		}
	}
	if peak.CountH > 0 {
		t, _ := time.Parse(time.DateOnly, peak.Date)
		d.PeakDate = t.Format("2 January")
		d.PeakCount = group(peak.CountH)
	}

	year12 := s.Monthly(start.AddDate(0, -11, 0), end)
	var top float64
	for _, o := range year12 {
		top = max(top, o.CountH)
	}
	for _, o := range year12 {
		t, _ := time.Parse("2006-01", o.Period)
		h := 2
		if top > 0 {
			h = max(2, int(math.Round(o.CountH/top*barMaxPx)))
		}
		d.Bars = append(d.Bars, Bar{
			Label:   t.Format("Jan"),
			Count:   group(o.CountH),
			Height:  h,
			Current: o.Period == m.Period,
		})
	}
	return d, true
}

// versus puts the month next to its norm in words.
func versus(count, median float64, month string) string {
	if median <= 0 {
		if count <= 0 {
			return "No fires, as usual for " + month
		}
		return month + " normally has no fires at all"
	}
	switch r := count / median; {
	case r >= 2:
		return fmt.Sprintf("%.1f× the %s norm", r, month)
	case r >= 1.05:
		return fmt.Sprintf("%.0f%% above the %s norm", (r-1)*100, month)
	case r <= 0.95:
		return fmt.Sprintf("%.0f%% below the %s norm", (1-r)*100, month)
	default:
		return "In line with the " + month + " norm"
	}
}

// group formats a count as a whole number with thousands separators.
func group(x float64) string {
	s := strconv.FormatInt(int64(math.Round(x)), 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// Ordinal is Rank as a word: 1 → "1st".
func (d Data) Ordinal() string {
	suffix := "th"
	if d.Rank%100 < 11 || d.Rank%100 > 13 {
		switch d.Rank % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(d.Rank) + suffix
}

// Links are the per-recipient parts of the letter.
type Links struct {
	Site        string // the Fire Calendar frontend
	Unsubscribe string
}

//go:embed email.html
var emailHTML string

//go:embed email.txt
var emailText string

var (
	htmlTmpl = htmltemplate.Must(htmltemplate.New("email.html").Parse(emailHTML))
	textTmpl = texttemplate.Must(texttemplate.New("email.txt").Parse(emailText))
)

// Render returns the HTML and plain-text bodies of the letter.
func Render(d Data, l Links) (html, text string, err error) {
	v := struct {
		Data
		Links
	}{d, l}
	var hb, tb bytes.Buffer
	if err := htmlTmpl.Execute(&hb, v); err != nil {
		return "", "", err
	}
	if err := textTmpl.Execute(&tb, v); err != nil {
		return "", "", err
	}
	return hb.String(), tb.String(), nil
}
