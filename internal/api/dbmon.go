package api

import (
	"cmp"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// Database query performance monitoring reads (docs/contracts/db-monitoring.md §4, D-138).
//
// Every endpoint is scoped to one database instance (service.instance.id of the integration instance, e.g.
// db1.internal:5432) except the instance list. Statement statistics are per-interval deltas, so totals are plain
// sums; session samples are counted, so "average active sessions" of a bucket is the number of samples in it
// divided by the number of sampling instants. A statement is addressed by its fingerprint (the processor's
// FNV-1a 64 of the normalized text, as a decimal string) and correlated with APM by the normalized text itself,
// which is exactly apm_db_queries_1m.db_statement_normalized for the same statement.

const (
	dbMaxInstances = 500
	// dbMaxQueries bounds one page. A smaller bound than the console's largest page would make it step over rows, since Next adds the
	// limit it asked for to the offset and not the one it was given.
	dbMaxQueries    = 1000
	dbDefaultLimit  = 50
	dbMaxSessions   = 1000
	dbMaxInstanceID = 512
	// dbSnapshotWindow is how far back GET /db/sessions looks for the latest sampling instant.
	dbSnapshotWindow = 5 * time.Minute
)

func (s *Server) dbRoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/db/instances", s.dbInstances)
	route("GET /api/v1/db/queries", s.dbQueries)
	route("GET /api/v1/db/queries/{fingerprint}", s.dbQueryDetail)
	route("GET /api/v1/db/activity", s.dbActivity)
	route("GET /api/v1/db/activity/waits", s.dbActivityWaits)
	route("GET /api/v1/db/activity/queries", s.dbActivityQueries)
	route("GET /api/v1/db/sessions", s.dbSessions)
	route("GET /api/v1/db/lookup", s.dbLookup)
}

// ---- parameters ----

func dbInstanceParam(r *http.Request) (string, error) {
	inst := strings.TrimSpace(r.URL.Query().Get("instance"))
	if inst == "" {
		return "", badRequest("instance is required")
	}
	if len(inst) > dbMaxInstanceID {
		return "", badRequest("instance must be at most %d bytes", dbMaxInstanceID)
	}
	return inst, nil
}

func parseFingerprint(v string) (uint64, error) {
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == 0 {
		return 0, badRequest("fingerprint must be a decimal unsigned 64-bit integer")
	}
	return n, nil
}

// dbRange bounds a raw db_* table (DateTime64 timestamp) to [from, to).
func dbRange(q *query.Select, col string, from, to time.Time) *query.Select {
	return q.Where(col+" >= fromUnixTimestamp64Nano({t_from:Int64}) AND "+col+" < fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano())
}

func dbInstanceWhere(q *query.Select, inst string) *query.Select {
	return q.Where("instance = {d_inst:String}").Param("d_inst", inst)
}

func fingerprintString(n uint64) string { return strconv.FormatUint(n, 10) }

// ---- shapes ----

type dbInstanceJSON struct {
	Instance      string   `json:"instance"`
	DBSystem      string   `json:"db_system"`
	HostID        string   `json:"host_id"`
	HostName      string   `json:"host_name"`
	ServerAddress string   `json:"server_address"`
	ServerPort    uint16   `json:"server_port"`
	Calls         uint64   `json:"calls"`
	Throughput    float64  `json:"throughput"` // calls per second over the range
	TotalTimeMs   float64  `json:"total_time_ms"`
	AvgMs         *float64 `json:"avg_ms"`
	Statements    uint64   `json:"statements"`
	Errors        uint64   `json:"errors"`
	// AvgActiveSessions is null while the instance sends no session samples (sampling disabled or an older agent).
	AvgActiveSessions *float64 `json:"avg_active_sessions"`
	// TopWait is the wait event type with the most samples ("CPU" for samples without a wait).
	TopWait  string `json:"top_wait"`
	LastSeen string `json:"last_seen"`

	last time.Time
}

type dbQueryJSON struct {
	Fingerprint  string   `json:"fingerprint"`
	QueryID      string   `json:"query_id"`
	Text         string   `json:"text"`
	DBNames      []string `json:"db_names"`
	Calls        uint64   `json:"calls"`
	Throughput   float64  `json:"throughput"`
	TotalTimeMs  float64  `json:"total_time_ms"`
	AvgMs        *float64 `json:"avg_ms"`
	TimeShare    float64  `json:"time_share"`
	Rows         uint64   `json:"rows"`
	RowsPerCall  *float64 `json:"rows_per_call"`
	RowsExamined uint64   `json:"rows_examined"`
	Errors       uint64   `json:"errors"`
	NoIndexUsed  uint64   `json:"no_index_used"`
	BlocksHit    uint64   `json:"blocks_hit"`
	BlocksRead   uint64   `json:"blocks_read"`
	// CacheHitRatio is blocks_hit / (blocks_hit + blocks_read), null without block counts.
	CacheHitRatio *float64 `json:"cache_hit_ratio"`
}

type dbPlanJSON struct {
	PlanHash   string  `json:"plan_hash"`
	Format     string  `json:"format"`
	Plan       string  `json:"plan"`
	TotalCost  float64 `json:"total_cost"`
	DBName     string  `json:"db_name"`
	FirstSeen  string  `json:"first_seen"`
	LastSeen   string  `json:"last_seen"`
	Captures   uint64  `json:"captures"`
	IsCurrent  bool    `json:"is_current"`
	PlanChange bool    `json:"plan_change"`
}

type dbWaitJSON struct {
	Type    string  `json:"type"`
	Event   string  `json:"event"`
	Samples uint64  `json:"samples"`
	Share   float64 `json:"share"`
}

type dbCallerJSON struct {
	ServiceName string   `json:"service_name"`
	Environment string   `json:"environment"`
	Calls       float64  `json:"calls"`
	AvgMs       *float64 `json:"avg_ms"`
	Errors      float64  `json:"errors"`
}

// ---- handlers ----

// dbInstancesList sorts in Go, because two of its columns come from a second table: nothing here reaches a statement, so the values only
// say which field a name reads. With no limit a request gets everything, as it did before there were pages.
var dbInstancesList = listSpec{
	columns: map[string]string{
		"time": "total_time_ms", "calls": "calls", "avg": "avg_ms", "aas": "avg_active_sessions", "errors": "errors", "statements": "statements",
		"instance": "instance", "host": "host_name", "wait": "top_wait", "last_seen": "last_seen",
	},
	sort:     "time",
	asc:      map[string]bool{"instance": true, "host": true, "wait": true},
	limit:    2 * dbMaxInstances,
	maxLimit: 2 * dbMaxInstances,
}

// sortDBInstances orders the merged list, and the instance name makes the order total so a row can't swap between pages. What has no value
// stays last in either direction, because a blank is not the smallest of anything.
func sortDBInstances(rows []dbInstanceJSON, l listRequest) {
	number := func(d *dbInstanceJSON) *float64 {
		var v float64
		switch l.sort {
		case "avg":
			return d.AvgMs
		case "aas":
			return d.AvgActiveSessions
		case "calls":
			v = float64(d.Calls)
		case "errors":
			v = float64(d.Errors)
		case "statements":
			v = float64(d.Statements)
		case "last_seen":
			v = float64(d.last.UnixMilli())
		default:
			v = d.TotalTimeMs
		}
		return &v
	}
	text := func(d *dbInstanceJSON) string {
		switch l.sort {
		case "instance":
			return d.Instance
		case "host":
			return d.HostName
		}
		return d.TopWait
	}
	directed := func(c int) int {
		if l.asc {
			return c
		}
		return -c
	}
	compare := func(a, b *dbInstanceJSON) int {
		if l.sort == "instance" || l.sort == "host" || l.sort == "wait" {
			x, y := text(a), text(b)
			if (x == "") != (y == "") {
				return cmp.Compare(boolRank(x == ""), boolRank(y == ""))
			}
			return directed(cmp.Compare(strings.ToLower(x), strings.ToLower(y)))
		}
		x, y := number(a), number(b)
		if x == nil || y == nil {
			return cmp.Compare(boolRank(x == nil), boolRank(y == nil))
		}
		return directed(cmp.Compare(*x, *y))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if c := compare(&rows[i], &rows[j]); c != 0 {
			return c < 0
		}
		return rows[i].Instance < rows[j].Instance
	})
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pageOfMerged cuts one page out of a list that was merged or sorted in Go, which is the only place its offset can apply.
func pageOfMerged[T any](rows []T, l listRequest) ([]T, bool) {
	if l.offset >= len(rows) {
		return rows[:0], false
	}
	return page(rows[l.offset:], l)
}

// dbInstances lists the database instances that sent statement statistics or session samples in the range.
//
// The two reads each stop at dbMaxInstances, the second at the instances with the most samples, so past that many instances this is complete
// only up to those caps: the page after the last one reports no more, and a sort by a column can miss an instance the caps left out.
func (s *Server) dbInstances(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	l, err := dbInstancesList.parse(r, s.cfg.MaxRows)
	if err != nil {
		return err
	}
	q := dbRange(sc.From(query.DBQueryStats).Columns(
		"instance", "anyLast(db_system) AS d_sys", "anyLast(host_id) AS d_host", "anyLast(host_name) AS d_hname",
		"anyLast(server_address) AS d_addr", "anyLast(server_port) AS d_port",
		"sum(calls) AS d_calls", "sum(total_time_ms) AS d_time", "uniq(fingerprint) AS d_stmts", "sum(errors) AS d_err",
		"max(timestamp) AS d_last",
	), "timestamp", from, to).GroupBy("instance").OrderBy("d_time DESC", "instance").Limit(min(s.cfg.MaxRows, dbMaxInstances))
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	byInst := map[string]*dbInstanceJSON{}
	var order []string
	secs := max(to.Sub(from).Seconds(), 1)
	for rows.Next() {
		var d dbInstanceJSON
		var last time.Time
		if err := rows.Scan(&d.Instance, &d.DBSystem, &d.HostID, &d.HostName, &d.ServerAddress, &d.ServerPort, &d.Calls,
			&d.TotalTimeMs, &d.Statements, &d.Errors, &last); err != nil {
			rows.Close()
			return err
		}
		d.Throughput = float64(d.Calls) / secs
		d.AvgMs = ratio(d.TotalTimeMs, float64(d.Calls))
		d.LastSeen, d.last = formatTime(last), last
		byInst[d.Instance] = &d
		order = append(order, d.Instance)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Session samples: average active sessions and the dominant wait type, per instance.
	sq := dbRange(sc.From(query.DBSessionSamples).Columns(
		"instance", "anyLast(db_system) AS d_sys", "anyLast(host_id) AS d_host", "anyLast(host_name) AS d_hname",
		"toUInt64(count()) AS d_n", "uniq(timestamp) AS d_ts", "topK(1)(if(wait_event_type = '', 'CPU', wait_event_type)) AS d_top",
		"max(timestamp) AS d_last",
	), "timestamp", from, to).GroupBy("instance").OrderBy("d_n DESC", "instance").Limit(min(s.cfg.MaxRows, dbMaxInstances))
	rows, err = sc.Query(r.Context(), sq)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var inst, sys, host, hname string
		var n, instants uint64
		var top []string
		var last time.Time
		if err := rows.Scan(&inst, &sys, &host, &hname, &n, &instants, &top, &last); err != nil {
			return err
		}
		d := byInst[inst]
		if d == nil {
			d = &dbInstanceJSON{Instance: inst, DBSystem: sys, HostID: host, HostName: hname, LastSeen: formatTime(last), last: last}
			byInst[inst] = d
			order = append(order, inst)
		}
		d.AvgActiveSessions = ratio(float64(n), float64(instants))
		if len(top) > 0 {
			d.TopWait = top[0]
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out := make([]dbInstanceJSON, 0, len(order))
	for _, k := range order {
		out = append(out, *byInst[k])
	}
	sortDBInstances(out, l)
	out, more := pageOfMerged(out, l)
	writeJSON(w, http.StatusOK, map[string]any{"instances": out, "has_more": more})
	return nil
}

var dbQueriesList = listSpec{
	columns: map[string]string{
		"time": "d_time", "calls": "d_calls", "avg": "d_avg", "rows": "d_rows", "errors": "d_err", "reads": "d_read",
		"rows_per_call": "d_rows / greatest(d_calls, 1)", "statement": "d_text",
	},
	sort:     "time",
	asc:      map[string]bool{"statement": true},
	tiebreak: []string{"fingerprint"},
	limit:    dbDefaultLimit,
	maxLimit: dbMaxQueries,
}

// dbQueries lists the statements of one instance, heaviest first.
func (s *Server) dbQueries(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	inst, err := dbInstanceParam(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	l, err := dbQueriesList.parse(r, s.cfg.MaxRows)
	if err != nil {
		return err
	}
	q := dbInstanceWhere(dbRange(sc.From(query.DBQueryStats).Columns(
		"fingerprint", "any(query_id) AS d_qid", "any(query_text) AS d_text", "groupUniqArray(5)(db_name) AS d_dbs",
		"sum(calls) AS d_calls", "sum(total_time_ms) AS d_time", "sum(total_time_ms) / greatest(sum(calls), 1) AS d_avg",
		"sum(rows) AS d_rows", "sum(rows_examined) AS d_rex", "sum(errors) AS d_err", "sum(no_index_used) AS d_noidx",
		"sum(blocks_hit) AS d_hit", "sum(blocks_read) AS d_read",
	), "timestamp", from, to), inst)
	if db := strings.TrimSpace(r.URL.Query().Get("db")); db != "" {
		q.Where("db_name = {d_db:String}").Param("d_db", db)
	}
	if text := strings.TrimSpace(r.URL.Query().Get("q")); text != "" {
		q.Where("positionCaseInsensitive(query_text, {d_q:String}) > 0").Param("d_q", text)
	}
	q.GroupBy("fingerprint").OrderBy(dbQueriesList.order(l)...).Limit(l.fetch()).Offset(l.offset)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []dbQueryJSON{}
	secs := max(to.Sub(from).Seconds(), 1)
	for rows.Next() {
		var d dbQueryJSON
		var fp uint64
		var avg float64
		if err := rows.Scan(&fp, &d.QueryID, &d.Text, &d.DBNames, &d.Calls, &d.TotalTimeMs, &avg, &d.Rows, &d.RowsExamined,
			&d.Errors, &d.NoIndexUsed, &d.BlocksHit, &d.BlocksRead); err != nil {
			return err
		}
		fillQuery(&d, fp, secs)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out, more := page(out, l)
	total, err := s.dbInstanceTime(r, sc, inst, from, to)
	if err != nil {
		return err
	}
	for i := range out {
		if total > 0 {
			out[i].TimeShare = out[i].TotalTimeMs / total
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queries": out, "total_time_ms": total, "has_more": more})
	return nil
}

func fillQuery(d *dbQueryJSON, fp uint64, secs float64) {
	d.Fingerprint = fingerprintString(fp)
	d.Throughput = float64(d.Calls) / secs
	d.AvgMs = ratio(d.TotalTimeMs, float64(d.Calls))
	d.RowsPerCall = ratio(float64(d.Rows), float64(d.Calls))
	d.CacheHitRatio = ratio(float64(d.BlocksHit), float64(d.BlocksHit+d.BlocksRead))
	if d.DBNames == nil {
		d.DBNames = []string{}
	}
}

// dbInstanceTime is the total statement time of an instance in the range (the denominator of time_share).
func (s *Server) dbInstanceTime(r *http.Request, sc *query.Scope, inst string, from, to time.Time) (float64, error) {
	q := dbInstanceWhere(dbRange(sc.From(query.DBQueryStats).Columns("sum(total_time_ms) AS d_time"), "timestamp", from, to), inst)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var total float64
	if rows.Next() {
		if err := rows.Scan(&total); err != nil {
			return 0, err
		}
	}
	return total, rows.Err()
}

// dbQueryDetail is one statement: totals, a time series, its plans, the waits it spent time in and the services
// that run it (APM).
func (s *Server) dbQueryDetail(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	inst, err := dbInstanceParam(r)
	if err != nil {
		return err
	}
	fp, err := parseFingerprint(r.PathValue("fingerprint"))
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	step, err := apmStep(r, from, to)
	if err != nil {
		return err
	}
	stmt := func(q *query.Select) *query.Select {
		return dbInstanceWhere(q, inst).Where("fingerprint = {d_fp:UInt64}").Param("d_fp", fp)
	}

	// Totals and identity.
	tq := stmt(dbRange(sc.From(query.DBQueryStats).Columns(
		"any(query_id) AS d_qid", "any(query_text) AS d_text", "groupUniqArray(5)(db_name) AS d_dbs", "anyLast(db_system) AS d_sys",
		"sum(calls) AS d_calls", "sum(total_time_ms) AS d_time", "sum(rows) AS d_rows", "sum(rows_examined) AS d_rex",
		"sum(errors) AS d_err", "sum(no_index_used) AS d_noidx", "sum(blocks_hit) AS d_hit", "sum(blocks_read) AS d_read",
		"toUInt64(count()) AS d_n",
	), "timestamp", from, to))
	rows, err := sc.Query(r.Context(), tq)
	if err != nil {
		return err
	}
	var d dbQueryJSON
	var sys string
	var n uint64
	if rows.Next() {
		if err := rows.Scan(&d.QueryID, &d.Text, &d.DBNames, &sys, &d.Calls, &d.TotalTimeMs, &d.Rows, &d.RowsExamined,
			&d.Errors, &d.NoIndexUsed, &d.BlocksHit, &d.BlocksRead, &n); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if n == 0 {
		return notFound("statement not found for this instance in the time range")
	}
	fillQuery(&d, fp, max(to.Sub(from).Seconds(), 1))
	if total, err := s.dbInstanceTime(r, sc, inst, from, to); err != nil {
		return err
	} else if total > 0 {
		d.TimeShare = d.TotalTimeMs / total
	}

	// Series.
	series := stmt(dbRange(sc.From(query.DBQueryStats).Columns(
		"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t", "sum(calls) AS d_calls", "sum(total_time_ms) AS d_time",
		"sum(rows) AS d_rows",
	), "timestamp", from, to)).Param("step", uint32(step/time.Second)).GroupBy("t").OrderBy("t").Limit(s.cfg.MaxRows)
	rows, err = sc.Query(r.Context(), series)
	if err != nil {
		return err
	}
	points := []map[string]any{}
	for rows.Next() {
		var t time.Time
		var calls, rowsN uint64
		var tm float64
		if err := rows.Scan(&t, &calls, &tm, &rowsN); err != nil {
			rows.Close()
			return err
		}
		points = append(points, map[string]any{"t": t.UnixMilli(), "calls": calls, "throughput": float64(calls) / step.Seconds(),
			"total_time_ms": tm, "avg_ms": ratio(tm, float64(calls)), "rows": rowsN})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	plans, err := s.dbPlans(r, sc, inst, fp)
	if err != nil {
		return err
	}
	waits, err := s.dbWaits(r, sc, inst, &fp, from, to, 10)
	if err != nil {
		return err
	}
	callers, err := s.dbCallers(r, sc, sys, d.Text, from, to)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": formatTime(from), "to": formatTime(to), "step": formatStep(step), "db_system": sys,
		"query": d, "points": points, "plans": plans, "waits": waits, "callers": callers,
	})
	return nil
}

// dbPlans returns the distinct plans of a statement, newest first (the plans' own 30-day retention, not the range:
// the plan that was current before a regression is exactly the one to compare with).
func (s *Server) dbPlans(r *http.Request, sc *query.Scope, inst string, fp uint64) ([]dbPlanJSON, error) {
	q := dbInstanceWhere(sc.From(query.DBQueryPlans).Columns(
		"plan_hash", "argMax(plan_format, captured_at) AS d_fmt", "argMax(plan, captured_at) AS d_plan",
		"argMax(total_cost, captured_at) AS d_cost", "argMax(db_name, captured_at) AS d_db", "min(captured_at) AS d_first",
		"max(captured_at) AS d_last", "toUInt64(count()) AS d_n",
	), inst).Where("fingerprint = {d_fp:UInt64}").Param("d_fp", fp).GroupBy("plan_hash").OrderBy("d_last DESC").Limit(20)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dbPlanJSON{}
	for rows.Next() {
		var p dbPlanJSON
		var first, last time.Time
		if err := rows.Scan(&p.PlanHash, &p.Format, &p.Plan, &p.TotalCost, &p.DBName, &first, &last, &p.Captures); err != nil {
			return nil, err
		}
		p.FirstSeen, p.LastSeen = formatTime(first), formatTime(last)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		out[0].IsCurrent = true
		// A plan change: the current plan appeared after another plan had been seen.
		out[0].PlanChange = len(out) > 1
	}
	return out, nil
}

// dbWaits returns the wait events of an instance (or one statement), by samples.
func (s *Server) dbWaits(r *http.Request, sc *query.Scope, inst string, fp *uint64, from, to time.Time, limit int) ([]dbWaitJSON, error) {
	q := dbInstanceWhere(dbRange(sc.From(query.DBSessionSamples).Columns(
		"if(wait_event_type = '', 'CPU', wait_event_type) AS d_type", "wait_event", "toUInt64(count()) AS d_n",
	), "timestamp", from, to), inst)
	if fp != nil {
		q.Where("fingerprint = {d_fp:UInt64}").Param("d_fp", *fp)
	}
	q.GroupBy("d_type", "wait_event").OrderBy("d_n DESC", "d_type", "wait_event").Limit(limit)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dbWaitJSON{}
	var total uint64
	for rows.Next() {
		var wj dbWaitJSON
		if err := rows.Scan(&wj.Type, &wj.Event, &wj.Samples); err != nil {
			return nil, err
		}
		total += wj.Samples
		out = append(out, wj)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Share = float64(out[i].Samples) / float64(max(total, 1))
	}
	return out, nil
}

// dbCallers returns the services whose client spans ran the statement: an equality on the normalized text, which
// the processor produces with the same function for both sides.
func (s *Server) dbCallers(r *http.Request, sc *query.Scope, system, text string, from, to time.Time) ([]dbCallerJSON, error) {
	out := []dbCallerJSON{}
	if text == "" {
		return out, nil
	}
	q := minuteRange(sc.From(query.ApmDBQueries1m).Columns(
		"service_name", "deployment_environment", "sum(calls) AS d_calls", "sum(duration_sum_ms) AS d_dur", "sum(errors) AS d_err",
	), from, to).Where("db_statement_normalized = {d_text:String}").Param("d_text", text).
		Where("db_system = {d_sys:String}").Param("d_sys", system).
		GroupBy("service_name", "deployment_environment").OrderBy("d_calls DESC").Limit(50)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c dbCallerJSON
		var dur float64
		if err := rows.Scan(&c.ServiceName, &c.Environment, &c.Calls, &dur, &c.Errors); err != nil {
			return nil, err
		}
		c.AvgMs = ratio(dur, c.Calls)
		out = append(out, c)
	}
	return out, rows.Err()
}

// dbActivity is the instance's load over time: average active sessions per wait type per step, the top wait events
// and the statements with the most samples (what the sessions were running).
func (s *Server) dbActivity(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	inst, err := dbInstanceParam(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	step, err := apmStep(r, from, to)
	if err != nil {
		return err
	}
	stepSec := uint32(step / time.Second)
	// Sampling instants per bucket are counted over the whole instance, so a bucket where only one wait type was
	// seen still divides by every instant of the bucket.
	iq := dbInstanceWhere(dbRange(sc.From(query.DBSessionSamples).Columns(
		"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t", "uniq(timestamp) AS d_ts",
	), "timestamp", from, to), inst).Param("step", stepSec).GroupBy("t").OrderBy("t").Limit(s.cfg.MaxRows)
	rows, err := sc.Query(r.Context(), iq)
	if err != nil {
		return err
	}
	instants := map[int64]uint64{}
	for rows.Next() {
		var t time.Time
		var n uint64
		if err := rows.Scan(&t, &n); err != nil {
			rows.Close()
			return err
		}
		instants[t.UnixMilli()] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	sq := dbInstanceWhere(dbRange(sc.From(query.DBSessionSamples).Columns(
		"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t", "if(wait_event_type = '', 'CPU', wait_event_type) AS d_type",
		"toUInt64(count()) AS d_n",
	), "timestamp", from, to), inst).Param("step", stepSec).GroupBy("t", "d_type").OrderBy("t", "d_type").Limit(s.cfg.MaxRows)
	rows, err = sc.Query(r.Context(), sq)
	if err != nil {
		return err
	}
	byType := map[string][][2]any{}
	for rows.Next() {
		var t time.Time
		var typ string
		var n uint64
		if err := rows.Scan(&t, &typ, &n); err != nil {
			rows.Close()
			return err
		}
		ms := t.UnixMilli()
		byType[typ] = append(byType[typ], [2]any{ms, float64(n) / float64(max(instants[ms], 1))})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	types := make([]string, 0, len(byType))
	for k := range byType {
		types = append(types, k)
	}
	sort.Strings(types)
	series := make([]map[string]any, 0, len(types))
	for _, k := range types {
		series = append(series, map[string]any{"wait_type": k, "points": byType[k]})
	}

	waits, err := s.dbWaits(r, sc, inst, nil, from, to, 20)
	if err != nil {
		return err
	}
	tq := dbActivityQueriesSelect(sc, inst, from, to).OrderBy("d_n DESC", "fingerprint").Limit(20)
	rows, err = sc.Query(r.Context(), tq)
	if err != nil {
		return err
	}
	defer rows.Close()
	var total uint64
	for _, n := range instants {
		total += n
	}
	top, err := scanDBActivityQueries(rows, total)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": formatTime(from), "to": formatTime(to), "step": formatStep(step),
		"series": series, "waits": waits, "top_queries": top,
	})
	return nil
}

// dbActivityTopJSON is one statement of an instance's load.
type dbActivityTopJSON struct {
	Fingerprint       string  `json:"fingerprint"`
	Text              string  `json:"text"`
	Samples           uint64  `json:"samples"`
	AvgActiveSessions float64 `json:"avg_active_sessions"`
	TopWait           string  `json:"top_wait"`
}

func dbActivityQueriesSelect(sc *query.Scope, inst string, from, to time.Time) *query.Select {
	return dbInstanceWhere(dbRange(sc.From(query.DBSessionSamples).Columns(
		"fingerprint", "any(query_text) AS d_text", "toUInt64(count()) AS d_n",
		"topK(1)(if(wait_event_type = '', 'CPU', concat(wait_event_type, ':', wait_event))) AS d_wait",
	), "timestamp", from, to), inst).Where("fingerprint != 0").GroupBy("fingerprint")
}

func scanDBActivityQueries(rows query.Rows, instants uint64) ([]dbActivityTopJSON, error) {
	out := []dbActivityTopJSON{}
	for rows.Next() {
		var fp uint64
		var tj dbActivityTopJSON
		var tw []string
		if err := rows.Scan(&fp, &tj.Text, &tj.Samples, &tw); err != nil {
			return nil, err
		}
		tj.Fingerprint = fingerprintString(fp)
		tj.AvgActiveSessions = float64(tj.Samples) / float64(max(instants, 1))
		if len(tw) > 0 {
			tj.TopWait = tw[0]
		}
		out = append(out, tj)
	}
	return out, rows.Err()
}

// dbSampleTotals is how many samples an instance has in the range and at how many instants. A share in a paged list has to be of all of these
// and not of its own page, or the same row would change its number when it moved to another page.
func dbSampleTotals(r *http.Request, sc *query.Scope, inst string, from, to time.Time) (samples, instants uint64, err error) {
	rows, err := sc.Query(r.Context(), dbInstanceWhere(dbRange(sc.From(query.DBSessionSamples).Columns(
		"toUInt64(count()) AS d_n", "toUInt64(uniq(timestamp)) AS d_ts",
	), "timestamp", from, to), inst))
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&samples, &instants); err != nil {
			return 0, 0, err
		}
	}
	return samples, instants, rows.Err()
}

var dbActivityWaitsList = listSpec{
	columns:  map[string]string{"samples": "d_n", "type": "d_type", "event": "wait_event"},
	sort:     "samples",
	asc:      map[string]bool{"type": true, "event": true},
	tiebreak: []string{"d_type", "wait_event"},
	limit:    dbDefaultLimit,
	maxLimit: dbMaxQueries,
}

func dbActivityWaitsSelect(sc *query.Scope, inst string, from, to time.Time, l listRequest) *query.Select {
	return dbInstanceWhere(dbRange(sc.From(query.DBSessionSamples).Columns(
		"if(wait_event_type = '', 'CPU', wait_event_type) AS d_type", "wait_event", "toUInt64(count()) AS d_n",
	), "timestamp", from, to), inst).GroupBy("d_type", "wait_event").OrderBy(dbActivityWaitsList.order(l)...).Limit(l.fetch()).Offset(l.offset)
}

// dbActivityWaits serves GET /api/v1/db/activity/waits, every wait event of the range where /db/activity holds only the top ones. They are a
// list of their own so that sorting them does not read the charts again.
func (s *Server) dbActivityWaits(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	inst, err := dbInstanceParam(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	l, err := dbActivityWaitsList.parse(r, s.cfg.MaxRows)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), dbActivityWaitsSelect(sc, inst, from, to, l))
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []dbWaitJSON{}
	for rows.Next() {
		var wj dbWaitJSON
		if err := rows.Scan(&wj.Type, &wj.Event, &wj.Samples); err != nil {
			return err
		}
		out = append(out, wj)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out, more := page(out, l)
	total, _, err := dbSampleTotals(r, sc, inst, from, to)
	if err != nil {
		return err
	}
	for i := range out {
		out[i].Share = float64(out[i].Samples) / float64(max(total, 1))
	}
	writeJSON(w, http.StatusOK, map[string]any{"waits": out, "has_more": more})
	return nil
}

var dbActivityQueriesList = listSpec{
	columns:  map[string]string{"samples": "d_n", "statement": "d_text", "wait": "d_wait"},
	sort:     "samples",
	asc:      map[string]bool{"statement": true, "wait": true},
	tiebreak: []string{"fingerprint"},
	limit:    dbDefaultLimit,
	maxLimit: dbMaxQueries,
}

// dbActivityQueries serves GET /api/v1/db/activity/queries, every statement the samples caught where /db/activity holds only the top ones.
func (s *Server) dbActivityQueries(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	inst, err := dbInstanceParam(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	l, err := dbActivityQueriesList.parse(r, s.cfg.MaxRows)
	if err != nil {
		return err
	}
	_, instants, err := dbSampleTotals(r, sc, inst, from, to)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), dbActivityQueriesSelect(sc, inst, from, to).OrderBy(dbActivityQueriesList.order(l)...).Limit(l.fetch()).Offset(l.offset))
	if err != nil {
		return err
	}
	defer rows.Close()
	out, err := scanDBActivityQueries(rows, instants)
	if err != nil {
		return err
	}
	out, more := page(out, l)
	writeJSON(w, http.StatusOK, map[string]any{"queries": out, "has_more": more})
	return nil
}

type dbSessionJSON struct {
	SessionID          string   `json:"session_id"`
	State              string   `json:"state"`
	WaitType           string   `json:"wait_type"`
	WaitEvent          string   `json:"wait_event"`
	DBName             string   `json:"db_name"`
	User               string   `json:"user"`
	Application        string   `json:"application"`
	ClientAddress      string   `json:"client_address"`
	DurationMs         float64  `json:"duration_ms"`
	Fingerprint        string   `json:"fingerprint"`
	Text               string   `json:"text"`
	BlockingSessionIDs []string `json:"blocking_session_ids"`
	// Blocks counts the sessions waiting (directly or transitively) for this one: the head of a blocking chain has
	// the largest number.
	Blocks int `json:"blocks"`
}

// dbSessions returns the latest sampling instant at or before `at` (default: now) within dbSnapshotWindow: every
// non-idle session with what it runs, what it waits for and whom it blocks.
//
// It has no pages, because a blocking chain can only be drawn over the whole sample, so the console sorts what it holds. That makes it complete
// only up to dbMaxSessions, the longest running ones, and has_more says when the sample was bigger than that.
func (s *Server) dbSessions(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	inst, err := dbInstanceParam(r)
	if err != nil {
		return err
	}
	at := time.Now()
	if v := r.URL.Query().Get("at"); v != "" {
		ms, err := strconv.ParseInt(v, 10, 64)
		if err != nil || ms <= 0 {
			return badRequest("at must be unix milliseconds")
		}
		at = time.UnixMilli(ms)
	}
	from := at.Add(-dbSnapshotWindow)
	lq := dbInstanceWhere(dbRange(sc.From(query.DBSessionSamples).Columns("max(timestamp) AS d_last", "toUInt64(count()) AS d_n"),
		"timestamp", from, at.Add(time.Millisecond)), inst)
	rows, err := sc.Query(r.Context(), lq)
	if err != nil {
		return err
	}
	var last time.Time
	var n uint64
	if rows.Next() {
		if err := rows.Scan(&last, &n); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	out := []dbSessionJSON{}
	if n == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"sampled_at": nil, "sessions": out})
		return nil
	}
	q := dbInstanceWhere(sc.From(query.DBSessionSamples).Columns(
		"session_id", "state", "wait_event_type", "wait_event", "db_name", "db_user", "application", "client_address",
		"duration_ms", "fingerprint", "query_text", "blocking_session_ids",
	), inst).Where("timestamp = fromUnixTimestamp64Milli({d_at:Int64})").Param("d_at", last.UnixMilli()).
		OrderBy("duration_ms DESC", "session_id").Limit(dbMaxSessions + 1)
	rows, err = sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sj dbSessionJSON
		var fp uint64
		if err := rows.Scan(&sj.SessionID, &sj.State, &sj.WaitType, &sj.WaitEvent, &sj.DBName, &sj.User, &sj.Application,
			&sj.ClientAddress, &sj.DurationMs, &fp, &sj.Text, &sj.BlockingSessionIDs); err != nil {
			return err
		}
		if fp != 0 {
			sj.Fingerprint = fingerprintString(fp)
		}
		if sj.BlockingSessionIDs == nil {
			sj.BlockingSessionIDs = []string{}
		}
		out = append(out, sj)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out, more := page(out, listRequest{limit: dbMaxSessions})
	countBlocked(out)
	writeJSON(w, http.StatusOK, map[string]any{"sampled_at": formatTime(last), "sessions": out, "has_more": more})
	return nil
}

// countBlocked sets Blocks: how many sessions wait for each one, directly or through a chain (cycles — a deadlock
// the server has not broken yet — count each waiter once).
func countBlocked(sessions []dbSessionJSON) {
	waiters := map[string][]string{} // holder → sessions waiting for it
	for _, s := range sessions {
		for _, h := range s.BlockingSessionIDs {
			waiters[h] = append(waiters[h], s.SessionID)
		}
	}
	for i := range sessions {
		seen := map[string]bool{sessions[i].SessionID: true}
		stack := append([]string(nil), waiters[sessions[i].SessionID]...)
		for len(stack) > 0 {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[id] {
				continue
			}
			seen[id] = true
			sessions[i].Blocks++
			stack = append(stack, waiters[id]...)
		}
	}
}

// dbLookup finds the instances whose statement statistics hold a normalized statement: the link from an APM
// database call to the server-side view of the same statement.
func (s *Server) dbLookup(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	stmt := r.URL.Query().Get("statement")
	system := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("db_system")))
	if stmt == "" || system == "" {
		return badRequest("statement and db_system are required")
	}
	if len(stmt) > 8192 {
		return badRequest("statement must be at most 8192 bytes")
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	q := dbRange(sc.From(query.DBQueryStats).Columns(
		"instance", "fingerprint", "anyLast(host_name) AS d_host", "sum(calls) AS d_calls", "sum(total_time_ms) AS d_time",
	), "timestamp", from, to).Where("query_text = {d_text:String}").Param("d_text", stmt).
		Where("db_system = {d_sys:String}").Param("d_sys", system).
		GroupBy("instance", "fingerprint").OrderBy("d_time DESC").Limit(20)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type matchJSON struct {
		Instance    string   `json:"instance"`
		Fingerprint string   `json:"fingerprint"`
		HostName    string   `json:"host_name"`
		Calls       uint64   `json:"calls"`
		AvgMs       *float64 `json:"avg_ms"`
	}
	out := []matchJSON{}
	for rows.Next() {
		var m matchJSON
		var fp uint64
		var tm float64
		if err := rows.Scan(&m.Instance, &fp, &m.HostName, &m.Calls, &tm); err != nil {
			return err
		}
		m.Fingerprint = fingerprintString(fp)
		m.AvgMs = ratio(tm, float64(m.Calls))
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": out})
	return nil
}

// ratio returns a / b, or nil when b is not positive.
func ratio(a, b float64) *float64 {
	if b <= 0 {
		return nil
	}
	v := a / b
	return &v
}
