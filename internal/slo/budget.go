package slo

import (
	"math"
	"time"

	"github.com/onuragtas/openlog/internal/apm"
)

// Bucket is one time bucket of an SLO's service (aggregated apm_transactions_1m rows, all counts weighted by
// the sampling probability, apm.md §4). Buckets of a range are contiguous and may be empty.
type Bucket struct {
	Start time.Time
	// Requests is Σ weight of entry spans, Errors Σ weight of failed ones.
	Requests, Errors float64
	// Good is the weight of requests meeting the SLI: non-error requests (availability) or requests whose
	// duration is at or below the threshold (latency, from the stored histogram — never from an average).
	Good float64
}

// Add merges o into b (the start of b is kept).
func (b Bucket) Add(o Bucket) Bucket {
	b.Requests += o.Requests
	b.Errors += o.Errors
	b.Good += o.Good
	return b
}

// GoodOf computes the good weight of one rollup row for the SLI of s. hist is the duration histogram of the
// row (only read for a latency SLI); requests and errors are the weighted counts.
func GoodOf(s *SLO, requests, errors float64, hist apm.Hist) float64 {
	var good float64
	if s.SLIType == SLILatency {
		good = apm.HistCountLE(hist, s.LatencyThresholdMs)
	} else {
		good = requests - errors
	}
	return math.Min(math.Max(good, 0), requests)
}

// Sum aggregates buckets into one (start = the first bucket's start).
func Sum(buckets []Bucket) Bucket {
	var out Bucket
	for i, b := range buckets {
		if i == 0 {
			out.Start = b.Start
		}
		out = out.Add(b)
	}
	return out
}

// Budget is the state of an SLO over one range (docs/contracts/slo.md §2).
type Budget struct {
	// Requests is the weighted number of requests in the range, Good those meeting the SLI, Bad the rest.
	Requests, Good, Bad float64
	// SLI is Good / Requests (NaN without requests).
	SLI float64
	// Objective is the target as a fraction (0.999 for 99.9 %).
	Objective float64
	// BudgetRequests is the number of bad requests the objective allows: (1 − objective) × Requests.
	BudgetRequests float64
	// BudgetConsumed is Bad, BudgetRemaining the rest (negative once the objective is missed).
	BudgetConsumed, BudgetRemaining float64
	// ConsumedRatio and RemainingRatio are those shares of BudgetRequests (NaN without requests);
	// RemainingRatio = 1 − ConsumedRatio.
	ConsumedRatio, RemainingRatio float64
	// BurnRate is how many times faster than allowed the budget is spent in this range:
	// (Bad / Requests) / (1 − objective). 1 exhausts the budget exactly at the end of the window.
	BurnRate float64
}

// Met reports whether the objective is met over the range (no requests: true, nothing failed).
func (b Budget) Met() bool { return math.IsNaN(b.SLI) || b.SLI >= b.Objective }

// Compute derives the budget of an aggregated bucket for the objective fraction (0 < objective < 1).
func Compute(b Bucket, objective float64) Budget {
	out := Budget{Requests: b.Requests, Good: b.Good, Objective: objective,
		SLI: math.NaN(), ConsumedRatio: math.NaN(), RemainingRatio: math.NaN(), BurnRate: math.NaN()}
	out.Bad = math.Max(0, b.Requests-b.Good)
	out.BudgetConsumed = out.Bad
	allowed := 1 - objective
	out.BudgetRequests = allowed * b.Requests
	out.BudgetRemaining = out.BudgetRequests - out.Bad
	if b.Requests <= 0 {
		return out
	}
	out.SLI = out.Good / b.Requests
	if allowed > 0 {
		out.BurnRate = (out.Bad / b.Requests) / allowed
		out.ConsumedRatio = out.BurnRate // = Bad / BudgetRequests
		out.RemainingRatio = 1 - out.ConsumedRatio
	}
	return out
}

// BurnWindow is one window pair of a multi-window multi-burn-rate condition (alerting.md §2.11): the budget
// burns at least Factor times too fast over Long and, so that the alert reacts to the current state and
// resolves quickly, also over the trailing Short window.
type BurnWindow struct {
	Name   string        `json:"name"`
	Factor float64       `json:"factor"`
	Long   time.Duration `json:"-"`
	Short  time.Duration `json:"-"`
}

// DefaultBurnWindows are the Google SRE workbook defaults: 14.4× over 1 h (2 % of a 30-day budget in an hour)
// and 6× over 6 h (5 % in six hours), each with a short window of a twelfth of the long one.
var DefaultBurnWindows = []BurnWindow{
	{Name: "fast", Factor: 14.4, Long: time.Hour, Short: 5 * time.Minute},
	{Name: "slow", Factor: 6, Long: 6 * time.Hour, Short: 30 * time.Minute},
}

// Burn is the result of one burn window.
type Burn struct {
	Window BurnWindow
	// Long and Short are the budgets of the two windows ending at the same time.
	Long, Short Budget
	// Rate is min(long burn rate, short burn rate) — the rate the window "sees"; NaN when a window has no
	// requests. Ratio is Rate / Factor: a window is breaching at 1.
	Rate, Ratio float64
}

// Breaching reports whether both windows burn at least Factor times too fast.
func (b Burn) Breaching() bool { return !math.IsNaN(b.Ratio) && b.Ratio >= 1 }

// BurnAt evaluates w over buckets ending at end (buckets must be sorted by start and contiguous).
// Only buckets fully inside a window count, so windows are whole multiples of the bucket width.
func BurnAt(buckets []Bucket, step time.Duration, end time.Time, objective float64, w BurnWindow) Burn {
	out := Burn{Window: w, Rate: math.NaN(), Ratio: math.NaN()}
	out.Long = Compute(Sum(between(buckets, step, end.Add(-w.Long), end)), objective)
	out.Short = Compute(Sum(between(buckets, step, end.Add(-w.Short), end)), objective)
	if math.IsNaN(out.Long.BurnRate) || math.IsNaN(out.Short.BurnRate) {
		return out
	}
	out.Rate = math.Min(out.Long.BurnRate, out.Short.BurnRate)
	if w.Factor > 0 {
		out.Ratio = out.Rate / w.Factor
	}
	return out
}

// between returns the buckets that lie completely in [from, to). Buckets must be contiguous and sorted
// (Load returns them that way), so the range is found by index arithmetic instead of a scan — burn windows
// are evaluated once per preview step.
func between(buckets []Bucket, step time.Duration, from, to time.Time) []Bucket {
	if len(buckets) == 0 || step <= 0 {
		return nil
	}
	origin := buckets[0].Start
	lo := int(math.Ceil(float64(from.Sub(origin)) / float64(step)))
	hi := int(math.Floor(float64(to.Sub(origin)) / float64(step)))
	lo, hi = max(lo, 0), min(hi, len(buckets))
	if lo >= hi {
		return nil
	}
	return buckets[lo:hi]
}

// Point is one point of the UI series: the bucket's own numbers plus the budget burndown, i.e. the share of
// the window's budget still left after the bucket. The budget base is the whole window's requests, so the
// last point equals the window's remaining budget share.
type Point struct {
	T                   time.Time
	Requests, Good, Bad float64
	SLI                 float64 // of the bucket (NaN without requests)
	BurnRate            float64 // of the bucket (NaN without requests)
	RemainingRatio      float64 // after the bucket, over the whole window (NaN without requests in the window)
}

// Series turns buckets into UI points. total is the aggregate of the same buckets (its budget is the base of
// the burndown).
func Series(buckets []Bucket, objective float64) []Point {
	budget := Compute(Sum(buckets), objective).BudgetRequests
	out := make([]Point, 0, len(buckets))
	var cumulativeBad float64
	for _, b := range buckets {
		p := Point{T: b.Start, Requests: b.Requests, Good: b.Good, SLI: math.NaN(),
			BurnRate: math.NaN(), RemainingRatio: math.NaN()}
		p.Bad = math.Max(0, b.Requests-b.Good)
		cumulativeBad += p.Bad
		if b.Requests > 0 {
			bucket := Compute(b, objective)
			p.SLI, p.BurnRate = bucket.SLI, bucket.BurnRate
		}
		if budget > 0 {
			p.RemainingRatio = 1 - cumulativeBad/budget
		}
		out = append(out, p)
	}
	return out
}
