// Package metrics implements the host metric collectors defined in
// semantic-conventions §2. Collectors read procfs/sysfs directly through a
// hostfs.FS.
package metrics

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Collector produces a set of metrics for one sample.
type Collector interface {
	Name() string
	Collect(now time.Time) ([]*metricspb.Metric, error)
}

// Set runs a list of collectors and records their durations.
type Set struct {
	collectors []Collector
	stats      *selfmon.Stats
	log        *slog.Logger
	procTop    *ProcessTop

	mu       sync.Mutex
	lastErrs map[string]string
}

// NewSet builds the enabled collectors. The self-telemetry collector is always
// included and runs last so that it reports this round's durations. ctr may be
// nil (no Docker metadata; container metrics then only carry container.id).
func NewSet(fs *hostfs.FS, cfg *config.Config, stats *selfmon.Stats, log *slog.Logger, ctr *containers.Source) *Set {
	fs = fs.WithRecorder(stats)
	boot := func() time.Time { return bootTime(fs) }
	c := cfg.Collectors
	var cs []Collector
	if c.CPU {
		cs = append(cs, &CPU{FS: fs.ForCollector("cpu"), BootTime: boot})
	}
	if c.Load {
		cs = append(cs, &Load{FS: fs.ForCollector("load")})
	}
	if c.Memory {
		cs = append(cs, &Memory{FS: fs.ForCollector("memory")})
	}
	if c.Filesystem {
		cs = append(cs, &Filesystem{FS: fs.ForCollector("filesystem"), AgentDirs: []string{cfg.StateDir, cfg.Buffer.Dir}})
	}
	if c.Disk {
		cs = append(cs, &Disk{FS: fs.ForCollector("disk"), BootTime: boot})
	}
	if c.Network {
		cs = append(cs, &Network{FS: fs.ForCollector("network"), BootTime: boot})
	}
	if c.Uptime {
		cs = append(cs, &Uptime{FS: fs.ForCollector("uptime")})
	}
	if c.Processes {
		cs = append(cs, &Processes{FS: fs.ForCollector("processes")})
	}
	s := &Set{stats: stats, log: log, lastErrs: map[string]string{}}
	if cfg.ProcessMetrics.Enabled && (cfg.ProcessMetrics.TopNCPU > 0 || cfg.ProcessMetrics.TopNMemory > 0) {
		s.procTop = &ProcessTop{FS: fs.ForCollector("process_metrics"), TopCPU: cfg.ProcessMetrics.TopNCPU, TopMemory: cfg.ProcessMetrics.TopNMemory}
		cs = append(cs, s.procTop)
	}
	if cfg.Containers.Enabled {
		cs = append(cs, &Containers{FS: fs.ForCollector("containers"), Source: ctr, MaxAge: cfg.Interval.D() / 2})
	}
	s.collectors = cs
	return s
}

// Add appends a collector (e.g. the Kubernetes kubelet collector); it runs before self-telemetry. Not safe
// concurrently with Collect: call before the agent runs.
func (s *Set) Add(c Collector) { s.collectors = append(s.collectors, c) }

// SetServiceLookup forwards the discovery mapping to the process metrics collector.
func (s *Set) SetServiceLookup(l ServiceLookup) {
	if s.procTop != nil {
		s.procTop.SetServiceLookup(l)
	}
}

// Collect runs every collector and returns all metrics, self-telemetry last.
func (s *Set) Collect(now time.Time) []*metricspb.Metric {
	var out []*metricspb.Metric
	for _, c := range s.collectors {
		start := time.Now()
		ms, err := c.Collect(now)
		s.stats.SetCollectorDuration(c.Name(), time.Since(start))
		s.logErr(c.Name(), err)
		out = append(out, ms...)
	}
	out = append(out, SelfTelemetry(s.stats, now)...)
	return out
}

// logErr logs a collector error once until it changes, to avoid log spam.
func (s *Set) logErr(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if s.lastErrs[name] == msg {
		return
	}
	s.lastErrs[name] = msg
	if err != nil && s.log != nil {
		s.log.Warn("metric collector failed", "collector", name, "error", err)
	}
}

func bootTime(fs *hostfs.FS) time.Time {
	b, err := fs.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	st, err := procfs.ParseStat(b)
	if err != nil {
		return time.Time{}
	}
	return st.BootTime
}

func joinErrs(errs []error) error { return errors.Join(errs...) }
