package phpforwarder

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

const (
	testTrace = "4c2953622b6086caa46723f43306ffb6"
	testRoot  = "5ccf0fb55209346e"
	testChild = "1111111111111111"
)

// msg returns a valid single-part message; mutate edits the decoded map before it is re-encoded.
func msg(t testing.TB, mutate func(m map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"v": 1, "pid": 4711, "trace_id": testTrace, "seq": 0, "last": true,
		"resource": map[string]any{
			"service.name": "shop-api", "service.namespace": "shop", "deployment.environment.name": "prod",
			"process.runtime.name": "php", "process.runtime.version": "8.3.11", "php.sapi": "fpm-fcgi",
			"telemetry.distro.name": "openlog-php", "telemetry.distro.version": "0.9.1",
		},
		"sampling_ratio": 0.25, "function_trace": false, "dropped_spans": 3,
		"spans": []any{
			map[string]any{
				"id": testRoot, "parent": "", "name": "GET /users/{id}/orders", "kind": 2,
				"start": 1789302480000000000, "dur": 12345678, "status": 2, "status_msg": "boom",
				"attrs": map[string]any{"http.request.method": "GET", "http.route": "/users/{id}/orders", "http.response.status_code": 500},
				"events": []any{map[string]any{"name": "exception", "time": 1789302480010000000,
					"attrs": map[string]any{"exception.type": "RuntimeException", "exception.message": "boom"}}},
			},
			map[string]any{
				"id": testChild, "parent": testRoot, "name": "SELECT shop.orders", "kind": 3,
				"start": 1789302480001000000, "dur": 1000000,
				"attrs": map[string]any{"db.system.name": "mysql", "server.port": 3306, "openlog.php.fast_calls_ns": 1.5,
					"tags": []any{"a", "b"}, "ratios": []any{1, 2.5}, "flag": true},
			},
		},
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func span(m map[string]any, i int) map[string]any { return m["spans"].([]any)[i].(map[string]any) }

func TestDecodeValidation(t *testing.T) {
	manyAttrs := map[string]any{}
	for i := 0; i <= MaxAttributes; i++ {
		manyAttrs[strings.Repeat("k", 1+i%10)+string(rune('a'+i%26))+strings.Repeat("x", i)] = i
	}
	cases := []struct {
		name string
		raw  []byte
		want string // "" = valid, "version" = unsupported version, else error substring
	}{
		{"valid", msg(t, nil), ""},
		{"not json", []byte(`{"v":1,`), "invalid JSON"},
		{"trailing data", append(msg(t, nil), []byte(` {}`)...), "trailing"},
		{"array", []byte(`[1]`), "invalid JSON"},
		{"oversized", msg(t, func(m map[string]any) { m["pad"] = strings.Repeat("x", MaxDatagramBytes) }), "exceeds"},
		{"unknown version", msg(t, func(m map[string]any) { m["v"] = 2 }), "version"},
		{"unknown version other schema", []byte(`{"v":2,"spans":{"x":1}}`), "version"},
		{"missing version", msg(t, func(m map[string]any) { delete(m, "v") }), "missing v"},
		{"bad pid", msg(t, func(m map[string]any) { m["pid"] = 0 }), "pid"},
		{"short trace id", msg(t, func(m map[string]any) { m["trace_id"] = "abc" }), "trace_id"},
		{"uppercase trace id", msg(t, func(m map[string]any) { m["trace_id"] = strings.ToUpper(testTrace) }), "trace_id"},
		{"non-hex trace id", msg(t, func(m map[string]any) { m["trace_id"] = "zz" + testTrace[2:] }), "trace_id"},
		{"zero trace id", msg(t, func(m map[string]any) { m["trace_id"] = strings.Repeat("0", 32) }), "zeros"},
		{"bad span id", msg(t, func(m map[string]any) { span(m, 0)["id"] = "5ccf0fb55209346" }), "spans[0]: id"},
		{"bad parent", msg(t, func(m map[string]any) { span(m, 1)["parent"] = "XYZ" }), "spans[1]: parent"},
		{"duplicate span id", msg(t, func(m map[string]any) { span(m, 1)["id"] = testRoot }), "duplicate"},
		{"seq negative", msg(t, func(m map[string]any) { m["seq"] = -1 }), "seq"},
		{"seq too high", msg(t, func(m map[string]any) { m["seq"] = MaxParts }), "seq"},
		{"sampling zero", msg(t, func(m map[string]any) { m["sampling_ratio"] = 0 }), "sampling_ratio"},
		{"sampling above one", msg(t, func(m map[string]any) { m["sampling_ratio"] = 1.5 }), "sampling_ratio"},
		{"sampling absent", msg(t, func(m map[string]any) { delete(m, "sampling_ratio") }), ""},
		{"resource non-string", msg(t, func(m map[string]any) { m["resource"].(map[string]any)["service.version"] = 1 }), "must be a string"},
		{"resource value too long", msg(t, func(m map[string]any) { m["resource"].(map[string]any)["service.name"] = strings.Repeat("s", MaxStringBytes+1) }), "too long"},
		{"kind", msg(t, func(m map[string]any) { span(m, 0)["kind"] = 6 }), "kind"},
		{"status", msg(t, func(m map[string]any) { span(m, 0)["status"] = 3 }), "status"},
		{"empty name", msg(t, func(m map[string]any) { span(m, 0)["name"] = "" }), "name"},
		{"no start", msg(t, func(m map[string]any) { span(m, 0)["start"] = 0 }), "start"},
		{"negative dur", msg(t, func(m map[string]any) { span(m, 0)["dur"] = -1 }), "invalid JSON"},
		{"float kind", msg(t, func(m map[string]any) { span(m, 0)["kind"] = 2.5 }), "invalid JSON"},
		{"too many attrs", msg(t, func(m map[string]any) { span(m, 0)["attrs"] = manyAttrs }), "more than 128"},
		{"128 attrs ok", msg(t, func(m map[string]any) {
			a := map[string]any{}
			for k, v := range manyAttrs {
				if len(a) < MaxAttributes {
					a[k] = v
				}
			}
			span(m, 0)["attrs"] = a
		}), ""},
		{"string attr too long", msg(t, func(m map[string]any) { span(m, 0)["attrs"] = map[string]any{"x": strings.Repeat("a", MaxStringBytes+1)} }), "longer than"},
		{"string attr at limit", msg(t, func(m map[string]any) { span(m, 0)["attrs"] = map[string]any{"x": strings.Repeat("a", MaxStringBytes)} }), ""},
		{"null attr", msg(t, func(m map[string]any) { span(m, 0)["attrs"] = map[string]any{"x": nil} }), "null"},
		{"object attr", msg(t, func(m map[string]any) { span(m, 0)["attrs"] = map[string]any{"x": map[string]any{}} }), "unsupported"},
		{"nested array", msg(t, func(m map[string]any) { span(m, 0)["attrs"] = map[string]any{"x": []any{[]any{1}}} }), "nested"},
		{"mixed array", msg(t, func(m map[string]any) { span(m, 0)["attrs"] = map[string]any{"x": []any{1, "a"}} }), "same type"},
		{"huge int", msg(t, func(m map[string]any) { span(m, 0)["attrs"] = map[string]any{"x": json.Number("1e999")} }), "out of range"},
		{"event attr too long", msg(t, func(m map[string]any) {
			span(m, 0)["events"] = []any{map[string]any{"name": "e", "attrs": map[string]any{"x": strings.Repeat("a", MaxStringBytes+1)}}}
		}), "events[0]"},
		{"event without name", msg(t, func(m map[string]any) { span(m, 0)["events"] = []any{map[string]any{"name": ""}} }), "events[0]"},
		{"unknown fields ignored", msg(t, func(m map[string]any) { m["future"] = 1; span(m, 0)["future"] = "x" }), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := decode(tc.raw)
			switch {
			case tc.want == "":
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if m == nil || len(m.spans) != 2 {
					t.Fatalf("decoded = %+v", m)
				}
			case tc.want == "version":
				if !errors.Is(err, errUnsupportedVersion) {
					t.Fatalf("err = %v, want unsupported version", err)
				}
			default:
				if err == nil || errors.Is(err, errUnsupportedVersion) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want %q", err, tc.want)
				}
			}
		})
	}
}

func TestDecodeValues(t *testing.T) {
	m, err := decode(msg(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*commonpb.AnyValue{}
	for _, kv := range m.spans[1].Attributes {
		got[kv.Key] = kv.Value
	}
	if got["server.port"].GetIntValue() != 3306 || got["openlog.php.fast_calls_ns"].GetDoubleValue() != 1.5 || !got["flag"].GetBoolValue() {
		t.Errorf("scalars: %v", got)
	}
	if a := got["tags"].GetArrayValue().GetValues(); len(a) != 2 || a[1].GetStringValue() != "b" {
		t.Errorf("tags: %v", a)
	}
	if a := got["ratios"].GetArrayValue().GetValues(); len(a) != 2 || a[0].GetDoubleValue() != 1 || a[1].GetDoubleValue() != 2.5 {
		t.Errorf("mixed numeric array must widen to doubles: %v", a)
	}
	if m.samplingRatio != 0.25 || m.droppedSpans != 3 || m.pid != 4711 || !m.last {
		t.Errorf("header: %+v", m)
	}
}
