package openlog

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/onuragtas/openlog/agents/go/openloghttp"
)

// backendWeight mirrors internal/apm/sampling.go (apm.md §4) for tracestate ot=th.
func backendWeight(t *testing.T, ts string) float64 {
	t.Helper()
	v, err := trace.ParseTraceState(ts)
	if err != nil {
		t.Fatalf("tracestate %q: %v", ts, err)
	}
	p, ok := parseOT(v.Get(otKey)).probability()
	if !ok {
		return 1
	}
	return 1 / p
}

func TestThresholdEncoding(t *testing.T) {
	for _, tc := range []struct {
		ratio float64
		th    string
	}{
		{0.5, "8"}, {0.25, "c"}, {0.125, "e"}, {0.75, "4"}, {0.1, "e6666666666666"}, {0.001, "ffbe76c8b43958"},
	} {
		s := newRatioRoot(tc.ratio).(ratioRoot)
		if s.th != tc.th {
			t.Errorf("ratio %g: th=%s want %s", tc.ratio, s.th, tc.th)
		}
		p, ok := otValue{{"th", s.th}}.probability()
		if !ok || math.Abs(p-tc.ratio) > 1e-12 {
			t.Errorf("ratio %g: decoded p=%v ok=%v", tc.ratio, p, ok)
		}
	}
	if _, ok := newRatioRoot(1 - 1e-18).(ratioRoot); ok {
		t.Error("ratio ~1 should be AlwaysSample")
	}
	if encodeThreshold(0) != "0" {
		t.Error("th:0")
	}
	if p, _ := (otValue{{"p", "2"}}).probability(); p != 0.25 {
		t.Errorf("legacy p:2 = %v", p)
	}
}

type fixedIDs struct{ traceID trace.TraceID }

func (g fixedIDs) NewIDs(context.Context) (trace.TraceID, trace.SpanID) {
	return g.traceID, trace.SpanID{1}
}
func (g fixedIDs) NewSpanID(context.Context, trace.TraceID) trace.SpanID { return trace.SpanID{2} }

func traceIDWithRandomness(r uint64) trace.TraceID {
	var id trace.TraceID
	id[0] = 0xab // upper bytes must not matter
	binary.BigEndian.PutUint64(id[8:], r|0xff<<56)
	return id
}

func TestRootConsistentDecisionAndTracestate(t *testing.T) {
	threshold := uint64(3) << 54 // ratio 0.25 → T = 0xc0000000000000
	for _, tc := range []struct {
		r       uint64
		sampled bool
	}{{0, false}, {threshold - 1, false}, {threshold, true}, {maxThreshold - 1, true}} {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(0.25)), sdktrace.WithSpanProcessor(rec),
			sdktrace.WithIDGenerator(fixedIDs{traceIDWithRandomness(tc.r)}))
		ctx, root := tp.Tracer("t").Start(context.Background(), "root")
		_, child := tp.Tracer("t").Start(ctx, "child")
		child.End()
		root.End()
		sc := root.SpanContext()
		if sc.IsSampled() != tc.sampled {
			t.Fatalf("r=%x sampled=%v want %v", tc.r, sc.IsSampled(), tc.sampled)
		}
		got := sc.TraceState().Get(otKey)
		if tc.sampled && got != "th:c" || !tc.sampled && got != "" {
			t.Fatalf("r=%x tracestate ot=%q", tc.r, got)
		}
		if tc.sampled {
			if child.SpanContext().TraceState().Get(otKey) != "th:c" {
				t.Fatal("local child lost ot=th")
			}
			for _, s := range rec.Ended() {
				if w := backendWeight(t, s.SpanContext().TraceState().String()); w != 4 {
					t.Fatalf("%s weight %v", s.Name(), w)
				}
			}
		}
	}
}

func TestRootHonoursRandomnessAndLimits(t *testing.T) {
	s := newRatioRoot(0.25)
	params := func(ts string) sdktrace.SamplingParameters {
		st, err := trace.ParseTraceState(ts)
		if err != nil {
			t.Fatal(err)
		}
		// Invalid (root) span context that still carries a tracestate.
		ctx := trace.ContextWithSpanContext(context.Background(), trace.SpanContext{}.WithTraceState(st))
		return sdktrace.SamplingParameters{ParentContext: ctx, TraceID: traceIDWithRandomness(0)}
	}

	// rv above the threshold wins over the trace id (which alone would be dropped).
	res := s.ShouldSample(params("vendor=x,ot=rv:d0000000000000;foo:bar"))
	if res.Decision != sdktrace.RecordAndSample {
		t.Fatalf("rv ignored: %v", res.Decision)
	}
	if got := res.Tracestate.String(); got != "ot=th:c;rv:d0000000000000;foo:bar,vendor=x" {
		t.Fatalf("tracestate %q", got)
	}
	// rv below the threshold drops and removes a stale th.
	res = s.ShouldSample(params("ot=th:0;rv:10000000000000"))
	if res.Decision != sdktrace.Drop || res.Tracestate.String() != "ot=rv:10000000000000" {
		t.Fatalf("drop: %v %q", res.Decision, res.Tracestate.String())
	}

	// 32 members: ot is inserted first, the right-most member dropped.
	members := make([]string, 32)
	for i := range members {
		members[i] = fmt.Sprintf("k%d=v", i)
	}
	res = s.ShouldSample(params("ot=rv:ffffffffffffff," + strings.Join(members[:31], ",")))
	if res.Tracestate.Len() != 32 || res.Tracestate.Get(otKey) != "th:c;rv:ffffffffffffff" {
		t.Fatalf("32 members with ot: len=%d ot=%q", res.Tracestate.Len(), res.Tracestate.Get(otKey))
	}
	res = s.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: params(strings.Join(members, ",")).ParentContext,
		TraceID:       traceIDWithRandomness(maxThreshold - 1),
	})
	if res.Tracestate.Len() != 32 || res.Tracestate.Get(otKey) != "th:c" || res.Tracestate.Get("k31") != "" {
		t.Fatalf("full tracestate: len=%d %q", res.Tracestate.Len(), res.Tracestate.String())
	}

	// ot value capped at 256 characters: unknown sub-keys are dropped, th and rv kept.
	// 253 characters on input; th:c pushes it over 256.
	long := "ot=rv:ffffffffffffff;a:" + strings.Repeat("x", 200) + ";b:" + strings.Repeat("y", 30)
	res = s.ShouldSample(params(long))
	ot := res.Tracestate.Get(otKey)
	if len(ot) > 256 || !strings.HasPrefix(ot, "th:c;rv:ffffffffffffff;a:") || strings.Contains(ot, ";b:") {
		t.Fatalf("long ot (%d): %q", len(ot), ot)
	}
}

func TestRemoteParentReadsThreshold(t *testing.T) {
	for _, tc := range []struct {
		ts   string
		want float64 // 0 = no attribute
	}{
		{"ot=th:c", 0.25}, {"ot=rv:01020304050607;th:8", 0.5}, {"ot=p:3", 0.125}, {"ot=th:0", 0}, {"", 0}, {"ot=th:zz", 0},
	} {
		st, _ := trace.ParseTraceState(tc.ts)
		parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1},
			TraceFlags: trace.FlagsSampled, TraceState: st, Remote: true})
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(1)), sdktrace.WithSpanProcessor(rec))
		ctx, entry := tp.Tracer("t").Start(trace.ContextWithRemoteSpanContext(context.Background(), parent), "entry")
		_, inner := tp.Tracer("t").Start(ctx, "inner")
		inner.End()
		entry.End()
		for _, s := range rec.Ended() {
			var got float64
			for _, a := range s.Attributes() {
				if a.Key == SamplingRatioKey {
					got = a.Value.AsFloat64()
				}
			}
			want := tc.want
			if s.Name() == "inner" {
				want = 0 // only the local entry span
			}
			if got != want {
				t.Errorf("%q %s: sampling.ratio=%v want %v", tc.ts, s.Name(), got, want)
			}
			if s.SpanContext().TraceState().String() != st.String() {
				t.Errorf("%q %s: tracestate %q not propagated", tc.ts, s.Name(), s.SpanContext().TraceState())
			}
		}
	}
}

// Two in-process services: frontend samples new traces at 0.25 and calls backend
// (configured ratio 1, parent-based). Both must yield the same weighted request count.
func TestSamplingPropagationRoundTrip(t *testing.T) {
	prop := propagation.TraceContext{}
	backRec := tracetest.NewSpanRecorder()
	backTP := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(1)), sdktrace.WithSpanProcessor(backRec))
	backMux := http.NewServeMux()
	backMux.HandleFunc("GET /work", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	back := httptest.NewServer(openloghttp.Middleware(backMux,
		openloghttp.WithTracerProvider(backTP), openloghttp.WithPropagators(prop)))
	defer back.Close()

	frontRec := tracetest.NewSpanRecorder()
	frontTP := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(0.25)), sdktrace.WithSpanProcessor(frontRec))
	client := &http.Client{Transport: openloghttp.Transport(nil,
		openloghttp.WithTracerProvider(frontTP), openloghttp.WithPropagators(prop))}
	frontMux := http.NewServeMux()
	frontMux.HandleFunc("GET /checkout", func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, back.URL+"/work", nil)
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_ = resp.Body.Close()
	})
	front := httptest.NewServer(openloghttp.Middleware(frontMux,
		openloghttp.WithTracerProvider(frontTP), openloghttp.WithPropagators(prop)))
	defer front.Close()

	const n = 2000
	for range n {
		resp, err := http.Get(front.URL + "/checkout")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	weighted := func(rec *tracetest.SpanRecorder, kind trace.SpanKind) (count int, sum float64) {
		for _, s := range rec.Ended() {
			if s.SpanKind() != kind {
				continue
			}
			count++
			w := backendWeight(t, s.SpanContext().TraceState().String())
			var ratio float64
			for _, a := range s.Attributes() {
				if a.Key == SamplingRatioKey {
					ratio = a.Value.AsFloat64()
				}
			}
			if w != 4 || ratio != 0.25 {
				t.Fatalf("%s: weight %v sampling.ratio %v tracestate %q", s.Name(), w, ratio, s.SpanContext().TraceState())
			}
			sum += w
		}
		return count, sum
	}
	frontN, frontW := weighted(frontRec, trace.SpanKindServer)
	backN, backW := weighted(backRec, trace.SpanKindServer)
	if frontN != backN || frontN == 0 {
		t.Fatalf("sampled entry spans: frontend %d backend %d", frontN, backN)
	}
	// Binomial(2000, 0.25): sd ≈ 19.4 spans; allow ±5 sd.
	if math.Abs(frontW-n) > 5*19.4*4 || frontW != backW {
		t.Fatalf("weighted requests frontend %v backend %v, want ≈%d", frontW, backW, n)
	}
	t.Logf("%d requests: %d sampled, weighted frontend=%v backend=%v", n, frontN, frontW, backW)
}
