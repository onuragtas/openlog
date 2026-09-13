// Package agent wires collectors, discovery, exporter and buffer together and
// runs the collection schedule.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"path"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/agents/infra/internal/buffer"
	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/ids"
	"github.com/onuragtas/openlog/agents/infra/internal/inventory"
	"github.com/onuragtas/openlog/agents/infra/internal/logs"
	"github.com/onuragtas/openlog/agents/infra/internal/metrics"
	"github.com/onuragtas/openlog/agents/infra/internal/resource"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
	"github.com/onuragtas/openlog/agents/infra/rules"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// MaxInterval caps automatic interval growth by the resource budget check.
const MaxInterval = 60 * time.Second

// cpuBudget is the fraction of one CPU the agent may use on average.
const cpuBudget = 0.01

// budgetWindow is the minimum sustained period before the interval is raised.
const budgetWindow = 60 * time.Second

// shutdownTimeout bounds how long pending payloads are flushed on shutdown
// before the remainder is persisted to the disk buffer.
const shutdownTimeout = 5 * time.Second

// LoadRules returns the embedded catalog overlaid with rules from dir.
func LoadRules(dir string) ([]*discovery.Rule, error) {
	base, err := discovery.LoadFS(rules.FS, "embedded")
	if err != nil {
		return nil, err
	}
	extra, err := discovery.LoadDir(dir)
	if err != nil {
		return nil, err
	}
	return discovery.Merge(base, extra), nil
}

// Agent is the running host agent.
type Agent struct {
	cfg     *config.Config
	version string
	log     *slog.Logger
	fs      *hostfs.FS
	stats   *selfmon.Stats
	hostID  string

	res     *resourcepb.Resource
	metrics *metrics.Set
	inv     *inventory.Collector
	engine  *discovery.Engine
	exp     *exporter.Exporter
	buf     *buffer.Buffer
	pipe    *pipeline
	ctr     *containers.Source
	logs    *logs.Manager

	interval      time.Duration
	lastInventory time.Time
	fingerprint   uint64

	// Resource budget: time spent collecting (not exporting) since budgetStart.
	budgetStart time.Time
	collectCost time.Duration

	shutdownTimeout time.Duration

	// Now is the clock for sample timestamps; replaceable in tests.
	Now func() time.Time

	// Test hooks.
	onCollect func(scheduled time.Time)
	onEnqueue func(signal exporter.Signal, items int)
}

// New builds an agent. The buffer and exporter are only created when
// forExport is true (i.e. not for -once).
func New(cfg *config.Config, version string, log *slog.Logger, forExport bool) (*Agent, error) {
	now := time.Now()
	a := &Agent{
		cfg: cfg, version: version, log: log,
		fs:       hostfs.New(cfg.Host.RootPath),
		stats:    selfmon.New(now),
		interval: cfg.Interval.D(),
		Now:      time.Now,

		budgetStart:     now,
		shutdownTimeout: shutdownTimeout,
	}
	hostID, persisted, err := resource.HostID(a.fs, cfg.StateDir)
	if err != nil || !persisted {
		log.Warn("host.id could not be persisted; a new id will be generated on restart", "error", err)
	}
	a.hostID = hostID
	a.res = resource.Detect(a.fs, hostID, version, cfg.Host.ExtraAttributes).Proto()
	a.stats.SetCollectionInterval(a.interval)
	if cfg.Containers.Enabled {
		a.ctr = containers.NewSource(a.fs, cfg.Containers.DockerSocket)
		a.ctr.Permission = func() { a.stats.PermissionDenied("containers") }
	}
	a.metrics = metrics.NewSet(a.fs, cfg, a.stats, log, a.ctr)
	a.inv = &inventory.Collector{FS: a.fs, Stats: a.stats, Log: log}
	if a.ctr != nil {
		a.inv.Containers = func() ([]inventory.Container, error) {
			cs, err := a.ctr.List(context.Background(), a.interval/2)
			if errors.Is(err, containers.ErrNoRuntime) {
				return nil, nil
			}
			return cs, err
		}
	}
	if cfg.Discovery.Enabled {
		rs, err := LoadRules(cfg.Discovery.RulesDir)
		if err != nil {
			return nil, fmt.Errorf("discovery rules: %w", err)
		}
		a.engine = discovery.NewEngine(rs)
	}
	if forExport {
		a.exp = exporter.New(cfg.Endpoint, cfg.LicenseKey, version, cfg.Export.Timeout.D())
		a.buf, err = buffer.Open(cfg.Buffer.Dir, cfg.Buffer.MaxBytes)
		if err != nil {
			return nil, err
		}
		a.stats.SetBufferBytes(a.buf.Bytes())
		a.pipe = newPipeline(a.exp, a.buf, a.stats, log)
		lc := cfg.Logs
		if lc.Enabled && (len(lc.Files) > 0 || lc.Journald.Enabled || (lc.AutoFromDiscovery && cfg.Discovery.Enabled)) {
			a.logs = logs.New(logs.Options{
				Config: lc, FS: a.fs, StateDir: cfg.StateDir, Resource: a.res, Scope: a.scope(),
				Emit: func(ld *logspb.LogsData, records int, ack func()) {
					parts := exporter.SplitLogs(ld, cfg.Export.MaxRequestBytes)
					for i, part := range parts {
						var partAck func()
						if i == len(parts)-1 {
							partAck = ack // FIFO delivery: the last part settles the batch
						}
						a.enqueueAck(exporter.SignalLogs, countRecords(part), part, partAck)
					}
				},
				Paused:        a.pipe.backlogged,
				Log:           log.With("component", "logs"),
				MaxBatchBytes: cfg.Export.MaxRequestBytes / 2,
			})
		}
	}
	return a, nil
}

// Stats returns the self-telemetry counters (shared with the update manager).
func (a *Agent) Stats() *selfmon.Stats { return a.stats }

// HostID returns the resolved host.id.
func (a *Agent) HostID() string { return a.hostID }

// HostName returns the host name as seen through host.root_path.
func (a *Agent) HostName() string { return resource.Hostname(a.fs) }

// OnExportSuccess registers fn to run after every payload the ingest accepted. It must be set
// before Run and must not block.
func (a *Agent) OnExportSuccess(fn func()) {
	if a.pipe != nil {
		a.pipe.onSent = fn
	}
}

// SelfTest runs every metric collector and the inventory/discovery collection once without
// sending anything (used by -self-test before an update is switched on).
func (a *Agent) SelfTest(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		now := a.Now()
		ms := a.metrics.Collect(now)
		if len(ms) == 0 {
			done <- errors.New("metric collectors produced no metrics")
			return
		}
		ld, _, err := a.CollectInventory(now)
		if err == nil {
			_, err = proto.Marshal(ld)
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Agent) scope() *commonpb.InstrumentationScope {
	return &commonpb.InstrumentationScope{Name: resource.AgentName, Version: a.version}
}

// CollectMetrics gathers one metrics sample.
func (a *Agent) CollectMetrics(now time.Time) *metricspb.MetricsData {
	return &metricspb.MetricsData{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource:     a.res,
		ScopeMetrics: []*metricspb.ScopeMetrics{{Scope: a.scope(), Metrics: a.metrics.Collect(now)}},
	}}}
}

// CollectInventory gathers a full inventory snapshot including discovery.
func (a *Agent) CollectInventory(now time.Time) (*logspb.LogsData, []discovery.Service, error) {
	d := a.inv.Collect()
	items := d.Items()
	var services []discovery.Service
	if a.engine != nil {
		start := time.Now()
		services = a.engine.Discover(d)
		a.stats.SetCollectorDuration("discovery", time.Since(start))
		items = append(items, discovery.Items(services)...)
		a.metrics.SetServiceLookup(discovery.NewServiceIndex(services).Lookup)
		if a.logs != nil {
			a.logs.SetDiscovered(DiscoveredLogs(services))
		}
	}
	snap := &inventory.Snapshot{ID: ids.NewV7(now), Time: now, Items: inventory.Normalize(items)}
	recs, err := snap.LogRecords(time.Now())
	if err != nil {
		return nil, nil, err
	}
	return &logspb.LogsData{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  a.res,
		ScopeLogs: []*logspb.ScopeLogs{{Scope: a.scope(), LogRecords: recs}},
	}}}, services, nil
}

// Once collects one round and writes it as JSON without sending anything.
func (a *Agent) Once(ctx context.Context, w io.Writer) error {
	// Prime counters so that utilization gauges have a delta. Inventory runs
	// in between so that process metrics carry openlog.discovery.id.
	a.metrics.Collect(a.Now())
	ld, services, err := a.CollectInventory(a.Now())
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
	}
	md := a.CollectMetrics(a.Now())
	mo := protojson.MarshalOptions{UseProtoNames: false}
	mj, err := mo.Marshal(md)
	if err != nil {
		return err
	}
	lj, err := mo.Marshal(ld)
	if err != nil {
		return err
	}
	if services == nil {
		services = []discovery.Service{}
	}
	out := struct {
		Metrics            json.RawMessage     `json:"metrics"`
		Logs               json.RawMessage     `json:"logs"`
		DiscoveredServices []discovery.Service `json:"discovered_services"`
	}{mj, lj, services}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// Run collects on a fixed schedule until ctx is cancelled. Export runs in a
// separate goroutine, so a slow or unavailable ingest never delays sampling.
// On shutdown collection stops, pending payloads are flushed for up to
// shutdownTimeout and whatever remains is persisted to the disk buffer.
func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("agent started", "version", a.version, "interval", a.interval, "root", a.fs.Root(),
		"rules", a.ruleCount(), "buffered_entries", a.buf.Len())
	a.budgetStart, a.collectCost = time.Now(), 0

	exportCtx, cancelExport := context.WithCancel(context.Background())
	defer cancelExport()
	draining := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		a.pipe.run(exportCtx, draining)
	}()

	logsCtx, cancelLogs := context.WithCancel(context.Background())
	logsDone := make(chan struct{})
	if a.logs != nil {
		go func() {
			defer close(logsDone)
			a.logs.Run(logsCtx)
		}()
	} else {
		close(logsDone)
	}

	a.collectLoop(ctx)
	cancelLogs()
	<-logsDone

	a.log.Info("agent stopping; flushing export queue", "timeout", a.shutdownTimeout)
	close(draining)
	timer := time.NewTimer(a.shutdownTimeout)
	select {
	case <-exited:
	case <-timer.C:
		cancelExport() // aborts an in-flight send; step persists it
		<-exited
	}
	timer.Stop()
	persisted := a.pipe.spillAll()
	if a.logs != nil {
		if err := a.logs.SaveState(); err != nil {
			a.log.Warn("log offsets not saved", "error", err)
		}
	}
	a.log.Info("agent stopped", "persisted_payloads", persisted, "buffered_entries", a.buf.Len())
	return nil
}

// collectLoop runs collection rounds on an absolute schedule (start + k·interval)
// so that the cadence does not drift. Rounds that overran are skipped, not queued.
func (a *Agent) collectLoop(ctx context.Context) {
	next := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if a.onCollect != nil {
			a.onCollect(time.Now())
		}
		a.collectRound()
		next = next.Add(a.interval)
		now := time.Now()
		if !next.After(now) {
			skipped := 0
			for !next.After(now) {
				next = next.Add(a.interval)
				skipped++
			}
			a.log.Warn("collection round overran the interval; skipping samples", "skipped", skipped)
		}
		timer.Reset(next.Sub(now))
	}
}

func (a *Agent) ruleCount() int {
	if a.engine == nil {
		return 0
	}
	return len(a.engine.Rules())
}

// Tick runs one collection round and then synchronously flushes pending
// payloads (used by tests; Run exports asynchronously).
func (a *Agent) Tick(ctx context.Context) {
	a.collectRound()
	if err := a.pipe.flush(ctx); err != nil {
		a.log.Warn("export failed; payloads kept for retry", "error", err)
	}
}

// collectRound collects metrics always and inventory when due, and enqueues
// the payloads. It never waits on the network.
func (a *Agent) collectRound() {
	start := time.Now()
	var enqueueTime time.Duration
	enqueue := func(signal exporter.Signal, items int, msg proto.Message) {
		t := time.Now()
		a.enqueue(signal, items, msg)
		enqueueTime += time.Since(t)
	}

	now := a.Now()
	md := a.CollectMetrics(now)
	enqueue(exporter.SignalMetrics, countPoints(md), md)

	if a.cfg.Inventory.Enabled {
		// The snapshot time is taken right before inventory is collected: it is
		// the moment the snapshot describes, not when the round started.
		now = a.Now()
		fp := Fingerprint(a.fs)
		if a.ctr != nil {
			fp ^= a.ctr.Fingerprint() * 1099511628211
		}
		due := a.lastInventory.IsZero() || now.Sub(a.lastInventory) >= a.cfg.InventoryInterval.D()
		changed := !a.lastInventory.IsZero() && fp != a.fingerprint
		if due || changed {
			if changed {
				a.log.Info("host change detected; sending inventory snapshot")
			}
			a.fingerprint, a.lastInventory = fp, now
			ld, _, err := a.CollectInventory(now)
			if err != nil {
				a.log.Error("inventory encoding failed", "error", err)
			} else {
				// Parts are enqueued in order; the snapshot-complete record is in the last one.
				for _, part := range exporter.SplitLogs(ld, a.cfg.Export.MaxRequestBytes) {
					enqueue(exporter.SignalLogs, countRecords(part), part)
				}
			}
		}
	}
	a.collectCost += time.Since(start) - enqueueTime
	a.checkBudget(time.Now())
}

func (a *Agent) enqueue(signal exporter.Signal, items int, msg proto.Message) {
	a.enqueueAck(signal, items, msg, nil)
}

func (a *Agent) enqueueAck(signal exporter.Signal, items int, msg proto.Message, ack func()) {
	data, err := proto.Marshal(msg)
	if err != nil {
		a.log.Error("payload encoding failed", "signal", signal, "error", err)
		a.stats.AddExportItems(string(signal), "dropped", items)
		if ack != nil {
			ack()
		}
		return
	}
	if a.onEnqueue != nil {
		a.onEnqueue(signal, items)
	}
	a.pipe.EnqueueAck(signal, items, data, ack)
}

// DiscoveredLogs lists the log globs of discovered services (deduplicated).
func DiscoveredLogs(services []discovery.Service) []logs.DiscoveredLog {
	seen := map[logs.DiscoveredLog]bool{}
	var out []logs.DiscoveredLog
	for _, s := range services {
		for _, p := range s.LogPaths {
			d := logs.DiscoveredLog{Path: p, DiscoveryID: s.RuleID}
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

// checkBudget doubles the interval when time spent collecting (excluding any
// export or network wait) stays above the CPU budget over a sustained window.
func (a *Agent) checkBudget(now time.Time) {
	elapsed := now.Sub(a.budgetStart)
	if elapsed < budgetWindow || elapsed < 6*a.interval {
		return
	}
	frac := float64(a.collectCost) / float64(elapsed)
	a.budgetStart, a.collectCost = now, 0
	if frac > cpuBudget && a.interval < MaxInterval {
		old := a.interval
		a.interval = min(a.interval*2, MaxInterval)
		a.stats.SetCollectionInterval(a.interval)
		a.log.Warn("resource budget exceeded; increasing collection interval",
			"collect_fraction", frac, "budget", cpuBudget, "old_interval", old, "new_interval", a.interval)
	}
}

// Interval returns the current (possibly budget-adjusted) interval.
func (a *Agent) Interval() time.Duration { return a.interval }

// Fingerprint is a cheap hash of signals that indicate inventory changes:
// package database mtimes, systemd unit directory mtimes and the listening port set.
func Fingerprint(fs *hostfs.FS) uint64 {
	h := fnv.New64a()
	stamp := func(p string) {
		if fi, err := fs.Stat(p); err == nil {
			fmt.Fprintf(h, "%s=%d/%d;", p, fi.ModTime().UnixNano(), fi.Size())
		}
	}
	for _, p := range append([]string{inventory.DpkgStatusPath, inventory.ApkInstalledPath}, inventory.RpmFingerprintPaths...) {
		stamp(p)
	}
	for _, dir := range inventory.UnitDirs {
		stamp(dir)
		if entries, err := fs.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					stamp(path.Join(dir, e.Name())) // *.wants changes on enable/disable
				}
			}
		}
	}
	fmt.Fprintf(h, "ports=%d", inventory.PortSetHash(fs))
	return h.Sum64()
}

func countPoints(md *metricspb.MetricsData) int {
	n := 0
	for _, rm := range md.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				switch d := m.Data.(type) {
				case *metricspb.Metric_Gauge:
					n += len(d.Gauge.DataPoints)
				case *metricspb.Metric_Sum:
					n += len(d.Sum.DataPoints)
				}
			}
		}
	}
	return n
}

func countRecords(ld *logspb.LogsData) int {
	n := 0
	for _, rl := range ld.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			n += len(sl.LogRecords)
		}
	}
	return n
}
