package phpforwarder

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
	"github.com/onuragtas/openlog/agents/infra/internal/resource"

	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

var update = flag.Bool("update", false, "rewrite golden files")

func hostResource() *resourcepb.Resource {
	return resource.Info{HostID: "a0de0000000000000000000000000003", HostName: "php-host", Arch: "arm64", OSName: "debian",
		OSVersion: "12", AgentVersion: "1.2.3", Extra: map[string]string{"team": "shop"}}.Proto()
}

func collect() (*Forwarder, *[]*tracepb.TracesData) {
	var out []*tracepb.TracesData
	f := New(Options{HostResource: hostResource(), Emit: func(td *tracepb.TracesData, _ int) { out = append(out, td) }})
	return f, &out
}

func TestConvertGolden(t *testing.T) {
	f, out := collect()
	// A request continued from an incoming traceparent (root has a remote parent) and a second one of another
	// service; both end up in one payload with one ResourceSpans per resource.
	f.Handle(msg(t, func(m map[string]any) { span(m, 0)["parent"] = "aaaaaaaaaaaaaaaa" }))
	f.Handle(msg(t, func(m map[string]any) {
		m["trace_id"] = "0af7651916cd43dd8448eb211c80319c"
		m["resource"] = map[string]any{"service.name": "cron", "php.sapi": "cli"}
		m["sampling_ratio"], m["dropped_spans"] = 1, 0
		m["spans"] = []any{map[string]any{"id": "b7ad6b7169203331", "name": "php artisan queue:work", "kind": 1,
			"start": 1789302481000000000, "dur": 5, "attrs": map[string]any{"openlog.php.cli": true}}}
	}))
	f.Flush()
	if len(*out) != 1 {
		t.Fatalf("payloads = %d", len(*out))
	}
	b, err := protojson.MarshalOptions{UseProtoNames: false}.Marshal((*out)[0])
	if err != nil {
		t.Fatal(err)
	}
	var compact, got bytes.Buffer
	if err := json.Compact(&compact, b); err != nil { // protojson randomizes whitespace
		t.Fatal(err)
	}
	if err := json.Indent(&got, compact.Bytes(), "", "  "); err != nil {
		t.Fatal(err)
	}
	got.WriteByte('\n')
	golden := filepath.Join("testdata", "otlp.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("OTLP output differs from %s (run go test -update):\n%s", golden, got.Bytes())
	}
}

// The extension must never be able to set host identity or agent attributes.
func TestResourceMergeSecurity(t *testing.T) {
	f, out := collect()
	f.Handle(msg(t, func(m map[string]any) {
		r := m["resource"].(map[string]any)
		r["host.id"] = "attacker"
		r["host.name"] = "evil"
		r["os.type"] = "windows"
		r["openlog.agent.name"] = "fake"
		r["openlog.entity.type"] = "host"
		r["telemetry.sdk.language"] = "go"
		r["tenant_id"] = "other"
		r["k8s.pod.name"] = "web-1"
		r["container.id"] = "abc"
		r["service.instance.id"] = "i-1"
	}))
	f.Flush()
	attrs := map[string][]string{}
	for _, kv := range (*out)[0].ResourceSpans[0].Resource.Attributes {
		attrs[kv.Key] = append(attrs[kv.Key], kv.Value.GetStringValue())
	}
	for key, want := range map[string]string{
		"host.id": "a0de0000000000000000000000000003", "host.name": "php-host", "os.type": "linux",
		"openlog.agent.name": "openlog-infra-agent", "telemetry.sdk.language": "php", "k8s.pod.name": "web-1",
		"container.id": "abc", "service.instance.id": "i-1", "service.name": "shop-api",
	} {
		if len(attrs[key]) != 1 || attrs[key][0] != want {
			t.Errorf("%s = %v, want exactly [%s]", key, attrs[key], want)
		}
	}
	for _, key := range []string{"tenant_id", "openlog.entity.type", "team"} {
		if _, ok := attrs[key]; ok {
			t.Errorf("%s must not be on a PHP resource", key)
		}
	}
}

func TestBatchSplitsLargePayloads(t *testing.T) {
	var out []*tracepb.TracesData
	f := New(Options{HostResource: hostResource(), MaxBatchBytes: 2000,
		Emit: func(td *tracepb.TracesData, n int) {
			if n != SpanCount(td) {
				t.Errorf("span count %d != %d", n, SpanCount(td))
			}
			out = append(out, td)
		}})
	for i := 0; i < 20; i++ {
		f.Handle(msg(t, func(m map[string]any) { m["trace_id"] = traceHex(i) }))
	}
	f.Flush()
	total := 0
	for _, td := range out {
		total += SpanCount(td)
	}
	if len(out) < 5 || total != 40 {
		t.Errorf("payloads = %d, spans = %d", len(out), total)
	}
	snap := f.o.Stats.Snapshot().PHP
	if snap.Messages["accepted"] != 20 || snap.Spans != 40 {
		t.Errorf("stats = %+v", snap)
	}
}

func TestStatsResults(t *testing.T) {
	f, _ := collect()
	f.Handle([]byte("garbage"))
	f.Handle(msg(t, func(m map[string]any) { m["v"] = 9 }))
	f.Handle(msg(t, nil))
	f.Flush()
	p := f.o.Stats.Snapshot().PHP
	if p.Messages["malformed"] != 1 || p.Messages["unsupported_version"] != 1 || p.Messages["accepted"] != 1 || p.Spans != 2 {
		t.Errorf("stats = %+v", p)
	}
	if f.LastReceived().IsZero() {
		t.Error("last received not recorded")
	}
}

func TestDetectRuntime(t *testing.T) {
	cases := map[string]struct {
		files    map[string]string
		services []string
		want     bool
	}{
		"nothing":            {map[string]string{"/usr/bin/python3": ""}, nil, false},
		"php-fpm service":    {nil, []string{"php-fpm"}, true},
		"cli debian":         {map[string]string{"/usr/bin/php8.3": ""}, nil, true},
		"cli docker image":   {map[string]string{"/usr/local/bin/php": ""}, nil, true},
		"phpize is not php":  {map[string]string{"/usr/bin/phpize": "", "/usr/bin/php-config": ""}, nil, false},
		"apache mod_php":     {map[string]string{"/etc/apache2/mods-enabled/php8.2.load": ""}, []string{"apache-httpd"}, true},
		"module w/o apache":  {map[string]string{"/usr/lib/apache2/modules/libphp8.2.so": ""}, nil, false},
		"apache without php": {map[string]string{"/etc/apache2/mods-enabled/status.load": ""}, []string{"apache-httpd"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := hostfstest.Build(t, tc.files)
			var svcs []discoveryService
			for _, id := range tc.services {
				svcs = append(svcs, discoveryService{RuleID: id})
			}
			if got, reason := DetectRuntime(fs, svcs); got != tc.want {
				t.Errorf("DetectRuntime = %v (%s), want %v", got, reason, tc.want)
			}
		})
	}
}
