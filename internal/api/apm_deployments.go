package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
)

// Deployments (apm.md §12): service.version changes detected from apm_service_versions_1m, and a before/after
// comparison of a deployment.

type deploymentJSON struct {
	Timestamp        string `json:"timestamp"`
	T                int64  `json:"t"`
	ServiceNamespace string `json:"service_namespace"`
	Environment      string `json:"environment"`
	Version          string `json:"version"`
	PreviousVersion  string `json:"previous_version"`
	Initial          bool   `json:"initial"`
	Rollback         bool   `json:"rollback"`
}

func parseBoundedDuration(r *http.Request, name string, def, lo, hi time.Duration) (time.Duration, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d > hi {
		return 0, badRequest("%s must be a duration between %s and %s", name, lo, hi)
	}
	return d, nil
}

func (s *Server) apmDeployments(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	gap, err := parseBoundedDuration(r, "gap", apm.DeploymentGap, 5*time.Minute, 24*time.Hour)
	if err != nil {
		return err
	}
	out, err := serviceDeployments(r.Context(), sc, f, from, to, gap)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": out, "gap_seconds": int(gap / time.Second)})
	return nil
}

func serviceDeployments(ctx context.Context, sc *query.Scope, f svcFilter, from, to time.Time, gap time.Duration) ([]deploymentJSON, error) {
	type nsEnv struct{ ns, env string }
	lookback := max(apm.DeploymentLookback, gap)
	rq := f.apply(minuteRange(sc.From(query.ApmServiceVersions1m).
		Columns("service_namespace", "deployment_environment", "timestamp", "service_version", "sum(span_count) AS m_spans"), from.Add(-lookback), to)).
		GroupBy("service_namespace", "deployment_environment", "timestamp", "service_version").OrderBy("timestamp").Limit(500000)
	rows, err := sc.Query(ctx, rq)
	if err != nil {
		return nil, err
	}
	minutes := map[nsEnv][]apm.VersionMinute{}
	for rows.Next() {
		var k nsEnv
		var vm apm.VersionMinute
		if err := rows.Scan(&k.ns, &k.env, &vm.Minute, &vm.Version, &vm.Spans); err != nil {
			rows.Close()
			return nil, err
		}
		minutes[k] = append(minutes[k], vm)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []deploymentJSON{}
	if len(minutes) == 0 {
		return out, nil
	}
	fq := f.apply(sc.From(query.ApmServiceVersions1m).Columns("service_namespace", "deployment_environment", "service_version", "min(first_seen) AS m_first")).
		GroupBy("service_namespace", "deployment_environment", "service_version").Limit(100000)
	frows, err := sc.Query(ctx, fq)
	if err != nil {
		return nil, err
	}
	first := map[nsEnv]map[string]time.Time{}
	for frows.Next() {
		var k nsEnv
		var version string
		var t time.Time
		if err := frows.Scan(&k.ns, &k.env, &version, &t); err != nil {
			frows.Close()
			return nil, err
		}
		if first[k] == nil {
			first[k] = map[string]time.Time{}
		}
		first[k][version] = t
	}
	frows.Close()
	if err := frows.Err(); err != nil {
		return nil, err
	}
	for k, rows := range minutes {
		for _, d := range apm.DetectDeployments(rows, first[k], from, gap) {
			out = append(out, deploymentJSON{Timestamp: formatTime(d.Time), T: d.Time.UnixMilli(), ServiceNamespace: k.ns, Environment: k.env,
				Version: d.Version, PreviousVersion: d.PreviousVersion, Initial: d.Initial, Rollback: d.Rollback})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].T != out[j].T {
			return out[i].T < out[j].T
		}
		return out[i].ServiceNamespace+"|"+out[i].Environment < out[j].ServiceNamespace+"|"+out[j].Environment
	})
	return out, nil
}

// txTotals aggregates apm_transactions_1m of a service over [from, to).
func txTotals(ctx context.Context, sc *query.Scope, f svcFilter, from, to time.Time) (redAgg, error) {
	q := f.apply(minuteRange(sc.From(query.ApmTransactions1m).Columns(txAggColumns...), from, to))
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return redAgg{}, err
	}
	defer rows.Close()
	var a redAgg
	if rows.Next() {
		dest, build := txAggDest()
		if err := rows.Scan(dest...); err != nil {
			return redAgg{}, err
		}
		a = build()
	}
	return a, rows.Err()
}

type periodJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
	redJSON
}

type newErrorGroupJSON struct {
	GroupID    string  `json:"group_id"`
	ErrorType  string  `json:"error_type"`
	Message    string  `json:"message"`
	FirstSeen  string  `json:"first_seen"`
	TotalCount float64 `json:"total_count"`
}

func (s *Server) apmDeploymentCompare(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	v := r.URL.Query().Get("at")
	if v == "" {
		return badRequest("at is required")
	}
	at, err := parseTime(v)
	if err != nil {
		return badRequest("at: %v", err)
	}
	window, err := parseBoundedDuration(r, "window", 30*time.Minute, 5*time.Minute, 24*time.Hour)
	if err != nil {
		return err
	}
	at = at.Truncate(time.Minute)
	now := s.now().UTC()
	if !at.Before(now) {
		return badRequest("at must be in the past")
	}
	afterEnd := at.Add(window)
	if afterEnd.After(now) {
		afterEnd = now
	}
	beforeFrom := at.Add(-window)
	ctx := r.Context()
	tMs, _ := s.apdexTMs(s.apmSettings(r), f.key())
	before, err := txTotals(ctx, sc, f, beforeFrom, at)
	if err != nil {
		return err
	}
	after, err := txTotals(ctx, sc, f, at, afterEnd)
	if err != nil {
		return err
	}
	gq := f.apply(sc.From(query.ApmErrorGroups).Columns("error_group_id", "min(first_seen) AS m_first", "sum(count) AS m_count",
		"anyLast(error_type) AS m_type", "anyLast(error_message) AS m_msg")).
		Where("last_seen >= fromUnixTimestamp64Nano({t_seen:Int64})").Param("t_seen", at.UnixNano()).
		GroupBy("error_group_id").Limit(5000)
	rows, err := sc.Query(ctx, gq)
	if err != nil {
		return err
	}
	defer rows.Close()
	groups := []newErrorGroupJSON{}
	for rows.Next() {
		var id uint64
		var first time.Time
		var g newErrorGroupJSON
		if err := rows.Scan(&id, &first, &g.TotalCount, &g.ErrorType, &g.Message); err != nil {
			return err
		}
		if first.Before(at) || !first.Before(afterEnd) {
			continue
		}
		g.GroupID, g.FirstSeen = apm.GroupIDString(id), formatTime(first)
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].TotalCount != groups[j].TotalCount {
			return groups[i].TotalCount > groups[j].TotalCount
		}
		return groups[i].GroupID < groups[j].GroupID
	})
	if len(groups) > 20 {
		groups = groups[:20]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"at": formatTime(at), "window_seconds": int(window / time.Second), "apdex_t_ms": tMs,
		"before":           periodJSON{From: formatTime(beforeFrom), To: formatTime(at), redJSON: before.red(rangeMinutes(beforeFrom, at), tMs)},
		"after":            periodJSON{From: formatTime(at), To: formatTime(afterEnd), redJSON: after.red(rangeMinutes(at, afterEnd), tMs)},
		"new_error_groups": groups,
	})
	return nil
}
