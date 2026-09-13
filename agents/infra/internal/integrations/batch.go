package integrations

import (
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// DefaultMaxPoints caps the data points of one collection (cardinality guard).
const DefaultMaxPoints = 20000

// Batch accumulates the metrics of one collection. Metrics may be split into
// several resources (e.g. one per PostgreSQL database / table, like the OTel
// receivers); every resource carries the instance resource attributes.
type Batch struct {
	now       time.Time
	start     time.Time
	common    []*commonpb.KeyValue
	scopes    []*Scope
	byKey     map[string]*Scope
	points    int
	maxPoints int
	dropped   int
}

// Scope collects the metrics of one resource.
type Scope struct {
	b       *Batch
	attrs   []*commonpb.KeyValue
	metrics []*metricspb.Metric
	byName  map[string]*metricspb.Metric
}

// NewBatch creates a batch with sample time now.
func NewBatch(now time.Time, maxPoints int) *Batch {
	if maxPoints <= 0 {
		maxPoints = DefaultMaxPoints
	}
	b := &Batch{now: now, byKey: map[string]*Scope{}, maxPoints: maxPoints}
	b.Resource()
	return b
}

// Now returns the sample timestamp.
func (b *Batch) Now() time.Time { return b.now }

// SetStartTime sets the start time of cumulative sums (e.g. server start derived from uptime).
func (b *Batch) SetStartTime(t time.Time) { b.start = t }

// SetResourceAttr adds a resource attribute to every resource of the batch.
func (b *Batch) SetResourceAttr(kv *commonpb.KeyValue) {
	for i, a := range b.common {
		if a.Key == kv.Key {
			b.common[i] = kv
			return
		}
	}
	b.common = append(b.common, kv)
}

// Dropped returns the number of points dropped by the cardinality guard.
func (b *Batch) Dropped() int { return b.dropped }

// Points returns the number of recorded points.
func (b *Batch) Points() int { return b.points }

// Resource returns the scope for a resource with extra attributes (none: the instance resource).
func (b *Batch) Resource(attrs ...*commonpb.KeyValue) *Scope {
	var sb strings.Builder
	for _, a := range attrs {
		sb.WriteString(a.Key)
		sb.WriteByte(0)
		sb.WriteString(a.Value.GetStringValue())
		sb.WriteByte(0)
	}
	k := sb.String()
	if s, ok := b.byKey[k]; ok {
		return s
	}
	s := &Scope{b: b, attrs: attrs, byName: map[string]*metricspb.Metric{}}
	b.byKey[k] = s
	b.scopes = append(b.scopes, s)
	return s
}

func (s *Scope) metric(name, unit string, build func() *metricspb.Metric) *metricspb.Metric {
	if m, ok := s.byName[name]; ok {
		return m
	}
	m := build()
	s.byName[name] = m
	s.metrics = append(s.metrics, m)
	return m
}

func (s *Scope) admit() bool {
	if s.b.points >= s.b.maxPoints {
		s.b.dropped++
		return false
	}
	s.b.points++
	return true
}

func (s *Scope) point(p otlputil.Point) *metricspb.NumberDataPoint {
	dp := &metricspb.NumberDataPoint{Attributes: p.Attrs, TimeUnixNano: uint64(s.b.now.UnixNano())}
	if p.IsInt {
		dp.Value = &metricspb.NumberDataPoint_AsInt{AsInt: p.Int}
	} else {
		dp.Value = &metricspb.NumberDataPoint_AsDouble{AsDouble: p.Double}
	}
	return dp
}

// Sum records a cumulative sum point.
func (s *Scope) Sum(name, unit string, monotonic bool, p otlputil.Point) {
	if !s.admit() {
		return
	}
	m := s.metric(name, unit, func() *metricspb.Metric {
		return &metricspb.Metric{Name: name, Unit: unit, Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			IsMonotonic:            monotonic,
		}}}
	})
	dp := s.point(p)
	if !s.b.start.IsZero() {
		dp.StartTimeUnixNano = uint64(s.b.start.UnixNano())
	}
	sum := m.GetSum()
	sum.DataPoints = append(sum.DataPoints, dp)
}

// Gauge records a gauge point.
func (s *Scope) Gauge(name, unit string, p otlputil.Point) {
	if !s.admit() {
		return
	}
	m := s.metric(name, unit, func() *metricspb.Metric {
		return &metricspb.Metric{Name: name, Unit: unit, Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{}}}
	})
	g := m.GetGauge()
	g.DataPoints = append(g.DataPoints, s.point(p))
}

// SumInt records an integer sum point.
func (s *Scope) SumInt(name, unit string, monotonic bool, v int64, attrs ...*commonpb.KeyValue) {
	s.Sum(name, unit, monotonic, otlputil.IntPoint(v, attrs...))
}

// GaugeInt records an integer gauge point.
func (s *Scope) GaugeInt(name, unit string, v int64, attrs ...*commonpb.KeyValue) {
	s.Gauge(name, unit, otlputil.IntPoint(v, attrs...))
}

// GaugeDouble records a double gauge point.
func (s *Scope) GaugeDouble(name, unit string, v float64, attrs ...*commonpb.KeyValue) {
	s.Gauge(name, unit, otlputil.DoublePoint(v, attrs...))
}

// SumDouble records a double sum point.
func (s *Scope) SumDouble(name, unit string, monotonic bool, v float64, attrs ...*commonpb.KeyValue) {
	s.Sum(name, unit, monotonic, otlputil.DoublePoint(v, attrs...))
}

// Metrics returns all metrics of the default resource (tests).
func (b *Batch) Metrics() []*metricspb.Metric { return b.scopes[0].metrics }

// ResourceMetrics builds the OTLP resources: base attributes (host resource +
// instance attributes), then batch-wide attributes, then per-resource ones.
func (b *Batch) ResourceMetrics(base []*commonpb.KeyValue, scope *commonpb.InstrumentationScope) []*metricspb.ResourceMetrics {
	var out []*metricspb.ResourceMetrics
	for _, s := range b.scopes {
		if len(s.metrics) == 0 {
			continue
		}
		attrs := mergeAttrs(base, b.common, s.attrs)
		out = append(out, &metricspb.ResourceMetrics{
			Resource:     &resourcepb.Resource{Attributes: attrs},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Scope: scope, Metrics: s.metrics}},
		})
	}
	return out
}

// mergeAttrs concatenates attribute lists; later lists override earlier keys.
func mergeAttrs(lists ...[]*commonpb.KeyValue) []*commonpb.KeyValue {
	idx := map[string]int{}
	var out []*commonpb.KeyValue
	for _, l := range lists {
		for _, a := range l {
			if i, ok := idx[a.Key]; ok {
				out[i] = a
				continue
			}
			idx[a.Key] = len(out)
			out = append(out, a)
		}
	}
	return out
}

// SortedKeys returns the keys of a string map in order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
