// Package diskspace measures how full the ClickHouse data disks are.
//
// Retention itself lives elsewhere: table TTLs are owned by internal/migrate and per-tenant trimming by
// internal/quota. Both are expressed in days, which bounds the age of the data but never its size: double the
// ingest and the same retention fills the disk. This package is the measurement that makes the difference
// visible, and the number every disk-pressure decision is made on.
//
// The numbers come from ClickHouse itself (system.disks) rather than from a statfs on the host, because the
// server usually runs in a container or on another machine, so the openlog process cannot see the filesystem
// that actually holds the parts.
package diskspace

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sync"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// SystemStateKey is the system_state row holding the last snapshot, so the API can answer without waiting for a
// check and without a Prometheus scrape.
const SystemStateKey = "clickhouse_disk_usage"

// Cluster names are interpolated into clusterAllReplicas(), which takes no query parameter: same guard as
// internal/migrate.
var clusterRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)

// Disk is one local disk of one replica. Remote disks (S3 and the cache in front of it) are left out: ClickHouse
// reports free_space/total_space for them as a placeholder, and `openlog-admin storage status` already prints
// "-" there for the same reason.
type Disk struct {
	Host   string `json:"host"`
	Name   string `json:"name"`
	Free   uint64 `json:"free_bytes"`
	Total  uint64 `json:"total_bytes"`
	Broken bool   `json:"broken"`
}

// UsedRatio is what fraction of a disk is not available, 0..1.
//
// It deliberately uses ClickHouse's free_space, which on ext4 excludes the blocks reserved for root. That is the
// honest number for this purpose: those blocks are not available to ClickHouse, so counting them as free would
// put the reported usage a few percent below the pressure the server actually feels.
//
// A disk of unknown size reads as empty rather than as full: 0 is the value that crosses no threshold, and a
// disk ClickHouse cannot size is not evidence of pressure.
func UsedRatio(free, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(total-min(free, total)) / float64(total)
}

// UsedRatio is what fraction of this disk is not available, 0..1.
func (d Disk) UsedRatio() float64 { return UsedRatio(d.Free, d.Total) }

// Key identifies a disk across checks and across restarts.
func (d Disk) Key() string { return d.Host + "/" + d.Name }

// UsedPercent is UsedRatio as a percentage.
func (d Disk) UsedPercent() float64 { return 100 * d.UsedRatio() }

// DefaultHysteresis is how many points a disk has to fall below the level it was reported at before that level
// can be reported again. Without it a disk sitting exactly on a threshold reports on every single check.
const DefaultHysteresis = 5

// Level is the highest threshold a disk at usedPercent has reached, or 0 for none.
func Level(usedPercent float64, thresholds []int) int {
	lvl := 0
	for _, t := range thresholds {
		if usedPercent >= float64(t) && t > lvl {
			lvl = t
		}
	}
	return lvl
}

// Snapshot is one round of measurement.
type Snapshot struct {
	CheckedAt time.Time `json:"checked_at"`
	Disks     []Disk    `json:"disks"`
	// Levels are the thresholds this round ran with, so a reader of the stored snapshot does not have to guess
	// which settings produced Reported.
	Levels Effective `json:"levels"`
	// Reported is the level each disk was last reported at, by Disk.Key. It is carried in the stored snapshot so
	// a restart does not re-report a disk that has not moved, and so the UI can show which disks are over a
	// threshold without recomputing the rule.
	Reported map[string]int `json:"reported,omitempty"`
}

// Fullest returns the disk under the most pressure. A cluster is as full as its fullest replica: an average
// would hide one node about to stop accepting parts.
func (s Snapshot) Fullest() (Disk, bool) {
	var worst Disk
	found := false
	for _, d := range s.Disks {
		if !found || d.UsedRatio() > worst.UsedRatio() {
			worst, found = d, true
		}
	}
	return worst, found
}

// Checker reads the disks every Interval (leader task).
type Checker struct {
	CH       clickhouse.Conn
	Cluster  string
	Interval time.Duration
	// Settings is read at the top of every round, so a change made in the UI applies without a restart. nil, or
	// an error, uses the built-in defaults.
	Settings func(context.Context) (Effective, error)
	// Save persists a snapshot; nil keeps it in memory only.
	Save func(context.Context, Snapshot) error
	// Load reads the stored snapshot once, so the levels already reported survive a restart; nil starts clean.
	Load func(context.Context) (Snapshot, bool, error)
	Log  *slog.Logger

	Registerer prometheus.Registerer
	metrics    *metrics
	once       sync.Once
	loadOnce   sync.Once

	mu   sync.RWMutex
	last Snapshot
}

type metrics struct {
	used  *prometheus.GaugeVec
	free  *prometheus.GaugeVec
	total *prometheus.GaugeVec
	level *prometheus.GaugeVec
	runs  *prometheus.CounterVec
}

func (c *Checker) init() {
	c.once.Do(func() {
		m := &metrics{
			used: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "openlog_clickhouse_disk_used_ratio",
				Help: "Used fraction (0..1) of a local ClickHouse disk, by replica."}, []string{"host", "disk"}),
			free: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "openlog_clickhouse_disk_free_bytes",
				Help: "Free bytes of a local ClickHouse disk, by replica."}, []string{"host", "disk"}),
			total: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "openlog_clickhouse_disk_total_bytes",
				Help: "Size in bytes of a local ClickHouse disk, by replica."}, []string{"host", "disk"}),
			level: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "openlog_clickhouse_disk_reported_level",
				Help: "Threshold a local ClickHouse disk is currently reported at (0 = below all of them)."},
				[]string{"host", "disk"}),
			runs: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_clickhouse_disk_checks_total",
				Help: "Disk usage checks by outcome."}, []string{"result"}),
		}
		m.runs.WithLabelValues("ok")
		m.runs.WithLabelValues("error")
		if c.Registerer != nil {
			c.Registerer.MustRegister(m.used, m.free, m.total, m.level, m.runs)
		}
		c.metrics = m
	})
}

// Last returns the most recent snapshot of this process.
func (c *Checker) Last() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.last
}

// restore reads the stored snapshot once, so a restart does not report a level that was already reported.
func (c *Checker) restore(ctx context.Context) {
	c.loadOnce.Do(func() {
		if c.Load == nil {
			return
		}
		prev, ok, err := c.Load(ctx)
		if err != nil {
			if c.Log != nil {
				c.Log.Warn("cannot read the stored disk usage snapshot; a level already reported may be reported once more", "err", err)
			}
			return
		}
		if !ok {
			return
		}
		c.mu.Lock()
		c.last = prev
		c.mu.Unlock()
	})
}

// levels decides what level every disk is at, against the levels of the previous round, and reports the changes.
func (c *Checker) levels(disks []Disk, eff Effective) map[string]int {
	thresholds := eff.Thresholds()
	prev := c.Last().Reported
	out := make(map[string]int, len(disks))
	for _, d := range disks {
		pct := d.UsedPercent()
		was, now := prev[d.Key()], Level(pct, thresholds)
		switch {
		case now > was:
			c.reportRise(d, pct, now, eff.High)
		case was > 0 && pct < float64(was-eff.Hysteresis):
			if c.Log != nil {
				c.Log.Info("ClickHouse disk is back under the level it was reported at", "host", d.Host,
					"disk", d.Name, "used_percent", math.Round(pct), "reported_at", was, "now", now)
			}
		default:
			// Latched. A disk drifting either side of a threshold must not report on every check, and a disk
			// that only dipped a point below it has not recovered.
			now = was
		}
		out[d.Key()] = now
	}
	return out
}

// reportRise logs a disk that has reached a level it was not at before.
func (c *Checker) reportRise(d Disk, pct float64, level, top int) {
	if c.Log == nil {
		return
	}
	args := []any{"host", d.Host, "disk", d.Name, "used_percent", math.Round(pct), "level", level,
		"free_bytes", d.Free, "total_bytes", d.Total}
	if level >= top {
		// At the top level a merge can fail for want of scratch space, and a failed merge stops ingest rather
		// than slowing it down.
		c.Log.Error("ClickHouse disk is nearly full", args...)
		return
	}
	c.Log.Warn("ClickHouse disk is filling up", args...)
}

// Read queries the disks of every replica.
func (c *Checker) Read(ctx context.Context) (Snapshot, error) {
	snap := Snapshot{CheckedAt: time.Now().UTC()}
	cluster := c.Cluster
	if cluster == "" {
		cluster = "openlog"
	}
	if !clusterRe.MatchString(cluster) {
		return snap, fmt.Errorf("invalid ClickHouse cluster %q", cluster)
	}
	// skip_unavailable_shards keeps one unreachable replica from failing the whole check: the disks that do
	// answer still move the gauges.
	qctx := ch.Context(ctx, ch.WithSettings(ch.Settings{"max_execution_time": 10, "skip_unavailable_shards": 1}))
	rows, err := c.CH.Query(qctx, fmt.Sprintf(
		"SELECT hostName(), name, free_space, total_space, is_broken FROM clusterAllReplicas('%s', system.disks) "+
			"WHERE is_remote = 0 ORDER BY 1, 2", cluster))
	if err != nil {
		return snap, fmt.Errorf("read system.disks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d Disk
		if err := rows.Scan(&d.Host, &d.Name, &d.Free, &d.Total, &d.Broken); err != nil {
			return snap, err
		}
		snap.Disks = append(snap.Disks, d)
	}
	return snap, rows.Err()
}

// RunOnce measures, publishes and stores one snapshot.
func (c *Checker) RunOnce(ctx context.Context) (Snapshot, error) {
	c.init()
	c.restore(ctx)
	snap, err := c.Read(ctx)
	if err != nil {
		c.metrics.runs.WithLabelValues("error").Inc()
		return snap, err
	}
	// Read at the top of the round rather than held in a field: a change made in the UI then applies on the next
	// check, with no restart and no cache to invalidate.
	eff := Defaults()
	if c.Settings != nil {
		s, serr := c.Settings(ctx)
		if serr != nil {
			if c.Log != nil {
				c.Log.Warn("cannot read the disk space settings; using the built-in levels for this round", "err", serr)
			}
		} else {
			eff = s
		}
	}
	snap.Levels = eff
	snap.Reported = c.levels(snap.Disks, eff)
	// Reset first: a disk that disappeared (a replica removed, a warm volume unmounted) must not leave its last
	// value behind as a series that looks current.
	c.metrics.used.Reset()
	c.metrics.free.Reset()
	c.metrics.total.Reset()
	c.metrics.level.Reset()
	for _, d := range snap.Disks {
		c.metrics.used.WithLabelValues(d.Host, d.Name).Set(d.UsedRatio())
		c.metrics.free.WithLabelValues(d.Host, d.Name).Set(float64(d.Free))
		c.metrics.total.WithLabelValues(d.Host, d.Name).Set(float64(d.Total))
		c.metrics.level.WithLabelValues(d.Host, d.Name).Set(float64(snap.Reported[d.Key()]))
	}
	c.mu.Lock()
	c.last = snap
	c.mu.Unlock()
	c.metrics.runs.WithLabelValues("ok").Inc()
	if c.Save != nil {
		if err := c.Save(ctx, snap); err != nil {
			return snap, fmt.Errorf("store snapshot: %w", err)
		}
	}
	return snap, nil
}

// Run measures every Interval until ctx is done.
func (c *Checker) Run(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	for {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := c.RunOnce(rctx)
		cancel()
		if err != nil && ctx.Err() == nil && c.Log != nil {
			c.Log.Warn("ClickHouse disk usage check failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
