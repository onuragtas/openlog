// Package testutil helps integration tests inspect collected OTLP metrics.
package testutil

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Instance returns a test instance.
func Instance() *integrations.Instance {
	return &integrations.Instance{Timeout: 3 * time.Second, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), HostName: "host-1"}
}

// Point is a flattened data point.
type Point struct {
	Resource  map[string]string
	Metric    *metricspb.Metric
	Attrs     map[string]string
	Int       int64
	Double    float64
	Monotonic bool
	IsSum     bool
}

func kv(attrs []*commonpb.KeyValue) map[string]string {
	out := map[string]string{}
	for _, a := range attrs {
		switch v := a.Value.Value.(type) {
		case *commonpb.AnyValue_StringValue:
			out[a.Key] = v.StringValue
		case *commonpb.AnyValue_IntValue:
			out[a.Key] = fmt.Sprint(v.IntValue)
		}
	}
	return out
}

// Points flattens a batch.
func Points(b *integrations.Batch) []Point {
	var out []Point
	for _, rm := range b.ResourceMetrics(nil, nil) {
		res := kv(rm.Resource.Attributes)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				var dps []*metricspb.NumberDataPoint
				p := Point{Resource: res, Metric: m}
				switch d := m.Data.(type) {
				case *metricspb.Metric_Sum:
					dps, p.IsSum, p.Monotonic = d.Sum.DataPoints, true, d.Sum.IsMonotonic
				case *metricspb.Metric_Gauge:
					dps = d.Gauge.DataPoints
				}
				for _, dp := range dps {
					q := p
					q.Attrs = kv(dp.Attributes)
					q.Int, q.Double = dp.GetAsInt(), dp.GetAsDouble()
					out = append(out, q)
				}
			}
		}
	}
	return out
}

// Find returns the points of a metric whose resource and point attributes contain match.
func Find(points []Point, name string, match map[string]string) []Point {
	var out []Point
	for _, p := range points {
		if p.Metric.Name != name {
			continue
		}
		ok := true
		for k, v := range match {
			if p.Attrs[k] != v && p.Resource[k] != v {
				ok = false
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

// One returns the single matching point or fails.
func One(t *testing.T, points []Point, name string, match map[string]string) Point {
	t.Helper()
	ps := Find(points, name, match)
	if len(ps) != 1 {
		t.Fatalf("%s %v: %d points", name, match, len(ps))
	}
	return ps[0]
}

// Expect checks name, unit, type and integer value of a single point.
func Expect(t *testing.T, points []Point, name, unit string, sum, monotonic bool, value int64, match map[string]string) {
	t.Helper()
	ps := Find(points, name, match)
	if len(ps) != 1 {
		t.Errorf("%s %v: %d points", name, match, len(ps))
		return
	}
	p := ps[0]
	if p.Metric.Unit != unit || p.IsSum != sum || (sum && p.Monotonic != monotonic) || p.Int != value {
		t.Errorf("%s %v: unit=%q sum=%v monotonic=%v value=%d; want unit=%q sum=%v monotonic=%v value=%d",
			name, match, p.Metric.Unit, p.IsSum, p.Monotonic, p.Int, unit, sum, monotonic, value)
	}
}
