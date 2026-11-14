// Package analysis turns per-day sums over an area into the calendar,
// norms and anomalies of contract 3. It is pure: the store supplies the
// days, the API supplies the area.
package analysis

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// Day is the sum over the area's cells for one UTC day.
type Day struct {
	Date   time.Time
	CountH float64
	FrpH   float64
	Modis  int
	Viirs  int
}

type month struct {
	year   int
	month  time.Month
	countH float64
	frpH   float64
	modis  int
	viirs  int
}

// Series is the dense daily and monthly history of one area over the whole
// data range, with the baseline (all full calendar years) precomputed.
type Series struct {
	first, last time.Time
	days        []Day   // index = days since first
	months      []month // index = months since first's month
	baseYears   []int
	baseline    [12][]float64 // count_h of each calendar month over baseYears
}

// New builds a series over [first, last]. Days outside the range are ignored,
// missing days are zero.
func New(sparse []Day, first, last time.Time) *Series {
	s := &Series{first: first, last: last}

	s.days = make([]Day, dayIndex(first, last)+1)
	for i := range s.days {
		s.days[i].Date = first.AddDate(0, 0, i)
	}
	for _, d := range sparse {
		if i := dayIndex(first, d.Date); i >= 0 && i < len(s.days) {
			s.days[i] = d
		}
	}

	for _, d := range s.days {
		if n := len(s.months); n == 0 || s.months[n-1].month != d.Date.Month() {
			s.months = append(s.months, month{year: d.Date.Year(), month: d.Date.Month()})
		}
		m := &s.months[len(s.months)-1]
		m.countH += d.CountH
		m.frpH += d.FrpH
		m.modis += d.Modis
		m.viirs += d.Viirs
	}

	firstFull, lastFull := first.Year(), last.Year()
	if first.YearDay() != 1 {
		firstFull++
	}
	if last.Month() != time.December || last.Day() != 31 {
		lastFull--
	}
	for y := firstFull; y <= lastFull; y++ {
		s.baseYears = append(s.baseYears, y)
		for m := time.January; m <= time.December; m++ {
			s.baseline[m-1] = append(s.baseline[m-1], s.monthAt(y, m).countH)
		}
	}
	return s
}

// BaselineYears returns the first and last full year, or ok=false if none.
func (s *Series) BaselineYears() (first, last int, ok bool) {
	if len(s.baseYears) == 0 {
		return 0, 0, false
	}
	return s.baseYears[0], s.baseYears[len(s.baseYears)-1], true
}

func (s *Series) monthAt(y int, m time.Month) month {
	i := (y-s.first.Year())*12 + int(m) - int(s.first.Month())
	if i < 0 || i >= len(s.months) {
		return month{year: y, month: m}
	}
	return s.months[i]
}

// dayIndex assumes both dates are UTC midnights.
func dayIndex(first, d time.Time) int {
	return int(d.Sub(first) / (24 * time.Hour))
}

// Month is one element of `monthly` (and a row of calendar.csv).
type Month struct {
	Period   string   `json:"period"`
	CountH   float64  `json:"count_h"`
	FrpH     float64  `json:"frp_h"`
	ModisRaw int      `json:"modis_raw"`
	ViirsRaw int      `json:"viirs_raw"`
	Median   float64  `json:"median"`
	Z        *float64 `json:"z"`
	Pct      *int     `json:"pct"`
	Anomaly  string   `json:"anomaly"`
}

// Monthly returns every month whose period overlaps [from, to].
func (s *Series) Monthly(from, to time.Time) []Month {
	out := []Month{}
	lo, hi := from.Year()*12+int(from.Month()), to.Year()*12+int(to.Month())
	for _, m := range s.months {
		if k := m.year*12 + int(m.month); k < lo || k > hi {
			continue
		}
		c := compare(m.countH, s.baseline[m.month-1])
		out = append(out, Month{
			Period:   fmt.Sprintf("%04d-%02d", m.year, m.month),
			CountH:   round2(m.countH),
			FrpH:     round2(m.frpH),
			ModisRaw: m.modis,
			ViirsRaw: m.viirs,
			Median:   round2(c.median),
			Z:        c.z,
			Pct:      c.pct,
			Anomaly:  c.anomaly,
		})
	}
	return out
}

// DayOut is one element of `daily`; raw counts appear only in calendar.csv.
type DayOut struct {
	Date     string  `json:"date"`
	CountH   float64 `json:"count_h"`
	FrpH     float64 `json:"frp_h"`
	ModisRaw int     `json:"-"`
	ViirsRaw int     `json:"-"`
}

// Daily returns every day in [from, to] ∩ data range, zeros included.
func (s *Series) Daily(from, to time.Time) []DayOut {
	out := []DayOut{}
	for _, d := range s.days[s.clamp(from):s.clamp(to.AddDate(0, 0, 1))] {
		out = append(out, DayOut{
			Date:     d.Date.Format(time.DateOnly),
			CountH:   round2(d.CountH),
			FrpH:     round2(d.FrpH),
			ModisRaw: d.Modis,
			ViirsRaw: d.Viirs,
		})
	}
	return out
}

func (s *Series) clamp(d time.Time) int {
	return min(max(dayIndex(s.first, d), 0), len(s.days))
}

type CurrentMonth struct {
	Period       string   `json:"period"`
	CountH       float64  `json:"count_h"`
	Median       float64  `json:"median"`
	Ratio        *float64 `json:"ratio"` // nil when the median is zero
	Pct          *int     `json:"pct"`
	Anomaly      string   `json:"anomaly"`
	DaysObserved int      `json:"days_observed"`
}

// CurrentMonth compares the last (possibly partial) month of data with the
// same first days_observed days of that month in every baseline year, so a
// half-month is not judged against full months.
func (s *Series) CurrentMonth() CurrentMonth {
	y, m, n := s.last.Date()
	partial := func(y int) float64 {
		var sum float64
		start := dayIndex(s.first, time.Date(y, m, 1, 0, 0, 0, 0, time.UTC))
		for i := start; i < start+n && i < len(s.days); i++ {
			if i >= 0 && s.days[i].Date.Month() == m {
				sum += s.days[i].CountH
			}
		}
		return sum
	}

	var base []float64
	for _, by := range s.baseYears {
		base = append(base, partial(by))
	}
	x := partial(y)
	c := compare(x, base)
	out := CurrentMonth{
		Period:       fmt.Sprintf("%04d-%02d", y, m),
		CountH:       round2(x),
		Median:       round2(c.median),
		Pct:          c.pct,
		Anomaly:      c.anomaly,
		DaysObserved: n,
	}
	if c.median > 0 {
		r := round2(x / c.median)
		out.Ratio = &r
	}
	return out
}

type YearTotal struct {
	Year   int     `json:"year"`
	CountH float64 `json:"count_h"`
}

// TopYears returns all full years, highest count_h first.
func (s *Series) TopYears() []YearTotal {
	out := []YearTotal{}
	for _, y := range s.baseYears {
		var sum float64
		for m := time.January; m <= time.December; m++ {
			sum += s.monthAt(y, m).countH
		}
		out = append(out, YearTotal{Year: y, CountH: round2(sum)})
	}
	slices.SortStableFunc(out, func(a, b YearTotal) int { return cmp.Compare(b.CountH, a.CountH) })
	return out
}

type Season struct {
	Month  int     `json:"month"`
	Median float64 `json:"median"`
	P25    float64 `json:"p25"`
	P75    float64 `json:"p75"`
}

// Seasonality returns 12 elements: median and quartiles of each calendar
// month over the baseline years.
func (s *Series) Seasonality() []Season {
	out := make([]Season, 12)
	for i, base := range s.baseline {
		sorted := slices.Sorted(slices.Values(base))
		out[i] = Season{
			Month:  i + 1,
			Median: round2(quantile(sorted, 0.5)),
			P25:    round2(quantile(sorted, 0.25)),
			P75:    round2(quantile(sorted, 0.75)),
		}
	}
	return out
}

type Totals struct {
	CountH      float64 `json:"count_h"`
	HotspotsRaw int     `json:"hotspots_raw"`
}

// Totals sums [from, to]; hotspots_raw counts the raw MODIS + VIIRS S-NPP
// points behind the harmonized series (industrial types excluded).
func (s *Series) Totals(from, to time.Time) Totals {
	var t Totals
	for _, d := range s.days[s.clamp(from):s.clamp(to.AddDate(0, 0, 1))] {
		t.CountH += d.CountH
		t.HotspotsRaw += d.Modis + d.Viirs
	}
	t.CountH = round2(t.CountH)
	return t
}

// Report is everything /analyze returns except the area itself.
type Report struct {
	Monthly      []Month      `json:"monthly"`
	Daily        []DayOut     `json:"daily"`
	CurrentMonth CurrentMonth `json:"current_month"`
	TopYears     []YearTotal  `json:"top_years"`
	Seasonality  []Season     `json:"seasonality"`
	Totals       Totals       `json:"totals"`
}

// Report builds the /analyze payload. `monthly` always covers the whole
// history; `daily` is empty when [from, to] spans three years or more.
func (s *Series) Report(from, to time.Time) Report {
	daily := []DayOut{}
	if to.Before(from.AddDate(3, 0, 0)) {
		daily = s.Daily(from, to)
	}
	return Report{
		Monthly:      s.Monthly(s.first, s.last),
		Daily:        daily,
		CurrentMonth: s.CurrentMonth(),
		TopYears:     s.TopYears(),
		Seasonality:  s.Seasonality(),
		Totals:       s.Totals(from, to),
	}
}
