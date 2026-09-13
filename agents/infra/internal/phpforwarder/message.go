// Package phpforwarder receives spans from the openlog PHP extension over a local datagram socket, reassembles
// split messages, converts them to OTLP and hands them to the agent's export pipeline
// (docs/contracts/php-agent.md §1, §2, §6).
package phpforwarder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Input limits (php-agent.md §1, §6).
const (
	// MaxDatagramBytes is the largest message the extension sends; longer datagrams are malformed.
	MaxDatagramBytes = 60000
	// MaxAttributes is the attribute limit per span and per span event.
	MaxAttributes = 128
	// MaxStringBytes limits every string value (attribute values, array elements, resource values, names).
	MaxStringBytes = 4 << 10
	// MaxKeyBytes limits attribute keys.
	MaxKeyBytes = 256
	// MaxParts limits the parts of one split message (seq 0..MaxParts-1).
	MaxParts = 256
)

// errUnsupportedVersion marks a syntactically valid message with an unknown "v".
var errUnsupportedVersion = errors.New("unsupported message version")

type wireEvent struct {
	Name  string         `json:"name"`
	Time  uint64         `json:"time"`
	Attrs map[string]any `json:"attrs"`
}

type wireSpan struct {
	ID        string         `json:"id"`
	Parent    string         `json:"parent"`
	Name      string         `json:"name"`
	Kind      int32          `json:"kind"`
	Start     uint64         `json:"start"`
	Dur       uint64         `json:"dur"`
	Status    int32          `json:"status"`
	StatusMsg string         `json:"status_msg"`
	Attrs     map[string]any `json:"attrs"`
	Events    []wireEvent    `json:"events"`
}

type wireMessage struct {
	V             int            `json:"v"`
	PID           int64          `json:"pid"`
	TraceID       string         `json:"trace_id"`
	Seq           int            `json:"seq"`
	Last          bool           `json:"last"`
	Resource      map[string]any `json:"resource"`
	SamplingRatio *float64       `json:"sampling_ratio"`
	FunctionTrace bool           `json:"function_trace"`
	DroppedSpans  int64          `json:"dropped_spans"`
	Spans         []wireSpan     `json:"spans"`
}

// message is one validated datagram, already converted to OTLP spans.
type message struct {
	pid           int64
	traceID       [16]byte
	seq           int
	last          bool
	resource      []*commonpb.KeyValue // allowed extension keys, sorted by key
	samplingRatio float64
	functionTrace bool
	droppedSpans  int64
	spans         []*tracepb.Span
	size          int // datagram bytes
}

// resourceKeys are the resource attributes the extension may set (php-agent.md §2.1), besides k8s.*.
var resourceKeys = map[string]bool{
	"service.name": true, "service.namespace": true, "service.version": true, "deployment.environment.name": true,
	"process.runtime.name": true, "process.runtime.version": true, "php.sapi": true,
	"telemetry.distro.name": true, "telemetry.distro.version": true,
	"service.instance.id": true, "container.id": true,
}

func allowedResourceKey(k string) bool {
	return resourceKeys[k] || (strings.HasPrefix(k, "k8s.") && len(k) > len("k8s."))
}

// decode parses and validates one datagram. It returns errUnsupportedVersion for a well-formed message of another
// version and a descriptive error for malformed input.
func decode(b []byte) (*message, error) {
	if len(b) > MaxDatagramBytes {
		return nil, fmt.Errorf("datagram of %d bytes exceeds %d", len(b), MaxDatagramBytes)
	}
	var w wireMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&w); err != nil {
		// Another version may use another schema: classify by "v" alone.
		var v struct {
			V json.Number `json:"v"`
		}
		if json.Unmarshal(b, &v) == nil && v.V != "" && v.V != "1" {
			return nil, errUnsupportedVersion
		}
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON object")
	}
	if w.V != 1 {
		if w.V == 0 {
			return nil, errors.New("missing v")
		}
		return nil, errUnsupportedVersion
	}
	m := &message{pid: w.PID, seq: w.Seq, last: w.Last, functionTrace: w.FunctionTrace, droppedSpans: w.DroppedSpans, size: len(b)}
	if w.PID <= 0 {
		return nil, errors.New("pid must be positive")
	}
	if err := parseHex(w.TraceID, m.traceID[:]); err != nil {
		return nil, fmt.Errorf("trace_id: %w", err)
	}
	if w.Seq < 0 || w.Seq >= MaxParts {
		return nil, fmt.Errorf("seq must be in [0, %d)", MaxParts)
	}
	if w.DroppedSpans < 0 {
		return nil, errors.New("dropped_spans must be >= 0")
	}
	m.samplingRatio = 1
	if w.SamplingRatio != nil {
		r := *w.SamplingRatio
		if !(r > 0 && r <= 1) {
			return nil, errors.New("sampling_ratio must be in (0, 1]")
		}
		m.samplingRatio = r
	}
	if len(w.Resource) > MaxAttributes {
		return nil, fmt.Errorf("resource has more than %d keys", MaxAttributes)
	}
	for k, v := range w.Resource {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("resource %q: value must be a string", k)
		}
		if len(k) > MaxKeyBytes || len(s) > MaxStringBytes {
			return nil, fmt.Errorf("resource %q: key or value too long", k)
		}
		if allowedResourceKey(k) { // host.*, os.*, openlog.* and unknown keys are never trusted
			m.resource = append(m.resource, strKV(k, s))
		}
	}
	sort.Slice(m.resource, func(i, j int) bool { return m.resource[i].Key < m.resource[j].Key })

	ids := make(map[[8]byte]bool, len(w.Spans))
	m.spans = make([]*tracepb.Span, 0, len(w.Spans))
	for i := range w.Spans {
		sp, err := convertSpan(m.traceID[:], &w.Spans[i])
		if err != nil {
			return nil, fmt.Errorf("spans[%d]: %w", i, err)
		}
		id := [8]byte(sp.SpanId)
		if ids[id] {
			return nil, fmt.Errorf("spans[%d]: duplicate id", i)
		}
		ids[id] = true
		m.spans = append(m.spans, sp)
	}
	return m, nil
}

func convertSpan(traceID []byte, w *wireSpan) (*tracepb.Span, error) {
	sp := &tracepb.Span{TraceId: traceID, SpanId: make([]byte, 8)}
	if err := parseHex(w.ID, sp.SpanId); err != nil {
		return nil, fmt.Errorf("id: %w", err)
	}
	if w.Parent != "" {
		sp.ParentSpanId = make([]byte, 8)
		if err := parseHex(w.Parent, sp.ParentSpanId); err != nil {
			return nil, fmt.Errorf("parent: %w", err)
		}
	}
	if w.Name == "" || len(w.Name) > MaxStringBytes {
		return nil, errors.New("name must be 1..4096 bytes")
	}
	if w.Kind < 1 || w.Kind > 5 {
		return nil, errors.New("kind must be 1..5")
	}
	if w.Status < 0 || w.Status > 2 {
		return nil, errors.New("status must be 0..2")
	}
	if len(w.StatusMsg) > MaxStringBytes {
		return nil, errors.New("status_msg too long")
	}
	if w.Start == 0 || w.Start+w.Dur < w.Start {
		return nil, errors.New("invalid start/dur")
	}
	sp.Name = w.Name
	sp.Kind = tracepb.Span_SpanKind(w.Kind)
	sp.StartTimeUnixNano = w.Start
	sp.EndTimeUnixNano = w.Start + w.Dur
	sp.Status = &tracepb.Status{Code: tracepb.Status_StatusCode(w.Status), Message: w.StatusMsg}
	var err error
	if sp.Attributes, err = convertAttrs(w.Attrs); err != nil {
		return nil, err
	}
	for i := range w.Events {
		e := &w.Events[i]
		if e.Name == "" || len(e.Name) > MaxStringBytes {
			return nil, fmt.Errorf("events[%d]: name must be 1..4096 bytes", i)
		}
		ev := &tracepb.Span_Event{Name: e.Name, TimeUnixNano: e.Time}
		if ev.TimeUnixNano == 0 {
			ev.TimeUnixNano = sp.EndTimeUnixNano
		}
		if ev.Attributes, err = convertAttrs(e.Attrs); err != nil {
			return nil, fmt.Errorf("events[%d]: %w", i, err)
		}
		sp.Events = append(sp.Events, ev)
	}
	return sp, nil
}

// convertAttrs converts attributes sorted by key. Values: string, integer, float, boolean or a homogeneous array of
// these (integers and floats mixed become floats).
func convertAttrs(attrs map[string]any) ([]*commonpb.KeyValue, error) {
	if len(attrs) > MaxAttributes {
		return nil, fmt.Errorf("more than %d attributes", MaxAttributes)
	}
	if len(attrs) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		if k == "" || len(k) > MaxKeyBytes {
			return nil, fmt.Errorf("attribute key must be 1..%d bytes", MaxKeyBytes)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*commonpb.KeyValue, 0, len(keys))
	for _, k := range keys {
		v, err := convertValue(attrs[k], true)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", k, err)
		}
		out = append(out, &commonpb.KeyValue{Key: k, Value: v})
	}
	return out, nil
}

func convertValue(v any, allowArray bool) (*commonpb.AnyValue, error) {
	switch x := v.(type) {
	case string:
		if len(x) > MaxStringBytes {
			return nil, fmt.Errorf("string value longer than %d bytes", MaxStringBytes)
		}
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: x}}, nil
	case bool:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: x}}, nil
	case json.Number:
		if i, err := x.Int64(); err == nil && !strings.ContainsAny(string(x), ".eE") {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: i}}, nil
		}
		f, err := x.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("number %s out of range", x)
		}
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: f}}, nil
	case []any:
		if !allowArray {
			return nil, errors.New("nested arrays are not allowed")
		}
		arr := &commonpb.ArrayValue{Values: make([]*commonpb.AnyValue, 0, len(x))}
		var kind string
		for _, e := range x {
			ev, err := convertValue(e, false)
			if err != nil {
				return nil, err
			}
			k := valueKind(ev)
			switch {
			case kind == "" || kind == k:
				kind = k
			case (kind == "int" || kind == "double") && (k == "int" || k == "double"):
				kind = "double"
			default:
				return nil, errors.New("array elements must have the same type")
			}
			arr.Values = append(arr.Values, ev)
		}
		if kind == "double" { // widen integers of a mixed numeric array
			for _, ev := range arr.Values {
				if iv, ok := ev.Value.(*commonpb.AnyValue_IntValue); ok {
					ev.Value = &commonpb.AnyValue_DoubleValue{DoubleValue: float64(iv.IntValue)}
				}
			}
		}
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: arr}}, nil
	case nil:
		return nil, errors.New("null values are not allowed")
	default:
		return nil, fmt.Errorf("unsupported value type %T", v)
	}
}

func valueKind(v *commonpb.AnyValue) string {
	switch v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return "string"
	case *commonpb.AnyValue_BoolValue:
		return "bool"
	case *commonpb.AnyValue_IntValue:
		return "int"
	default:
		return "double"
	}
}

// parseHex decodes len(dst)*2 lowercase hex digits into dst and rejects all-zero IDs.
func parseHex(s string, dst []byte) error {
	if len(s) != 2*len(dst) {
		return fmt.Errorf("must be %d lowercase hex digits", 2*len(dst))
	}
	nonZero := false
	for i := 0; i < len(dst); i++ {
		hi, ok1 := hexNibble(s[2*i])
		lo, ok2 := hexNibble(s[2*i+1])
		if !ok1 || !ok2 {
			return fmt.Errorf("must be %d lowercase hex digits", 2*len(dst))
		}
		dst[i] = hi<<4 | lo
		nonZero = nonZero || dst[i] != 0
	}
	if !nonZero {
		return errors.New("must not be all zeros")
	}
	return nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

func strKV(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}
