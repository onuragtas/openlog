package alert

import (
	"math"
	"testing"
	"time"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestWindowValueGauge(t *testing.T) {
	bs := []bucketStats{
		{Count: 2, Sum: 3, Min: 1, Max: 2, First: 1, Last: 2},
		{},
		{Count: 2, Sum: 7, Min: 3, Max: 4, First: 3, Last: 4},
	}
	cases := map[string]float64{"avg": 2.5, "min": 1, "max": 4, "sum": 10, "count": 4, "last": 4}
	for agg, want := range cases {
		if got, ok := windowValue(bs, agg, 60); !ok || !approx(got, want) {
			t.Errorf("%s = %v %v, want %v", agg, got, ok, want)
		}
	}
	if _, ok := windowValue([]bucketStats{{}, {}}, "avg", 60); ok {
		t.Error("empty window reported a value")
	}
}

func TestWindowValueRate(t *testing.T) {
	// Cumulative counter 100 → 160 within bucket 1 (inc 60), 160 → 190 across the boundary and inside bucket 2,
	// then a reset (190 → 5) inside bucket 3 counted as 0 plus 5 → 25.
	bs := []bucketStats{
		{Count: 3, First: 100, Last: 160, Inc: 60, Sum: 390, Min: 100, Max: 160},
		{Count: 2, First: 170, Last: 190, Inc: 20, Sum: 360, Min: 170, Max: 190},
		{Count: 2, First: 5, Last: 25, Inc: 20, Sum: 30, Min: 5, Max: 25},
	}
	// increase = 60 + (170-160) + 20 + max(5-190, 0) + 20 = 110 over 100 s.
	if got, _ := windowValue(bs, "rate", 100); !approx(got, 1.1) {
		t.Errorf("cumulative rate = %v, want 1.1", got)
	}
	// Delta temporality: sum of the points.
	delta := []bucketStats{{Count: 2, Sum: 30, Delta: true}, {Count: 1, Sum: 30, Delta: true}}
	if got, _ := windowValue(delta, "rate", 60); !approx(got, 1) {
		t.Errorf("delta rate = %v, want 1", got)
	}
}

func TestSeriesAggregate(t *testing.T) {
	vals := []float64{0.2, math.NaN(), 0.5, 0.1}
	for how, want := range map[string]float64{"avg": 0.8 / 3, "sum": 0.8, "min": 0.1, "max": 0.5} {
		if got, ok := seriesAggregate(vals, how); !ok || !approx(got, want) {
			t.Errorf("%s = %v, want %v", how, got, want)
		}
	}
	if _, ok := seriesAggregate([]float64{math.NaN()}, "avg"); ok {
		t.Error("all-NaN aggregate reported a value")
	}
}

func TestCombineQuantiles(t *testing.T) {
	v, ok := combineQuantiles([]float64{10, 50, math.NaN()}, []uint64{90, 10, 0}, 0.95)
	if !ok || v != 50 {
		t.Errorf("p95 = %v, want 50", v)
	}
	v, _ = combineQuantiles([]float64{10, 50}, []uint64{90, 10}, 0.5)
	if v != 10 {
		t.Errorf("p50 = %v, want 10", v)
	}
	if _, ok := combineQuantiles([]float64{1}, []uint64{0}, 0.5); ok {
		t.Error("empty quantile reported")
	}
}

func TestPreviewStepAndRangeEnds(t *testing.T) {
	if got := PreviewStep(time.Minute, 6*time.Hour); got != time.Minute {
		t.Errorf("6h at 1m = %v", got)
	}
	if got := PreviewStep(10*time.Second, 24*time.Hour); got != time.Minute {
		t.Errorf("24h at 10s = %v, want 1m (≤1440 steps)", got)
	}
	ends := rangeEnds(t0, t0.Add(5*time.Minute), time.Minute)
	if len(ends) != 5 || !ends[0].Equal(t0.Add(time.Minute)) || !ends[4].Equal(t0.Add(5*time.Minute)) {
		t.Errorf("ends = %v", ends)
	}
	if humanDuration(90*time.Second) != "1m30s" || humanDuration(time.Hour) != "1h" || roundSig(0.123456) != 0.1235 {
		t.Error("formatting helpers")
	}
}
