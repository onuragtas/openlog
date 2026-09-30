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

// Snapshot is one round of measurement.
type Snapshot struct {
	CheckedAt time.Time `json:"checked_at"`
	Disks     []Disk    `json:"disks"`
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
	// Save persists a snapshot; nil keeps it in memory only.
	Save func(context.Context, Snapshot) error
	Log  *slog.Logger

	Registerer prometheus.Registerer
	metrics    *metrics
	once       sync.Once

	mu   sync.RWMutex
	last Snapshot
}

type metrics struct {
	used  *prometheus.GaugeVec
	free  *prometheus.GaugeVec
	total *prometheus.GaugeVec
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
			runs: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_clickhouse_disk_checks_total",
				Help: "Disk usage checks by outcome."}, []string{"result"}),
		}
		m.runs.WithLabelValues("ok")
		m.runs.WithLabelValues("error")
		if c.Registerer != nil {
			c.Registerer.MustRegister(m.used, m.free, m.total, m.runs)
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
	snap, err := c.Read(ctx)
	if err != nil {
		c.metrics.runs.WithLabelValues("error").Inc()
		return snap, err
	}
	// Reset first: a disk that disappeared (a replica removed, a warm volume unmounted) must not leave its last
	// value behind as a series that looks current.
	c.metrics.used.Reset()
	c.metrics.free.Reset()
	c.metrics.total.Reset()
	for _, d := range snap.Disks {
		c.metrics.used.WithLabelValues(d.Host, d.Name).Set(d.UsedRatio())
		c.metrics.free.WithLabelValues(d.Host, d.Name).Set(float64(d.Free))
		c.metrics.total.WithLabelValues(d.Host, d.Name).Set(float64(d.Total))
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
