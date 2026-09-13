package alert

import (
	"math"
	"sort"
)

// bucketStats are the aggregates of one time series in one bucket (one step of a range, or the whole window).
type bucketStats struct {
	Count  uint64
	Sum    float64
	Min    float64
	Max    float64
	First  float64
	Last   float64
	TFirst int64 // unix ns
	TLast  int64
	Inc    float64 // sum of positive consecutive differences inside the bucket
	Delta  bool    // delta temporality
}

// Time aggregations (per series over the window).
var timeAggs = map[string]bool{"avg": true, "min": true, "max": true, "sum": true, "last": true, "count": true, "rate": true,
	"p50": true, "p95": true, "p99": true}

// Series aggregations (across the series of a group).
var seriesAggs = map[string]bool{"avg": true, "sum": true, "min": true, "max": true}

func quantileOf(agg string) (float64, bool) {
	switch agg {
	case "p50":
		return 0.5, true
	case "p95":
		return 0.95, true
	case "p99":
		return 0.99, true
	}
	return 0, false
}

// windowValue combines the buckets of one series covering a window of windowSec seconds into one value.
// Buckets are in time order; empty buckets have Count 0. ok is false when the series has no point in the window.
func windowValue(buckets []bucketStats, agg string, windowSec float64) (v float64, ok bool) {
	var (
		cnt       uint64
		sum       float64
		mn        = math.Inf(1)
		mx        = math.Inf(-1)
		last      float64
		inc       float64
		havePrev  bool
		prevLast  float64
		deltaSeen bool
	)
	for _, b := range buckets {
		if b.Count == 0 {
			continue
		}
		cnt += b.Count
		sum += b.Sum
		mn = math.Min(mn, b.Min)
		mx = math.Max(mx, b.Max)
		last = b.Last
		if b.Delta {
			deltaSeen = true
		}
		inc += b.Inc
		if havePrev {
			// Increase across the bucket boundary; a reset (first < previous last) counts as 0.
			inc += math.Max(b.First-prevLast, 0)
		}
		prevLast, havePrev = b.Last, true
	}
	if cnt == 0 {
		return math.NaN(), false
	}
	switch agg {
	case "avg":
		return sum / float64(cnt), true
	case "min":
		return mn, true
	case "max":
		return mx, true
	case "sum":
		return sum, true
	case "count":
		return float64(cnt), true
	case "last":
		return last, true
	case "rate":
		if windowSec <= 0 {
			return math.NaN(), false
		}
		if deltaSeen {
			return sum / windowSec, true
		}
		return inc / windowSec, true
	}
	return math.NaN(), false
}

// seriesAggregate combines per-series values of one group. NaN values are ignored; ok is false if none remain.
func seriesAggregate(values []float64, how string) (float64, bool) {
	var (
		n   int
		sum float64
		mn  = math.Inf(1)
		mx  = math.Inf(-1)
	)
	for _, v := range values {
		if math.IsNaN(v) {
			continue
		}
		n++
		sum += v
		mn = math.Min(mn, v)
		mx = math.Max(mx, v)
	}
	if n == 0 {
		return math.NaN(), false
	}
	switch how {
	case "sum":
		return sum, true
	case "min":
		return mn, true
	case "max":
		return mx, true
	default:
		return sum / float64(n), true
	}
}

// combineQuantiles approximates the quantile of a window from per-bucket quantiles (weighted by point count):
// the weighted median of bucket quantiles for p50, else the weighted quantile of the bucket values.
func combineQuantiles(qs []float64, counts []uint64, q float64) (float64, bool) {
	type wv struct {
		v float64
		w uint64
	}
	var items []wv
	var total uint64
	for i, v := range qs {
		if counts[i] == 0 || math.IsNaN(v) {
			continue
		}
		items = append(items, wv{v, counts[i]})
		total += counts[i]
	}
	if total == 0 {
		return math.NaN(), false
	}
	if len(items) == 1 {
		return items[0].v, true
	}
	sort.Slice(items, func(a, b int) bool { return items[a].v < items[b].v })
	target := q * float64(total)
	var acc float64
	for _, it := range items {
		acc += float64(it.w)
		if acc >= target {
			return it.v, true
		}
	}
	return items[len(items)-1].v, true
}

// roundSig rounds f to 4 significant digits for summaries.
func roundSig(f float64) float64 {
	if f == 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return f
	}
	p := math.Pow(10, 3-math.Floor(math.Log10(math.Abs(f))))
	return math.Round(f*p) / p
}
