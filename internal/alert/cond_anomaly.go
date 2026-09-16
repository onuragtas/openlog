package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
)

// anomaly rules (alerting.md §2.12): the signal is compared with the distribution of the same seasonal slot over
// the lookback instead of with a fixed threshold. The series value is the deviation divided by the allowed band
// (sensitivity × σ), so the comparison is fixed at `gte 1` and the sensitivity carries the configuration — the
// same shape slo_burn uses for its burn ratio.

// TypeAnomaly is a baseline (anomaly) condition on a metric or an APM service signal.
const TypeAnomaly = "anomaly"

type anomalyType struct{}

func (anomalyType) Name() string              { return TypeAnomaly }
func (anomalyType) Available() bool           { return true }
func (anomalyType) UnavailableReason() string { return "" }
func (anomalyType) DefaultInterval() int      { return 60 }

// Anomaly signals.
const (
	AnomalySignalMetric = "metric"
	AnomalySignalAPM    = "apm"
)

// anomalySeasons are the seasonal periods in minutes; "none" repeats the window itself (recent history).
var anomalySeasons = map[string]int{"none": 0, "hourly": 60, "daily": 1440, "weekly": 10080}

// rollupAggs are the aggregations the 1-minute metric rollup can serve: metrics_1m keeps min/max/sum/count and
// the last value per minute, so `rate` and the percentiles (which need the raw points) are not available here.
var rollupAggs = map[string]bool{"avg": true, "min": true, "max": true, "sum": true, "count": true, "last": true}

// metricRollupCols are the filter/group-by columns of metrics_1m: it has no host name and no resource attributes.
var metricRollupCols = columns{hostID: "host_id", service: "service_name", attrs: "attributes"}

const (
	madScale              = 1.4826 // makes the MAD a consistent estimator of σ for normally distributed data
	anomalySigmaFloor     = 0.01   // the band is never narrower than 1 % of the baseline level
	maxAnomalyRatio       = 1e6    // reported instead of +Inf when the history has no variation at all
	maxAnomalyLags        = 200    // baseline samples per series (bounds the query)
	maxAnomalyBuckets     = 8000   // compact minute buckets per series (bounds a preview)
	maxAnomalyLookback    = 28     // days
	minAnomalySamples     = 2
	maxAnomalySamples     = 50
	minAnomalySensitivity = 0.5
	maxAnomalySensitivity = 20
)

// AnomalyCondition is an anomaly condition (alerting.md §2.12).
type AnomalyCondition struct {
	Signal string `json:"signal"`
	// Metric signal: the metric_threshold selector of §2.2, read from the 1-minute rollup.
	Metric            string   `json:"metric"`
	Aggregation       string   `json:"aggregation"`
	SeriesAggregation string   `json:"series_aggregation"`
	Filters           []Filter `json:"filters"`
	GroupBy           []string `json:"group_by"`
	// APM signal: the apm selector of §2.6, read from apm_transactions_1m.
	ServiceName      string  `json:"service_name"`
	ServiceNamespace *string `json:"service_namespace"`
	Environment      *string `json:"environment"`
	TransactionType  string  `json:"transaction_type"`
	TransactionName  string  `json:"transaction_name"`
	MinRequests      float64 `json:"min_requests"`
	// Baseline.
	WindowSeconds int     `json:"window_seconds"`
	Seasonality   string  `json:"seasonality"`
	LookbackDays  int     `json:"lookback_days"`
	Direction     string  `json:"direction"`
	Sensitivity   float64 `json:"sensitivity"`
	MinSamples    int     `json:"min_samples"`
	MinDeviation  float64 `json:"min_deviation"`
}

func (anomalyType) Parse(raw json.RawMessage) (Condition, error) {
	var c AnomalyCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	if c.Signal == "" {
		c.Signal = AnomalySignalMetric
	}
	var err error
	switch c.Signal {
	case AnomalySignalMetric:
		if c.Metric == "" || len(c.Metric) > maxMetricBytes {
			return nil, invalid("metric", "required, at most %d bytes", maxMetricBytes)
		}
		if c.Aggregation == "" {
			c.Aggregation = "avg"
		}
		if !rollupAggs[c.Aggregation] {
			return nil, invalid("aggregation", "must be avg, min, max, sum, count or last: a baseline reads the 1-minute rollup, which keeps no raw points for rate or percentiles")
		}
		if c.SeriesAggregation == "" {
			c.SeriesAggregation = "avg"
			if c.Aggregation == "count" || c.Aggregation == "sum" {
				c.SeriesAggregation = "sum"
			}
		}
		if !seriesAggs[c.SeriesAggregation] {
			return nil, invalid("series_aggregation", "must be avg, sum, min or max")
		}
		if c.Filters, err = validateFilters(c.Filters, metricRollupCols); err != nil {
			return nil, err
		}
		if c.GroupBy, err = validateGroupBy(c.GroupBy, metricRollupCols, nil); err != nil {
			return nil, err
		}
		if c.ServiceName != "" || c.ServiceNamespace != nil || c.Environment != nil ||
			c.TransactionType != "" || c.TransactionName != "" || c.MinRequests != 0 {
			return nil, invalid("signal", "service_name, service_namespace, environment, transaction_type, transaction_name and min_requests need signal apm")
		}
	case AnomalySignalAPM:
		if c.ServiceName == "" || len(c.ServiceName) > 512 {
			return nil, invalid("service_name", "required, at most 512 bytes")
		}
		for f, v := range map[string]*string{"service_namespace": c.ServiceNamespace, "environment": c.Environment} {
			if v != nil && len(*v) > 512 {
				return nil, invalid(f, "at most 512 bytes")
			}
		}
		if len(c.TransactionType) > 64 || len(c.TransactionName) > maxValueBytes {
			return nil, invalid("transaction_name", "transaction_type ≤ 64 and transaction_name ≤ %d bytes", maxValueBytes)
		}
		if !apmMetrics[c.Metric] {
			return nil, invalid("metric", "must be throughput, error_rate, errors, avg_ms, p50_ms, p95_ms, p99_ms or apdex")
		}
		if c.MinRequests < 0 || !finite(c.MinRequests) {
			return nil, invalid("min_requests", "must be >= 0")
		}
		if c.Aggregation != "" || c.SeriesAggregation != "" || len(c.Filters) > 0 {
			return nil, invalid("signal", "aggregation, series_aggregation and filters need signal metric")
		}
		seen := map[string]bool{}
		gb := []string{}
		for i, gr := range c.GroupBy {
			if gr != "environment" && gr != "transaction" {
				return nil, invalid(fmt.Sprintf("group_by[%d]", i), "must be environment or transaction")
			}
			if !seen[gr] {
				seen[gr], gb = true, append(gb, gr)
			}
		}
		c.GroupBy = gb
	default:
		return nil, invalid("signal", "must be metric or apm")
	}
	if err := validateWindow("window_seconds", &c.WindowSeconds, defaultWindow, 60, maxMetricWin); err != nil {
		return nil, err
	}
	c.WindowSeconds = int(ceilDiv(int64(c.WindowSeconds), 60) * 60) // whole minutes (1-minute rollups)
	if c.Seasonality == "" {
		c.Seasonality = "daily"
	}
	period, ok := anomalySeasons[c.Seasonality]
	if !ok {
		return nil, invalid("seasonality", "must be none, hourly, daily or weekly")
	}
	if w := c.WindowSeconds / 60; period > 0 && period%w != 0 {
		return nil, invalid("window_seconds", "must divide the %s season (%d minutes) so that the baseline windows cover the same slot", c.Seasonality, period)
	}
	if c.LookbackDays == 0 {
		c.LookbackDays = 7
	}
	if c.LookbackDays < 1 || c.LookbackDays > maxAnomalyLookback {
		return nil, invalid("lookback_days", "must be between 1 and %d", maxAnomalyLookback)
	}
	if c.Direction == "" {
		c.Direction = "upper"
	}
	switch c.Direction {
	case "upper", "lower", "both":
	default:
		return nil, invalid("direction", "must be upper, lower or both")
	}
	if c.Sensitivity == 0 {
		c.Sensitivity = 3
	}
	if !finite(c.Sensitivity) || c.Sensitivity < minAnomalySensitivity || c.Sensitivity > maxAnomalySensitivity {
		return nil, invalid("sensitivity", "must be between %g and %d standard deviations", minAnomalySensitivity, maxAnomalySensitivity)
	}
	if c.MinSamples == 0 {
		c.MinSamples = 3
	}
	if c.MinSamples < minAnomalySamples || c.MinSamples > maxAnomalySamples {
		return nil, invalid("min_samples", "must be between %d and %d", minAnomalySamples, maxAnomalySamples)
	}
	if c.MinDeviation < 0 || !finite(c.MinDeviation) {
		return nil, invalid("min_deviation", "must be >= 0")
	}
	if n := c.lags(); n < c.MinSamples {
		return nil, invalid("lookback_days", "%d days of %s seasonality give only %d baseline samples, fewer than min_samples (%d)",
			c.LookbackDays, c.Seasonality, n, c.MinSamples)
	}
	return c, nil
}

// Judge is fixed: the value is the deviation divided by the allowed band, which reaches 1 at `sensitivity` σ.
func (c AnomalyCondition) Judge() Judge { return Judge{Operator: "gte", Threshold: 1, Recovery: 1} }

func (c AnomalyCondition) Window() time.Duration {
	return time.Duration(c.WindowSeconds) * time.Second
}
func (c AnomalyCondition) IgnoresFor() bool { return false }
func (c AnomalyCondition) Missing() string  { return "keep" }

// periodMinutes is the distance between two baseline samples; without seasonality the windows are adjacent.
func (c AnomalyCondition) periodMinutes() int {
	if p := anomalySeasons[c.Seasonality]; p > 0 {
		return p
	}
	return c.WindowSeconds / 60
}

// lags is the number of baseline samples an evaluation uses (the lookback, bounded).
func (c AnomalyCondition) lags() int {
	return min(c.LookbackDays*1440/c.periodMinutes(), maxAnomalyLags)
}

func (c AnomalyCondition) Summary(s Sample, _ string) string {
	signal := c.Metric + " " + c.Aggregation
	if c.Signal == AnomalySignalAPM {
		signal = c.ServiceName + " " + c.Metric
	}
	where := "above"
	if s.Labels["anomaly.direction"] == "below" {
		where = "below"
	}
	season := c.Seasonality
	if season == "none" {
		season = "recent"
	}
	baseline := ""
	if b := s.Labels["anomaly.baseline"]; b != "" {
		baseline = " " + b
	}
	return fmt.Sprintf("%s over %s is %sσ %s the %s baseline%s (≥ %sσ)%s", signal, humanDuration(c.Window()),
		formatValue(s.Value*c.Sensitivity), where, season, baseline, formatValue(c.Sensitivity), anomalyLabels(s.Labels))
}

// anomalyLabels renders the series labels of a summary without the computed anomaly.* ones.
func anomalyLabels(labels map[string]string) string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if !strings.HasPrefix(k, "anomaly.") {
			out[k] = v
		}
	}
	return labelSuffix(out)
}

// ---- the baseline grid ----

// anomalyGrid maps the sparse minute buckets a baseline needs into one compact, time-ordered array per series.
// Bucket bk counts whole minutes backwards from the anchor (bk = 0 is the last minute before it). An evaluation
// ending at anchor − j·step needs bk ∈ [j·step + m·period, j·step + m·period + window) for every lag m ∈ [0, lags]
// (m = 0 is the current window, m ≥ 1 the baseline samples). Those are lags+1 runs of
// runLen = (ends−1)·step + window minutes; when the runs would overlap (period ≤ runLen) one contiguous range is
// read instead. Reading only these minutes keeps a daily 7-day baseline at 8×5 buckets per series instead of 10 080.
type anomalyGrid struct {
	anchor time.Time
	window int // minutes
	step   int // minutes between consecutive evaluation ends
	ends   int
	period int
	lags   int
	runLen int
	dense  bool
}

func (c AnomalyCondition) grid(anchor time.Time, ends, step, lags int) anomalyGrid {
	w := c.WindowSeconds / 60
	g := anomalyGrid{anchor: anchor, window: w, step: step, ends: ends, period: c.periodMinutes(), lags: lags}
	g.runLen = (ends-1)*step + w
	g.dense = g.period <= g.runLen
	return g
}

// spanMinutes is how far back the query reads.
func (g anomalyGrid) spanMinutes() int { return g.lags*g.period + g.runLen }

// size is the length of the compact per-series array.
func (g anomalyGrid) size() int {
	if g.dense {
		return g.spanMinutes()
	}
	return (g.lags + 1) * g.runLen
}

// index maps a minute bucket to its compact position, or -1 when the bucket is not needed.
func (g anomalyGrid) index(bk int64) int {
	if bk < 0 || bk > int64(g.spanMinutes()) {
		return -1
	}
	if g.dense {
		if int(bk) >= g.size() {
			return -1
		}
		return g.size() - 1 - int(bk)
	}
	m, q := int(bk)/g.period, int(bk)%g.period
	if m > g.lags || q >= g.runLen {
		return -1
	}
	return m*g.runLen + g.runLen - 1 - q
}

// slice returns the compact range of the window of end j (0 = the anchor) at baseline lag m (0 = the current one).
func (g anomalyGrid) slice(j, m int) (int, int) {
	off := j * g.step
	if g.dense {
		off += m * g.period
		hi := g.size() - off
		return hi - g.window, hi
	}
	hi := m*g.runLen + g.runLen - off
	return hi - g.window, hi
}

// bucketExpr is the minute bucket of a rollup row relative to the anchor.
func (g anomalyGrid) bucketExpr(q *query.Select) string {
	q.Param("a_anchor", g.anchor.Unix())
	return "intDiv({a_anchor:Int64} - toInt64(toUnixTimestamp(timestamp)) - 1, 60)"
}

// restrict limits the read to the minutes the baseline needs.
func (g anomalyGrid) restrict(q *query.Select) {
	bk := g.bucketExpr(q)
	q.Where("timestamp >= toDateTime({a_lo:Int64}) AND timestamp < toDateTime({a_anchor:Int64})").
		Param("a_lo", g.anchor.Unix()-int64(g.spanMinutes())*60)
	if !g.dense {
		q.Where("modulo("+bk+", {a_period:Int64}) < {a_run:Int64}").
			Param("a_period", int64(g.period)).Param("a_run", int64(g.runLen))
	}
}

// ---- reading the signal ----

// anomalyGroup is one series of the watched signal; value aggregates the compact buckets [lo, hi).
type anomalyGroup struct {
	key    string
	labels map[string]string
	value  func(lo, hi int) float64
}

func (c AnomalyCondition) fetch(ctx context.Context, sc *query.Scope, g anomalyGrid, lim Limits) ([]anomalyGroup, error) {
	if c.Signal == AnomalySignalAPM {
		return c.fetchAPM(ctx, sc, g, lim)
	}
	return c.fetchMetric(ctx, sc, g, lim)
}

// fetchMetric reads the 1-minute metric rollup (metrics_1m) per time series and bucket.
func (c AnomalyCondition) fetchMetric(ctx context.Context, sc *query.Scope, g anomalyGrid, lim Limits) ([]anomalyGroup, error) {
	type group struct {
		key    string
		labels map[string]string
		series map[uint64][]bucketStats
	}
	ds := buildDims(c.GroupBy, metricRollupCols)
	q := sc.From(query.Metrics1m)
	bk := g.bucketExpr(q)
	keys := dimExprs(q, ds)
	cols := make([]string, 0, len(keys)+7)
	for i, k := range keys {
		cols = append(cols, "any("+k+") AS d"+fmt.Sprint(i))
	}
	cols = append(cols, "series_id", bk+" AS bk", "sum(value_count) AS cnt", "sum(value_sum) AS sv",
		"min(value_min) AS mn", "max(value_max) AS mx", "argMaxMerge(value_last) AS lv")
	q.Columns(cols...).GroupBy("series_id", "bk")
	q.Where("metric_name = {metric:String}").Param("metric", c.Metric)
	g.restrict(q)
	applyFilters(q, c.Filters, metricRollupCols, "")
	q.Limit(lim.MaxRows + 1)

	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string]*group{}
	count := 0
	for rows.Next() {
		if count++; count > lim.MaxRows {
			return nil, &LimitError{Msg: fmt.Sprintf("the baseline matches more than %d rollup buckets; add filters, fewer dimensions or a shorter lookback", lim.MaxRows)}
		}
		dvals := make([]string, len(ds))
		dest := make([]any, 0, len(ds)+7)
		for i := range dvals {
			dest = append(dest, &dvals[i])
		}
		var (
			sid            uint64
			bkv            int64
			cnt            uint64
			sv, mn, mx, lv float64
		)
		dest = append(dest, &sid, &bkv, &cnt, &sv, &mn, &mx, &lv)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		key := seriesKey(ds, dvals)
		grp, ok := groups[key]
		if !ok {
			labels := groupLabels(ds, dvals, "")
			delete(labels, "host.name") // the rollup has no host name column
			grp = &group{key: key, labels: labels, series: map[uint64][]bucketStats{}}
			groups[key] = grp
		}
		bs, ok := grp.series[sid]
		if !ok {
			bs = make([]bucketStats, g.size())
			grp.series[sid] = bs
		}
		if i := g.index(bkv); i >= 0 {
			bs[i] = bucketStats{Count: cnt, Sum: sv, Min: mn, Max: mx, Last: lv}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]anomalyGroup, 0, len(groups))
	for _, grp := range groups {
		out = append(out, anomalyGroup{key: grp.key, labels: grp.labels, value: c.metricValue(grp.series)})
	}
	return out, nil
}

// metricValue combines the time series of one group over a bucket range, like metric_threshold does.
func (c AnomalyCondition) metricValue(series map[uint64][]bucketStats) func(lo, hi int) float64 {
	return func(lo, hi int) float64 {
		vals := make([]float64, 0, len(series))
		for _, bs := range series {
			if v, ok := windowValue(bs[lo:hi], c.Aggregation, float64(c.WindowSeconds)); ok && finite(v) {
				vals = append(vals, v)
			}
		}
		v, ok := seriesAggregate(vals, c.SeriesAggregation)
		if !ok {
			return math.NaN()
		}
		return v
	}
}

// apmInner is the equivalent apm condition: its metric math (weighted requests, histogram quantiles, Apdex)
// computes the signal of an APM baseline from the same rollup buckets.
func (c AnomalyCondition) apmInner() APMCondition {
	return APMCondition{ServiceName: c.ServiceName, ServiceNamespace: c.ServiceNamespace, Environment: c.Environment,
		TransactionType: c.TransactionType, TransactionName: c.TransactionName, Metric: c.Metric,
		GroupBy: c.GroupBy, WindowSeconds: c.WindowSeconds, MinRequests: c.MinRequests}
}

// fetchAPM reads apm_transactions_1m per bucket (apm.md §10).
func (c AnomalyCondition) fetchAPM(ctx context.Context, sc *query.Scope, g anomalyGrid, lim Limits) ([]anomalyGroup, error) {
	inner := c.apmInner()
	q := sc.From(query.ApmTransactions1m)
	bk := g.bucketExpr(q)
	cols, groupBy := []string{}, []string{}
	if contains(c.GroupBy, "environment") {
		cols = append(cols, "toString(deployment_environment) AS d_env")
		groupBy = append(groupBy, "d_env")
	}
	if contains(c.GroupBy, "transaction") {
		cols = append(cols, "toString(transaction_type) AS d_ttype", "toString(transaction_name) AS d_tname")
		groupBy = append(groupBy, "d_ttype", "d_tname")
	}
	cols = append(cols,
		"any(toString(service_namespace)) AS ns", "any(toString(deployment_environment)) AS envv", bk+" AS bk",
		"sum(requests) AS req", "sum(errors) AS errs", "sum(duration_sum_ms) AS dsum",
		"tupleElement(sumMap(duration_hist), 1) AS hk", "tupleElement(sumMap(duration_hist), 2) AS hv",
		"tupleElement(sumMap(ok_hist), 1) AS okk", "tupleElement(sumMap(ok_hist), 2) AS okv")
	q.Columns(cols...).GroupBy(append(groupBy, "bk")...)
	q.Where("service_name = {svc:String}").Param("svc", c.ServiceName)
	if c.ServiceNamespace != nil {
		q.Where("service_namespace = {svc_ns:String}").Param("svc_ns", *c.ServiceNamespace)
	}
	if c.Environment != nil {
		q.Where("deployment_environment = {svc_env:String}").Param("svc_env", *c.Environment)
	}
	if c.TransactionType != "" {
		q.Where("transaction_type = {tx_type:String}").Param("tx_type", c.TransactionType)
	}
	if c.TransactionName != "" {
		q.Where("transaction_name = {tx_name:String}").Param("tx_name", c.TransactionName)
	}
	g.restrict(q)
	q.Limit(lim.MaxRows + 1)

	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string]*apmGroup{}
	order := []string{}
	count := 0
	for rows.Next() {
		if count++; count > lim.MaxRows {
			return nil, &LimitError{Msg: fmt.Sprintf("the baseline matches more than %d transaction buckets; shorten the lookback", lim.MaxRows)}
		}
		var (
			dEnv, dType, dName, ns, env string
			bkv                         int64
			req, errs, dsum             float64
			hk, okk                     []int16
			hv, okv                     []float64
		)
		dest := []any{}
		if contains(c.GroupBy, "environment") {
			dest = append(dest, &dEnv)
		}
		if contains(c.GroupBy, "transaction") {
			dest = append(dest, &dType, &dName)
		}
		dest = append(dest, &ns, &env, &bkv, &req, &errs, &dsum, &hk, &hv, &okk, &okv)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		labels := map[string]string{"service.name": c.ServiceName}
		ds := []dim{{label: "service.name"}}
		vals := []string{c.ServiceName}
		if c.ServiceNamespace != nil {
			labels["service.namespace"] = *c.ServiceNamespace
		}
		if contains(c.GroupBy, "environment") {
			labels["environment"] = dEnv
			ds, vals = append(ds, dim{label: "environment"}), append(vals, dEnv)
		} else if c.Environment != nil {
			labels["environment"] = *c.Environment
		}
		if contains(c.GroupBy, "transaction") {
			labels["transaction.type"], labels["transaction.name"] = dType, dName
			ds, vals = append(ds, dim{label: "transaction.type"}, dim{label: "transaction.name"}), append(vals, dType, dName)
		}
		key := seriesKey(ds, vals)
		grp, ok := groups[key]
		if !ok {
			grp = &apmGroup{key: key, labels: labels, namespace: ns, env: env, buckets: make([]apmBucket, g.size())}
			groups[key], order = grp, append(order, key)
		}
		if i := g.index(bkv); i >= 0 {
			b := &grp.buckets[i]
			b.req += req
			b.err += errs
			b.dsum += dsum
			b.hist = b.hist.Add(apm.NewHist(hk, hv))
			b.ok = b.ok.Add(apm.NewHist(okk, okv))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tFn := apdexT(ctx)
	out := make([]anomalyGroup, 0, len(order))
	for _, key := range order {
		grp := groups[key]
		tMs := tFn(c.ServiceName, grp.namespace, grp.env)
		out = append(out, anomalyGroup{key: grp.key, labels: grp.labels, value: func(lo, hi int) float64 {
			return inner.value(grp, lo, hi, c.Window().Minutes(), tMs)
		}})
	}
	return out, nil
}

// ---- the baseline itself ----

// anomalyScore is one series' deviation from its baseline.
type anomalyScore struct {
	ratio    float64 // deviation ÷ (sensitivity × σ): NaN = no value, 0 = inside the band, 1 = exactly at its edge
	baseline float64
	above    bool
}

// score compares current with the baseline samples of the same seasonal slot.
//
// The centre is the median and the scale is 1.4826 × MAD (the median absolute deviation, scaled so that it
// estimates σ for normally distributed data) rather than mean + standard deviation: an alerting baseline is
// computed over a history that contains the previous incidents, and mean/stddev have a breakdown point of 0 —
// one past spike raises the centre and inflates the band for the whole lookback, so the next real anomaly is
// swallowed. The median and the MAD only move once half the samples are outliers.
func (c AnomalyCondition) score(current float64, samples []float64) anomalyScore {
	out := anomalyScore{ratio: math.NaN(), baseline: math.NaN()}
	if math.IsNaN(current) || len(samples) < c.MinSamples {
		return out
	}
	med := medianOf(samples)
	devs := make([]float64, len(samples))
	for i, v := range samples {
		devs[i] = math.Abs(v - med)
	}
	sigma := madScale * medianOf(devs)
	if f := anomalySigmaFloor * math.Abs(med); sigma < f {
		sigma = f // a quiet signal must still move by 1 % of its level before the sensitivity applies
	}
	diff := current - med
	out.baseline, out.above = med, diff >= 0
	dev := diff
	switch c.Direction {
	case "lower":
		dev = -diff
	case "both":
		dev = math.Abs(diff)
	}
	switch {
	case dev <= 0 || dev < c.MinDeviation:
		out.ratio = 0 // on the safe side, or below the absolute noise floor
	case c.Sensitivity*sigma <= 0:
		out.ratio = maxAnomalyRatio // a history without any variation: every movement is an anomaly
	default:
		out.ratio = math.Min(dev/(c.Sensitivity*sigma), maxAnomalyRatio)
	}
	return out
}

func medianOf(vs []float64) float64 {
	if len(vs) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// scoreAt evaluates one group at end j (0 = the grid anchor).
func (c AnomalyCondition) scoreAt(g anomalyGrid, grp anomalyGroup, j int) anomalyScore {
	lo, hi := g.slice(j, 0)
	current := grp.value(lo, hi)
	if math.IsNaN(current) {
		return anomalyScore{ratio: math.NaN(), baseline: math.NaN()}
	}
	samples := make([]float64, 0, g.lags)
	for m := 1; m <= g.lags; m++ {
		lo, hi = g.slice(j, m)
		if v := grp.value(lo, hi); !math.IsNaN(v) {
			samples = append(samples, v)
		}
	}
	return c.score(current, samples)
}

// labelsFor adds the baseline and its side to the series labels (they reach incidents and notifications).
func labelsFor(grp anomalyGroup, s anomalyScore) map[string]string {
	out := make(map[string]string, len(grp.labels)+2)
	for k, v := range grp.labels {
		out[k] = v
	}
	out["anomaly.baseline"] = formatValue(s.baseline)
	out["anomaly.direction"] = "below"
	if s.above {
		out["anomaly.direction"] = "above"
	}
	return out
}

func (c AnomalyCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error) {
	lim = lim.withDefaults()
	endM := end.Truncate(time.Minute) // complete minutes only (the rollups are per minute)
	g := c.grid(endM, 1, c.WindowSeconds/60, c.lags())
	groups, err := c.fetch(ctx, sc, g, lim)
	if err != nil {
		return nil, err
	}
	if len(groups) > lim.MaxSeries {
		return nil, &LimitError{Msg: fmt.Sprintf("too many series (%d > %d); add filters or fewer group_by dimensions", len(groups), lim.MaxSeries)}
	}
	res := &EvalResult{Unit: "1"} // the value is a dimensionless deviation ratio
	for _, grp := range groups {
		if s := c.scoreAt(g, grp, 0); !math.IsNaN(s.ratio) {
			res.Samples = append(res.Samples, Sample{Key: grp.key, Labels: labelsFor(grp, s), Value: s.ratio})
		}
	}
	sort.Slice(res.Samples, func(i, j int) bool { return res.Samples[i].Key < res.Samples[j].Key })
	return res, nil
}

func (c AnomalyCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error) {
	lim = lim.withDefaults()
	stepM := time.Duration(ceilDiv(int64(step), int64(time.Minute))) * time.Minute
	ends := rangeEnds(from.Truncate(time.Minute), to.Truncate(time.Minute), stepM)
	res := &RangeResult{Ends: ends, Unit: "1", Approximate: stepM != step}
	if len(ends) == 0 {
		return res, nil
	}
	g := c.grid(ends[len(ends)-1], len(ends), int(stepM/time.Minute), c.lags())
	// A long preview of a seasonal baseline would read far more history than one query should: use fewer (the
	// most recent) baseline samples and mark the preview approximate, the way other types do for coarse steps.
	for g.size() > maxAnomalyBuckets && g.lags > c.MinSamples {
		g.lags--
		res.Approximate = true
	}
	if g.size() > maxAnomalyBuckets {
		return nil, &LimitError{Msg: fmt.Sprintf("the preview would read %d minutes per series; shorten the range or the lookback", g.size())}
	}
	groups, err := c.fetch(ctx, sc, g, lim)
	if err != nil {
		return nil, err
	}
	for _, grp := range groups {
		rs := RangeSeries{Key: grp.key, Labels: grp.labels, Values: nanSlice(len(ends))}
		for i := range ends {
			rs.Values[i] = c.scoreAt(g, grp, len(ends)-1-i).ratio
		}
		res.Series = append(res.Series, rs)
	}
	sortSeries(res.Series)
	if len(res.Series) > lim.MaxSeries {
		res.Series, res.Truncated = res.Series[:lim.MaxSeries], true
	}
	return res, nil
}
