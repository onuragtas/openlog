package processor

import (
	"math"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Exponential histogram limits of exponentialBuckets.
const (
	minExpScale = -10 // OTLP allows scales -10..20
	maxExpScale = 20
	// maxExpBuckets bounds the buckets of one data point (the SDK default is at most 160 per sign).
	maxExpBuckets = 1024
)

// explicitLayout accumulates buckets in ascending value order: bounds b[0] < … < b[n-1] and n+1 counts, counts[i]
// holding the values in (b[i-1], b[i]] (counts[0] from -Inf, counts[n] to +Inf).
type explicitLayout struct {
	bounds []float64
	counts []uint64 // one per bound; the +Inf count is appended by done
}

// add appends the bucket ending at upper with c values; a non-increasing or non-finite bound merges c into the
// previous bucket (or the first one).
func (l *explicitLayout) add(upper float64, c uint64) {
	n := len(l.bounds)
	if math.IsInf(upper, 0) || math.IsNaN(upper) || (n > 0 && upper <= l.bounds[n-1]) {
		if n == 0 {
			l.bounds, l.counts = append(l.bounds, -math.MaxFloat64), append(l.counts, 0)
			n = 1
		}
		l.counts[n-1] += c
		return
	}
	l.bounds = append(l.bounds, upper)
	l.counts = append(l.counts, c)
}

func (l *explicitLayout) done(overflow uint64) ([]float64, []uint64) {
	return l.bounds, append(l.counts, overflow)
}

// exponentialBuckets converts the buckets of an exponential histogram data point to the explicit-bounds layout of
// the metrics table. Bucket index i of each sign covers magnitudes (base^i, base^(i+1)] with base = 2^(2^-scale); the
// zero bucket covers [-zero_threshold, zero_threshold]. Every bucket contributes its exact lower boundary as an
// (empty) bucket edge, so gaps between the negative buckets, the zero bucket and sparse positive buckets stay empty
// and interpolation uses the real boundaries. Positive magnitudes beyond float64 count into the +Inf bucket. An
// invalid scale or too many buckets yields no buckets (count, sum and mean are still stored).
func exponentialBuckets(dp *metricspb.ExponentialHistogramDataPoint) ([]float64, []uint64) {
	scale := dp.GetScale()
	neg, pos := dp.GetNegative().GetBucketCounts(), dp.GetPositive().GetBucketCounts()
	if scale < minExpScale || scale > maxExpScale || len(neg)+len(pos) > maxExpBuckets {
		return nil, nil
	}
	base := math.Pow(2, math.Pow(2, -float64(scale)))
	boundary := func(index int64) float64 { return math.Pow(base, float64(index)) }
	var l explicitLayout

	// Negative buckets, largest magnitude first: index i covers [-base^(i+1), -base^i).
	negOffset := int64(dp.GetNegative().GetOffset())
	for k := len(neg) - 1; k >= 0; k-- {
		i := negOffset + int64(k)
		if len(l.bounds) == 0 {
			l.add(-boundary(i+1), 0)
		}
		l.add(-boundary(i), neg[k])
	}
	zt := math.Abs(dp.GetZeroThreshold())
	if len(l.bounds) > 0 || zt > 0 {
		l.add(-zt, 0)
	}
	l.add(zt, dp.GetZeroCount())

	var overflow uint64
	posOffset := int64(dp.GetPositive().GetOffset())
	for k, c := range pos {
		i := posOffset + int64(k)
		upper := boundary(i + 1)
		if math.IsInf(upper, 0) {
			overflow += c
			continue
		}
		l.add(boundary(i), 0)
		l.add(upper, c)
	}
	return l.done(overflow)
}
