package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
)

// apm_error rules (alerting.md §2.9, apm.md §3.4): a new error group appeared, or a resolved group regressed.

type apmErrorType struct{}

func (apmErrorType) Name() string              { return TypeAPMError }
func (apmErrorType) Available() bool           { return true }
func (apmErrorType) UnavailableReason() string { return "" }
func (apmErrorType) DefaultInterval() int      { return 60 }

// APMErrorCondition is an apm_error condition.
type APMErrorCondition struct {
	Event            string  `json:"event"`
	ServiceName      string  `json:"service_name"`
	ServiceNamespace *string `json:"service_namespace"`
	Environment      *string `json:"environment"`
	Match            string  `json:"match"`
	WindowSeconds    int     `json:"window_seconds"`
	MinCount         float64 `json:"min_count"`
}

const (
	APMErrorNewGroup  = "new_group"
	APMErrorRegressed = "regressed"
)

func (apmErrorType) Parse(raw json.RawMessage) (Condition, error) {
	var c APMErrorCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	switch c.Event {
	case APMErrorNewGroup, APMErrorRegressed:
	default:
		return nil, invalid("event", "must be new_group or regressed")
	}
	if len(c.ServiceName) > 512 {
		return nil, invalid("service_name", "at most 512 bytes")
	}
	for f, v := range map[string]*string{"service_namespace": c.ServiceNamespace, "environment": c.Environment} {
		if v != nil && len(*v) > 512 {
			return nil, invalid(f, "at most 512 bytes")
		}
	}
	if len(c.Match) > maxValueBytes {
		return nil, invalid("match", "at most %d bytes", maxValueBytes)
	}
	if err := validateWindow("window_seconds", &c.WindowSeconds, 300, 60, 86400); err != nil {
		return nil, err
	}
	c.WindowSeconds = int(ceilDiv(int64(c.WindowSeconds), 60) * 60)
	if c.MinCount < 0 || !finite(c.MinCount) {
		return nil, invalid("min_count", "must be >= 0")
	}
	if c.Event == APMErrorRegressed && c.MinCount != 0 {
		return nil, invalid("min_count", "only applies to new_group")
	}
	return c, nil
}

func (c APMErrorCondition) Judge() Judge { return Judge{Operator: "gte", Threshold: 1, Recovery: 1} }
func (c APMErrorCondition) Window() time.Duration {
	return time.Duration(c.WindowSeconds) * time.Second
}
func (c APMErrorCondition) IgnoresFor() bool { return true }
func (c APMErrorCondition) Missing() string  { return "expire" }

func (c APMErrorCondition) Summary(s Sample, _ string) string {
	what := "new error group"
	if c.Event == APMErrorRegressed {
		what = "error group regressed"
	}
	svc := s.Labels["service.name"]
	if env := s.Labels["environment"]; env != "" {
		svc += " (" + env + ")"
	}
	return fmt.Sprintf("%s in %s: %s: %s", what, svc, s.Labels["error.type"], s.Labels["error.message"])
}

// ErrorWorkflow gives apm_error conditions the organization's error group states.
type ErrorWorkflow struct {
	OrgID string
	Store apm.ErrorStateStore
}

type errorWorkflowKey struct{}

// WithErrorWorkflow makes wf available to apm_error conditions evaluated with ctx.
func WithErrorWorkflow(ctx context.Context, wf ErrorWorkflow) context.Context {
	return context.WithValue(ctx, errorWorkflowKey{}, wf)
}

func errorWorkflowFrom(ctx context.Context) (ErrorWorkflow, bool) {
	wf, ok := ctx.Value(errorWorkflowKey{}).(ErrorWorkflow)
	return wf, ok && wf.Store != nil && wf.OrgID != ""
}

func (c APMErrorCondition) scope(q *query.Select) {
	if c.ServiceName != "" {
		q.Where("service_name = {ae_service:String}").Param("ae_service", c.ServiceName)
	}
	if c.ServiceNamespace != nil {
		q.Where("service_namespace = {ae_ns:String}").Param("ae_ns", *c.ServiceNamespace)
	}
	if c.Environment != nil {
		q.Where("deployment_environment = {ae_env:String}").Param("ae_env", *c.Environment)
	}
}

func (c APMErrorCondition) stateFilter() apm.ErrorStateFilter {
	return apm.ErrorStateFilter{Service: c.ServiceName, Namespace: c.ServiceNamespace, Environment: c.Environment, Limit: 2000}
}

type errGroup struct {
	key       apm.ServiceKey
	first     time.Time
	typ, msg  string
	windowSum float64
}

func (c APMErrorCondition) matches(g errGroup) bool {
	if c.Match == "" {
		return true
	}
	m := strings.ToLower(c.Match)
	return strings.Contains(strings.ToLower(g.typ), m) || strings.Contains(strings.ToLower(g.msg), m)
}

func (c APMErrorCondition) labels(id uint64, g errGroup) (string, map[string]string) {
	gid := apm.GroupIDString(id)
	return "error.group_id=" + gid, map[string]string{"service.name": g.key.Name, "service.namespace": g.key.Namespace,
		"environment": g.key.Environment, "error.group_id": gid, "error.type": g.typ, "error.message": g.msg}
}

// groups reads apm_error_groups for groups seen since since (ids nil) or for ids.
func (c APMErrorCondition) groups(ctx context.Context, sc *query.Scope, since time.Time, ids []uint64, lim Limits) (map[uint64]errGroup, error) {
	q := sc.From(query.ApmErrorGroups).Columns("service_name", "service_namespace", "deployment_environment", "error_group_id",
		"min(first_seen) AS m_first", "anyLast(error_type) AS m_type", "anyLast(error_message) AS m_msg").
		GroupBy("service_name", "service_namespace", "deployment_environment", "error_group_id").Limit(lim.MaxRows + 1)
	c.scope(q)
	if ids != nil {
		strs := make([]string, len(ids))
		for i, id := range ids {
			strs[i] = strconv.FormatUint(id, 10)
		}
		q.Where("has({ae_ids:Array(String)}, toString(error_group_id))").Param("ae_ids", strs)
	} else {
		q.Where("last_seen >= fromUnixTimestamp64Nano({ae_since:Int64})").Param("ae_since", since.UnixNano())
	}
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]errGroup{}
	for rows.Next() {
		if len(out) >= lim.MaxRows {
			return nil, &LimitError{Msg: fmt.Sprintf("the condition matches more than %d error groups; set service_name or match", lim.MaxRows)}
		}
		var id uint64
		var g errGroup
		if err := rows.Scan(&g.key.Name, &g.key.Namespace, &g.key.Environment, &id, &g.first, &g.typ, &g.msg); err != nil {
			return nil, err
		}
		out[id] = g
	}
	return out, rows.Err()
}

// ignored returns the ids of ignored groups among ids (none without the workflow).
func ignored(ctx context.Context, ids []uint64) (map[uint64]bool, error) {
	out := map[uint64]bool{}
	wf, ok := errorWorkflowFrom(ctx)
	if !ok || len(ids) == 0 {
		return out, nil
	}
	states, err := wf.Store.States(ctx, wf.OrgID, apm.ErrorStateFilter{GroupIDs: ids, Statuses: []apm.ErrorStatus{apm.StatusIgnored}})
	if err != nil {
		return nil, err
	}
	for _, st := range states {
		out[st.GroupID] = true
	}
	return out, nil
}

func (c APMErrorCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error) {
	lim = lim.withDefaults()
	start := end.Add(-c.Window())
	res := &EvalResult{}
	if c.Event == APMErrorRegressed {
		return c.evalRegressed(ctx, sc, start, end, lim)
	}
	groups, err := c.groups(ctx, sc, start, nil, lim)
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for id, g := range groups {
		if g.first.Before(start) || !g.first.Before(end) || !c.matches(g) {
			delete(groups, id)
			continue
		}
		ids = append(ids, id)
	}
	skip, err := ignored(ctx, ids)
	if err != nil {
		return nil, err
	}
	if c.MinCount > 0 && len(ids) > 0 {
		strs := make([]string, len(ids))
		for i, id := range ids {
			strs[i] = strconv.FormatUint(id, 10)
		}
		q := sc.From(query.ApmErrors1m).Columns("error_group_id", "sum(count) AS m_count").
			Where("timestamp >= toDateTime({ae_from:Int64}, 'UTC') AND timestamp < toDateTime({ae_to:Int64}, 'UTC')").
			Param("ae_from", start.Truncate(time.Minute).Unix()).Param("ae_to", end.Unix()).
			Where("has({ae_ids:Array(String)}, toString(error_group_id))").Param("ae_ids", strs).GroupBy("error_group_id")
		c.scope(q)
		rows, err := sc.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id uint64
			var n float64
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, err
			}
			if g, ok := groups[id]; ok {
				g.windowSum = n
				groups[id] = g
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for id, g := range groups {
		if skip[id] || g.windowSum < c.MinCount {
			continue
		}
		key, labels := c.labels(id, g)
		res.Samples = append(res.Samples, Sample{Key: key, Labels: labels, Value: 1})
	}
	if len(res.Samples) > lim.MaxSeries {
		return nil, &LimitError{Msg: fmt.Sprintf("more than %d new error groups; set service_name or match", lim.MaxSeries)}
	}
	sortSamples(res.Samples)
	return res, nil
}

// ErrErrorWorkflowUnavailable is returned by regressed conditions evaluated without the error workflow.
var ErrErrorWorkflowUnavailable = errors.New("the APM error workflow (PostgreSQL) is not available")

func (c APMErrorCondition) evalRegressed(ctx context.Context, sc *query.Scope, start, end time.Time, lim Limits) (*EvalResult, error) {
	wf, ok := errorWorkflowFrom(ctx)
	if !ok {
		return nil, ErrErrorWorkflowUnavailable
	}
	sf := c.stateFilter()
	sf.Statuses = []apm.ErrorStatus{apm.StatusResolved}
	resolved, err := wf.Store.States(ctx, wf.OrgID, sf)
	if err != nil {
		return nil, err
	}
	if _, _, err := apm.CheckRegressions(ctx, sc, wf.Store, wf.OrgID, resolved); err != nil {
		return nil, err
	}
	sf = c.stateFilter()
	sf.RegressedSince = &start
	recent, err := wf.Store.States(ctx, wf.OrgID, sf)
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for _, st := range recent {
		if st.RegressedAt != nil && st.RegressedAt.Before(end) {
			ids = append(ids, st.GroupID)
		}
	}
	res := &EvalResult{}
	if len(ids) == 0 {
		return res, nil
	}
	groups, err := c.groups(ctx, sc, time.Time{}, ids, lim)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		g, ok := groups[id]
		if !ok || !c.matches(g) {
			continue
		}
		key, labels := c.labels(id, g)
		res.Samples = append(res.Samples, Sample{Key: key, Labels: labels, Value: 1})
	}
	sortSamples(res.Samples)
	return res, nil
}

func sortSamples(s []Sample) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Key < s[j-1].Key; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Range evaluates the event per step end. new_group uses first_seen (min_count is not applied); regressed uses the
// stored regressed_at (no detection). Both are approximate.
func (c APMErrorCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error) {
	lim = lim.withDefaults()
	ends := rangeEnds(from, to, step)
	res := &RangeResult{Ends: ends, Approximate: true}
	w := c.Window()
	type event struct {
		at time.Time
		g  errGroup
	}
	events := map[uint64]event{}
	if c.Event == APMErrorRegressed {
		wf, ok := errorWorkflowFrom(ctx)
		if !ok {
			return nil, ErrErrorWorkflowUnavailable
		}
		sf := c.stateFilter()
		since := from.Add(-w)
		sf.RegressedSince = &since
		states, err := wf.Store.States(ctx, wf.OrgID, sf)
		if err != nil {
			return nil, err
		}
		var ids []uint64
		for _, st := range states {
			ids = append(ids, st.GroupID)
		}
		if len(ids) > 0 {
			groups, err := c.groups(ctx, sc, time.Time{}, ids, lim)
			if err != nil {
				return nil, err
			}
			for _, st := range states {
				if g, ok := groups[st.GroupID]; ok && st.RegressedAt != nil && c.matches(g) {
					events[st.GroupID] = event{at: *st.RegressedAt, g: g}
				}
			}
		}
	} else {
		groups, err := c.groups(ctx, sc, from.Add(-w), nil, lim)
		if err != nil {
			return nil, err
		}
		var ids []uint64
		for id, g := range groups {
			if !g.first.Before(from.Add(-w)) && !g.first.After(to) && c.matches(g) {
				events[id] = event{at: g.first, g: g}
				ids = append(ids, id)
			}
		}
		skip, err := ignored(ctx, ids)
		if err != nil {
			return nil, err
		}
		for id := range skip {
			delete(events, id)
		}
	}
	for id, ev := range events {
		key, labels := c.labels(id, ev.g)
		rs := RangeSeries{Key: key, Labels: labels, Values: nanSlice(len(ends))}
		any := false
		for i, end := range ends {
			if !ev.at.Before(end.Add(-w)) && ev.at.Before(end) {
				rs.Values[i] = 1
				any = true
			} else if ev.at.Before(end) {
				rs.Values[i] = 0
			} else {
				rs.Values[i] = math.NaN()
			}
		}
		if any {
			res.Series = append(res.Series, rs)
		}
	}
	sortSeries(res.Series)
	if len(res.Series) > lim.MaxSeries {
		res.Series, res.Truncated = res.Series[:lim.MaxSeries], true
	}
	return res, nil
}
