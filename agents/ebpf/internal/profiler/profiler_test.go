package profiler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	colprofilespb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/agents/ebpf/internal/aggregate"
	"github.com/onuragtas/openlog/agents/ebpf/internal/config"
	"github.com/onuragtas/openlog/agents/ebpf/internal/otlpprofiles"
)

type fakeSym struct{}

func (fakeSym) Resolve(_ int, a uint64) otlpprofiles.Frame {
	return otlpprofiles.Frame{Function: fmt.Sprintf("f%d", a), Address: a}
}

type namer map[int]string

func (n namer) Name(pid int) string { return n[pid] }

// scriptedSampler returns one batch per call and cancels the context when the script runs out, so the loop
// terminates without a timer.
type scriptedSampler struct {
	batches [][]aggregate.RawSample
	errs    []error
	calls   int
	closed  bool
	cancel  context.CancelFunc
}

func (s *scriptedSampler) Sample(context.Context, time.Duration) ([]aggregate.RawSample, error) {
	i := s.calls
	s.calls++
	if i >= len(s.batches) {
		s.cancel()
		return nil, nil
	}
	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return s.batches[i], err
}

func (s *scriptedSampler) Close() error { s.closed = true; return nil }

type recorder struct {
	bodies [][]byte
	err    error
}

func (r *recorder) Send(_ context.Context, b []byte) error {
	r.bodies = append(r.bodies, b)
	return r.err
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func run(t *testing.T, s *scriptedSampler, e *recorder, n namer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	cfg, err := config.Load(config.Env(map[string]string{"OPENLOG_LICENSE_KEY": "olk_test"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, Options{
		Config: cfg, Sampler: s, Exporter: e, Symbolizer: fakeSym{}, Namer: n,
		HostID: "host-1", Version: "1.2.3", Log: quiet(),
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func decode(t *testing.T, body []byte) *colprofilespb.ExportProfilesServiceRequest {
	t.Helper()
	var req colprofilespb.ExportProfilesServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		t.Fatalf("payload is not an ExportProfilesServiceRequest: %v", err)
	}
	return &req
}

func serviceOf(t *testing.T, req *colprofilespb.ExportProfilesServiceRequest) string {
	t.Helper()
	for _, rp := range req.GetResourceProfiles() {
		for _, kv := range rp.GetResource().GetAttributes() {
			if kv.GetKey() == "service.name" {
				return kv.GetValue().GetStringValue()
			}
		}
	}
	return ""
}

func TestOneRequestPerServiceInAStableOrder(t *testing.T) {
	s := &scriptedSampler{batches: [][]aggregate.RawSample{{
		{PID: 1, Addrs: []uint64{1, 2}, Count: 3},
		{PID: 2, Addrs: []uint64{3}, Count: 1},
	}}}
	e := &recorder{}
	run(t, s, e, namer{1: "redis", 2: "nginx"})

	if len(e.bodies) != 2 {
		t.Fatalf("sent %d requests, want one per service", len(e.bodies))
	}
	if got := serviceOf(t, decode(t, e.bodies[0])); got != "nginx" {
		t.Errorf("first request is %q, want nginx (sorted)", got)
	}
	if got := serviceOf(t, decode(t, e.bodies[1])); got != "redis" {
		t.Errorf("second request is %q, want redis", got)
	}
}

// An interval in which nothing ran must cost no round trip.
func TestIdleIntervalSendsNothing(t *testing.T) {
	s := &scriptedSampler{batches: [][]aggregate.RawSample{nil, {}}}
	e := &recorder{}
	run(t, s, e, namer{})
	if len(e.bodies) != 0 {
		t.Errorf("sent %d requests for an idle machine", len(e.bodies))
	}
}

// A window that could not be sampled must not end the loop: the next one is worth more than this one.
func TestSamplingErrorDoesNotStopTheLoop(t *testing.T) {
	s := &scriptedSampler{
		batches: [][]aggregate.RawSample{nil, {{PID: 1, Addrs: []uint64{1}, Count: 1}}},
		errs:    []error{errors.New("perf_event_open: permission denied")},
	}
	e := &recorder{}
	run(t, s, e, namer{1: "redis"})
	if len(e.bodies) != 1 {
		t.Errorf("sent %d requests: the loop did not recover", len(e.bodies))
	}
}

func TestExportErrorDoesNotStopTheLoop(t *testing.T) {
	s := &scriptedSampler{batches: [][]aggregate.RawSample{
		{{PID: 1, Addrs: []uint64{1}, Count: 1}},
		{{PID: 1, Addrs: []uint64{1}, Count: 1}},
	}}
	e := &recorder{err: errors.New("ingest unreachable")}
	run(t, s, e, namer{1: "redis"})
	if len(e.bodies) != 2 {
		t.Errorf("attempted %d exports, want both windows", len(e.bodies))
	}
}

// Being asked to stop is not a failure, and the kernel resources must be released.
func TestCancellationClosesTheSampler(t *testing.T) {
	s := &scriptedSampler{}
	e := &recorder{}
	run(t, s, e, namer{})
	if !s.closed {
		t.Error("the sampler was not closed")
	}
}

// The resource is what ties a profile to the host its metrics are already on.
func TestResourceCarriesHostAndScope(t *testing.T) {
	s := &scriptedSampler{batches: [][]aggregate.RawSample{{{PID: 1, Addrs: []uint64{1}, Count: 1}}}}
	e := &recorder{}
	run(t, s, e, namer{1: "redis"})
	req := decode(t, e.bodies[0])
	found := map[string]string{}
	for _, kv := range req.GetResourceProfiles()[0].GetResource().GetAttributes() {
		found[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	for k, want := range map[string]string{
		"service.name": "redis", "host.id": "host-1", "os.type": "linux",
		"telemetry.sdk.name": "openlog-ebpf", "telemetry.sdk.version": "1.2.3",
	} {
		if found[k] != want {
			t.Errorf("%s = %q, want %q", k, found[k], want)
		}
	}
}
