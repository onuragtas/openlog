// Package otlputil provides small builders for OTLP protobuf messages.
//
// The agent uses the data messages MetricsData and LogsData instead of the
// collector Export*ServiceRequest types: they are wire-compatible (both are
// "repeated Resource* = 1") and avoid linking gRPC into the binary.
package otlputil

import (
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Str builds a string attribute.
func Str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// Bool builds a bool attribute.
func Bool(k string, v bool) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v}}}
}

// StrSlice builds a string array attribute.
func StrSlice(k string, vs []string) *commonpb.KeyValue {
	arr := &commonpb.ArrayValue{Values: make([]*commonpb.AnyValue, 0, len(vs))}
	for _, v := range vs {
		arr.Values = append(arr.Values, &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}})
	}
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: arr}}}
}

// Int builds an int attribute.
func Int(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

// Point is a number data point before timestamps are applied.
type Point struct {
	Int    int64
	Double float64
	IsInt  bool
	Attrs  []*commonpb.KeyValue
}

// IntPoint returns an integer point.
func IntPoint(v int64, attrs ...*commonpb.KeyValue) Point {
	return Point{Int: v, IsInt: true, Attrs: attrs}
}

// DoublePoint returns a floating point value.
func DoublePoint(v float64, attrs ...*commonpb.KeyValue) Point {
	return Point{Double: v, Attrs: attrs}
}

func toProto(points []Point, start, now time.Time) []*metricspb.NumberDataPoint {
	out := make([]*metricspb.NumberDataPoint, 0, len(points))
	for _, p := range points {
		dp := &metricspb.NumberDataPoint{Attributes: p.Attrs, TimeUnixNano: uint64(now.UnixNano())}
		if !start.IsZero() {
			dp.StartTimeUnixNano = uint64(start.UnixNano())
		}
		if p.IsInt {
			dp.Value = &metricspb.NumberDataPoint_AsInt{AsInt: p.Int}
		} else {
			dp.Value = &metricspb.NumberDataPoint_AsDouble{AsDouble: p.Double}
		}
		out = append(out, dp)
	}
	return out
}

// Gauge builds a gauge metric.
func Gauge(name, unit string, now time.Time, points ...Point) *metricspb.Metric {
	return &metricspb.Metric{
		Name: name, Unit: unit,
		Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: toProto(points, time.Time{}, now)}},
	}
}

// Sum builds a cumulative sum metric.
func Sum(name, unit string, monotonic bool, start, now time.Time, points ...Point) *metricspb.Metric {
	return &metricspb.Metric{
		Name: name, Unit: unit,
		Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			IsMonotonic:            monotonic,
			DataPoints:             toProto(points, start, now),
		}},
	}
}
