package processor

import (
	"time"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/onuragtas/openlog/internal/otlputil"
)

// TableMetricExemplars holds OTLP metric exemplars: the trace a data point came from (schema 0092_metric_exemplars,
// D-130). Rows are written with the chunk's deduplication token like every other table.
const TableMetricExemplars = "metric_exemplars"

// Exemplar volume is bounded here, not by the SDK. SDKs already sample (the default reservoir keeps one exemplar per
// data point, but a histogram reservoir keeps up to one per bucket), and a tenant with many series exporting every
// 10 s would otherwise write tens of thousands of rows per minute for a signal that only needs a handful of dots on
// a chart.
const (
	// maxExemplarsPerSeriesMinute is kept per (tenant, metric, series, minute) within one converted chunk. The
	// counters live on Rows, so the cap is exact within a chunk and approximate across chunks: a minute whose data
	// points arrive in several Kafka chunks can keep up to this many per chunk. That is deliberate — an exact cap
	// would need state shared by every processor, and overshooting a bounded amount costs a few rows, not a spike.
	maxExemplarsPerSeriesMinute = 5
	// maxExemplarsPerChunk bounds the rows one chunk can produce regardless of how many series it carries.
	maxExemplarsPerChunk = 10000
)

// ExemplarRow is one exemplar of one data point.
type ExemplarRow struct {
	TenantID           string
	MetricName         string
	MetricType         string
	Unit               string
	ServiceName        string
	HostID             string
	HostName           string
	ScopeName          string
	SeriesID           uint64
	ResourceAttributes map[string]string
	Attributes         map[string]string
	Timestamp          time.Time
	Value              float64
	TraceID            string
	SpanID             string
	FilteredAttributes map[string]string
}

// Values implements row.
func (r *ExemplarRow) Values() []any {
	return []any{r.TenantID, r.MetricName, r.MetricType, r.Unit, r.ServiceName, r.HostID, r.HostName, r.ScopeName,
		r.SeriesID, r.ResourceAttributes, r.Attributes, r.Timestamp, r.Value, r.TraceID, r.SpanID, r.FilteredAttributes}
}

// exemplarKey counts the exemplars already kept for one series in one minute.
type exemplarKey struct {
	tenant, metric string
	series         uint64
	minute         int64
}

// exemplarValue returns the exemplar's own measurement.
func exemplarValue(e *metricspb.Exemplar) float64 {
	switch v := e.Value.(type) {
	case *metricspb.Exemplar_AsDouble:
		return v.AsDouble
	case *metricspb.Exemplar_AsInt:
		return float64(v.AsInt)
	}
	return 0
}

// addExemplars stores the exemplars of one data point. row carries the data point's identifying columns (already
// filled by AddMetrics), fallback its timestamp for exemplars that carry none.
func (r *Rows) addExemplars(row *MetricRow, exemplars []*metricspb.Exemplar, fallback time.Time) {
	for _, e := range exemplars {
		traceID := otlputil.HexID(e.GetTraceId())
		if traceID == "" {
			// Nothing to open: an exemplar without a trace id cannot be a link to a trace.
			r.Dropped["exemplar_without_trace"]++
			continue
		}
		if len(r.Exemplars) >= maxExemplarsPerChunk {
			r.Dropped["exemplar_chunk_cap"]++
			continue
		}
		ts := tsOr(e.GetTimeUnixNano(), fallback)
		k := exemplarKey{tenant: row.TenantID, metric: row.MetricName, series: row.SeriesID, minute: ts.Truncate(time.Minute).Unix()}
		if r.exemplarCounts[k] >= maxExemplarsPerSeriesMinute {
			r.Dropped["exemplar_series_cap"]++
			continue
		}
		if r.exemplarCounts == nil {
			r.exemplarCounts = map[exemplarKey]int{}
		}
		r.exemplarCounts[k]++
		r.Exemplars = append(r.Exemplars, ExemplarRow{
			TenantID: row.TenantID, MetricName: row.MetricName, MetricType: row.MetricType, Unit: row.Unit,
			ServiceName: row.ServiceName, HostID: row.HostID, HostName: row.HostName, ScopeName: row.ScopeName,
			SeriesID: row.SeriesID, ResourceAttributes: row.ResourceAttributes, Attributes: row.Attributes,
			Timestamp: ts, Value: exemplarValue(e), TraceID: traceID, SpanID: otlputil.HexID(e.GetSpanId()),
			FilteredAttributes: otlputil.AttrsToMap(e.GetFilteredAttributes()),
		})
	}
}
