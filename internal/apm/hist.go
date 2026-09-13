package apm

import (
	"math"
	"sort"
)

// Latency histogram (apm.md §4.1): 8 log-linear buckets per power of two of milliseconds.
const (
	HistMinBucket     = -80
	HistMaxBucket     = 200
	BucketsPerOctave  = 8
	minBucketDuration = 1000 // ns
)

// Bucket returns the histogram bucket of a duration. It matches the ClickHouse
// expression in schema/clickhouse/0006_apm.sql.
func Bucket(durationNs uint64) int16 {
	if durationNs <= minBucketDuration {
		return HistMinBucket
	}
	b := math.Ceil(BucketsPerOctave * math.Log2(float64(durationNs)/1e6))
	return int16(math.Max(HistMinBucket, math.Min(HistMaxBucket, b)))
}

// BucketUpperMs is the inclusive upper bound of bucket b in milliseconds.
func BucketUpperMs(b int16) float64 { return math.Pow(2, float64(b)/BucketsPerOctave) }

// BucketLowerMs is the exclusive lower bound of bucket b in milliseconds.
func BucketLowerMs(b int16) float64 {
	if b <= HistMinBucket {
		return 0
	}
	return math.Pow(2, float64(b-1)/BucketsPerOctave)
}

// Hist is a merged histogram: weights per bucket (sumMap result).
type Hist struct {
	Keys []int16
	Vals []float64
}

// NewHist sorts keys (sumMap returns them sorted; this also accepts unsorted input).
func NewHist(keys []int16, vals []float64) Hist {
	n := min(len(keys), len(vals))
	h := Hist{Keys: append([]int16(nil), keys[:n]...), Vals: append([]float64(nil), vals[:n]...)}
	if !sort.SliceIsSorted(h.Keys, func(i, j int) bool { return h.Keys[i] < h.Keys[j] }) {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
		for i, j := range idx {
			h.Keys[i], h.Vals[i] = keys[j], vals[j]
		}
	}
	return h
}

// Add merges o into h.
func (h Hist) Add(o Hist) Hist {
	m := map[int16]float64{}
	for i, k := range h.Keys {
		m[k] += h.Vals[i]
	}
	for i, k := range o.Keys {
		m[k] += o.Vals[i]
	}
	keys := make([]int16, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	vals := make([]float64, len(keys))
	for i, k := range keys {
		vals[i] = m[k]
	}
	return Hist{Keys: keys, Vals: vals}
}

// Total is the summed weight.
func (h Hist) Total() float64 {
	var t float64
	for _, v := range h.Vals {
		t += v
	}
	return t
}

// HistQuantile returns the q-quantile (0..1) in milliseconds, or NaN for an empty histogram.
// Inside the bucket where the cumulative weight reaches q*total the value is interpolated
// geometrically between the bucket bounds (linearly in the lowest bucket).
func HistQuantile(h Hist, q float64) float64 {
	total := h.Total()
	if total <= 0 {
		return math.NaN()
	}
	q = math.Min(1, math.Max(0, q))
	target := q * total
	var cum float64
	for i, k := range h.Keys {
		w := h.Vals[i]
		if w <= 0 {
			continue
		}
		if cum+w >= target {
			f := (target - cum) / w
			lo, hi := BucketLowerMs(k), BucketUpperMs(k)
			if lo <= 0 {
				return hi * f
			}
			return lo * math.Pow(hi/lo, f)
		}
		cum += w
	}
	return BucketUpperMs(h.Keys[len(h.Keys)-1])
}

// HistCountLE returns the weight of durations <= ms (the bucket containing ms counts linearly).
func HistCountLE(h Hist, ms float64) float64 {
	var c float64
	for i, k := range h.Keys {
		lo, hi := BucketLowerMs(k), BucketUpperMs(k)
		switch {
		case hi <= ms:
			c += h.Vals[i]
		case lo < ms:
			c += h.Vals[i] * (ms - lo) / (hi - lo)
		}
	}
	return c
}

// HistApdex computes Apdex with threshold tMs from the total request weight and the
// histogram of non-error requests (apm.md §4). ok is false when there are no requests.
func HistApdex(total float64, okHist Hist, tMs float64) (apdex, satisfied, tolerating float64, ok bool) {
	if total <= 0 {
		return 0, 0, 0, false
	}
	satisfied = HistCountLE(okHist, tMs)
	tolerating = math.Max(0, HistCountLE(okHist, 4*tMs)-satisfied)
	apdex = math.Min(1, math.Max(0, (satisfied+tolerating/2)/total))
	return apdex, satisfied, tolerating, true
}
