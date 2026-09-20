package jobs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/onuragtas/openlog/internal/api/query"
)

// The history of runs (ClickHouse `job_runs`) and the metric data points every run is mirrored as, so a
// metric alert rule watches a job like any other signal (alerting.md §2.2) and the 1-minute rollup keeps it
// for 395 days.

// Run is one concluded run of one monitor: the row of job_runs and the source of the metric points.
type Run struct {
	MonitorID string
	TenantID  string
	// Name is copied from the definition, so the history keeps describing what ran after a rename.
	Name string
	// Status is StatusSuccess, StatusFailure, StatusMissed or StatusOverrun; a run is written when it is
	// concluded, never while it is still running.
	Status string
	// StartedAt is zero when openlog never saw a start ping (a job that only reports when it finishes).
	StartedAt  time.Time
	FinishedAt time.Time
	DurationMs float64
	ExitCode   int
	Message    string
	// LateSeconds is how far past the expected time the run was concluded; negative means early.
	LateSeconds float64
	Source      string
}

// Metric names mirrored into `metrics`.
const (
	// MetricSuccess is 1 for a successful run and 0 for a failed or missed one; avg over a window is the
	// share of runs that worked.
	MetricSuccess = "jobs.run.success"
	// MetricDuration is how long the run took, when openlog saw both ends of it.
	MetricDuration = "jobs.run.duration"
	// MetricLate is how many seconds past its expected time the run was concluded — the number that says
	// "the nightly backup is finishing later every week" before it starts failing.
	MetricLate = "jobs.run.late"
	// MetricMissed is 1 for a run that never reported, 0 for one that did: a rule on "max over 10 minutes
	// above 0" is the alert for a job that stopped running at all.
	MetricMissed = "jobs.run.missed"
)

// scopeName marks the emitted data points as openlog's own (they have no OTLP sender).
const scopeName = "openlog/jobs"

// runColumns is the column list of job_runs (schema 0099_job_runs).
var runColumns = []string{"tenant_id", "monitor_id", "monitor_name", "timestamp", "status", "started_at",
	"duration_ms", "exit_code", "late_seconds", "message", "source"}

// metricColumns is the column list of `metrics` (schema 0002_metrics, 0081); the order matches metricRows.
var metricColumns = []string{"tenant_id", "metric_name", "metric_type", "temporality", "is_monotonic", "unit",
	"description", "service_name", "host_id", "host_name", "series_id", "resource_attributes", "scope_name",
	"attributes", "start_timestamp", "timestamp", "value", "count", "sum", "bucket_counts", "explicit_bounds",
	"flags", "quantiles", "quantile_values"}

// Succeeded reports whether the run is a success.
func (r Run) Succeeded() bool { return r.Status == StatusSuccess }

func runRows(rows []Run) [][]any {
	out := make([][]any, len(rows))
	for i, r := range rows {
		started := r.StartedAt
		if started.IsZero() {
			started = time.Unix(0, 0)
		}
		out[i] = []any{r.TenantID, r.MonitorID, r.Name, r.FinishedAt.UTC(), r.Status, started.UTC(),
			float32(r.DurationMs), int32(clampExit(r.ExitCode)), float32(r.LateSeconds),
			truncate(r.Message, MaxMessageBytes), r.Source}
	}
	return out
}

func clampExit(code int) int {
	if code < 0 || code > MaxExitCode {
		return MaxExitCode
	}
	return code
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// metricRows mirrors every concluded run as gauge data points.
func metricRows(rows []Run) [][]any {
	out := make([][]any, 0, 3*len(rows))
	for _, r := range rows {
		attrs := map[string]string{"job.id": r.MonitorID, "job.name": r.Name, "job.status": r.Status}
		success, missed := 0.0, 0.0
		switch r.Status {
		case StatusSuccess:
			success = 1
		case StatusMissed:
			missed = 1
		}
		out = append(out,
			metricRow(r, MetricSuccess, "1", success, attrs),
			metricRow(r, MetricMissed, "1", missed, attrs),
			metricRow(r, MetricLate, "s", r.LateSeconds, attrs))
		// A duration is only meaningful when openlog saw the run start; a job that reports once at the end
		// would otherwise contribute a zero that drags every average down.
		if !r.StartedAt.IsZero() && r.DurationMs > 0 {
			out = append(out, metricRow(r, MetricDuration, "ms", r.DurationMs, attrs))
		}
	}
	return out
}

func metricRow(r Run, name, unit string, value float64, attrs map[string]string) []any {
	ts := r.FinishedAt.UTC()
	return []any{r.TenantID, name, "gauge", "unspecified", false, unit, "", "", "", "",
		seriesID(r.TenantID, name, attrs), map[string]string{}, scopeName, attrs, ts, ts, value,
		uint64(0), float64(0), []uint64{}, []float64{}, uint32(0), []float64{}, []float64{}}
}

// seriesID is the stable 64-bit hash of (tenant, metric name, attributes) the processor computes for OTLP
// data points, so these points join the same series model as everything else.
func seriesID(tenant, metric string, attrs map[string]string) uint64 {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(tenant)
	b.WriteByte(0)
	b.WriteString(metric)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte(1)
		b.WriteString(attrs[k])
	}
	return xxhash.Sum64String(b.String())
}

// ---- reading (GET /jobs/monitors/{id}/runs) ----

// HistoryMaxRange bounds a runs query (the rows are kept 90 days).
const HistoryMaxRange = 92 * 24 * time.Hour

// History is a page of concluded runs, newest first.
func History(ctx context.Context, sc *query.Scope, monitorID string, from, to time.Time, limit int) ([]Run, error) {
	if monitorID == "" {
		return nil, invalid("monitor_id", "required")
	}
	if limit <= 0 || limit > maxRunsPerMonitor {
		limit = 100
	}
	q := sc.From(query.JobRuns).Columns(
		"toInt64(toUnixTimestamp64Milli(timestamp)) AS at_ms",
		"status AS st",
		"toInt64(toUnixTimestamp(started_at)) AS started",
		"toFloat64(duration_ms) AS ms",
		"toInt32(exit_code) AS code",
		"toFloat64(late_seconds) AS late",
		"message AS msg",
		"source AS src",
	).Where("monitor_id = {monitor_id:String}").
		Where("timestamp >= {from:DateTime64(3)}").
		Where("timestamp < {to:DateTime64(3)}").
		Param("monitor_id", monitorID).Param("from", from.UTC()).Param("to", to.UTC()).
		OrderBy("timestamp DESC").Limit(limit)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var (
			atMs, started int64
			st, msg, src  string
			ms, late      float64
			code          int32
		)
		if err := rows.Scan(&atMs, &st, &started, &ms, &code, &late, &msg, &src); err != nil {
			return nil, err
		}
		r := Run{MonitorID: monitorID, Status: st, FinishedAt: time.UnixMilli(atMs).UTC(),
			DurationMs: ms, ExitCode: int(code), LateSeconds: late, Message: msg, Source: src}
		if started > 0 {
			r.StartedAt = time.Unix(started, 0).UTC()
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Summary is the outcome of a monitor's runs over a range.
type Summary struct {
	Runs     uint64
	Failures uint64
	Missed   uint64
	AvgMs    *float64
	MaxMs    *float64
	LastAt   *time.Time
}

// Summaries returns the outcome per monitor in [from, to). monitorID limits it to one ("" = every monitor
// of the tenant); monitors without a run in the range are absent from the map.
func Summaries(ctx context.Context, sc *query.Scope, monitorID string, from, to time.Time) (map[string]Summary, error) {
	q := sc.From(query.JobRuns).Columns(
		"monitor_id AS id",
		"count() AS runs",
		fmt.Sprintf("countIf(status = '%s' OR status = '%s') AS failures", StatusFailure, StatusOverrun),
		fmt.Sprintf("countIf(status = '%s') AS missed", StatusMissed),
		"avgIf(toFloat64(duration_ms), duration_ms > 0) AS avg_ms",
		"maxIf(toFloat64(duration_ms), duration_ms > 0) AS max_ms",
		"toInt64(toUnixTimestamp64Milli(max(timestamp))) AS last_ms",
	).Where("timestamp >= {from:DateTime64(3)}").
		Where("timestamp < {to:DateTime64(3)}").
		Param("from", from.UTC()).Param("to", to.UTC()).
		GroupBy("monitor_id").Limit(maxRunsPerMonitor)
	if monitorID != "" {
		q = q.Where("monitor_id = {monitor_id:String}").Param("monitor_id", monitorID)
	}
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Summary{}
	for rows.Next() {
		var (
			id                     string
			runs, failures, missed uint64
			avgMs, maxMs           float64
			lastMs                 int64
		)
		if err := rows.Scan(&id, &runs, &failures, &missed, &avgMs, &maxMs, &lastMs); err != nil {
			return nil, err
		}
		s := Summary{Runs: runs, Failures: failures, Missed: missed}
		if avgMs > 0 {
			s.AvgMs = &avgMs
		}
		if maxMs > 0 {
			s.MaxMs = &maxMs
		}
		if lastMs > 0 {
			at := time.UnixMilli(lastMs).UTC()
			s.LastAt = &at
		}
		out[id] = s
	}
	return out, rows.Err()
}
