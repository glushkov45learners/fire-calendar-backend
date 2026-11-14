package analysis

import (
	"math"
	"slices"
)

// Anomaly classes (contract 3). Thresholds are a placeholder until the
// formula from the model owner is final; keep all of it in classify().
const (
	Low     = "low"
	Normal  = "normal"
	High    = "high"
	Extreme = "extreme"
)

func classify(pct int) string {
	switch {
	case pct >= 95:
		return Extreme
	case pct >= 80:
		return High
	case pct <= 10:
		return Low
	default:
		return Normal
	}
}

// comparison of one value against its baseline sample.
type comparison struct {
	median  float64
	z       *float64 // nil when the baseline has no spread
	pct     *int     // nil when there is no baseline
	anomaly string
}

func compare(x float64, base []float64) comparison {
	if len(base) == 0 {
		return comparison{anomaly: Normal}
	}
	sorted := slices.Sorted(slices.Values(base))
	c := comparison{median: quantile(sorted, 0.5)}

	mean, sd := meanSD(base)
	if sd > 0 {
		z := round2((x - mean) / sd)
		c.z = &z
	}
	p := percentileRank(x, base)
	c.pct = &p
	c.anomaly = classify(p)
	return c
}

// quantile with linear interpolation over an ascending slice.
func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// meanSD returns the mean and the sample standard deviation.
func meanSD(v []float64) (mean, sd float64) {
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	if len(v) < 2 {
		return mean, 0
	}
	var ss float64
	for _, x := range v {
		ss += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(ss / float64(len(v)-1))
}

// percentileRank uses mid-ranks, so a value equal to the whole baseline
// (for example zero fires in a month that never burns) lands at 50.
func percentileRank(x float64, base []float64) int {
	var below, equal int
	for _, b := range base {
		switch {
		case b < x:
			below++
		case b == x:
			equal++
		}
	}
	return int(math.Round(100 * (float64(below) + 0.5*float64(equal)) / float64(len(base))))
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
