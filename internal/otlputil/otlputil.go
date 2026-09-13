// Package otlputil contains helpers shared by ingest and processor for working
// with OTLP protobuf messages: attribute flattening and Kafka partition keys.
package otlputil

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Well-known resource attribute keys (docs/contracts/semantic-conventions.md).
const (
	AttrHostID        = "host.id"
	AttrHostName      = "host.name"
	AttrHostArch      = "host.arch"
	AttrServiceName   = "service.name"
	AttrOSType        = "os.type"
	AttrOSDescription = "os.description"
	AttrAgentName     = "openlog.agent.name"
	AttrAgentVersion  = "openlog.agent.version"
)

// AnyValueString renders an OTLP AnyValue as a string suitable for a
// Map(String, String) column. Scalars use their natural text form, bytes are
// base64, arrays and maps are JSON.
func AnyValueString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue)
	default:
		b, err := json.Marshal(anyValueJSON(v))
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func anyValueJSON(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		out := make([]any, 0, len(x.ArrayValue.GetValues()))
		for _, e := range x.ArrayValue.GetValues() {
			out = append(out, anyValueJSON(e))
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		out := make(map[string]any, len(x.KvlistValue.GetValues()))
		for _, kv := range x.KvlistValue.GetValues() {
			out[kv.GetKey()] = anyValueJSON(kv.GetValue())
		}
		return out
	}
	return nil
}

// AttrsToMap flattens attributes. Later duplicates win.
func AttrsToMap(kvs []*commonpb.KeyValue) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[kv.GetKey()] = AnyValueString(kv.GetValue())
	}
	return m
}

// FindAttr returns the value of key, or nil.
func FindAttr(kvs []*commonpb.KeyValue, key string) *commonpb.AnyValue {
	for i := len(kvs) - 1; i >= 0; i-- {
		if kvs[i].GetKey() == key {
			return kvs[i].GetValue()
		}
	}
	return nil
}

// AttrString returns the string form of attribute key, or "".
func AttrString(kvs []*commonpb.KeyValue, key string) string {
	return AnyValueString(FindAttr(kvs, key))
}

// HexID renders a trace or span id as lowercase hex; all-zero or empty ids render as "".
func HexID(b []byte) string {
	for _, c := range b {
		if c != 0 {
			return hex.EncodeToString(b)
		}
	}
	return ""
}

// resourceKey implements the metrics/logs partition key rule of kafka.md:
// <tenant>/<host.id> of the first resource having host.id, else
// <tenant>/<service.name> of the first resource having service.name, else <tenant>/.
func resourceKey(tenant string, resources []*resourcepb.Resource) string {
	for _, r := range resources {
		if h := AttrString(r.GetAttributes(), AttrHostID); h != "" {
			return tenant + "/" + h
		}
	}
	for _, r := range resources {
		if s := AttrString(r.GetAttributes(), AttrServiceName); s != "" {
			return tenant + "/" + s
		}
	}
	return tenant + "/"
}

// MetricsPartitionKey returns the Kafka key for a metrics export request.
func MetricsPartitionKey(tenant string, req *colmetrics.ExportMetricsServiceRequest) string {
	rs := make([]*resourcepb.Resource, 0, len(req.GetResourceMetrics()))
	for _, r := range req.GetResourceMetrics() {
		rs = append(rs, r.GetResource())
	}
	return resourceKey(tenant, rs)
}

// LogsPartitionKey returns the Kafka key for a logs export request.
func LogsPartitionKey(tenant string, req *collogs.ExportLogsServiceRequest) string {
	rs := make([]*resourcepb.Resource, 0, len(req.GetResourceLogs()))
	for _, r := range req.GetResourceLogs() {
		rs = append(rs, r.GetResource())
	}
	return resourceKey(tenant, rs)
}

// TracesPartitionKey returns <tenant>/<hex trace_id of the first span>.
func TracesPartitionKey(tenant string, req *coltrace.ExportTraceServiceRequest) string {
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				return tenant + "/" + hex.EncodeToString(sp.GetTraceId())
			}
		}
	}
	return tenant + "/"
}
