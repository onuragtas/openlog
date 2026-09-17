package rum

import (
	"math"
	"sort"
)

// Vital histogram (rum.md §5.1). This is the APM latency histogram of apm.md §4.1 — 8 log-linear buckets per
// power of two — widened downwards, and it is a separate scheme rather than a reuse of internal/apm for one
// reason: APM clamps at bucket -80 because it measures durations and a span shorter than a microsecond is
// noise, while CLS is a unitless score whose *good* values live around 0.05. Clamping CLS at the APM floor
// would put almost every good measurement in one bucket and make its p75 meaningless.
//
// Buckets are therefore clamped at MinBucket = -160 (values at or below 2^-20 ≈ 1e-6) and the same
// MaxBucket = 200 as APM. Page-load durations keep the APM scheme exactly: they are milliseconds like any
// other span duration and share the bucket expression with apm_transactions_1m.
const (
	MinBucket        = -160
	MaxBucket        = 200
	bucketsPerOctave = 8
)

// Bucket returns the histogram bucket of a vital value. It matches the ClickHouse expression in
// schema/clickhouse/0094_rum.sql:
//
//	toInt16(if(v <= 0, -160, greatest(-160, least(200, ceil(8 * log2(v))))))
func Bucket(value float64) int16 {
	if value <= 0 || math.IsNaN(value) {
		return MinBucket
	}
	b := math.Ceil(bucketsPerOctave * math.Log2(value))
	return int16(math.Max(MinBucket, math.Min(MaxBucket, b)))
}

// BucketUpper is the inclusive upper bound of bucket b.
func BucketUpper(b int16) float64 { return math.Pow(2, float64(b)/bucketsPerOctave) }

// BucketLower is the exclusive lower bound of bucket b; the lowest bucket starts at 0.
func BucketLower(b int16) float64 {
	if b <= MinBucket {
		return 0
	}
	return math.Pow(2, float64(b-1)/bucketsPerOctave)
}

// Hist is a merged vital histogram: the weight per bucket, as sumMap returns it.
type Hist struct {
	Keys []int16
	Vals []float64
}

// NewHist pairs keys with values and sorts them (sumMap already returns sorted keys; unsorted input is
// accepted so callers do not have to care where the arrays came from).
func NewHist(keys []int16, vals []float64) Hist {
	n := min(len(keys), len(vals))
	h := Hist{Keys: append([]int16(nil), keys[:n]...), Vals: append([]float64(nil), vals[:n]...)}
	if sort.SliceIsSorted(h.Keys, func(i, j int) bool { return h.Keys[i] < h.Keys[j] }) {
		return h
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
	for i, j := range idx {
		h.Keys[i], h.Vals[i] = keys[j], vals[j]
	}
	return h
}

// Total is the summed weight.
func (h Hist) Total() float64 {
	var t float64
	for _, v := range h.Vals {
		t += v
	}
	return t
}

// Quantile returns the weighted quantile q (0..1) of the histogram, interpolating geometrically inside the
// bucket the cumulative weight reaches it in — the same walk as apm.HistQuantile, so a RUM percentile and an
// APM percentile are computed the same way and are off by at most one bucket width (< 9.1 %). It returns
// NaN for an empty histogram, which the API renders as null rather than as a confident zero.
func (h Hist) Quantile(q float64) float64 {
	total := h.Total()
	if total <= 0 || len(h.Keys) == 0 {
		return math.NaN()
	}
	q = math.Max(0, math.Min(1, q))
	target := q * total
	var cum float64
	for i, k := range h.Keys {
		w := h.Vals[i]
		if w <= 0 {
			continue
		}
		if cum+w < target {
			cum += w
			continue
		}
		lo, hi := BucketLower(k), BucketUpper(k)
		if lo <= 0 {
			// The lowest bucket has no meaningful lower bound to interpolate from.
			return hi
		}
		frac := (target - cum) / w
		return lo * math.Pow(hi/lo, math.Max(0, math.Min(1, frac)))
	}
	return BucketUpper(h.Keys[len(h.Keys)-1])
}
