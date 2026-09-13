package apm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// LinkerOptions configure the edge-linking job (apm.md §6).
type LinkerOptions struct {
	Database string
	Cluster  string
	// Conn holds user, password and TLS for replica connections (Addr is unused).
	Conn     clickhouse.Options
	Interval time.Duration // default 1m
	Lookback time.Duration // default 10m
	Delay    time.Duration // default 1m
	// ResolveAddr maps a replica to a dialable address (tests). Nil: host_name:port.
	ResolveAddr func(clickhouse.Replica) string
	// CatchUp re-links the previous UTC day once a day (spans that arrived later than Lookback), starting
	// CatchUpAt after 00:00 UTC (default 3h), in windows of CatchUpBatch (default 1h) with CatchUpPause
	// between them (default 10s, negative = none).
	CatchUp      bool
	CatchUpAt    time.Duration
	CatchUpBatch time.Duration
	CatchUpPause time.Duration
}

// catchUpWindow is how long after CatchUpAt a missed catch-up pass is still started (leader change, restart).
const catchUpWindow = 6 * time.Hour

// Linker computes trace-linked service edges (client span -> child server span of another
// service) on every shard with INSERT ... SELECT over the shard's local tables. Spans are
// sharded by (tenant_id, trace_id), so a span and its children are on the same shard.
type Linker struct {
	opts      LinkerOptions
	bootstrap clickhouse.Conn
	log       *slog.Logger
	now       func() time.Time
	open      func(ctx context.Context, addr string) (clickhouse.Conn, error)

	mu    sync.Mutex
	conns map[string]clickhouse.Conn

	lastEnd  time.Time
	runs     *prometheus.CounterVec
	rows     prometheus.Counter
	duration prometheus.Histogram
	lag      prometheus.GaugeFunc

	catchUpDone    time.Time // start of the last day caught up successfully
	catchUpTried   time.Time // last attempt (failures are retried at most every 30 minutes)
	catchingUp     atomic.Bool
	catchUpRuns    *prometheus.CounterVec
	lateCalls      prometheus.Counter
	catchUpSeconds prometheus.Histogram
}

// NewLinker creates the job. bootstrap is a connection through OPENLOG_CLICKHOUSE_ADDR; reg may be nil.
func NewLinker(bootstrap clickhouse.Conn, opts LinkerOptions, log *slog.Logger, reg prometheus.Registerer) *Linker {
	if opts.Interval <= 0 {
		opts.Interval = time.Minute
	}
	if opts.Lookback <= 0 {
		opts.Lookback = 10 * time.Minute
	}
	if opts.Delay < 0 {
		opts.Delay = time.Minute
	}
	if opts.Database == "" {
		opts.Database = "openlog"
	}
	if opts.CatchUpAt <= 0 {
		opts.CatchUpAt = 3 * time.Hour
	}
	if opts.CatchUpBatch <= 0 {
		opts.CatchUpBatch = time.Hour
	}
	if opts.CatchUpPause == 0 {
		opts.CatchUpPause = 10 * time.Second
	}
	l := &Linker{opts: opts, bootstrap: bootstrap, log: log, now: time.Now, conns: map[string]clickhouse.Conn{},
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_apm_link_runs_total", Help: "Edge-linking job runs over all shards, by result.",
		}, []string{"result"}),
		rows: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openlog_apm_link_rows_total", Help: "Trace-linked service edge rows written (per shard and minute).",
		}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "openlog_apm_link_duration_seconds", Help: "Duration of one edge-linking run over all shards.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 14),
		}),
		catchUpRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_apm_link_catchup_runs_total", Help: "Daily catch-up passes over the previous UTC day, by result.",
		}, []string{"result"}),
		lateCalls: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openlog_apm_link_late_calls_total", Help: "Weighted client calls linked by the catch-up pass that the regular runs had not linked (spans later than the lookback).",
		}),
		catchUpSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "openlog_apm_link_catchup_duration_seconds", Help: "Duration of one daily catch-up pass over all shards.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 14),
		}),
	}
	l.lag = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "openlog_apm_link_lag_seconds", Help: "Time since the end of the last window linked on every shard.",
	}, func() float64 {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.lastEnd.IsZero() {
			return 0
		}
		return l.now().Sub(l.lastEnd).Seconds()
	})
	l.open = func(ctx context.Context, addr string) (clickhouse.Conn, error) {
		o := opts.Conn
		o.Addr, o.Database, o.MaxConns = []string{addr}, opts.Database, 2
		return clickhouse.Open(ctx, o)
	}
	for _, r := range []string{"ok", "error"} {
		l.catchUpRuns.WithLabelValues(r)
	}
	if reg != nil {
		reg.MustRegister(l.runs, l.rows, l.duration, l.lag, l.catchUpRuns, l.lateCalls, l.catchUpSeconds)
	}
	return l
}

// Run links every Interval until ctx is done, and starts the daily catch-up pass when it is due.
func (l *Linker) Run(ctx context.Context) {
	l.log.Info("apm edge linking started", "interval", l.opts.Interval.String(), "lookback", l.opts.Lookback.String(), "delay", l.opts.Delay.String(),
		"catch_up", l.opts.CatchUp, "catch_up_at", l.opts.CatchUpAt.String(), "catch_up_batch", l.opts.CatchUpBatch.String())
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		l.close()
	}()
	for {
		rctx, cancel := context.WithTimeout(ctx, max(l.opts.Interval, time.Minute))
		if err := l.RunOnce(rctx); err != nil && ctx.Err() == nil {
			l.log.Warn("apm edge linking failed", "err", err)
		}
		cancel()
		l.maybeCatchUp(ctx, &wg)
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.opts.Interval):
		}
	}
}

// CatchUpDue returns the UTC day [from, to) a catch-up pass should cover at now: the previous day, when now is
// within catchUpWindow after 00:00 UTC + at and that day (done = its start) was not caught up yet.
func CatchUpDue(now time.Time, at time.Duration, done time.Time) (time.Time, time.Time, bool) {
	day := now.UTC().Truncate(24 * time.Hour)
	start := day.Add(at)
	if now.Before(start) || !now.Before(start.Add(catchUpWindow)) {
		return time.Time{}, time.Time{}, false
	}
	from := day.Add(-24 * time.Hour)
	if done.Equal(from) {
		return time.Time{}, time.Time{}, false
	}
	return from, day, true
}

// maybeCatchUp starts the catch-up pass in the background (regular runs continue meanwhile; they cover the
// last lookback, the pass the previous day, so they never write the same minutes).
func (l *Linker) maybeCatchUp(ctx context.Context, wg *sync.WaitGroup) {
	if !l.opts.CatchUp {
		return
	}
	now := l.now()
	l.mu.Lock()
	from, to, due := CatchUpDue(now, l.opts.CatchUpAt, l.catchUpDone)
	if due && now.Sub(l.catchUpTried) < 30*time.Minute {
		due = false
	}
	if due {
		l.catchUpTried = now
	}
	l.mu.Unlock()
	if !due || !l.catchingUp.CompareAndSwap(false, true) {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer l.catchingUp.Store(false)
		began := time.Now()
		late, err := l.CatchUp(ctx, from, to)
		if err != nil {
			if ctx.Err() == nil {
				l.log.Warn("apm edge linking catch-up failed; retried within 30 minutes", "day", from.Format(time.DateOnly), "err", err)
			}
			return
		}
		l.mu.Lock()
		l.catchUpDone = from
		l.mu.Unlock()
		l.log.Info("apm edge linking catch-up done", "day", from.Format(time.DateOnly), "late_calls", late, "duration", time.Since(began).String())
	}()
}

// CatchUp re-links [from, to) in windows of CatchUpBatch with CatchUpPause between them (low load: one
// window at a time, max_threads=2) and returns the weighted calls it linked that were not linked before.
func (l *Linker) CatchUp(ctx context.Context, from, to time.Time) (float64, error) {
	began := time.Now()
	var late float64
	for s := from; s.Before(to); s = s.Add(l.opts.CatchUpBatch) {
		e := s.Add(l.opts.CatchUpBatch)
		if e.After(to) {
			e = to
		}
		_, n, err := l.runWindow(ctx, s, e, true)
		late += n
		l.lateCalls.Add(n)
		if err != nil {
			l.catchUpRuns.WithLabelValues("error").Inc()
			return late, fmt.Errorf("window %s: %w", s.Format(time.RFC3339), err)
		}
		if l.opts.CatchUpPause > 0 && e.Before(to) {
			select {
			case <-ctx.Done():
				l.catchUpRuns.WithLabelValues("error").Inc()
				return late, ctx.Err()
			case <-time.After(l.opts.CatchUpPause):
			}
		}
	}
	l.catchUpRuns.WithLabelValues("ok").Inc()
	l.catchUpSeconds.Observe(time.Since(began).Seconds())
	return late, nil
}

func (l *Linker) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		_ = c.Close()
	}
	l.conns = map[string]clickhouse.Conn{}
}

// Window returns the minutes [start, end) linked by a run at now.
func (l *Linker) Window(now time.Time) (time.Time, time.Time) {
	end := now.Add(-l.opts.Delay).UTC().Truncate(time.Minute)
	return end.Add(-l.opts.Lookback).Truncate(time.Minute), end
}

// RunOnce links the current window on every shard. Shards are processed independently;
// the error joins the shards that failed on all replicas.
func (l *Linker) RunOnce(ctx context.Context) error {
	start, end := l.Window(l.now())
	return l.RunWindow(ctx, start, end)
}

// RunWindow links [start, end) on every shard.
func (l *Linker) RunWindow(ctx context.Context, start, end time.Time) error {
	began := time.Now()
	_, _, err := l.runWindow(ctx, start, end, false)
	l.duration.Observe(time.Since(began).Seconds())
	if err != nil {
		l.runs.WithLabelValues("error").Inc()
		return err
	}
	l.runs.WithLabelValues("ok").Inc()
	l.mu.Lock()
	if end.After(l.lastEnd) {
		l.lastEnd = end
	}
	l.mu.Unlock()
	return nil
}

// runWindow links [start, end) on every shard; with measure it also returns the calls that were not linked before.
func (l *Linker) runWindow(ctx context.Context, start, end time.Time, measure bool) (uint64, float64, error) {
	topo, err := clickhouse.LoadTopology(ctx, l.bootstrap, l.opts.Cluster)
	if err != nil {
		return 0, 0, err
	}
	single := len(topo.Shards) == 1 && len(topo.Shards[0].Replicas) == 1 && l.opts.ResolveAddr == nil
	computedAt := time.Now().UTC().Truncate(time.Millisecond)
	var (
		errs  []error
		rows  uint64
		lateN float64
	)
	for _, shard := range topo.Shards {
		n, late, err := l.linkShard(ctx, shard, single, start, end, computedAt, measure)
		if err != nil {
			errs = append(errs, fmt.Errorf("shard %d: %w", shard.Num, err))
			continue
		}
		rows += n
		lateN += late
		l.rows.Add(float64(n))
	}
	return rows, lateN, errors.Join(errs...)
}

func (l *Linker) conn(ctx context.Context, r clickhouse.Replica, single bool) (clickhouse.Conn, error) {
	if single {
		return l.bootstrap, nil
	}
	addr := r.Addr()
	if l.opts.ResolveAddr != nil {
		addr = l.opts.ResolveAddr(r)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if c, ok := l.conns[addr]; ok {
		return c, nil
	}
	c, err := l.open(ctx, addr)
	if err != nil {
		return nil, err
	}
	l.conns[addr] = c
	return c, nil
}

func (l *Linker) dropConn(r clickhouse.Replica) {
	addr := r.Addr()
	if l.opts.ResolveAddr != nil {
		addr = l.opts.ResolveAddr(r)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if c, ok := l.conns[addr]; ok {
		_ = c.Close()
		delete(l.conns, addr)
	}
}

// linkShard runs the INSERT ... SELECT on the first replica of the shard that accepts it.
func (l *Linker) linkShard(ctx context.Context, shard clickhouse.Shard, single bool, start, end, computedAt time.Time, measure bool) (uint64, float64, error) {
	var errs []error
	for _, r := range shard.Replicas {
		c, err := l.conn(ctx, r, single)
		if err != nil {
			errs = append(errs, fmt.Errorf("replica %s: %w", r.Addr(), err))
			continue
		}
		n, late, err := l.link(ctx, c, start, end, computedAt, measure)
		if err == nil {
			return n, late, nil
		}
		errs = append(errs, fmt.Errorf("replica %s: %w", r.Addr(), err))
		if !single {
			l.dropConn(r)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return 0, 0, errors.Join(errs...)
}

// childSlack bounds how much later than its client span a child server span may start.
const childSlack = 5 * time.Minute

// LinkSQL is the per-shard statement (exported for tests and documentation).
func LinkSQL(database string) string {
	db := "`" + database + "`"
	bucket := "toInt16(if(c.duration_ns <= 1000, -80, greatest(-80, least(200, ceil(8 * log2(c.duration_ns / 1e6))))))"
	return `INSERT INTO ` + db + `.apm_service_links_1m_local
	(tenant_id, service_name, service_namespace, deployment_environment, timestamp, target_service, target_namespace,
	 target_environment, via, calls, samples, errors, duration_sum_ms, duration_hist, computed_at)
SELECT c.tenant_id, c.service_name, c.service_namespace, c.deployment_environment, toStartOfMinute(c.timestamp) AS minute,
	s.service_name, s.service_namespace, s.deployment_environment, c.peer_name,
	sum(c.sample_weight), toUInt64(count()), sumIf(c.sample_weight, c.is_error), sum(c.sample_weight * c.duration_ns / 1e6),
	sumMap([` + bucket + `], [c.sample_weight]),
	fromUnixTimestamp64Milli({computed_at:Int64})
FROM ` + db + `.spans_local AS c
INNER JOIN
(
	SELECT tenant_id, trace_id, parent_span_id, service_name, service_namespace, deployment_environment
	FROM ` + db + `.spans_local
	WHERE kind IN ('server', 'consumer') AND service_name != '' AND parent_span_id != ''
	  AND timestamp >= fromUnixTimestamp64Nano({s_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({s_to:Int64})
) AS s ON s.tenant_id = c.tenant_id AND s.trace_id = c.trace_id AND s.parent_span_id = c.span_id
WHERE c.kind IN ('client', 'producer') AND c.service_name != '' AND c.sample_weight > 0
  AND c.timestamp >= fromUnixTimestamp64Nano({c_from:Int64}) AND c.timestamp < fromUnixTimestamp64Nano({c_to:Int64})
  AND (s.service_name, s.service_namespace, s.deployment_environment) != (c.service_name, c.service_namespace, c.deployment_environment)
GROUP BY c.tenant_id, c.service_name, c.service_namespace, c.deployment_environment, minute,
	s.service_name, s.service_namespace, s.deployment_environment, c.peer_name`
}

func (l *Linker) link(ctx context.Context, c clickhouse.Conn, start, end, computedAt time.Time, measure bool) (uint64, float64, error) {
	params := ch.Parameters{
		"computed_at": strconv.FormatInt(computedAt.UnixMilli(), 10),
		"c_from":      strconv.FormatInt(start.UnixNano(), 10),
		"c_to":        strconv.FormatInt(end.UnixNano(), 10),
		"s_from":      strconv.FormatInt(start.UnixNano(), 10),
		"s_to":        strconv.FormatInt(end.Add(childSlack).UnixNano(), 10),
	}
	window := ch.Parameters{"from": strconv.FormatInt(start.Unix(), 10), "to": strconv.FormatInt(end.Unix(), 10)}
	var before float64
	if measure {
		var err error
		if before, err = l.linkedCalls(ctx, c, window); err != nil {
			return 0, 0, err
		}
	}
	settings := ch.Settings{"insert_deduplicate": 1}
	if measure {
		settings["max_threads"] = 2 // the catch-up pass runs next to live traffic
	}
	qctx := ch.Context(ctx, ch.WithParameters(params), ch.WithSettings(settings))
	if err := c.Exec(qctx, LinkSQL(l.opts.Database)); err != nil {
		return 0, 0, err
	}
	var n uint64
	cctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{
		"computed_at": params["computed_at"], "from": window["from"], "to": window["to"],
	}))
	if err := c.QueryRow(cctx, "SELECT count() FROM `"+l.opts.Database+"`.apm_service_links_1m_local "+
		"WHERE timestamp >= toDateTime({from:Int64}, 'UTC') AND timestamp < toDateTime({to:Int64}, 'UTC') "+
		"AND computed_at = fromUnixTimestamp64Milli({computed_at:Int64})").Scan(&n); err != nil {
		return 0, 0, err
	}
	if !measure {
		return n, 0, nil
	}
	after, err := l.linkedCalls(ctx, c, window)
	if err != nil {
		return n, 0, err
	}
	return n, max(0, after-before), nil
}

// linkedCalls is the weighted number of linked calls in a window on this replica (latest result per key).
func (l *Linker) linkedCalls(ctx context.Context, c clickhouse.Conn, window ch.Parameters) (float64, error) {
	var v float64
	err := c.QueryRow(ch.Context(ctx, ch.WithParameters(window)), "SELECT sum(calls) FROM `"+l.opts.Database+"`.apm_service_links_1m_local FINAL "+
		"WHERE timestamp >= toDateTime({from:Int64}, 'UTC') AND timestamp < toDateTime({to:Int64}, 'UTC')").Scan(&v)
	return v, err
}
