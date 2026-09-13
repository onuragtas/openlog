package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
)

type apmType struct{}

func (apmType) Name() string              { return TypeAPM }
func (apmType) Available() bool           { return true }
func (apmType) UnavailableReason() string { return "" }
func (apmType) DefaultInterval() int      { return 60 }

// APM metrics (docs/contracts/apm.md §4).
var apmMetrics = map[string]bool{"throughput": true, "error_rate": true, "errors": true, "avg_ms": true,
	"p50_ms": true, "p95_ms": true, "p99_ms": true, "apdex": true}

// APMCondition is an apm condition (alerting.md §2.6, apm.md §10).
type APMCondition struct {
	ServiceName       string   `json:"service_name"`
	ServiceNamespace  *string  `json:"service_namespace"`
	Environment       *string  `json:"environment"`
	TransactionType   string   `json:"transaction_type"`
	TransactionName   string   `json:"transaction_name"`
	Metric            string   `json:"metric"`
	GroupBy           []string `json:"group_by"`
	WindowSeconds     int      `json:"window_seconds"`
	MinRequests       float64  `json:"min_requests"`
	Operator          string   `json:"operator"`
	Threshold         *float64 `json:"threshold"`
	RecoveryThreshold *float64 `json:"recovery_threshold"`
	MissingData       string   `json:"missing_data"`

	judge Judge
}

func (apmType) Parse(raw json.RawMessage) (Condition, error) {
	var c APMCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
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
	seen := map[string]bool{}
	gb := []string{}
	for i, g := range c.GroupBy {
		if g != "environment" && g != "transaction" {
			return nil, invalid(fmt.Sprintf("group_by[%d]", i), "must be environment or transaction")
		}
		if !seen[g] {
			seen[g] = true
			gb = append(gb, g)
		}
	}
	c.GroupBy = gb
	if err := validateWindow("window_seconds", &c.WindowSeconds, defaultWindow, 60, maxMetricWin); err != nil {
		return nil, err
	}
	c.WindowSeconds = int(ceilDiv(int64(c.WindowSeconds), 60) * 60) // whole minutes (1-minute rollup)
	if c.MinRequests < 0 || !finite(c.MinRequests) {
		return nil, invalid("min_requests", "must be >= 0")
	}
	var err error
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
	return c, nil
}

func (c APMCondition) Judge() Judge          { return c.judge }
func (c APMCondition) Window() time.Duration { return time.Duration(c.WindowSeconds) * time.Second }
func (c APMCondition) IgnoresFor() bool      { return false }
func (c APMCondition) Missing() string       { return c.MissingData }

func (c APMCondition) Summary(s Sample, _ string) string {
	target := "service " + c.ServiceName
	if n := s.Labels["transaction.name"]; n != "" {
		target += " transaction " + n
	} else if c.TransactionName != "" {
		target += " transaction " + c.TransactionName
	}
	if env := s.Labels["environment"]; env != "" {
		target += " (" + env + ")"
	}
	return fmt.Sprintf("%s %s over %s is %s (%s %s)", target, c.Metric, humanDuration(c.Window()), formatValue(s.Value),
		opSymbol(c.Operator), formatValue(c.judge.Threshold))
}

// ApdexTFunc returns the Apdex threshold in milliseconds of a service (apm.md §4).
type ApdexTFunc func(service, namespace, environment string) float64

type apdexKey struct{}

// WithApdexT makes fn available to APM conditions evaluated with ctx.
func WithApdexT(ctx context.Context, fn ApdexTFunc) context.Context {
	return context.WithValue(ctx, apdexKey{}, fn)
}

func apdexT(ctx context.Context) ApdexTFunc {
	if fn, ok := ctx.Value(apdexKey{}).(ApdexTFunc); ok && fn != nil {
		return fn
	}
	return func(string, string, string) float64 { return float64(apm.DefaultApdexT / time.Millisecond) }
}

// ApdexLookup builds an ApdexTFunc from the settings of one organization.
func ApdexLookup(settings []apm.Setting, def time.Duration) ApdexTFunc {
	return func(service, namespace, environment string) float64 {
		if s := apm.ResolveApdexT(settings, apm.ServiceKey{Name: service, Namespace: namespace, Environment: environment}); s != nil {
			return float64(s.ApdexTMs)
		}
		return float64(def / time.Millisecond)
	}
}

type apmBucket struct {
	req, err, dsum float64
	hist, ok       apm.Hist
}

type apmGroup struct {
	key       string
	labels    map[string]string
	namespace string
	env       string
	buckets   []apmBucket
}

func (c APMCondition) fetch(ctx context.Context, sc *query.Scope, origin time.Time, step time.Duration, n int, lim Limits) (map[string]*apmGroup, error) {
	lim = lim.withDefaults()
	q := sc.From(query.ApmTransactions1m)
	stepS := int64(step / time.Second)
	cols := []string{}
	groupBy := []string{}
	if contains(c.GroupBy, "environment") {
		cols = append(cols, "toString(deployment_environment) AS d_env")
		groupBy = append(groupBy, "d_env")
	}
	if contains(c.GroupBy, "transaction") {
		cols = append(cols, "toString(transaction_type) AS d_ttype", "toString(transaction_name) AS d_tname")
		groupBy = append(groupBy, "d_ttype", "d_tname")
	}
	cols = append(cols,
		"any(toString(service_namespace)) AS ns", "any(toString(deployment_environment)) AS envv",
		"intDiv(toInt64(toUnixTimestamp(timestamp)) - {b_origin_s:Int64}, {b_step_s:Int64}) AS bk",
		"sum(requests) AS req", "sum(errors) AS errs", "sum(duration_sum_ms) AS dsum",
		"tupleElement(sumMap(duration_hist), 1) AS hk", "tupleElement(sumMap(duration_hist), 2) AS hv",
		"tupleElement(sumMap(ok_hist), 1) AS okk", "tupleElement(sumMap(ok_hist), 2) AS okv")
	q.Columns(cols...).Param("b_origin_s", origin.Unix()).Param("b_step_s", stepS).GroupBy(append(groupBy, "bk")...)
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
	q.Where("timestamp >= toDateTime({ts_start:Int64}) AND timestamp < toDateTime({ts_end:Int64})").
		Param("ts_start", origin.Unix()).Param("ts_end", origin.Add(time.Duration(n)*step).Unix())
	q.Limit(lim.MaxRows + 1)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string]*apmGroup{}
	count := 0
	for rows.Next() {
		count++
		if count > lim.MaxRows {
			return nil, &LimitError{Msg: fmt.Sprintf("the condition matches more than %d transaction buckets", lim.MaxRows)}
		}
		var (
			dEnv, dType, dName, ns, env string
			bk                          int64
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
		dest = append(dest, &ns, &env, &bk, &req, &errs, &dsum, &hk, &hv, &okk, &okv)
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
		g, ok := groups[key]
		if !ok {
			g = &apmGroup{key: key, labels: labels, namespace: ns, env: env, buckets: make([]apmBucket, n)}
			groups[key] = g
		}
		if bk >= 0 && bk < int64(n) {
			b := &g.buckets[bk]
			b.req += req
			b.err += errs
			b.dsum += dsum
			b.hist = b.hist.Add(apm.NewHist(hk, hv))
			b.ok = b.ok.Add(apm.NewHist(okk, okv))
		}
	}
	return groups, rows.Err()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// value computes the metric over buckets [lo, hi) covering windowMin minutes. NaN = undefined.
func (c APMCondition) value(g *apmGroup, lo, hi int, windowMin float64, tMs float64) float64 {
	var agg apmBucket
	for _, b := range g.buckets[lo:hi] {
		agg.req += b.req
		agg.err += b.err
		agg.dsum += b.dsum
		agg.hist = agg.hist.Add(b.hist)
		agg.ok = agg.ok.Add(b.ok)
	}
	if agg.req <= 0 || agg.req < c.MinRequests {
		if c.Metric == "throughput" || c.Metric == "errors" {
			if c.MinRequests > 0 && agg.req < c.MinRequests {
				return math.NaN()
			}
			if c.Metric == "throughput" {
				return agg.req / windowMin
			}
			return agg.err
		}
		return math.NaN()
	}
	switch c.Metric {
	case "throughput":
		return agg.req / windowMin
	case "errors":
		return agg.err
	case "error_rate":
		return agg.err / agg.req
	case "avg_ms":
		return agg.dsum / agg.req
	case "p50_ms":
		return apm.HistQuantile(agg.hist, 0.5)
	case "p95_ms":
		return apm.HistQuantile(agg.hist, 0.95)
	case "p99_ms":
		return apm.HistQuantile(agg.hist, 0.99)
	case "apdex":
		if a, _, _, ok := apm.HistApdex(agg.req, agg.ok, tMs); ok {
			return a
		}
	}
	return math.NaN()
}

func (c APMCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error) {
	lim = lim.withDefaults()
	endM := end.Truncate(time.Minute) // complete minutes only (apm.md §10)
	w := c.Window()
	groups, err := c.fetch(ctx, sc, endM.Add(-w), w, 1, lim)
	if err != nil {
		return nil, err
	}
	if len(groups) > lim.MaxSeries {
		return nil, &LimitError{Msg: fmt.Sprintf("too many series (%d > %d)", len(groups), lim.MaxSeries)}
	}
	tFn := apdexT(ctx)
	res := &EvalResult{Unit: apmUnit(c.Metric)}
	for _, g := range groups {
		if v := c.value(g, 0, 1, w.Minutes(), tFn(c.ServiceName, g.namespace, g.env)); !math.IsNaN(v) {
			res.Samples = append(res.Samples, Sample{Key: g.key, Labels: g.labels, Value: v})
		}
	}
	sort.Slice(res.Samples, func(i, j int) bool { return res.Samples[i].Key < res.Samples[j].Key })
	return res, nil
}

func (c APMCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error) {
	lim = lim.withDefaults()
	stepM := time.Duration(ceilDiv(int64(step), int64(time.Minute))) * time.Minute
	fromM := from.Truncate(time.Minute)
	ends := rangeEnds(fromM, to.Truncate(time.Minute), stepM)
	w := c.Window()
	k := int(ceilDiv(int64(w), int64(stepM)))
	origin := fromM.Add(-time.Duration(k) * stepM)
	groups, err := c.fetch(ctx, sc, origin, stepM, k+len(ends), lim)
	if err != nil {
		return nil, err
	}
	tFn := apdexT(ctx)
	res := &RangeResult{Ends: ends, Unit: apmUnit(c.Metric), Approximate: stepM != step || int64(w)%int64(stepM) != 0}
	effMin := (time.Duration(k) * stepM).Minutes()
	for _, g := range groups {
		rs := RangeSeries{Key: g.key, Labels: g.labels, Values: nanSlice(len(ends))}
		t := tFn(c.ServiceName, g.namespace, g.env)
		for i := range ends {
			rs.Values[i] = c.value(g, i+1, i+1+k, effMin, t)
		}
		res.Series = append(res.Series, rs)
	}
	sortSeries(res.Series)
	if len(res.Series) > lim.MaxSeries {
		res.Series, res.Truncated = res.Series[:lim.MaxSeries], true
	}
	return res, nil
}

func apmUnit(metric string) string {
	switch metric {
	case "throughput":
		return "{requests}/min"
	case "error_rate", "apdex":
		return "1"
	case "errors":
		return "{errors}"
	default:
		return "ms"
	}
}
