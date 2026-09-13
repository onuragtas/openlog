package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

type noDataType struct{}

func (noDataType) Name() string              { return TypeNoData }
func (noDataType) Available() bool           { return true }
func (noDataType) UnavailableReason() string { return "" }
func (noDataType) DefaultInterval() int      { return 60 }

// NoDataCondition is a no_data condition (§2.4).
type NoDataCondition struct {
	Signal          string   `json:"signal"`
	Metric          string   `json:"metric"`
	Filters         []Filter `json:"filters"`
	GroupBy         []string `json:"group_by"`
	WindowSeconds   int      `json:"window_seconds"`
	LookbackSeconds int      `json:"lookback_seconds"`
}

func (noDataType) Parse(raw json.RawMessage) (Condition, error) {
	var c NoDataCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	cols := telemetryCols
	switch c.Signal {
	case "host":
		cols = hostTableCols
		if c.Metric != "" {
			return nil, invalid("metric", "only allowed with signal metric")
		}
	case "metric":
		if c.Metric == "" || len(c.Metric) > maxMetricBytes {
			return nil, invalid("metric", "required for signal metric, at most %d bytes", maxMetricBytes)
		}
	case "log":
		if c.Metric != "" {
			return nil, invalid("metric", "only allowed with signal metric")
		}
	default:
		return nil, invalid("signal", "must be host, metric or log")
	}
	if err := validateWindow("window_seconds", &c.WindowSeconds, 300, 60, 86400); err != nil {
		return nil, err
	}
	if c.LookbackSeconds == 0 {
		c.LookbackSeconds = 86400
	}
	if c.LookbackSeconds < 600 || c.LookbackSeconds > 604800 || c.LookbackSeconds <= c.WindowSeconds {
		return nil, invalid("lookback_seconds", "must be between 600 and 604800 and greater than window_seconds")
	}
	var err error
	if c.Filters, err = validateFilters(c.Filters, cols); err != nil {
		return nil, err
	}
	allowed := map[string]bool{"host": true, "service": c.Signal != "host"}
	if len(c.GroupBy) == 0 {
		return nil, invalid("group_by", "required (host or service)")
	}
	if c.GroupBy, err = validateGroupBy(c.GroupBy, cols, allowed); err != nil {
		return nil, err
	}
	return c, nil
}

func (c NoDataCondition) Judge() Judge {
	w := float64(c.WindowSeconds)
	return Judge{Operator: "gte", Threshold: w, Recovery: w}
}
func (c NoDataCondition) Window() time.Duration { return time.Duration(c.WindowSeconds) * time.Second }
func (c NoDataCondition) IgnoresFor() bool      { return false }
func (c NoDataCondition) Missing() string       { return "expire" }

func (c NoDataCondition) Summary(s Sample, _ string) string {
	what := map[string]string{"host": "no telemetry", "metric": "no " + c.Metric + " data", "log": "no logs"}[c.Signal]
	return fmt.Sprintf("%s for %s (limit %s)%s", what, humanDuration(time.Duration(s.Value)*time.Second),
		humanDuration(c.Window()), labelSuffix(s.Labels))
}

type lastSeenGroup struct {
	key    string
	labels map[string]string
	last   []int64 // max timestamp (unix ms) per bucket, 0 = none
}

func (c NoDataCondition) fetch(ctx context.Context, sc *query.Scope, origin time.Time, step time.Duration, n int, lim Limits) (map[string]*lastSeenGroup, error) {
	lim = lim.withDefaults()
	var (
		q      *query.Select
		cols   = telemetryCols
		tsCol  = "timestamp"
		hostNm = "any(host_name)"
	)
	switch c.Signal {
	case "host":
		q, cols, tsCol, hostNm = sc.From(query.Hosts), hostTableCols, "last_seen", "argMax(host_name, last_seen)"
	case "metric":
		q = sc.From(query.Metrics).Where("metric_name = {metric:String}").Param("metric", c.Metric)
	default:
		q = sc.From(query.Logs).Where("NOT startsWith(event_name, {inventory_prefix:String})").Param("inventory_prefix", "openlog.inventory.")
	}
	ds := buildDims(c.GroupBy, cols)
	keys := dimExprs(q, ds)
	b := bucketExpr(q, tsCol, origin, step)
	sel := make([]string, 0, len(keys)+3)
	groupBy := make([]string, 0, len(keys)+1)
	for i, k := range keys {
		sel = append(sel, k+" AS d"+strconv.Itoa(i))
		groupBy = append(groupBy, "d"+strconv.Itoa(i))
	}
	sel = append(sel, hostNm+" AS hn", b+" AS bk", "toInt64(toUnixTimestamp64Milli(max("+tsCol+"))) AS ls")
	q.Columns(sel...).GroupBy(append(groupBy, "bk")...)
	tsRange(q, tsCol, origin, origin.Add(time.Duration(n)*step))
	applyFilters(q, c.Filters, cols, "")
	q.Limit(lim.MaxRows + 1)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string]*lastSeenGroup{}
	count := 0
	for rows.Next() {
		count++
		if count > lim.MaxRows {
			return nil, &LimitError{Msg: fmt.Sprintf("the condition matches more than %d group buckets; add filters", lim.MaxRows)}
		}
		dvals := make([]string, len(ds))
		dest := make([]any, 0, len(ds)+3)
		for i := range dvals {
			dest = append(dest, &dvals[i])
		}
		var (
			hn     string
			bk, ls int64
		)
		dest = append(dest, &hn, &bk, &ls)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		key := seriesKey(ds, dvals)
		g, ok := groups[key]
		if !ok {
			g = &lastSeenGroup{key: key, labels: groupLabels(ds, dvals, hn), last: make([]int64, n)}
			groups[key] = g
		}
		if bk >= 0 && bk < int64(n) && ls > g.last[bk] {
			g.last[bk] = ls
		}
	}
	return groups, rows.Err()
}

func ageSeconds(end time.Time, lastMs int64) float64 {
	return math.Max(0, float64(end.UnixMilli()-lastMs)/1000)
}

func (c NoDataCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error) {
	lim = lim.withDefaults()
	lb := time.Duration(c.LookbackSeconds) * time.Second
	groups, err := c.fetch(ctx, sc, end.Add(-lb), lb, 1, lim)
	if err != nil {
		return nil, err
	}
	return lastSeenEval(groups, end, lim)
}

func (c NoDataCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error) {
	lim = lim.withDefaults()
	ends := rangeEnds(from, to, step)
	lb := time.Duration(c.LookbackSeconds) * time.Second
	l := int(ceilDiv(int64(lb), int64(step)))
	origin := from.Add(-time.Duration(l) * step)
	groups, err := c.fetch(ctx, sc, origin, step, l+len(ends), lim)
	if err != nil {
		return nil, err
	}
	res := lastSeenRange(groups, ends, l, lim)
	res.Approximate = int64(lb)%int64(step) != 0
	return res, nil
}

// lastSeenEval turns the last data point of every group within the lookback (one bucket) into ages (no-data
// semantics, §2.4): groups without data in the lookback have no sample, so their series expire.
func lastSeenEval(groups map[string]*lastSeenGroup, end time.Time, lim Limits) (*EvalResult, error) {
	if len(groups) > lim.MaxSeries {
		return nil, &LimitError{Msg: fmt.Sprintf("too many series (%d > %d); add filters", len(groups), lim.MaxSeries)}
	}
	res := &EvalResult{Unit: "s"}
	for _, g := range groups {
		if g.last[0] > 0 {
			res.Samples = append(res.Samples, Sample{Key: g.key, Labels: g.labels, Value: ageSeconds(end, g.last[0])})
		}
	}
	sort.Slice(res.Samples, func(i, j int) bool { return res.Samples[i].Key < res.Samples[j].Key })
	return res, nil
}

// lastSeenRange computes ages at every end from per-bucket last data points; the first l buckets precede ends[0].
func lastSeenRange(groups map[string]*lastSeenGroup, ends []time.Time, l int, lim Limits) *RangeResult {
	res := &RangeResult{Ends: ends, Unit: "s"}
	for _, g := range groups {
		rs := RangeSeries{Key: g.key, Labels: g.labels, Values: nanSlice(len(ends))}
		for i, e := range ends {
			// End i = origin + (l+i+1)·step: data in buckets [i+1, l+i+1) is within the lookback.
			var last int64
			for j := i + 1; j < l+i+1; j++ {
				last = max(last, g.last[j])
			}
			if last > 0 {
				rs.Values[i] = ageSeconds(e, last)
			}
		}
		res.Series = append(res.Series, rs)
	}
	sortSeries(res.Series)
	if len(res.Series) > lim.MaxSeries {
		res.Series, res.Truncated = res.Series[:lim.MaxSeries], true
	}
	return res
}
