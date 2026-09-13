// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func testConfig() config {
	return config{timeout: 5 * time.Second, flushInterval: time.Second, maxPending: 100, maxBatchBytes: 1 << 20}
}

func testHost() []*commonpb.KeyValue {
	return []*commonpb.KeyValue{strKV("host.name", "php-host"), strKV("host.id", "machine-1"), strKV("os.type", "linux"),
		strKV("openlog.agent.name", "openlog-php-forwarder")}
}

type collector struct{ out []*tracepb.TracesData }

func newTestForwarder(now *time.Time) (*forwarder, *collector, *counters) {
	col := &collector{}
	st := &counters{}
	f := newForwarder(testConfig(), testHost(), st, func(td *tracepb.TracesData, _ int) { col.out = append(col.out, td) })
	if now != nil {
		f.now = func() time.Time { return *now }
	}
	return f, col, st
}

func (c *collector) spans() []*tracepb.Span {
	var out []*tracepb.Span
	for _, td := range c.out {
		for _, rs := range td.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				out = append(out, ss.Spans...)
			}
		}
	}
	return out
}

func attr(sp *tracepb.Span, key string) *commonpb.AnyValue {
	for _, kv := range sp.Attributes {
		if kv.Key == key {
			return kv.Value
		}
	}
	return nil
}

// The extension writes seq, last and dropped_spans at the end; field order must not matter.
func TestFieldOrderAndTypes(t *testing.T) {
	raw := `{"v":1,"pid":42,"trace_id":"4c2953622b6086caa46723f43306ffb6","resource":{"service.name":"php-laravel-83",` +
		`"php.sapi":"fpm-fcgi"},"sampling_ratio":1,"function_trace":true,"spans":[` +
		`{"attrs":{"http.response.status_code":500,"big":9007199254740993,"ratio":0.5,"ok":false,"ports":[80,443]},` +
		`"events":[{"name":"exception","time":1789302480010000000,"attrs":{"exception.type":"RuntimeException"}}],` +
		`"status_msg":"boom","status":2,"dur":1000,"start":1789302480000000000,"kind":2,"name":"GET /boom","parent":"","id":"5ccf0fb55209346e"}],` +
		`"seq":0,"last":true,"dropped_spans":7}`
	f, col, st := newTestForwarder(nil)
	f.handle([]byte(raw))
	f.sweep(true)
	sp := col.spans()
	if len(sp) != 1 || st.accepted.Load() != 1 {
		t.Fatalf("spans = %d, stats accepted %d malformed %d", len(sp), st.accepted.Load(), st.malformed.Load())
	}
	root := sp[0]
	if attr(root, "big").GetIntValue() != 9007199254740993 {
		t.Errorf("integers must stay int64: %v", attr(root, "big"))
	}
	if attr(root, "ratio").GetDoubleValue() != 0.5 || attr(root, "http.response.status_code").GetIntValue() != 500 {
		t.Errorf("scalar types: %v", root.Attributes)
	}
	if attr(root, AttrSamplingRatio).GetDoubleValue() != 1 || attr(root, AttrDroppedSpans).GetIntValue() != 7 {
		t.Errorf("root attrs: %v", root.Attributes)
	}
	if attr(root, AttrIncomplete) != nil {
		t.Error("complete trace marked incomplete")
	}
	if root.Status.Code != tracepb.Status_STATUS_CODE_ERROR || root.Status.Message != "boom" || len(root.Events) != 1 ||
		root.Events[0].Name != "exception" || root.EndTimeUnixNano != root.StartTimeUnixNano+1000 {
		t.Errorf("status/events: %+v", root)
	}
}

// Split message: parts arrive out of order; the trace is exported only once complete, with one root.
func TestSplitReassembly(t *testing.T) {
	now := time.Unix(1000, 0)
	f, col, _ := newTestForwarder(&now)
	parts := splitMessage(t, "0af7651916cd43dd8448eb211c80319c", "", 3)
	f.handle(parts[2])
	f.handle(parts[0])
	f.sweep(true)
	if len(col.out) != 0 {
		t.Fatal("exported before all parts arrived")
	}
	f.handle(parts[1])
	f.sweep(true)
	sp := col.spans()
	if len(sp) != 4 {
		t.Fatalf("spans = %d, want 4", len(sp))
	}
	roots := 0
	for _, s := range sp {
		if attr(s, AttrSamplingRatio) != nil {
			roots++
			if s.Name != "GET /users/{id}/orders" || attr(s, AttrIncomplete) != nil {
				t.Errorf("wrong root: %s %v", s.Name, s.Attributes)
			}
		}
	}
	if roots != 1 {
		t.Errorf("roots = %d", roots)
	}
}

// A trace whose root part never arrives is exported after the timeout with every span marked incomplete; a trace
// whose root arrived but a child part is lost gets the mark on the root only.
func TestReassemblyTimeoutIncomplete(t *testing.T) {
	now := time.Unix(1000, 0)
	f, col, st := newTestForwarder(&now)
	lostRoot := splitMessage(t, "11111111111111111111111111111111", "", 3)
	f.handle(lostRoot[0]) // the last part (with the root) never arrives
	f.handle(lostRoot[1])
	lostChild := splitMessage(t, "22222222222222222222222222222222", "", 3)
	f.handle(lostChild[0])
	f.handle(lostChild[2])
	now = now.Add(4 * time.Second)
	f.sweep(true)
	if len(col.out) != 0 {
		t.Fatal("exported before the timeout")
	}
	now = now.Add(1100 * time.Millisecond)
	f.sweep(true)
	if st.timeouts.Load() != 2 {
		t.Fatalf("timeouts = %d", st.timeouts.Load())
	}
	for _, s := range col.spans() {
		inc := attr(s, AttrIncomplete).GetBoolValue()
		switch fmt.Sprintf("%x", s.TraceId) {
		case "11111111111111111111111111111111":
			if !inc {
				t.Errorf("rootless trace: span %s not incomplete", s.Name)
			}
		default:
			if isRoot := len(s.ParentSpanId) == 0; inc != isRoot {
				t.Errorf("span %s incomplete=%v root=%v", s.Name, inc, isRoot)
			}
		}
	}
}

// A root continued from an incoming traceparent has a parent that is not in the message: it is still the root.
func TestRemoteParentRoot(t *testing.T) {
	f, col, _ := newTestForwarder(nil)
	parts := splitMessage(t, "33333333333333333333333333333333", "00f067aa0ba902b7", 1)
	f.handle(parts[0])
	f.sweep(true)
	for _, s := range col.spans() {
		isRoot := s.Name == "GET /users/{id}/orders"
		if (attr(s, AttrSamplingRatio) != nil) != isRoot {
			t.Errorf("span %s: sampling.ratio presence wrong", s.Name)
		}
		if isRoot && s.Flags&flagIsRemote == 0 {
			t.Errorf("remote parent flag missing: %x", s.Flags)
		}
	}
}

// Resource: extension attributes + forwarder host attributes; host keys cannot be overridden; one ResourceSpans per
// distinct resource.
func TestResources(t *testing.T) {
	f, col, _ := newTestForwarder(nil)
	f.handle(msg(t, func(m map[string]any) {
		r := m["resource"].(map[string]any)
		r["host.name"], r["host.id"], r["telemetry.sdk.language"] = "evil", "attacker", "go"
	}))
	f.handle(msg(t, func(m map[string]any) {
		m["trace_id"] = "0af7651916cd43dd8448eb211c80319c"
		m["resource"].(map[string]any)["service.name"] = "other"
	}))
	f.handle(msg(t, func(m map[string]any) { m["trace_id"] = "0af7651916cd43dd8448eb211c80319d" }))
	f.sweep(true)
	if len(col.out) != 1 || len(col.out[0].ResourceSpans) != 2 {
		t.Fatalf("payloads %d, resource spans %d", len(col.out), len(col.out[0].ResourceSpans))
	}
	rs := col.out[0].ResourceSpans[0]
	got := map[string][]string{}
	for _, kv := range rs.Resource.Attributes {
		got[kv.Key] = append(got[kv.Key], kv.Value.GetStringValue())
	}
	for k, want := range map[string]string{"host.name": "php-host", "host.id": "machine-1", "telemetry.sdk.language": "php",
		"service.name": "shop-api", "os.type": "linux"} {
		if len(got[k]) != 1 || got[k][0] != want {
			t.Errorf("%s = %v, want [%s]", k, got[k], want)
		}
	}
	if n := len(rs.ScopeSpans[0].Spans); n != 4 {
		t.Errorf("shop-api spans = %d, want 4", n)
	}
}

func TestDebugDump(t *testing.T) {
	f, _, _ := newTestForwarder(nil)
	var buf bytes.Buffer
	f.dump = &buf
	f.cfg.debug = 1
	f.handle(msg(t, nil))
	f.handle([]byte(`{"v":1}`))
	out := buf.String()
	for _, want := range []string{"MSG pid=4711", "TRACE 4c2953622b6086caa46723f43306ffb6", "GET /users/{id}/orders", "DROP malformed"} {
		if !strings.Contains(out, want) {
			t.Errorf("dump lacks %q:\n%s", want, out)
		}
	}
}

func TestUnixSocketEndToEnd(t *testing.T) {
	dir, err := os.MkdirTemp("", "fwd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "sub", "php.sock")
	conn, err := listenUnix(path, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}
	f, col, _ := newTestForwarder(nil)
	done := make(chan struct{})
	go func() { readLoop(t.Context(), conn, f); close(done) }()
	c, err := net.Dial("unixgram", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(msg(t, nil)); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.sweep(true)
		if len(col.spans()) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("span not received")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = conn.Close()
	<-done
	// A stale socket file is replaced on restart.
	conn2, err := listenUnix(path, 0o666)
	if err != nil {
		t.Fatalf("rebind over stale socket: %v", err)
	}
	_ = conn2.Close()
}

func TestExporterRetries(t *testing.T) {
	var calls atomic.Int32
	var gotKey, gotType string
	var gotSpans int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		gotKey, gotType = r.Header.Get("openlog-license-key"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		var req collectortrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(b, &req); err == nil {
			for _, rs := range req.ResourceSpans {
				gotSpans += len(rs.ScopeSpans[0].Spans)
			}
		}
	}))
	defer srv.Close()
	st := &counters{}
	e := newExporter(srv.URL+"/v1/traces", "dev-license-key", st)
	var slept []time.Duration
	e.sleep = func(d time.Duration) { slept = append(slept, d) }
	f := newForwarder(testConfig(), testHost(), st, e.enqueue)
	f.handle(msg(t, nil))
	f.sweep(true)
	e.close()
	e.run()
	if calls.Load() != 2 || gotKey != "dev-license-key" || gotType != "application/x-protobuf" || gotSpans != 2 {
		t.Errorf("calls %d key %q type %q spans %d", calls.Load(), gotKey, gotType, gotSpans)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Errorf("Retry-After not honored: %v", slept)
	}
	if st.exportedSpans.Load() != 2 || st.exportRetries.Load() != 1 {
		t.Errorf("stats exported %d retries %d", st.exportedSpans.Load(), st.exportRetries.Load())
	}

	// Permanent rejection is not retried.
	calls.Store(0)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	e2 := newExporter(bad.URL, "wrong", st)
	e2.sleep = func(time.Duration) {}
	if e2.post([]byte{}) || calls.Load() != 1 {
		t.Errorf("401 must fail without retry, calls %d", calls.Load())
	}
}

// splitMessage builds a trace of 4 spans (root + 3 children) split into n datagrams in the extension's style (root
// in the last part, seq/last/dropped_spans at the end of the JSON).
func splitMessage(t *testing.T, traceID, remoteParent string, n int) [][]byte {
	t.Helper()
	root := fmt.Sprintf(`{"id":"a000000000000001","parent":%q,"name":"GET /users/{id}/orders","kind":2,"start":1789302480000000000,"dur":900000000,"status":0,"status_msg":"","attrs":{"http.route":"/users/{id}/orders"},"events":[]}`, remoteParent)
	child := func(i int) string {
		return fmt.Sprintf(`{"id":"b00000000000000%d","parent":"a000000000000001","name":"App\\Services\\Report::build","kind":1,"start":%d,"dur":1000,"attrs":{"openlog.php.segment":"function"}}`, i, 1789302480000000000+i)
	}
	spans := []string{child(1), child(2), child(3), root}
	var out [][]byte
	for seq := 0; seq < n; seq++ { // one child per part, the rest (always including the root) in the last part
		lo, hi := seq, seq+1
		if seq == n-1 {
			hi = len(spans)
		}
		out = append(out, []byte(fmt.Sprintf(`{"v":1,"pid":99,"trace_id":%q,"resource":{"service.name":"php-laravel-83"},"sampling_ratio":0.5,"function_trace":true,"spans":[%s],"seq":%d,"last":%t,"dropped_spans":0}`,
			traceID, strings.Join(spans[lo:hi], ","), seq, seq == n-1)))
	}
	return out
}
