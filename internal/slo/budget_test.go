package slo

import (
	"math"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/apm"
)

func availability(objective float64) *SLO {
	return &SLO{Input: Input{SLIType: SLIAvailability, Objective: objective, WindowDays: 30}}
}

func latency(thresholdMs, objective float64) *SLO {
	return &SLO{Input: Input{SLIType: SLILatency, LatencyThresholdMs: thresholdMs, Objective: objective, WindowDays: 30}}
}

func nearly(t *testing.T, got, want, tol float64, what string) {
	t.Helper()
	if math.IsNaN(want) {
		if !math.IsNaN(got) {
			t.Errorf("%s = %v, want NaN", what, got)
		}
		return
	}
	if math.IsNaN(got) || math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v (±%v)", what, got, want, tol)
	}
}

// Weighted rows (sampling correction, apm.md §4): the budget uses the weighted counts, not the stored samples.
func TestComputeWeightedAvailability(t *testing.T) {
	s := availability(99) // 1 % of requests may fail
	// 10 000 weighted requests (e.g. 1 000 stored spans at weight 10), 150 of them errors.
	b := Bucket{Requests: 10000, Errors: 150}
	b.Good = GoodOf(s, b.Requests, b.Errors, apm.Hist{})
	got := Compute(b, s.ObjectiveFraction())

	nearly(t, got.Good, 9850, 1e-9, "good")
	nearly(t, got.Bad, 150, 1e-9, "bad")
	nearly(t, got.SLI, 0.985, 1e-9, "sli")
	nearly(t, got.BudgetRequests, 100, 1e-9, "budget requests")
	nearly(t, got.BudgetRemaining, -50, 1e-9, "budget remaining")
	nearly(t, got.RemainingRatio, -0.5, 1e-9, "remaining ratio")
	nearly(t, got.BurnRate, 1.5, 1e-9, "burn rate") // 1.5 % failures against a 1 % budget
	if got.Met() {
		t.Error("objective reported as met at 98.5 % with a 99 % target")
	}
}

// Without requests every ratio is undefined and the objective counts as met (nothing failed).
func TestComputeWithoutRequests(t *testing.T) {
	got := Compute(Bucket{}, 0.999)
	nearly(t, got.SLI, math.NaN(), 0, "sli")
	nearly(t, got.BurnRate, math.NaN(), 0, "burn rate")
	nearly(t, got.RemainingRatio, math.NaN(), 0, "remaining ratio")
	if got.BudgetRequests != 0 || !got.Met() {
		t.Errorf("empty budget = %+v", got)
	}
}

// Latency SLIs read the stored duration histogram, never an average: a few very slow requests must not hide
// behind fast ones (the average of this distribution is far above the threshold, the SLI is not).
func TestGoodOfLatencyUsesHistogram(t *testing.T) {
	h := apm.Hist{}
	// 900 requests around 40 ms, 100 requests around 8 s.
	h = h.Add(apm.NewHist([]int16{apm.Bucket(40e6)}, []float64{900}))
	h = h.Add(apm.NewHist([]int16{apm.Bucket(8000e6)}, []float64{100}))
	s := latency(300, 99)

	good := GoodOf(s, 1000, 0, h)
	nearly(t, good, 900, 1e-9, "good below 300 ms")

	got := Compute(Bucket{Requests: 1000, Good: good}, s.ObjectiveFraction())
	nearly(t, got.SLI, 0.9, 1e-9, "sli")
	nearly(t, got.BurnRate, 10, 1e-9, "burn rate") // 10 % slow against a 1 % budget

	// A threshold inside a bucket counts that bucket linearly (apm.md §4.1): 40 ms lands in the bucket
	// (38.05 ms, 41.51 ms], so a 41 ms threshold counts its share of those 900 requests.
	b := apm.Bucket(40e6)
	lo, hi := apm.BucketLowerMs(b), apm.BucketUpperMs(b)
	nearly(t, GoodOf(latency(41, 99), 1000, 0, h), 900*(41-lo)/(hi-lo), 1e-9, "good below 41 ms")
	// A threshold above the bucket counts all of it.
	nearly(t, GoodOf(latency(50, 99), 1000, 0, h), 900, 1e-9, "good below 50 ms")
	// Errors are not subtracted twice: a latency SLI counts fast requests, errors included.
	if got := GoodOf(latency(300, 99), 1000, 50, h); got != 900 {
		t.Errorf("good with errors = %v, want 900", got)
	}
	// Good is clamped to the request count even when the histogram carries more weight (late merges).
	if got := GoodOf(latency(300, 99), 500, 0, h); got != 500 {
		t.Errorf("clamped good = %v, want 500", got)
	}
}

// Availability never reports negative or excessive good weight.
func TestGoodOfAvailabilityClamped(t *testing.T) {
	s := availability(99)
	if got := GoodOf(s, 10, 12, apm.Hist{}); got != 0 {
		t.Errorf("good with more errors than requests = %v, want 0", got)
	}
	if got := GoodOf(s, 10, 0, apm.Hist{}); got != 10 {
		t.Errorf("good without errors = %v, want 10", got)
	}
}

func minuteBuckets(start time.Time, n int, requests, errors float64) []Bucket {
	out := make([]Bucket, n)
	for i := range out {
		out[i] = Bucket{Start: start.Add(time.Duration(i) * time.Minute), Requests: requests, Errors: errors,
			Good: requests - errors}
	}
	return out
}

// Only whole buckets inside a window count and the window ends are exclusive, so a burn window sees exactly
// its own minutes.
func TestBurnAtWindowEdges(t *testing.T) {
	start := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	// 60 minutes, 100 requests each; the last 5 minutes fail completely, the rest is clean.
	buckets := minuteBuckets(start, 60, 100, 0)
	for i := 55; i < 60; i++ {
		buckets[i].Errors, buckets[i].Good = 100, 0
	}
	end := start.Add(60 * time.Minute)
	objective := 0.99 // 1 % budget

	fast := BurnAt(buckets, time.Minute, end, objective, DefaultBurnWindows[0]) // 1 h / 5 m
	nearly(t, fast.Long.Requests, 6000, 1e-9, "long requests")
	nearly(t, fast.Short.Requests, 500, 1e-9, "short requests")
	nearly(t, fast.Long.BurnRate, 500.0/6000/0.01, 1e-9, "long burn rate") // 8.33×
	nearly(t, fast.Short.BurnRate, 100, 1e-9, "short burn rate")           // everything failed
	nearly(t, fast.Rate, fast.Long.BurnRate, 1e-9, "rate (the smaller one)")
	if fast.Breaching() {
		t.Errorf("fast window breaching at %v× (threshold %v×)", fast.Rate, fast.Window.Factor)
	}

	// One minute later the long window has 6 failing minutes and crosses 14.4×.
	buckets = append(buckets, Bucket{Start: end, Requests: 100, Errors: 100})
	fast = BurnAt(buckets, time.Minute, end.Add(time.Minute), objective, DefaultBurnWindows[0])
	nearly(t, fast.Long.BurnRate, 600.0/6000/0.01, 1e-9, "long burn rate") // 10×
	if fast.Breaching() {
		t.Error("fast window breaching at 10×")
	}

	// A short window that ends before the failures sees a clean budget even while the long window burns.
	early := BurnAt(buckets, time.Minute, start.Add(50*time.Minute), objective, DefaultBurnWindows[0])
	nearly(t, early.Short.BurnRate, 0, 1e-9, "short burn rate before the failures")
	nearly(t, early.Rate, 0, 1e-9, "rate before the failures")

	// A window without any bucket (before the data) has no value at all.
	empty := BurnAt(buckets, time.Minute, start, objective, DefaultBurnWindows[0])
	nearly(t, empty.Rate, math.NaN(), 0, "rate without data")
	if empty.Breaching() {
		t.Error("window without data reported as breaching")
	}
}

// Both windows must burn: a single bad minute an hour ago does not fire, a sustained outage does.
func TestBurnAtMultiWindow(t *testing.T) {
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	buckets := minuteBuckets(start, 360, 100, 0) // 6 h clean
	end := start.Add(360 * time.Minute)
	objective := 0.999

	// 30 minutes of 20 % failures at the end: 6 000 bad of 36 000 requests in 6 h.
	for i := 330; i < 360; i++ {
		buckets[i].Errors, buckets[i].Good = 20, 80
	}
	slow := BurnAt(buckets, time.Minute, end, objective, DefaultBurnWindows[1]) // 6 h / 30 m
	nearly(t, slow.Long.BurnRate, 600.0/36000/0.001, 1e-9, "6 h burn rate")     // 16.7×
	nearly(t, slow.Short.BurnRate, 0.2/0.001, 1e-9, "30 m burn rate")           // 200×
	if !slow.Breaching() {
		t.Errorf("slow window not breaching: rate %v, ratio %v", slow.Rate, slow.Ratio)
	}
	nearly(t, slow.Ratio, slow.Rate/6, 1e-9, "ratio")
}

// The series burns the window's budget down cumulatively and ends at the window's remaining share.
func TestSeriesBurndown(t *testing.T) {
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	buckets := minuteBuckets(start, 4, 1000, 0)
	buckets[1].Errors, buckets[1].Good = 5, 995
	buckets[3].Errors, buckets[3].Good = 15, 985
	objective := 0.99 // budget: 40 of 4 000 requests

	points := Series(buckets, objective)
	if len(points) != 4 {
		t.Fatalf("points = %d, want 4", len(points))
	}
	nearly(t, points[0].RemainingRatio, 1, 1e-9, "remaining after a clean bucket")
	nearly(t, points[1].RemainingRatio, 1-5.0/40, 1e-9, "remaining after 5 bad")
	nearly(t, points[2].RemainingRatio, 1-5.0/40, 1e-9, "remaining stays flat without failures")
	nearly(t, points[3].RemainingRatio, 1-20.0/40, 1e-9, "remaining at the end")
	nearly(t, points[1].BurnRate, 0.005/0.01, 1e-9, "bucket burn rate")
	nearly(t, points[0].BurnRate, 0, 1e-9, "clean bucket burn rate")

	total := Compute(Sum(buckets), objective)
	nearly(t, points[3].RemainingRatio, total.RemainingRatio, 1e-9, "last point equals the window budget")

	// Empty buckets keep the line flat and have no SLI of their own.
	buckets = append(buckets, Bucket{Start: start.Add(4 * time.Minute)})
	points = Series(buckets, objective)
	nearly(t, points[4].SLI, math.NaN(), 0, "sli of an empty bucket")
	nearly(t, points[4].RemainingRatio, points[3].RemainingRatio, 1e-9, "remaining over an empty bucket")
}

// Bucketing is exact: the budget of a range does not depend on the step used to read it.
func TestSumIsStepIndependent(t *testing.T) {
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	minutes := minuteBuckets(start, 60, 100, 1)
	coarse := []Bucket{{Start: start, Requests: 6000, Errors: 60, Good: 5940}}
	a, b := Compute(Sum(minutes), 0.995), Compute(Sum(coarse), 0.995)
	nearly(t, a.SLI, b.SLI, 1e-12, "sli")
	nearly(t, a.BurnRate, b.BurnRate, 1e-12, "burn rate")
	nearly(t, a.RemainingRatio, b.RemainingRatio, 1e-12, "remaining ratio")
}
