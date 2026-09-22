// Package profiler is the interval loop: sample, group, convert, send (docs/contracts/ebpf-profiler.md).
//
// Everything that touches the kernel is behind Sampler, which is the seam that keeps this loop testable on
// a machine that cannot run eBPF at all. The loop itself never fails the process: a window that could not
// be sampled or sent is logged and the next one is attempted, because a profiler is not worth taking an
// application's host down for.
package profiler

import (
	"context"
	"log/slog"
	"sort"
	"time"

	colprofilespb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/agents/ebpf/internal/aggregate"
	"github.com/onuragtas/openlog/agents/ebpf/internal/config"
	"github.com/onuragtas/openlog/agents/ebpf/internal/otlpprofiles"
)

// Scope is the instrumentation scope of the profiles this component produces.
const Scope = "github.com/onuragtas/openlog/agents/ebpf"

// Sampler collects raw samples for one window. The Linux implementation opens perf events and a BPF
// program; tests supply their own.
type Sampler interface {
	Sample(ctx context.Context, d time.Duration) ([]aggregate.RawSample, error)
	Close() error
}

// Exporter sends one marshalled ExportProfilesServiceRequest.
type Exporter interface {
	Send(ctx context.Context, body []byte) error
}

// Options are the loop's dependencies.
type Options struct {
	Config     config.Config
	Sampler    Sampler
	Exporter   Exporter
	Symbolizer aggregate.Symbolizer
	Namer      aggregate.Namer
	HostID     string
	Version    string
	Log        *slog.Logger
	// Now is time.Now; tests replace it so a profile's window is predictable.
	Now func() time.Time
}

// Run loops until ctx is cancelled, then closes the sampler. It returns nil on cancellation: being asked to
// stop is not a failure.
func Run(ctx context.Context, o Options) error {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	defer func() {
		if err := o.Sampler.Close(); err != nil {
			o.Log.Warn("sampler did not close cleanly", "error", err)
		}
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}
		start := o.Now()
		raw, err := o.Sampler.Sample(ctx, o.Config.Interval)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			o.Log.Warn("sampling failed", "error", err)
			continue
		}
		o.exportWindow(ctx, raw, start)
	}
}

func (o Options) exportWindow(ctx context.Context, raw []aggregate.RawSample, start time.Time) {
	byService := aggregate.Run(raw, o.Symbolizer, o.Namer, o.Config.PeriodNanos())
	if len(byService) == 0 {
		// An interval in which nothing ran produces no request at all: an empty profile costs both sides a
		// round trip and draws an empty flame graph.
		return
	}
	// Sorted so a window's requests are in a stable order, which makes a failure reproducible.
	services := make([]string, 0, len(byService))
	for name := range byService {
		services = append(services, name)
	}
	sort.Strings(services)

	duration := uint64(o.Config.Interval)
	for _, name := range services {
		data := otlpprofiles.FromSamples(
			byService[name], o.resourceAttrs(name), Scope, o.Version,
			uint64(start.UnixNano()), duration, o.Config.PeriodNanos(),
		)
		if data == nil {
			continue
		}
		body, err := proto.Marshal(&colprofilespb.ExportProfilesServiceRequest{
			ResourceProfiles: data.ResourceProfiles,
			Dictionary:       data.Dictionary,
		})
		if err != nil {
			o.Log.Warn("profile could not be marshalled", "service", name, "error", err)
			continue
		}
		if err := o.Exporter.Send(ctx, body); err != nil {
			// Never fatal: the next window is worth more than this one.
			o.Log.Warn("profile export failed", "service", name, "error", err)
		}
	}
}

func (o Options) resourceAttrs(service string) []*commonpb.KeyValue {
	str := func(k, v string) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
	}
	attrs := []*commonpb.KeyValue{
		str("service.name", service),
		str("telemetry.sdk.name", "openlog-ebpf"),
		str("telemetry.sdk.language", "go"),
		str("telemetry.sdk.version", o.Version),
		str("os.type", "linux"),
	}
	if o.HostID != "" {
		// The same id the infra agent publishes, so a profile lands on the host its metrics are already on.
		attrs = append(attrs, str("host.id", o.HostID))
	}
	return attrs
}
