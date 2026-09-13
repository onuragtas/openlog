package ingest

import (
	"fmt"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/otlputil"
	"github.com/onuragtas/openlog/internal/queue"
)

// prepared is a validated export request ready to be produced.
type prepared struct {
	key      string
	rejected int64
	errMsg   string
	empty    bool
	// changed reports whether items were removed, so the original protobuf
	// bytes can no longer be forwarded as-is.
	changed bool
	msg     proto.Message
}

func newRequest(s queue.Signal) proto.Message {
	switch s {
	case queue.SignalMetrics:
		return &colmetrics.ExportMetricsServiceRequest{}
	case queue.SignalLogs:
		return &collogs.ExportLogsServiceRequest{}
	default:
		return &coltrace.ExportTraceServiceRequest{}
	}
}

// prepare validates the request, drops invalid items (reported via OTLP
// partial success) and computes the partition key.
func prepare(s queue.Signal, tenantID string, msg proto.Message) prepared {
	switch req := msg.(type) {
	case *colmetrics.ExportMetricsServiceRequest:
		rejected := filterMetrics(req)
		p := prepared{key: otlputil.MetricsPartitionKey(tenantID, req), rejected: rejected, changed: rejected > 0, msg: req, empty: len(req.GetResourceMetrics()) == 0}
		if rejected > 0 {
			p.errMsg = fmt.Sprintf("%d data points rejected: metric name is empty", rejected)
		}
		return p
	case *collogs.ExportLogsServiceRequest:
		return prepared{key: otlputil.LogsPartitionKey(tenantID, req), msg: req, empty: len(req.GetResourceLogs()) == 0}
	case *coltrace.ExportTraceServiceRequest:
		rejected := filterSpans(req)
		p := prepared{key: otlputil.TracesPartitionKey(tenantID, req), rejected: rejected, changed: rejected > 0, msg: req, empty: len(req.GetResourceSpans()) == 0}
		if rejected > 0 {
			p.errMsg = fmt.Sprintf("%d spans rejected: trace_id must be 16 bytes and span_id 8 bytes", rejected)
		}
		return p
	}
	panic("unknown signal " + string(s))
}

// partialSuccess builds the Export*ServiceResponse for the signal.
func partialSuccess(s queue.Signal, rejected int64, msg string) proto.Message {
	switch s {
	case queue.SignalMetrics:
		r := &colmetrics.ExportMetricsServiceResponse{}
		if rejected > 0 || msg != "" {
			r.PartialSuccess = &colmetrics.ExportMetricsPartialSuccess{RejectedDataPoints: rejected, ErrorMessage: msg}
		}
		return r
	case queue.SignalLogs:
		r := &collogs.ExportLogsServiceResponse{}
		if rejected > 0 || msg != "" {
			r.PartialSuccess = &collogs.ExportLogsPartialSuccess{RejectedLogRecords: rejected, ErrorMessage: msg}
		}
		return r
	default:
		r := &coltrace.ExportTraceServiceResponse{}
		if rejected > 0 || msg != "" {
			r.PartialSuccess = &coltrace.ExportTracePartialSuccess{RejectedSpans: rejected, ErrorMessage: msg}
		}
		return r
	}
}

// filterMetrics removes metrics without a name and returns the number of data
// points removed. Empty resources/scopes are pruned.
func filterMetrics(req *colmetrics.ExportMetricsServiceRequest) int64 {
	var rejected int64
	rms := req.ResourceMetrics[:0]
	for _, rm := range req.ResourceMetrics {
		sms := rm.ScopeMetrics[:0]
		for _, sm := range rm.ScopeMetrics {
			ms := sm.Metrics[:0]
			for _, m := range sm.Metrics {
				if m.GetName() == "" {
					rejected += int64(dataPointCount(m))
					continue
				}
				ms = append(ms, m)
			}
			sm.Metrics = ms
			if len(ms) > 0 {
				sms = append(sms, sm)
			}
		}
		rm.ScopeMetrics = sms
		if len(sms) > 0 {
			rms = append(rms, rm)
		}
	}
	req.ResourceMetrics = rms
	return rejected
}

func dataPointCount(m *metricspb.Metric) int {
	switch d := m.Data.(type) {
	case *metricspb.Metric_Gauge:
		return len(d.Gauge.GetDataPoints())
	case *metricspb.Metric_Sum:
		return len(d.Sum.GetDataPoints())
	case *metricspb.Metric_Histogram:
		return len(d.Histogram.GetDataPoints())
	case *metricspb.Metric_ExponentialHistogram:
		return len(d.ExponentialHistogram.GetDataPoints())
	case *metricspb.Metric_Summary:
		return len(d.Summary.GetDataPoints())
	}
	return 0
}

// filterSpans removes spans with malformed ids.
func filterSpans(req *coltrace.ExportTraceServiceRequest) int64 {
	var rejected int64
	rss := req.ResourceSpans[:0]
	for _, rs := range req.ResourceSpans {
		sss := rs.ScopeSpans[:0]
		for _, ss := range rs.ScopeSpans {
			spans := ss.Spans[:0]
			for _, sp := range ss.Spans {
				if !validSpan(sp) {
					rejected++
					continue
				}
				spans = append(spans, sp)
			}
			ss.Spans = spans
			if len(spans) > 0 {
				sss = append(sss, ss)
			}
		}
		rs.ScopeSpans = sss
		if len(sss) > 0 {
			rss = append(rss, rs)
		}
	}
	req.ResourceSpans = rss
	return rejected
}

func validSpan(sp *tracepb.Span) bool {
	return len(sp.GetTraceId()) == 16 && otlputil.HexID(sp.GetTraceId()) != "" &&
		len(sp.GetSpanId()) == 8 && otlputil.HexID(sp.GetSpanId()) != "" &&
		(len(sp.GetParentSpanId()) == 0 || len(sp.GetParentSpanId()) == 8)
}
