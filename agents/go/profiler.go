package openlog

// Continuous CPU profiling: a profile of this process, taken over a window and exported as OTLP profiles
// (docs/contracts/profiles.md).
//
// On by default: a service nobody profiled is a service whose slow span has no answer, and the sampling cost
// is a few percent of one core. It can be turned off (OPENLOG_PROFILING=false), and that escape hatch is not
// decoration — Go allows one CPU profile per process, so while this runs, net/http/pprof's own
// /debug/pprof/profile returns "cpu profiling already in use". A process that needs that endpoint turns this
// off; without the switch its only way out would be to drop the SDK.
//
// HTTP only. The published OTLP profiles modules ship message types without gRPC service stubs, so there is
// no generated profiles client to dial — and posting protobuf over HTTP to a port that speaks gRPC would
// fail in a way that looks like a network fault. With the gRPC protocol the profiler stays off and says so.
//
// The exporter is written here rather than reused from the OTel SDK because the SDK has no profiles
// exporter: profiles are not part of its stable surface. It follows the same rules as the others all the
// same — the license key header, gzip, the export timeout and the retry schedule all come from Config.

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/pprof/profile"
	"go.opentelemetry.io/otel/attribute"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	colprofilespb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/agents/go/internal/otlpprofiles"
)

// ProfileScope is the instrumentation scope of the profiles this agent produces.
const ProfileScope = "github.com/onuragtas/openlog/agents/go/profiler"

type profiler struct {
	url      string
	headers  map[string]string
	gzip     bool
	timeout  time.Duration
	retry    retrySettings
	interval time.Duration
	res      []*commonpb.KeyValue
	log      *slog.Logger
	client   *http.Client

	stop chan struct{}
	done chan struct{}
	once sync.Once

	// busyLogged keeps "another profile is already running" to one line: it is a condition that lasts for
	// as long as the other profiler does, and one warning per minute would be noise, not information.
	busyLogged bool
}

func newProfiler(cfg *Config, res *sdkresource.Resource, log *slog.Logger) (*profiler, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("endpoint: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/profiles"
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	return &profiler{
		url:      u.String(),
		headers:  headers,
		gzip:     cfg.Compression == "gzip",
		timeout:  cfg.ExportTimeout,
		retry:    cfg.retry,
		interval: cfg.ProfileInterval,
		res:      resourceAttrs(res),
		log:      log,
		client:   &http.Client{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}, nil
}

func (p *profiler) start() {
	go p.run()
}

func (p *profiler) run() {
	defer close(p.done)
	for {
		prof, stopped, err := p.collect()
		if err != nil {
			if errors.Is(err, errProfileBusy) {
				if !p.busyLogged {
					p.busyLogged = true
					p.log.Warn("profiling is not running: another CPU profile is already active in this process (net/http/pprof or a test)")
				}
			} else {
				p.log.Debug("cpu profile", "error", err)
			}
			if stopped {
				return
			}
			continue
		}
		p.busyLogged = false
		if prof != nil {
			p.exportProfile(prof)
		}
		if stopped {
			return
		}
	}
}

var errProfileBusy = errors.New("cpu profiling already in use")

// collect takes one CPU profile covering the interval. It reports whether shutdown ended the window, so the
// final, partial profile is still exported rather than thrown away — the last minute before a crash or a
// deploy is usually the one worth having.
func (p *profiler) collect() (*profile.Profile, bool, error) {
	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		// The runtime allows one CPU profile at a time; wait out the window and try again.
		select {
		case <-time.After(p.interval):
			return nil, false, errProfileBusy
		case <-p.stop:
			return nil, true, errProfileBusy
		}
	}
	stopped := false
	select {
	case <-time.After(p.interval):
	case <-p.stop:
		stopped = true
	}
	pprof.StopCPUProfile()
	if buf.Len() == 0 {
		return nil, stopped, nil
	}
	prof, err := profile.ParseData(buf.Bytes())
	if err != nil {
		return nil, stopped, err
	}
	if len(prof.Sample) == 0 {
		// An idle process produces a profile with no samples; sending it would add an empty flame graph.
		return nil, stopped, nil
	}
	return prof, stopped, nil
}

func (p *profiler) exportProfile(prof *profile.Profile) {
	data := otlpprofiles.FromPprof(prof, p.res, ProfileScope, Version)
	if data == nil {
		return
	}
	body, err := proto.Marshal(&colprofilespb.ExportProfilesServiceRequest{
		ResourceProfiles: data.ResourceProfiles,
		Dictionary:       data.Dictionary,
	})
	if err != nil {
		p.log.Debug("profiles marshal", "error", err)
		return
	}
	if p.gzip {
		var zbuf bytes.Buffer
		zw := gzip.NewWriter(&zbuf)
		if _, err := zw.Write(body); err == nil && zw.Close() == nil {
			body = zbuf.Bytes()
		}
	}
	if err := p.post(body); err != nil {
		p.log.Debug("profiles export", "error", err)
	}
}

// post sends one payload, retrying the statuses the ingest contract marks retryable (D-014) on the SDK's own
// schedule. A profile is a measurement of a window that has already passed, so retries are bounded by
// retry.elapsed and then dropped: holding them would push the next window's profile out instead.
func (p *profiler) post(body []byte) error {
	deadline := time.Now().Add(p.retry.elapsed)
	wait := p.retry.initial
	for attempt := 0; ; attempt++ {
		status, retryAfter, err := p.attempt(body)
		if err == nil {
			return nil
		}
		if status != 0 && !retryableStatus(status) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		d := wait
		if retryAfter > 0 {
			d = retryAfter
		}
		select {
		case <-time.After(d):
		case <-p.stop:
			return err
		}
		if wait *= 2; wait > p.retry.max {
			wait = p.retry.max
		}
	}
}

func (p *profiler) attempt(body []byte) (int, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	if p.gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 == 2 {
		return resp.StatusCode, 0, nil
	}
	var after time.Duration
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			after = time.Duration(secs) * time.Second
		}
	}
	return resp.StatusCode, after, fmt.Errorf("profiles export: %s", resp.Status)
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// Shutdown ends the current window, exports what it holds and returns.
func (p *profiler) Shutdown(ctx context.Context) error {
	p.once.Do(func() { close(p.stop) })
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// resourceAttrs converts the agent's resource to OTLP. Types are preserved rather than flattened to strings:
// the resource is what the server rebuilds identity from, and an int that arrives as "8" is not the same
// fact as an int that arrives as 8.
func resourceAttrs(res *sdkresource.Resource) []*commonpb.KeyValue {
	if res == nil {
		return nil
	}
	attrs := res.Attributes()
	out := make([]*commonpb.KeyValue, 0, len(attrs))
	for _, kv := range attrs {
		var v *commonpb.AnyValue
		switch kv.Value.Type() {
		case attribute.BOOL:
			v = &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: kv.Value.AsBool()}}
		case attribute.INT64:
			v = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: kv.Value.AsInt64()}}
		case attribute.FLOAT64:
			v = &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: kv.Value.AsFloat64()}}
		default:
			v = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: kv.Value.Emit()}}
		}
		out = append(out, &commonpb.KeyValue{Key: string(kv.Key), Value: v})
	}
	return out
}
