package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

type apmNoDataType struct{}

func (apmNoDataType) Name() string              { return TypeAPMNoData }
func (apmNoDataType) Available() bool           { return true }
func (apmNoDataType) UnavailableReason() string { return "" }
func (apmNoDataType) DefaultInterval() int      { return 60 }

// APMNoDataCondition is an apm_no_data condition (alerting.md §2.7): a service stopped reporting transactions.
type APMNoDataCondition struct {
	// ServiceName "" = every service (one series per service).
	ServiceName      string   `json:"service_name"`
	ServiceNamespace *string  `json:"service_namespace"`
	Environment      *string  `json:"environment"`
	GroupBy          []string `json:"group_by"`
	WindowSeconds    int      `json:"window_seconds"`
	LookbackSeconds  int      `json:"lookback_seconds"`
}

func (apmNoDataType) Parse(raw json.RawMessage) (Condition, error) {
	var c APMNoDataCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	if len(c.ServiceName) > 512 {
		return nil, invalid("service_name", "at most 512 bytes")
	}
	for f, v := range map[string]*string{"service_namespace": c.ServiceNamespace, "environment": c.Environment} {
		if v != nil && len(*v) > 512 {
			return nil, invalid(f, "at most 512 bytes")
		}
	}
	seen := map[string]bool{}
	for i, g := range c.GroupBy {
		if g != "namespace" && g != "environment" {
			return nil, invalid(fmt.Sprintf("group_by[%d]", i), "must be namespace or environment")
		}
		seen[g] = true
	}
	// Stable order: the series key does not depend on the order in the input.
	c.GroupBy = []string{}
	for _, g := range []string{"namespace", "environment"} {
		if seen[g] {
			c.GroupBy = append(c.GroupBy, g)
		}
	}
	if err := validateWindow("window_seconds", &c.WindowSeconds, 600, 60, 86400); err != nil {
		return nil, err
	}
	c.WindowSeconds = int(ceilDiv(int64(c.WindowSeconds), 60) * 60) // the rollup has whole minutes
	if c.LookbackSeconds == 0 {
		c.LookbackSeconds = 86400
	}
	c.LookbackSeconds = int(ceilDiv(int64(c.LookbackSeconds), 60) * 60)
	if c.LookbackSeconds < 600 || c.LookbackSeconds > 604800 || c.LookbackSeconds <= c.WindowSeconds {
		return nil, invalid("lookback_seconds", "must be between 600 and 604800 and greater than window_seconds")
	}
	return c, nil
}

func (c APMNoDataCondition) Judge() Judge {
	w := float64(c.WindowSeconds)
	return Judge{Operator: "gte", Threshold: w, Recovery: w}
}
func (c APMNoDataCondition) Window() time.Duration {
	return time.Duration(c.WindowSeconds) * time.Second
}
func (c APMNoDataCondition) IgnoresFor() bool { return false }
func (c APMNoDataCondition) Missing() string  { return "expire" }

func (c APMNoDataCondition) Summary(s Sample, _ string) string {
	target := "service " + s.Labels["service.name"]
	if ns := s.Labels["service.namespace"]; ns != "" {
		target += " (namespace " + ns + ")"
	}
	if env := s.Labels["environment"]; env != "" {
		target += " (" + env + ")"
	}
	return fmt.Sprintf("%s reported no transactions for %s (limit %s)", target,
		humanDuration(time.Duration(s.Value)*time.Second), humanDuration(c.Window()))
}

// fetch returns, per series, the end of the last minute with transactions in each bucket of width step (whole
// minutes) starting at origin.
func (c APMNoDataCondition) fetch(ctx context.Context, sc *query.Scope, origin time.Time, step time.Duration, n int, lim Limits) (map[string]*lastSeenGroup, error) {
	q := sc.From(query.ApmTransactions1m)
	byNS, byEnv := contains(c.GroupBy, "namespace"), contains(c.GroupBy, "environment")
	cols := []string{"toString(service_name) AS d_svc"}
	groupBy := []string{"d_svc"}
	if byNS {
		cols, groupBy = append(cols, "toString(service_namespace) AS d_ns"), append(groupBy, "d_ns")
	}
	if byEnv {
		cols, groupBy = append(cols, "toString(deployment_environment) AS d_env"), append(groupBy, "d_env")
	}
	cols = append(cols,
		"intDiv(toInt64(toUnixTimestamp(timestamp)) - {b_origin_s:Int64}, {b_step_s:Int64}) AS bk",
		// A row of minute m means transactions up to m + 1 minute.
		"(toInt64(toUnixTimestamp(max(timestamp))) + 60) * 1000 AS ls")
	q.Columns(cols...).Param("b_origin_s", origin.Unix()).Param("b_step_s", int64(step/time.Second)).GroupBy(append(groupBy, "bk")...)
	if c.ServiceName != "" {
		q.Where("service_name = {svc:String}").Param("svc", c.ServiceName)
	}
	if c.ServiceNamespace != nil {
		q.Where("service_namespace = {svc_ns:String}").Param("svc_ns", *c.ServiceNamespace)
	}
	if c.Environment != nil {
		q.Where("deployment_environment = {svc_env:String}").Param("svc_env", *c.Environment)
	}
	q.Where("timestamp >= toDateTime({ts_start:Int64}) AND timestamp < toDateTime({ts_end:Int64})").
		Param("ts_start", origin.Unix()).Param("ts_end", origin.Add(time.Duration(n)*step).Unix())
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
			return nil, &LimitError{Msg: fmt.Sprintf("the condition matches more than %d service buckets; name a service", lim.MaxRows)}
		}
		var (
			svc, ns, env string
			bk, ls       int64
		)
		dest := []any{&svc}
		if byNS {
			dest = append(dest, &ns)
		}
		if byEnv {
			dest = append(dest, &env)
		}
		dest = append(dest, &bk, &ls)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		key, labels := c.series(svc, ns, env)
		g, ok := groups[key]
		if !ok {
			g = &lastSeenGroup{key: key, labels: labels, last: make([]int64, n)}
			groups[key] = g
		}
		if bk >= 0 && bk < int64(n) && ls > g.last[bk] {
			g.last[bk] = ls
		}
	}
	return groups, rows.Err()
}

// series returns the key and labels of a series. Fixed namespace/environment filters appear as labels too.
func (c APMNoDataCondition) series(svc, ns, env string) (string, map[string]string) {
	labels := map[string]string{"service.name": svc}
	ds := []dim{{label: "service.name"}}
	vals := []string{svc}
	if contains(c.GroupBy, "namespace") {
		labels["service.namespace"] = ns
		ds, vals = append(ds, dim{label: "service.namespace"}), append(vals, ns)
	} else if c.ServiceNamespace != nil {
		labels["service.namespace"] = *c.ServiceNamespace
	}
	if contains(c.GroupBy, "environment") {
		labels["environment"] = env
		ds, vals = append(ds, dim{label: "environment"}), append(vals, env)
	} else if c.Environment != nil {
		labels["environment"] = *c.Environment
	}
	return seriesKey(ds, vals), labels
}

// Evaluate uses complete minutes: the window ends at the last whole minute before end (apm.md §10).
func (c APMNoDataCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error) {
	lim = lim.withDefaults()
	endM := end.Truncate(time.Minute)
	lb := time.Duration(c.LookbackSeconds) * time.Second
	groups, err := c.fetch(ctx, sc, endM.Add(-lb), lb, 1, lim)
	if err != nil {
		return nil, err
	}
	return lastSeenEval(groups, endM, lim)
}

func (c APMNoDataCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error) {
	lim = lim.withDefaults()
	stepM := time.Duration(ceilDiv(int64(step), int64(time.Minute))) * time.Minute
	fromM := from.Truncate(time.Minute)
	ends := rangeEnds(fromM, to.Truncate(time.Minute), stepM)
	lb := time.Duration(c.LookbackSeconds) * time.Second
	l := int(ceilDiv(int64(lb), int64(stepM)))
	origin := fromM.Add(-time.Duration(l) * stepM)
	groups, err := c.fetch(ctx, sc, origin, stepM, l+len(ends), lim)
	if err != nil {
		return nil, err
	}
	res := lastSeenRange(groups, ends, l, lim)
	res.Approximate = stepM != step || int64(lb)%int64(stepM) != 0
	return res, nil
}
