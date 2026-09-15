package api

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// distRow is one (series, step bucket) of a histogram or summary query.
type distRow struct {
	series    uint64
	g         []string
	t         int64 // bucket start, unix ms
	clast     uint64
	csum      uint64
	slast     float64
	ssum      float64
	blast     []uint64 // histogram: latest bucket counts in the bucket
	bsum      []uint64 // histogram: bucket counts summed over the bucket's points (delta temporality)
	bounds    []float64
	quantiles []float64 // summary: quantile levels of the latest point
	qvalues   []float64
}

// distBucket is the merged distribution of one group in one step bucket.
type distBucket struct {
	g        []string
	t        int64
	bounds   []float64
	counts   []uint64
	count    uint64
	sum      float64
	hasCount bool
	// summary quantiles: level -> (sum of values, series)
	qsum map[float64]float64
	qn   map[float64]int
}

// distAccumulator turns per-series rows (ordered by series, then time) into per-group increases.
type distAccumulator struct {
	delta bool
	prev  *distRow
	byKey map[string]*distBucket
	order []*distBucket
}

func newDistAccumulator(delta bool) *distAccumulator {
	return &distAccumulator{delta: delta, byKey: map[string]*distBucket{}}
}

func (a *distAccumulator) bucket(g []string, t int64) *distBucket {
	k := strings.Join(g, "\x00") + "\x01" + strconv.FormatInt(t, 10)
	b, ok := a.byKey[k]
	if !ok {
		b = &distBucket{g: g, t: t, qsum: map[float64]float64{}, qn: map[float64]int{}}
		a.byKey[k] = b
		a.order = append(a.order, b)
	}
	return b
}

// cumulativeDelta returns cur - prev, or cur after a reset (prev > cur).
func cumulativeDelta(cur, prev uint64) uint64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// bucketDelta returns the element-wise increase of cumulative bucket counts; a reset or changed layout yields cur.
func bucketDelta(cur, prev []uint64) []uint64 {
	out := make([]uint64, len(cur))
	if len(cur) != len(prev) {
		copy(out, cur)
		return out
	}
	for i := range cur {
		if cur[i] < prev[i] {
			copy(out, cur)
			return out
		}
		out[i] = cur[i] - prev[i]
	}
	return out
}

func (a *distAccumulator) add(row *distRow) {
	prev := a.prev
	if prev != nil && prev.series != row.series {
		prev = nil
	}
	a.prev = row
	b := a.bucket(row.g, row.t)
	// Summary quantiles are point-in-time values: averaged across the group's series.
	for i, lvl := range row.quantiles {
		if i < len(row.qvalues) && finite(row.qvalues[i]) {
			b.qsum[lvl] += row.qvalues[i]
			b.qn[lvl]++
		}
	}
	var counts []uint64
	var count uint64
	var sum float64
	switch {
	case a.delta:
		counts, count, sum = row.bsum, row.csum, row.ssum
	case prev == nil:
		// The first bucket of a cumulative series has no increase.
		return
	default:
		counts = bucketDelta(row.blast, prev.blast)
		count = cumulativeDelta(row.clast, prev.clast)
		if row.clast < prev.clast || row.slast < prev.slast {
			sum = row.slast
		} else {
			sum = row.slast - prev.slast
		}
	}
	b.count += count
	b.sum += sum
	b.hasCount = true
	if len(counts) == 0 || len(row.bounds)+1 != len(counts) {
		return
	}
	if b.bounds == nil {
		b.bounds, b.counts = row.bounds, make([]uint64, len(counts))
	} else if !equalBounds(b.bounds, row.bounds) {
		return // series with a different bucket layout cannot be merged
	}
	for i, c := range counts {
		b.counts[i] += c
	}
}

func equalBounds(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (a *distAccumulator) buckets() []*distBucket {
	sort.SliceStable(a.order, func(i, j int) bool { return a.order[i].t < a.order[j].t })
	return a.order
}

var quantileLevels = map[string]float64{"p50": 0.5, "p75": 0.75, "p90": 0.9, "p95": 0.95, "p99": 0.99}

// value returns the aggregation of the bucket; ok=false when it has no data for it.
func (b *distBucket) value(agg string, step time.Duration) (float64, bool) {
	if lvl, ok := quantileLevels[agg]; ok {
		if len(b.qn) > 0 {
			for l, n := range b.qn {
				if math.Abs(l-lvl) < 1e-9 && n > 0 {
					return b.qsum[l] / float64(n), true
				}
			}
			return 0, false
		}
		v := histogramQuantile(lvl, b.bounds, b.counts)
		return v, !math.IsNaN(v)
	}
	if !b.hasCount {
		return 0, false
	}
	switch agg {
	case "avg":
		if b.count == 0 {
			return 0, false
		}
		return b.sum / float64(b.count), true
	case "count":
		return float64(b.count), true
	case "sum":
		return b.sum, true
	case "rate":
		return float64(b.count) / step.Seconds(), true
	}
	return 0, false
}

// histogramQuantile estimates the q-quantile of explicit-bounds bucket counts (len(counts) = len(bounds)+1) by linear
// interpolation inside the bucket holding the rank, like Prometheus histogram_quantile: the first bucket starts at 0
// when its upper bound is positive, and a rank in the overflow bucket returns the largest bound. NaN without data.
func histogramQuantile(q float64, bounds []float64, counts []uint64) float64 {
	if len(counts) == 0 || len(counts) != len(bounds)+1 || q < 0 || q > 1 {
		return math.NaN()
	}
	var total uint64
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return math.NaN()
	}
	rank := q * float64(total)
	var cum float64
	for i, c := range counts {
		if c == 0 || cum+float64(c) < rank {
			cum += float64(c)
			continue
		}
		if i == len(bounds) {
			return bounds[len(bounds)-1]
		}
		upper := bounds[i]
		lower := 0.0
		switch {
		case i > 0:
			lower = bounds[i-1]
		case upper <= 0:
			return upper
		}
		return lower + (upper-lower)*(rank-cum)/float64(c)
	}
	if len(bounds) == 0 {
		return math.NaN()
	}
	return bounds[len(bounds)-1]
}
