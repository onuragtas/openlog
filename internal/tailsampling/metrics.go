package tailsampling

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the openlog_tailsampling_* series (docs/operations/tail-sampling.md).
type Metrics struct {
	TracesBuffered  prometheus.Gauge
	SpansBuffered   prometheus.Gauge
	BytesBuffered   prometheus.Gauge
	CacheEntries    prometheus.Gauge
	Decisions       *prometheus.CounterVec // {policy, decision}
	Spans           *prometheus.CounterVec // {decision}
	LateSpans       *prometheus.CounterVec // {decision}
	Evictions       *prometheus.CounterVec // {reason}
	RateLimited     prometheus.Counter
	RecordsRejected *prometheus.CounterVec // {reason}
	ProduceFailures prometheus.Counter
	DecisionDelay   prometheus.Histogram
	Lag             *prometheus.GaugeVec // {topic, partition}
	PolicyErrors    prometheus.Counter
}

// NewMetrics creates the metrics and registers them on reg (may be nil).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		TracesBuffered: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openlog_tailsampling_traces_buffered", Help: "Traces waiting for a tail sampling decision on this instance.",
		}),
		SpansBuffered: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openlog_tailsampling_spans_buffered", Help: "Spans of buffered traces.",
		}),
		BytesBuffered: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openlog_tailsampling_buffered_bytes", Help: "Approximate protobuf bytes of buffered spans.",
		}),
		CacheEntries: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openlog_tailsampling_decision_cache_entries", Help: "Decided traces remembered for late spans.",
		}),
		Decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_tailsampling_decisions_total", Help: "Trace decisions by matching policy rule (baseline when none matched) and outcome (kept, dropped).",
		}, []string{"policy", "decision"}),
		Spans: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_tailsampling_spans_total", Help: "Spans kept or dropped (including late spans).",
		}, []string{"decision"}),
		LateSpans: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_tailsampling_late_spans_total", Help: "Spans that arrived after their trace was decided and followed the cached decision.",
		}, []string{"decision"}),
		Evictions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_tailsampling_evictions_total",
			Help: "Traces decided before their decision wait ended, by reason (max_traces, max_spans, max_bytes, revoked, shutdown).",
		}, []string{"reason"}),
		RateLimited: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openlog_tailsampling_rate_limited_traces_total", Help: "Traces whose keep probability was lowered by the tenant span rate limit.",
		}),
		RecordsRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_tailsampling_records_rejected_total", Help: "Kafka records or spans skipped by the sampler.",
		}, []string{"reason"}),
		ProduceFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openlog_tailsampling_produce_failures_total", Help: "Failed produce attempts to the sampled traces topic (retried).",
		}),
		DecisionDelay: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "openlog_tailsampling_decision_delay_seconds", Help: "Time from a trace's first buffered span to its decision.",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 20, 30, 45, 60, 120},
		}),
		Lag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "openlog_tailsampling_consumer_lag_records", Help: "Log end offset minus committed offset for raw traces partitions assigned to this sampler.",
		}, []string{"topic", "partition"}),
		PolicyErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openlog_tailsampling_policy_load_failures_total", Help: "Failed policy reloads (the previous policies stay in use).",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.TracesBuffered, m.SpansBuffered, m.BytesBuffered, m.CacheEntries, m.Decisions, m.Spans, m.LateSpans,
			m.Evictions, m.RateLimited, m.RecordsRejected, m.ProduceFailures, m.DecisionDelay, m.Lag, m.PolicyErrors)
	}
	return m
}
