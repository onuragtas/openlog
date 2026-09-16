package processor

import (
	"testing"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Metric exemplars (exemplars.go, D-130): parsed from every data point kind that carries them, capped per series and
// minute, and always scoped to the tenant of the export request.

const (
	traceA = "5b8efff798038103d269b633813fc60c"
	traceB = "2f1e4d6c8a0b9c7d5e3f1a2b4c6d8e00"
	spanA  = "eee19b7ec3c1b174"
)

// exemplar builds an OTLP exemplar with a double value.
func exemplar(traceID, spanID string, value float64, ts uint64, attrs ...*commonpb.KeyValue) *metricspb.Exemplar {
	e := &metricspb.Exemplar{
		TimeUnixNano:       ts,
		Value:              &metricspb.Exemplar_AsDouble{AsDouble: value},
		FilteredAttributes: attrs,
	}
	if traceID != "" {
		e.TraceId = mustHex(traceID)
	}
	if spanID != "" {
		e.SpanId = mustHex(spanID)
	}
	return e
}

// gaugeWithExemplars is one gauge metric whose single data point carries exemplars.
func gaugeWithExemplars(name string, exemplars ...*metricspb.Exemplar) *colmetrics.ExportMetricsServiceRequest {
	return &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: hostResource(),
		ScopeMetrics: []*metricspb.ScopeMetrics{{
			Scope: &commonpb.InstrumentationScope{Name: "hostmetrics"},
			Metrics: []*metricspb.Metric{{Name: name, Unit: "s", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{
					Attributes:   []*commonpb.KeyValue{s("route", "/checkout")},
					TimeUnixNano: ts1,
					Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 0.5},
					Exemplars:    exemplars,
				}},
			}}}},
		}},
	}}}
}

func TestAddMetricsExemplars(t *testing.T) {
	req := gaugeWithExemplars("http.client.duration",
		exemplar(traceA, spanA, 1.25, ts1, s("http.status_code", "500")),
		// No time of its own: it takes the data point's timestamp.
		exemplar(traceB, "", 0.75, 0))
	r := NewRows()
	r.AddMetrics("t1", recv, req)

	if len(r.Exemplars) != 2 {
		t.Fatalf("exemplars = %d, want 2", len(r.Exemplars))
	}
	e := r.Exemplars[0]
	if e.TenantID != "t1" || e.MetricName != "http.client.duration" || e.MetricType != "gauge" || e.Unit != "s" ||
		e.TraceID != traceA || e.SpanID != spanA || e.Value != 1.25 || !e.Timestamp.Equal(time.Unix(0, int64(ts1))) {
		t.Errorf("exemplar %+v", e)
	}
	if e.FilteredAttributes["http.status_code"] != "500" {
		t.Errorf("filtered attributes %v", e.FilteredAttributes)
	}
	// The exemplar carries its data point's identity, so the explorer's filters apply to it unchanged.
	if e.Attributes["route"] != "/checkout" || e.HostID != "h-1" || e.HostName != "web-1" || e.ScopeName != "hostmetrics" {
		t.Errorf("data point columns %+v", e)
	}
	if e.SeriesID != r.Metrics[0].SeriesID {
		t.Errorf("series id %d != data point series id %d", e.SeriesID, r.Metrics[0].SeriesID)
	}
	if e.ResourceAttributes["env"] != "prod" {
		t.Errorf("resource attributes %v", e.ResourceAttributes)
	}
	// A span id is optional; the timestamp falls back to the data point's.
	if second := r.Exemplars[1]; second.SpanID != "" || second.TraceID != traceB || !second.Timestamp.Equal(time.Unix(0, int64(ts1))) {
		t.Errorf("second exemplar %+v", second)
	}
	if vals := r.Values(TableMetricExemplars); len(vals) != 2 || len(vals[0]) != len(Columns[TableMetricExemplars]) {
		t.Errorf("values/columns mismatch %d vs %d", len(vals[0]), len(Columns[TableMetricExemplars]))
	}
	if r.Len(TableMetricExemplars) != 2 {
		t.Errorf("Len = %d", r.Len(TableMetricExemplars))
	}
}

func TestExemplarsAreTenantScoped(t *testing.T) {
	r := NewRows()
	r.AddMetrics("t1", recv, gaugeWithExemplars("m", exemplar(traceA, spanA, 1, ts1)))
	r.AddMetrics("t2", recv, gaugeWithExemplars("m", exemplar(traceA, spanA, 1, ts1)))
	if len(r.Exemplars) != 2 {
		t.Fatalf("exemplars = %d", len(r.Exemplars))
	}
	if r.Exemplars[0].TenantID != "t1" || r.Exemplars[1].TenantID != "t2" {
		t.Errorf("tenants %q %q", r.Exemplars[0].TenantID, r.Exemplars[1].TenantID)
	}
	// The same series of two tenants must not share a cap slot.
	if r.Exemplars[0].SeriesID == r.Exemplars[1].SeriesID {
		t.Error("series id must include the tenant")
	}
}

func TestExemplarWithoutTraceIsDropped(t *testing.T) {
	r := NewRows()
	r.AddMetrics("t1", recv, gaugeWithExemplars("m", exemplar("", spanA, 1, ts1), exemplar(traceA, spanA, 2, ts1)))
	if len(r.Exemplars) != 1 || r.Exemplars[0].TraceID != traceA {
		t.Fatalf("exemplars %+v", r.Exemplars)
	}
	if r.Dropped["exemplar_without_trace"] != 1 {
		t.Errorf("dropped %v", r.Dropped)
	}
}

func TestExemplarsCappedPerSeriesAndMinute(t *testing.T) {
	var es []*metricspb.Exemplar
	for i := range maxExemplarsPerSeriesMinute + 7 {
		es = append(es, exemplar(traceA, spanA, float64(i), ts1))
	}
	r := NewRows()
	r.AddMetrics("t1", recv, gaugeWithExemplars("m", es...))
	if len(r.Exemplars) != maxExemplarsPerSeriesMinute {
		t.Fatalf("exemplars = %d, want %d", len(r.Exemplars), maxExemplarsPerSeriesMinute)
	}
	if r.Dropped["exemplar_series_cap"] != 7 {
		t.Errorf("dropped %v", r.Dropped)
	}
}

func TestExemplarsOfAnotherMinuteGetTheirOwnCap(t *testing.T) {
	const nextMinute = ts1 + uint64(time.Minute)
	var es []*metricspb.Exemplar
	for i := range maxExemplarsPerSeriesMinute {
		es = append(es, exemplar(traceA, spanA, float64(i), ts1))
		es = append(es, exemplar(traceB, spanA, float64(i), nextMinute))
	}
	r := NewRows()
	r.AddMetrics("t1", recv, gaugeWithExemplars("m", es...))
	// Both minutes fill their own cap, so nothing is dropped.
	if len(r.Exemplars) != 2*maxExemplarsPerSeriesMinute {
		t.Fatalf("exemplars = %d, want %d", len(r.Exemplars), 2*maxExemplarsPerSeriesMinute)
	}
	if r.Dropped["exemplar_series_cap"] != 0 {
		t.Errorf("dropped %v", r.Dropped)
	}
}

func TestSummaryDataPointsHaveNoExemplars(t *testing.T) {
	req := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: hostResource(),
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
			{Name: "rpc.duration", Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{
				DataPoints: []*metricspb.SummaryDataPoint{{TimeUnixNano: ts1, Count: 2, Sum: 4}},
			}}},
		}}},
	}}}
	r := NewRows()
	r.AddMetrics("t1", recv, req)
	if len(r.Metrics) != 1 {
		t.Fatalf("metrics = %d", len(r.Metrics))
	}
	if len(r.Exemplars) != 0 {
		t.Errorf("summaries carry no exemplars in OTLP, got %d", len(r.Exemplars))
	}
}

func TestHistogramExemplarsAreStored(t *testing.T) {
	req := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: hostResource(),
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
			{Name: "http.server.duration", Unit: "ms", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints: []*metricspb.HistogramDataPoint{{
					TimeUnixNano: ts1, Count: 4, Sum: ptr(10.0), BucketCounts: []uint64{1, 2, 1}, ExplicitBounds: []float64{1, 5},
					Exemplars: []*metricspb.Exemplar{exemplar(traceA, spanA, 7.5, ts1)},
				}},
			}}},
		}}},
	}}}
	r := NewRows()
	r.AddMetrics("t1", recv, req)
	if len(r.Exemplars) != 1 {
		t.Fatalf("exemplars = %d", len(r.Exemplars))
	}
	// The exemplar keeps its own measurement, not the data point's mean (which is 2.5 here).
	if e := r.Exemplars[0]; e.Value != 7.5 || e.MetricType != "histogram" {
		t.Errorf("histogram exemplar %+v", e)
	}
}
