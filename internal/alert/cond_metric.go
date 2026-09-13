package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

type metricType struct{}

func (metricType) Name() string              { return TypeMetricThreshold }
func (metricType) Available() bool           { return true }
func (metricType) UnavailableReason() string { return "" }
func (metricType) DefaultInterval() int      { return 60 }

// MetricCondition is a metric_threshold condition (§2.2).
type MetricCondition struct {
	Metric            string   `json:"metric"`
	Aggregation       string   `json:"aggregation"`
	SeriesAggregation string   `json:"series_aggregation"`
	WindowSeconds     int      `json:"window_seconds"`
	Filters           []Filter `json:"filters"`
	GroupBy           []string `json:"group_by"`
	Operator          string   `json:"operator"`
	Threshold         *float64 `json:"threshold"`
	RecoveryThreshold *float64 `json:"recovery_threshold"`
	MissingData       string   `json:"missing_data"`

	judge Judge
}

func (metricType) Parse(raw json.RawMessage) (Condition, error) {
	var c MetricCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	if c.Metric == "" || len(c.Metric) > maxMetricBytes {
		return nil, invalid("metric", "required, at most %d bytes", maxMetricBytes)
	}
	if c.Aggregation == "" {
		c.Aggregation = "avg"
	}
	if !timeAggs[c.Aggregation] {
		return nil, invalid("aggregation", "must be avg, min, max, sum, last, count, rate, p50, p95 or p99")
	}
	if c.SeriesAggregation == "" {
		c.SeriesAggregation = "avg"
		if c.Aggregation == "rate" || c.Aggregation == "count" {
			c.SeriesAggregation = "sum"
		}
	}
	if !seriesAggs[c.SeriesAggregation] {
		return nil, invalid("series_aggregation", "must be avg, sum, min or max")
	}
	if err := validateWindow("window_seconds", &c.WindowSeconds, defaultWindow, minWindow, maxMetricWin); err != nil {
		return nil, err
	}
	var err error
	if c.Filters, err = validateFilters(c.Filters, telemetryCols); err != nil {
		return nil, err
	}
	if c.GroupBy, err = validateGroupBy(c.GroupBy, telemetryCols, nil); err != nil {
		return nil, err
	}
	if c.judge, err = validateThreshold(c.Operator, c.Threshold, c.RecoveryThreshold); err != nil {
		return nil, err
	}
	switch c.MissingData {
	case "":
		c.MissingData = "keep"
	case "keep", "ok", "breach":
	default:
		return nil, invalid("missing_data", "must be keep, ok or breach")
	}
	if c.RecoveryThreshold == nil {
		c.RecoveryThreshold = nil // stored as null: recovery at the threshold
	}
	return c, nil
}

func (c MetricCondition) Judge() Judge          { return c.judge }
func (c MetricCondition) Window() time.Duration { return time.Duration(c.WindowSeconds) * time.Second }
func (c MetricCondition) IgnoresFor() bool      { return false }
func (c MetricCondition) Missing() string       { return c.MissingData }

func (c MetricCondition) Summary(s Sample, unit string) string {
	return fmt.Sprintf("%s %s over %s is %s (%s %s)%s", c.Metric, c.Aggregation, humanDuration(c.Window()),
		formatValue(s.Value), opSymbol(c.Operator), formatValue(c.judge.Threshold), labelSuffix(s.Labels))
}

// metricGroup accumulates the time series of one group.
type metricGroup struct {
	key    string
	labels map[string]string
	series map[uint64][]bucketStats
	qs     []float64 // quantile per bucket (percentile aggregations)
	qcnt   []uint64
}

// fetch reads per-series (or, for percentiles, per-group) bucket aggregates for buckets [0, n) starting at origin.
func (c MetricCondition) fetch(ctx context.Context, sc *query.Scope, origin time.Time, step time.Duration, n int, lim Limits) (map[string]*metricGroup, string, error) {
	lim = lim.withDefaults()
	ds := buildDims(c.GroupBy, telemetryCols)
	end := origin.Add(time.Duration(n) * step)
	q := sc.From(query.Metrics)
	b := bucketExpr(q, "timestamp", origin, step)
	keys := dimExprs(q, ds)
	_, isQuantile := quantileOf(c.Aggregation)
	cols := make([]string, 0, len(keys)+14)
	if isQuantile {
		for i, k := range keys {
			cols = append(cols, k+" AS d"+fmt.Sprint(i))
		}
		qf := map[string]string{"p50": "quantile(0.5)(value)", "p95": "quantile(0.95)(value)", "p99": "quantile(0.99)(value)"}[c.Aggregation]
		cols = append(cols, "any(host_name) AS hn", b+" AS bk", qf+" AS qv", "count() AS cnt", "any(unit) AS u")
		groupBy := make([]string, 0, len(keys)+1)
		for i := range keys {
			groupBy = append(groupBy, "d"+fmt.Sprint(i))
		}
		q.Columns(cols...).GroupBy(append(groupBy, "bk")...)
	} else {
		for i, k := range keys {
			cols = append(cols, "any("+k+") AS d"+fmt.Sprint(i))
		}
		cols = append(cols, "series_id", "any(host_name) AS hn", b+" AS bk",
			"count() AS cnt", "sum(value) AS sv", "min(value) AS mn", "max(value) AS mx",
			"argMin(value, timestamp) AS fv", "argMax(value, timestamp) AS lv",
			"toInt64(toUnixTimestamp64Nano(min(timestamp))) AS tf", "toInt64(toUnixTimestamp64Nano(max(timestamp))) AS tl",
			"arraySum(arrayMap(d -> greatest(d, 0), arrayDifference(arrayMap(p -> p.2, arraySort(p -> p.1, groupArray((timestamp, value))))))) AS inc",
			"toString(any(temporality)) = {delta_name:String} AS dl", "any(unit) AS u")
		q.Columns(cols...).Param("delta_name", "delta").GroupBy("series_id", "bk")
	}
	q.Where("metric_name = {metric:String}").Param("metric", c.Metric)
	tsRange(q, "timestamp", origin, end)
	applyFilters(q, c.Filters, telemetryCols, "")
	q.Limit(lim.MaxRows + 1)

	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	groups := map[string]*metricGroup{}
	unit := ""
	count := 0
	for rows.Next() {
		count++
		if count > lim.MaxRows {
			return nil, "", &LimitError{Msg: fmt.Sprintf("the condition matches more than %d time series buckets; add filters", lim.MaxRows)}
		}
		dvals := make([]string, len(ds))
		dest := make([]any, 0, len(ds)+14)
		for i := range dvals {
			dest = append(dest, &dvals[i])
		}
		var (
			hostName, u string
			bk          int64
			cnt         uint64
		)
		if isQuantile {
			var qv float64
			dest = append(dest, &hostName, &bk, &qv, &cnt, &u)
			if err := rows.Scan(dest...); err != nil {
				return nil, "", err
			}
			g := groupFor(groups, ds, dvals, hostName, n)
			if bk >= 0 && bk < int64(n) {
				g.qs[bk], g.qcnt[bk] = qv, cnt
			}
		} else {
			var (
				sid                    uint64
				sv, mn, mx, fv, lv, in float64
				tf, tl                 int64
				dl                     uint8
			)
			dest = append(dest, &sid, &hostName, &bk, &cnt, &sv, &mn, &mx, &fv, &lv, &tf, &tl, &in, &dl, &u)
			if err := rows.Scan(dest...); err != nil {
				return nil, "", err
			}
			g := groupFor(groups, ds, dvals, hostName, n)
			bs, ok := g.series[sid]
			if !ok {
				bs = make([]bucketStats, n)
				g.series[sid] = bs
			}
			if bk >= 0 && bk < int64(n) {
				bs[bk] = bucketStats{Count: cnt, Sum: sv, Min: mn, Max: mx, First: fv, Last: lv, TFirst: tf, TLast: tl, Inc: in, Delta: dl != 0}
			}
		}
		if unit == "" {
			unit = u
		}
	}
	return groups, unit, rows.Err()
}

func groupFor(groups map[string]*metricGroup, ds []dim, vals []string, hostName string, n int) *metricGroup {
	key := seriesKey(ds, vals)
	g, ok := groups[key]
	if !ok {
		g = &metricGroup{key: key, labels: groupLabels(ds, vals, hostName), series: map[uint64][]bucketStats{},
			qs: nanSlice(n), qcnt: make([]uint64, n)}
		groups[key] = g
	}
	if hostName != "" && hasHostDim(ds) && g.labels["host.name"] == "" {
		g.labels["host.name"] = hostName
	}
	return g
}

// valueAt computes the group value over buckets [lo, hi).
func (c MetricCondition) valueAt(g *metricGroup, lo, hi int, windowSec float64) (float64, bool) {
	if q, ok := quantileOf(c.Aggregation); ok {
		return combineQuantiles(g.qs[lo:hi], g.qcnt[lo:hi], q)
	}
	vals := make([]float64, 0, len(g.series))
	for _, bs := range g.series {
		if v, ok := windowValue(bs[lo:hi], c.Aggregation, windowSec); ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
			vals = append(vals, v)
		}
	}
	return seriesAggregate(vals, c.SeriesAggregation)
}

func (c MetricCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error) {
	lim = lim.withDefaults()
	w := c.Window()
	groups, unit, err := c.fetch(ctx, sc, end.Add(-w), w, 1, lim)
	if err != nil {
		return nil, err
	}
	if len(groups) > lim.MaxSeries {
		return nil, &LimitError{Msg: fmt.Sprintf("too many series (%d > %d); add filters or fewer group_by dimensions", len(groups), lim.MaxSeries)}
	}
	res := &EvalResult{Unit: unit}
	for _, g := range groups {
		if v, ok := c.valueAt(g, 0, 1, w.Seconds()); ok {
			res.Samples = append(res.Samples, Sample{Key: g.key, Labels: g.labels, Value: v})
		}
	}
	sort.Slice(res.Samples, func(i, j int) bool { return res.Samples[i].Key < res.Samples[j].Key })
	return res, nil
}

func (c MetricCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error) {
	lim = lim.withDefaults()
	ends := rangeEnds(from, to, step)
	w := c.Window()
	k := int(ceilDiv(int64(w), int64(step)))
	origin := from.Add(-time.Duration(k) * step)
	n := k + len(ends)
	groups, unit, err := c.fetch(ctx, sc, origin, step, n, lim)
	if err != nil {
		return nil, err
	}
	_, isQ := quantileOf(c.Aggregation)
	res := &RangeResult{Ends: ends, Unit: unit, Approximate: int64(w)%int64(step) != 0 || (isQ && k > 1)}
	effWindow := (time.Duration(k) * step).Seconds()
	for _, g := range groups {
		rs := RangeSeries{Key: g.key, Labels: g.labels, Values: nanSlice(len(ends))}
		for i := range ends {
			// End i (1-based) = origin + (k+i+1)·step → buckets [i+1, i+1+k).
			if v, ok := c.valueAt(g, i+1, i+1+k, effWindow); ok {
				rs.Values[i] = v
			}
		}
		res.Series = append(res.Series, rs)
	}
	sortSeries(res.Series)
	if len(res.Series) > lim.MaxSeries {
		res.Series, res.Truncated = res.Series[:lim.MaxSeries], true
	}
	return res, nil
}
