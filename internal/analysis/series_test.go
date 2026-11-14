package analysis

import (
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBaselineFullYearsOnly(t *testing.T) {
	s := New(nil, date("2000-11-01"), date("2026-11-14"))
	first, last, ok := s.BaselineYears()
	if !ok || first != 2001 || last != 2025 {
		t.Fatalf("baseline = %d..%d %v, want 2001..2025", first, last, ok)
	}
	if got := len(s.TopYears()); got != 25 {
		t.Fatalf("top_years = %d, want 25", got)
	}
}

func TestMonthlyZeroFilledAndAnomaly(t *testing.T) {
	// Every April 2001..2010 has 10 fires on the 4th; April 2008 has 100.
	var days []Day
	for y := 2001; y <= 2010; y++ {
		n := 10.0
		if y == 2008 {
			n = 100
		}
		days = append(days, Day{Date: time.Date(y, 4, 4, 0, 0, 0, 0, time.UTC), CountH: n, Modis: 1, Viirs: 2})
	}
	s := New(days, date("2001-01-01"), date("2010-12-31"))

	monthly := s.Monthly(date("2001-01-01"), date("2010-12-31"))
	if len(monthly) != 120 {
		t.Fatalf("monthly = %d, want 120 (zeros included)", len(monthly))
	}
	var apr2008, jan2005 Month
	for _, m := range monthly {
		switch m.Period {
		case "2008-04":
			apr2008 = m
		case "2005-01":
			jan2005 = m
		}
	}
	if apr2008.Median != 10 || apr2008.Anomaly != Extreme || *apr2008.Pct != 95 || apr2008.ModisRaw != 1 {
		t.Errorf("2008-04 = %+v", apr2008)
	}
	if jan2005.CountH != 0 || jan2005.Anomaly != Normal || *jan2005.Pct != 50 || jan2005.Z != nil {
		t.Errorf("2005-01 (never burns) = %+v", jan2005)
	}
	if season := s.Seasonality(); len(season) != 12 || season[3].Median != 10 || season[0].Median != 0 {
		t.Errorf("seasonality = %+v", season)
	}
}

func TestCurrentMonthComparesSameDays(t *testing.T) {
	var days []Day
	for y := 2001; y <= 2003; y++ {
		days = append(days,
			Day{Date: time.Date(y, 11, 2, 0, 0, 0, 0, time.UTC), CountH: 4},
			Day{Date: time.Date(y, 11, 20, 0, 0, 0, 0, time.UTC), CountH: 1000}, // after day 14: must not count
		)
	}
	days = append(days, Day{Date: date("2004-11-10"), CountH: 9})
	s := New(days, date("2001-01-01"), date("2004-11-14"))

	cm := s.CurrentMonth()
	if cm.Period != "2004-11" || cm.DaysObserved != 14 || cm.CountH != 9 || cm.Median != 4 || *cm.Ratio != 2.25 {
		t.Fatalf("current_month = %+v", cm)
	}
}

func TestDailyAndTotalsRange(t *testing.T) {
	days := []Day{{Date: date("2020-04-04"), CountH: 96, FrpH: 1210.2, Modis: 3, Viirs: 14}}
	s := New(days, date("2000-11-01"), date("2026-11-14"))

	r := s.Report(date("2020-04-01"), date("2020-04-30"))
	if len(r.Daily) != 30 || r.Daily[3].CountH != 96 || r.Daily[0].CountH != 0 {
		t.Fatalf("daily = %d entries, [3]=%+v", len(r.Daily), r.Daily[3])
	}
	if r.Totals.CountH != 96 || r.Totals.HotspotsRaw != 17 {
		t.Fatalf("totals = %+v", r.Totals)
	}
	if long := s.Report(date("2020-01-01"), date("2023-01-01")); len(long.Daily) != 0 {
		t.Fatalf("daily over 3 years = %d entries, want 0", len(long.Daily))
	}
	// Range sticking out of the data is clamped, not a panic.
	if d := s.Daily(date("2026-11-10"), date("2027-01-01")); len(d) != 5 {
		t.Fatalf("clamped daily = %d, want 5", len(d))
	}
}
