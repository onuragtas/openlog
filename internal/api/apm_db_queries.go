package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
)

// Database statements as applications measured them, across services, so they show with no connection to the database server
// (apm.md §7.1). They are bound in the query as parameters: the query layer refuses the name of the product in a fragment.
const (
	attrDBPlan          = "openlog.db.plan"
	attrDBPlanStatement = "openlog.db.plan.statement"
	attrDBPlanSystem    = "openlog.db.plan.system"
	attrDBPlanGeneric   = "openlog.db.plan.generic"
	// dbPlanSpanName is what agents/go/instrumentation/pgx names the span a plan travels on.
	dbPlanSpanName = "db.explain"
	// maxDBPlans is how many a statement shows, and maxDBPlanScan how many spans are looked through for them: the statement is
	// only known after normalizing, so it can't be filtered on in the query.
	maxDBPlans    = 5
	maxDBPlanScan = 200
)

// apmDBQueriesSelect groups a statement across the services running it.
func apmDBQueriesSelect(sc *query.Scope, r *http.Request, from, to time.Time, order string, limit int) *query.Select {
	q := minuteRange(sc.From(query.ApmDBQueries1m).Columns(cols([]string{"db_system", "db_name", "db_statement_normalized", "anyLast(db_operation) AS a_op"},
		callAggColumns, []string{"max(duration_max_ms) AS a_max", "sum(duration_sum_ms) / greatest(sum(calls), 1e-9) AS a_avg",
			"arraySort(groupUniqArray(50)(toString(service_name))) AS a_services"})...), from, to).
		GroupBy("db_system", "db_name", "db_statement_normalized").OrderBy(order).Limit(limit)
	params := r.URL.Query()
	if v := params.Get("db_system"); v != "" {
		q.Where("db_system = {db_system:String}").Param("db_system", v)
	}
	if v := params.Get("service"); v != "" {
		q.Where("service_name = {service:String}").Param("service", v)
	}
	if v := params.Get("environment"); v != "" {
		q.Where("deployment_environment = {environment:String}").Param("environment", v)
	}
	if v := strings.TrimSpace(params.Get("q")); v != "" {
		q.Where("positionCaseInsensitiveUTF8(db_statement_normalized, {q:String}) > 0").Param("q", v)
	}
	return q
}

// apmDBQueries serves GET /api/v1/apm/db-queries.
func (s *Server) apmDBQueries(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.apmLimit(r, 100, 1000)
	if err != nil {
		return err
	}
	order := map[string]string{"": "a_dsum DESC", "time": "a_dsum DESC", "calls": "a_req DESC", "slowest": "a_avg DESC", "errors": "a_err DESC"}[r.URL.Query().Get("sort")]
	if order == "" {
		return badRequest("sort must be time, calls, slowest or errors")
	}
	rows, err := sc.Query(r.Context(), apmDBQueriesSelect(sc, r, from, to, order, limit))
	if err != nil {
		return err
	}
	defer rows.Close()
	minutes := rangeMinutes(from, to)
	out := []apmDBQueryJSON{}
	var totalTime float64
	for rows.Next() {
		var d apmDBQueryJSON
		var avg float64
		dest, build := callAggDest()
		if err := rows.Scan(append(append([]any{&d.DBSystem, &d.DBName, &d.Statement, &d.Operation}, dest...), &d.MaxMs, &avg, &d.Services)...); err != nil {
			return err
		}
		red := build().red(minutes, 0)
		d.Calls, d.Throughput, d.Errors, d.ErrorRate, d.AvgMs, d.P95Ms = red.Requests, red.Throughput, red.Errors, red.ErrorRate, red.AvgMs, red.P95Ms
		d.TimeConsumedMs = build().durSum
		totalTime += d.TimeConsumedMs
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range out {
		if totalTime > 0 {
			out[i].TimeShare = out[i].TimeConsumedMs / totalTime
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queries": out})
	return nil
}

type dbPlanSpanJSON struct {
	Plan       string `json:"plan"`
	Service    string `json:"service"`
	Generic    bool   `json:"generic"`
	CapturedAt string `json:"captured_at"`
	TraceID    string `json:"trace_id"`
}

func apmDBPlansSelect(sc *query.Scope, r *http.Request, system string, from, to time.Time) *query.Select {
	q := spanRange(sc.From(query.Spans).Columns(
		"attributes[{a_plan:String}] AS p_plan", "attributes[{a_stmt:String}] AS p_stmt", "attributes[{a_gen:String}] AS p_gen",
		"toString(service_name) AS p_service", "timestamp AS p_at", "trace_id AS p_trace",
	), from, to).
		Param("a_plan", attrDBPlan).Param("a_stmt", attrDBPlanStatement).Param("a_gen", attrDBPlanGeneric).Param("a_sys", attrDBPlanSystem).
		Where("name = {p_name:String}").Param("p_name", dbPlanSpanName).
		Where("attributes[{a_sys:String}] = {p_sys:String}").Param("p_sys", system).
		OrderBy("timestamp DESC").Limit(maxDBPlanScan)
	if v := r.URL.Query().Get("service"); v != "" {
		q.Where("service_name = {service:String}").Param("service", v)
	}
	return q
}

// apmDBQueryPlans serves GET /api/v1/apm/db-queries/plans. Equal plans collapse into the newest of them, since what is worth seeing
// is a plan changing.
func (s *Server) apmDBQueryPlans(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	params := r.URL.Query()
	statement, system := params.Get("statement"), params.Get("db_system")
	if statement == "" || system == "" {
		return badRequest("statement and db_system are required")
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), apmDBPlansSelect(sc, r, system, from, to))
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []dbPlanSpanJSON{}
	seen := map[string]bool{}
	for rows.Next() {
		var plan, stmt, generic, service, trace string
		var at time.Time
		if err := rows.Scan(&plan, &stmt, &generic, &service, &at, &trace); err != nil {
			return err
		}
		if len(out) >= maxDBPlans || seen[plan] || apm.NormalizeStatement(system, stmt) != statement {
			continue
		}
		seen[plan] = true
		out = append(out, dbPlanSpanJSON{Plan: plan, Service: service, Generic: generic == "true", CapturedAt: formatTime(at), TraceID: trace})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": out})
	return nil
}
