package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

type logType struct{}

func (logType) Name() string              { return TypeLogMatch }
func (logType) Available() bool           { return true }
func (logType) UnavailableReason() string { return "" }
func (logType) DefaultInterval() int      { return 60 }

// LogCondition is a log_match condition (§2.3).
type LogCondition struct {
	Query             string   `json:"query"`
	SeverityMin       string   `json:"severity_min"`
	Filters           []Filter `json:"filters"`
	GroupBy           []string `json:"group_by"`
	WindowSeconds     int      `json:"window_seconds"`
	Operator          string   `json:"operator"`
	Threshold         *float64 `json:"threshold"`
	RecoveryThreshold *float64 `json:"recovery_threshold"`

	severity int
	judge    Judge
}

var severityNames = map[string]int{"TRACE": 1, "DEBUG": 5, "INFO": 9, "WARN": 13, "WARNING": 13, "ERROR": 17, "FATAL": 21}

func (logType) Parse(raw json.RawMessage) (Condition, error) {
	var c LogCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	if len(c.Query) > maxValueBytes {
		return nil, invalid("query", "at most %d bytes", maxValueBytes)
	}
	if v := strings.TrimSpace(c.SeverityMin); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 24 {
			c.severity = n
		} else if n, ok := severityNames[strings.ToUpper(v)]; ok {
			c.severity = n
			v = strings.ToUpper(v)
		} else {
			return nil, invalid("severity_min", "must be a number 1-24 or a severity name (TRACE…FATAL)")
		}
		c.SeverityMin = v
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
	return c, nil
}

func (c LogCondition) Judge() Judge          { return c.judge }
func (c LogCondition) Window() time.Duration { return time.Duration(c.WindowSeconds) * time.Second }
func (c LogCondition) IgnoresFor() bool      { return false }
func (c LogCondition) Missing() string       { return "zero" }

func (c LogCondition) Summary(s Sample, _ string) string {
	what := "log records"
	if c.Query != "" {
		what = fmt.Sprintf("log records containing %q", c.Query)
	}
	if c.SeverityMin != "" {
		what += " at " + c.SeverityMin + " or above"
	}
	return fmt.Sprintf("%s %s in the last %s (%s %s)%s", formatValue(s.Value), what, humanDuration(c.Window()),
		opSymbol(c.Operator), formatValue(c.judge.Threshold), labelSuffix(s.Labels))
}

type logGroup struct {
	key    string
	labels map[string]string
	counts []uint64
}

func (c LogCondition) fetch(ctx context.Context, sc *query.Scope, origin time.Time, step time.Duration, n int, lim Limits) (map[string]*logGroup, error) {
	lim = lim.withDefaults()
	ds := buildDims(c.GroupBy, telemetryCols)
	q := sc.From(query.Logs)
	b := bucketExpr(q, "timestamp", origin, step)
	keys := dimExprs(q, ds)
	cols := make([]string, 0, len(keys)+3)
	groupBy := make([]string, 0, len(keys)+1)
	for i, k := range keys {
		cols = append(cols, k+" AS d"+strconv.Itoa(i))
		groupBy = append(groupBy, "d"+strconv.Itoa(i))
	}
	cols = append(cols, "any(host_name) AS hn", b+" AS bk", "count() AS cnt")
	q.Columns(cols...).GroupBy(append(groupBy, "bk")...)
	tsRange(q, "timestamp", origin, origin.Add(time.Duration(n)*step))
	q.Where("NOT startsWith(event_name, {inventory_prefix:String})").Param("inventory_prefix", "openlog.inventory.")
	if c.Query != "" {
		q.Where("positionCaseInsensitiveUTF8(body, {body_q:String}) > 0").Param("body_q", c.Query)
	}
	if c.severity > 0 {
		q.Where("severity_number >= {severity_min:UInt8}").Param("severity_min", c.severity)
	}
	applyFilters(q, c.Filters, telemetryCols, "")
	q.Limit(lim.MaxRows + 1)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string]*logGroup{}
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
			hn  string
			bk  int64
			cnt uint64
		)
		dest = append(dest, &hn, &bk, &cnt)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		key := seriesKey(ds, dvals)
		g, ok := groups[key]
		if !ok {
			g = &logGroup{key: key, labels: groupLabels(ds, dvals, hn), counts: make([]uint64, n)}
			groups[key] = g
		}
		if bk >= 0 && bk < int64(n) {
			g.counts[bk] += cnt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ds) == 0 && groups["*"] == nil {
		groups["*"] = &logGroup{key: "*", labels: map[string]string{}, counts: make([]uint64, n)}
	}
	return groups, nil
}

func (c LogCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error) {
	lim = lim.withDefaults()
	w := c.Window()
	groups, err := c.fetch(ctx, sc, end.Add(-w), w, 1, lim)
	if err != nil {
		return nil, err
	}
	if len(groups) > lim.MaxSeries {
		return nil, &LimitError{Msg: fmt.Sprintf("too many series (%d > %d); add filters or fewer group_by dimensions", len(groups), lim.MaxSeries)}
	}
	res := &EvalResult{Unit: "{records}"}
	for _, g := range groups {
		res.Samples = append(res.Samples, Sample{Key: g.key, Labels: g.labels, Value: float64(g.counts[0])})
	}
	sort.Slice(res.Samples, func(i, j int) bool { return res.Samples[i].Key < res.Samples[j].Key })
	return res, nil
}

func (c LogCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error) {
	lim = lim.withDefaults()
	ends := rangeEnds(from, to, step)
	w := c.Window()
	k := int(ceilDiv(int64(w), int64(step)))
	origin := from.Add(-time.Duration(k) * step)
	groups, err := c.fetch(ctx, sc, origin, step, k+len(ends), lim)
	if err != nil {
		return nil, err
	}
	res := &RangeResult{Ends: ends, Unit: "{records}", Approximate: int64(w)%int64(step) != 0}
	for _, g := range groups {
		rs := RangeSeries{Key: g.key, Labels: g.labels, Values: make([]float64, len(ends))}
		for i := range ends {
			var sum uint64
			for j := i + 1; j < i+1+k; j++ {
				sum += g.counts[j]
			}
			rs.Values[i] = float64(sum)
		}
		res.Series = append(res.Series, rs)
	}
	sortSeries(res.Series)
	if len(res.Series) > lim.MaxSeries {
		res.Series, res.Truncated = res.Series[:lim.MaxSeries], true
	}
	return res, nil
}
