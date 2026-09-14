package openlog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func traceparentFlags(ctx context.Context) string {
	c := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, c)
	tp := c.Get("traceparent")
	return tp[strings.LastIndexByte(tp, '-')+1:]
}

func TestRootSpansCarryRandomFlag(t *testing.T) {
	sdk := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(1, false)))
	tr := randomTracerProvider{tp: sdk}.Tracer("t")

	ctx, root := tr.Start(context.Background(), "root")
	cctx, child := tr.Start(ctx, "child")
	if !root.SpanContext().IsRandom() || !child.SpanContext().IsRandom() {
		t.Fatalf("random flag: root=%v child=%v", root.SpanContext().IsRandom(), child.SpanContext().IsRandom())
	}
	if f := traceparentFlags(cctx); f != "03" {
		t.Fatalf("traceparent flags %q, want 03", f)
	}

	// A remote W3C Level 1 parent (no random flag) is continued unchanged; a Level 2 parent keeps it.
	for _, tc := range []struct {
		flags trace.TraceFlags
		want  string
	}{{trace.FlagsSampled, "01"}, {trace.FlagsSampled | trace.FlagsRandom, "03"}} {
		parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: tc.flags, Remote: true})
		ctx, _ := tr.Start(trace.ContextWithRemoteSpanContext(context.Background(), parent), "entry")
		if f := traceparentFlags(ctx); f != tc.want {
			t.Errorf("remote parent flags %v: traceparent flags %q, want %q", tc.flags, f, tc.want)
		}
	}

	// A tracestate on an invalid (root) context still reaches the root sampler.
	sdk = sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(0.25, false)),
		sdktrace.WithIDGenerator(fixedIDs{traceIDWithRandomness(0)}))
	st, _ := trace.ParseTraceState("ot=rv:ffffffffffffff")
	_, root = randomTracerProvider{tp: sdk}.Tracer("t").Start(trace.ContextWithSpanContext(context.Background(), trace.SpanContext{}.WithTraceState(st)), "root")
	if !root.SpanContext().IsSampled() || !root.SpanContext().IsRandom() || root.SpanContext().TraceState().String() != "ot=th:c;rv:ffffffffffffff" {
		t.Fatalf("root with incoming rv: %+v %q", root.SpanContext(), root.SpanContext().TraceState())
	}
}

func TestSamplerWritesRV(t *testing.T) {
	var next uint64
	calls := 0
	orig := newRandomness
	newRandomness = func() (uint64, string) {
		calls++
		return next, fmt.Sprintf("%014x", next)
	}
	defer func() { newRandomness = orig }()

	start := func(ratio float64, ctx context.Context) (trace.Span, trace.Span) {
		// Trace id randomness 0 would always be dropped at 0.25: the decision must use rv.
		sdk := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(ratio, true)),
			sdktrace.WithIDGenerator(fixedIDs{traceIDWithRandomness(0)}))
		tr := randomTracerProvider{tp: sdk}.Tracer("t")
		ctx, root := tr.Start(ctx, "root")
		_, child := tr.Start(ctx, "child")
		return root, child
	}
	for _, tc := range []struct {
		ratio   float64
		rv      uint64
		sampled bool
		ts      string
	}{
		{0.25, 0xd0000000000000, true, "ot=th:c;rv:d0000000000000"},
		{0.25, 0x10000000000000, false, "ot=rv:10000000000000"},
		{1, 0x01020304050607, true, "ot=rv:01020304050607"},
		{0, 0xffffffffffffff, false, "ot=rv:ffffffffffffff"},
	} {
		next = tc.rv
		root, child := start(tc.ratio, context.Background())
		if root.SpanContext().IsSampled() != tc.sampled || root.SpanContext().TraceState().String() != tc.ts {
			t.Errorf("ratio %g rv %x: sampled=%v tracestate %q, want %v %q", tc.ratio, tc.rv,
				root.SpanContext().IsSampled(), root.SpanContext().TraceState(), tc.sampled, tc.ts)
		}
		if child.SpanContext().TraceState().String() != tc.ts {
			t.Errorf("ratio %g: child tracestate %q", tc.ratio, child.SpanContext().TraceState())
		}
	}

	// An incoming rv is never replaced.
	calls = 0
	st, _ := trace.ParseTraceState("ot=rv:ffffffffffffff")
	for _, ratio := range []float64{0.25, 1} {
		root, _ := start(ratio, trace.ContextWithSpanContext(context.Background(), trace.SpanContext{}.WithTraceState(st)))
		if got := parseOT(root.SpanContext().TraceState().Get(otKey)); !strings.Contains(got.String(), "rv:ffffffffffffff") || calls != 0 {
			t.Errorf("ratio %g: incoming rv replaced: %q (calls %d)", ratio, got, calls)
		}
	}

	// The default generator yields 14 hex digits of 56 bits.
	r, s := orig()
	if len(s) != 14 || r > randomnessMask {
		t.Errorf("newRandomness = %x %q", r, s)
	}
}

func TestResolveHostIDPrefersInfraRuntimeFile(t *testing.T) {
	ctx := context.Background()
	defer func(f func(context.Context) string) { platformHostID = f }(platformHostID)
	platformHostID = func(context.Context) string { return "" }
	write := func(p, v string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	write(filepath.Join(root, "etc/machine-id"), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")

	// Absent → machine-id.
	if id, src := resolveHostID(ctx, hostFS{root: root}, "/run/openlog-infra-agent", "", t.TempDir()); id != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || src != "/etc/machine-id" {
		t.Fatalf("no runtime file: %q %q", id, src)
	}
	// Published under the host root wins over the (image's) machine-id.
	write(filepath.Join(root, "run/openlog-infra-agent/host-id"), "3f0e9c52-1b7a-4c1e-9d0a-2f7f5e1c8b11\n")
	if id, src := resolveHostID(ctx, hostFS{root: root}, "/run/openlog-infra-agent", "", t.TempDir()); id != "3f0e9c52-1b7a-4c1e-9d0a-2f7f5e1c8b11" || src != hostIDSourceInfraRun {
		t.Fatalf("runtime file: %q %q", id, src)
	}
	// With a host root, a directly mounted runtime dir (plain path) is used too.
	mounted := t.TempDir()
	write(filepath.Join(mounted, "host-id"), "0badc0de-0000-4000-8000-000000000001")
	if id, src := resolveHostID(ctx, hostFS{root: t.TempDir()}, mounted, "", t.TempDir()); id != "0badc0de-0000-4000-8000-000000000001" || src != hostIDSourceInfraRun {
		t.Fatalf("mounted runtime dir: %q %q", id, src)
	}
	// Invalid content is ignored.
	write(filepath.Join(root, "run/openlog-infra-agent/host-id"), "bad id")
	if id, _ := resolveHostID(ctx, hostFS{root: root}, "/run/openlog-infra-agent", "", t.TempDir()); id != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("invalid runtime file: %q", id)
	}
}

func TestConfigSamplingRVAndRuntimeDir(t *testing.T) {
	env := map[string]string{"OPENLOG_SAMPLING_RV": "true", "OPENLOG_INFRA_RUNTIME_DIR": "/mnt/openlog"}
	cfg, _, err := loadConfig(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, nil)
	if err != nil || !cfg.SamplingRV || cfg.InfraRuntimeDir != "/mnt/openlog" {
		t.Fatalf("env: %+v %v", cfg, err)
	}
	cfg, _, err = loadConfig(func(string) (string, bool) { return "", false }, []Option{WithSamplingRV(true)})
	if err != nil || !cfg.SamplingRV || cfg.InfraRuntimeDir != defaultInfraRuntimeDir {
		t.Fatalf("defaults/option: %+v %v", cfg, err)
	}
}
