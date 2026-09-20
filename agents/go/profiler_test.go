package openlog

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	"go.opentelemetry.io/otel/attribute"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	colprofilespb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	"google.golang.org/protobuf/proto"
)

func testCPUProfile() *profile.Profile {
	main := &profile.Function{ID: 1, Name: "main.main"}
	work := &profile.Function{ID: 2, Name: "main.work"}
	lm := &profile.Location{ID: 1, Line: []profile.Line{{Function: main}}}
	lw := &profile.Location{ID: 2, Line: []profile.Line{{Function: work}}}
	return &profile.Profile{
		SampleType:    []*profile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		Period:        10_000_000,
		DurationNanos: 1_000_000_000,
		Function:      []*profile.Function{main, work},
		Location:      []*profile.Location{lm, lw},
		Sample:        []*profile.Sample{{Location: []*profile.Location{lw, lm}, Value: []int64{40_000_000}}},
	}
}

func testProfiler(t *testing.T, endpoint, compression string, retry retrySettings) *profiler {
	t.Helper()
	cfg := &Config{
		Endpoint:        endpoint,
		Compression:     compression,
		Headers:         map[string]string{LicenseKeyHeader: "olk_test"},
		ExportTimeout:   2 * time.Second,
		ProfileInterval: time.Second,
		retry:           retry,
	}
	res := sdkresource.NewSchemaless(attribute.String("service.name", "checkout"), attribute.Int64("process.pid", 42))
	p, err := newProfiler(cfg, res, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type capture struct {
	mu     sync.Mutex
	calls  int
	path   string
	header http.Header
	body   []byte
}

func (c *capture) handler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.calls++
		c.path, c.header, c.body = r.URL.Path, r.Header.Clone(), b
		c.mu.Unlock()
		w.WriteHeader(status)
	}
}

// The profile must reach /v1/profiles as OTLP protobuf, authenticated like every other signal this agent
// sends: the profiler writes its own exporter because the SDK has none, and that is exactly the place where
// a rule could quietly go missing.
func TestProfilerPostsProfile(t *testing.T) {
	var c capture
	srv := httptest.NewServer(c.handler(http.StatusOK))
	defer srv.Close()

	p := testProfiler(t, srv.URL, "none", retrySettings{initial: time.Millisecond, max: time.Millisecond, elapsed: 10 * time.Millisecond})
	p.exportProfile(testCPUProfile())

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls != 1 {
		t.Fatalf("%d requests", c.calls)
	}
	if c.path != "/v1/profiles" {
		t.Errorf("path %q", c.path)
	}
	if got := c.header.Get(LicenseKeyHeader); got != "olk_test" {
		t.Errorf("license key header %q", got)
	}
	if got := c.header.Get("Content-Type"); got != "application/x-protobuf" {
		t.Errorf("content type %q", got)
	}
	var req colprofilespb.ExportProfilesServiceRequest
	if err := proto.Unmarshal(c.body, &req); err != nil {
		t.Fatalf("body is not an OTLP profiles request: %v", err)
	}
	if req.Dictionary == nil || len(req.ResourceProfiles) != 1 {
		t.Fatalf("request %+v", &req)
	}
	// The resource the agent detected travels with the profile, typed: an int must not arrive as a string,
	// because the server rebuilds identity from these.
	attrs := map[string]string{}
	for _, kv := range req.ResourceProfiles[0].Resource.Attributes {
		attrs[kv.Key] = kv.Value.String()
	}
	if _, ok := attrs["service.name"]; !ok {
		t.Errorf("resource attributes %v", attrs)
	}
	for _, kv := range req.ResourceProfiles[0].Resource.Attributes {
		if kv.Key == "process.pid" && kv.Value.GetIntValue() != 42 {
			t.Errorf("process.pid did not survive as an int: %v", kv.Value)
		}
	}
}

func TestProfilerCompresses(t *testing.T) {
	var c capture
	srv := httptest.NewServer(c.handler(http.StatusOK))
	defer srv.Close()

	p := testProfiler(t, srv.URL, "gzip", retrySettings{initial: time.Millisecond, max: time.Millisecond, elapsed: 10 * time.Millisecond})
	p.exportProfile(testCPUProfile())

	c.mu.Lock()
	defer c.mu.Unlock()
	if got := c.header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("content encoding %q", got)
	}
	zr, err := gzip.NewReader(bytes.NewReader(c.body))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var req colprofilespb.ExportProfilesServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decompressed body is not an OTLP profiles request: %v", err)
	}
}

// A profile measures a window that has already passed. Retrying it forever would push the next window out,
// so retries are bounded and then the profile is dropped.
func TestProfilerRetriesAndGivesUp(t *testing.T) {
	var c capture
	srv := httptest.NewServer(c.handler(http.StatusServiceUnavailable))
	defer srv.Close()

	p := testProfiler(t, srv.URL, "none", retrySettings{initial: time.Millisecond, max: 2 * time.Millisecond, elapsed: 30 * time.Millisecond})
	done := make(chan struct{})
	go func() { defer close(done); p.exportProfile(testCPUProfile()) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("export did not give up")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls < 2 {
		t.Errorf("%d requests, want a retry", c.calls)
	}
}

// A 4xx is the server saying the request is wrong; sending it again cannot make it right.
func TestProfilerDoesNotRetryRejections(t *testing.T) {
	var c capture
	srv := httptest.NewServer(c.handler(http.StatusBadRequest))
	defer srv.Close()

	p := testProfiler(t, srv.URL, "none", retrySettings{initial: time.Millisecond, max: time.Millisecond, elapsed: time.Second})
	p.exportProfile(testCPUProfile())

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls != 1 {
		t.Errorf("%d requests, want 1", c.calls)
	}
}

func TestProfilerURLKeepsBasePath(t *testing.T) {
	p := testProfiler(t, "https://ingest.example.com:4318/otlp", "none", retrySettings{})
	if p.url != "https://ingest.example.com:4318/otlp/v1/profiles" {
		t.Errorf("url %q", p.url)
	}
}

// Shutdown must return even when no profile is running, and must not block on the window.
func TestProfilerShutdownIsPrompt(t *testing.T) {
	var c capture
	srv := httptest.NewServer(c.handler(http.StatusOK))
	defer srv.Close()

	p := testProfiler(t, srv.URL, "none", retrySettings{initial: time.Millisecond, max: time.Millisecond, elapsed: 10 * time.Millisecond})
	p.interval = 50 * time.Millisecond
	p.start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	// Shutting down twice is what happens when a caller wraps the agent's own shutdown; it must not panic.
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}
