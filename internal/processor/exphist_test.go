package processor

import (
	"math"
	"reflect"
	"testing"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func expPoint(scale int32, zeroCount uint64, zt float64, negOffset int32, neg []uint64, posOffset int32, pos []uint64) *metricspb.ExponentialHistogramDataPoint {
	return &metricspb.ExponentialHistogramDataPoint{
		Scale: scale, ZeroCount: zeroCount, ZeroThreshold: zt,
		Negative: &metricspb.ExponentialHistogramDataPoint_Buckets{Offset: negOffset, BucketCounts: neg},
		Positive: &metricspb.ExponentialHistogramDataPoint_Buckets{Offset: posOffset, BucketCounts: pos},
	}
}

func TestExponentialBuckets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dp     *metricspb.ExponentialHistogramDataPoint
		bounds []float64
		counts []uint64
	}{
		{"positive, base 2", expPoint(0, 3, 0, 0, nil, 0, []uint64{1, 2}),
			[]float64{0, 1, 2, 4}, []uint64{3, 0, 1, 2, 0}},
		{"sparse offset keeps the real lower bound", expPoint(0, 0, 0, 0, nil, 3, []uint64{5}),
			[]float64{0, 8, 16}, []uint64{0, 0, 5, 0}},
		{"negative and zero threshold", expPoint(0, 2, 0.5, 1, []uint64{5}, 0, nil),
			[]float64{-4, -2, -0.5, 0.5}, []uint64{0, 5, 0, 2, 0}},
		{"scale 1 (base √2)", expPoint(1, 0, 0, 0, nil, 0, []uint64{1, 1}),
			[]float64{0, 1, math.Sqrt2, 2}, []uint64{0, 0, 1, 1, 0}},
		{"invalid scale", expPoint(21, 1, 0, 0, nil, 0, []uint64{1}), nil, nil},
	} {
		bounds, counts := exponentialBuckets(tc.dp)
		if len(bounds) != len(tc.bounds) || !reflect.DeepEqual(counts, tc.counts) {
			t.Errorf("%s: %v %v, want %v %v", tc.name, bounds, counts, tc.bounds, tc.counts)
			continue
		}
		for i := range bounds {
			if math.Abs(bounds[i]-tc.bounds[i]) > 1e-12 {
				t.Errorf("%s: bounds %v, want %v", tc.name, bounds, tc.bounds)
			}
		}
	}
	// Invariants: strictly increasing finite bounds, one more count than bounds, no value lost.
	for _, dp := range []*metricspb.ExponentialHistogramDataPoint{
		expPoint(-10, 1, 0, 0, []uint64{2}, 0, []uint64{3, 4}),
		expPoint(20, 1, 1e-9, -5, []uint64{1, 0, 2}, -3, []uint64{4, 0, 0, 5}),
		expPoint(3, 0, 0, 1000, nil, 1000, []uint64{1}),
		expPoint(-2, 7, 0, 300, []uint64{1}, 300, []uint64{2}),
	} {
		bounds, counts := exponentialBuckets(dp)
		if len(counts) != len(bounds)+1 {
			t.Errorf("%v: %d bounds, %d counts", dp, len(bounds), len(counts))
		}
		var want, got uint64 = dp.GetZeroCount(), 0
		for _, c := range append(append([]uint64{}, dp.GetNegative().GetBucketCounts()...), dp.GetPositive().GetBucketCounts()...) {
			want += c
		}
		for _, c := range counts {
			got += c
		}
		if got != want {
			t.Errorf("%v: counts %v sum to %d, want %d", dp, counts, got, want)
		}
		for i := range bounds {
			if math.IsInf(bounds[i], 0) || math.IsNaN(bounds[i]) || (i > 0 && bounds[i] <= bounds[i-1]) {
				t.Errorf("%v: bounds %v not strictly increasing and finite", dp, bounds)
			}
		}
	}
}

func TestAddMetricsDistributions(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	ts := uint64(now.UnixNano())
	req := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
			{Name: "rpc.latency", Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{DataPoints: []*metricspb.SummaryDataPoint{{
				TimeUnixNano: ts, Count: 10, Sum: 25,
				QuantileValues: []*metricspb.SummaryDataPoint_ValueAtQuantile{{Quantile: 0.5, Value: 2}, {Quantile: 0.99, Value: 9}},
			}}}}},
			{Name: "http.duration", Data: &metricspb.Metric_ExponentialHistogram{ExponentialHistogram: &metricspb.ExponentialHistogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints: []*metricspb.ExponentialHistogramDataPoint{func() *metricspb.ExponentialHistogramDataPoint {
					dp := expPoint(0, 1, 0, 0, nil, 0, []uint64{2})
					sum := 4.0
					dp.TimeUnixNano, dp.Count, dp.Sum = ts, 3, &sum
					return dp
				}()},
			}}},
			{Name: "queue.depth", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{
				{TimeUnixNano: ts, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 4}},
				{TimeUnixNano: ts, Flags: uint32(metricspb.DataPointFlags_DATA_POINT_FLAGS_NO_RECORDED_VALUE_MASK)},
			}}}},
		}}},
	}}}
	rows := NewRows()
	rows.AddMetrics("t1", now, req)
	if len(rows.Metrics) != 3 || rows.Dropped["no_recorded_value"] != 1 {
		t.Fatalf("rows %d dropped %v", len(rows.Metrics), rows.Dropped)
	}
	s := rows.Metrics[0]
	if s.MetricType != "summary" || s.Temporality != "cumulative" || !reflect.DeepEqual(s.Quantiles, []float64{0.5, 0.99}) ||
		!reflect.DeepEqual(s.QuantileValues, []float64{2, 9}) || s.Value != 2.5 {
		t.Errorf("summary row %+v", s)
	}
	e := rows.Metrics[1]
	if e.MetricType != "exponential_histogram" || e.Temporality != "delta" || !reflect.DeepEqual(e.ExplicitBounds, []float64{0, 1, 2}) ||
		!reflect.DeepEqual(e.BucketCounts, []uint64{1, 0, 2, 0}) {
		t.Errorf("exponential histogram row %+v", e)
	}
	if g := rows.Metrics[2]; g.MetricName != "queue.depth" || g.Value != 4 {
		t.Errorf("gauge row %+v", g)
	}
	cols := Columns[TableMetrics]
	if v := s.Values(); len(v) != len(cols) || cols[len(cols)-2] != "quantiles" {
		t.Errorf("%d values for %d columns", len(v), len(cols))
	}
}
