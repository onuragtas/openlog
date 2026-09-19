package mssql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/dbmon"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/sqlredact"
)

// Query performance monitoring of SQL Server (db-monitoring.md §3.3): statement deltas from
// sys.dm_exec_query_stats aggregated by query_hash, active requests (waits, blocking) from sys.dm_exec_requests and
// the cached showplan XML of the top statements from sys.dm_exec_query_plan. All of it needs VIEW SERVER STATE
// (SQL Server 2022: VIEW SERVER PERFORMANCE STATE), the permission the integration already requires.

// QueryStatsQuery reads the statements with the most cumulative elapsed time, one row per (query_hash, database),
// without system databases and monitoring statements over the dynamic management views (the agent's own).
// Times are microseconds; the plan handle of the most expensive cached plan is kept for the showplan.
func QueryStatsQuery(n int) string {
	return fmt.Sprintf(`SELECT TOP (%d) CONVERT(varchar(20), qs.query_hash, 1), COALESCE(DB_NAME(CONVERT(int, pa.value)), ''),
SUM(qs.execution_count), SUM(qs.total_elapsed_time), SUM(qs.total_rows), SUM(qs.total_logical_reads), SUM(qs.total_physical_reads),
MAX(LEFT(SUBSTRING(st.text, (qs.statement_start_offset / 2) + 1,
  ((CASE qs.statement_end_offset WHEN -1 THEN DATALENGTH(st.text) ELSE qs.statement_end_offset END - qs.statement_start_offset) / 2) + 1), 4000)),
CONVERT(varchar(130), MAX(qs.plan_handle), 1)
FROM sys.dm_exec_query_stats qs
CROSS APPLY sys.dm_exec_sql_text(qs.sql_handle) st
OUTER APPLY (SELECT value FROM sys.dm_exec_plan_attributes(qs.plan_handle) WHERE attribute = 'dbid') pa
WHERE (st.dbid IS NULL OR st.dbid > 4) AND st.text NOT LIKE '%%sys.dm[_]%%' AND st.text NOT LIKE '%%sys.master[_]files%%'
GROUP BY qs.query_hash, pa.value
ORDER BY SUM(qs.total_elapsed_time) DESC`, n)
}

// RequestsQuery samples the running user requests, and SleepingBlockersQuery the sessions that block others while
// not running anything themselves (an open transaction left by the application: the head of most blocking chains).
const (
	RequestsQuery = `SELECT TOP (200) CAST(r.session_id AS varchar(10)), COALESCE(DB_NAME(r.database_id), ''), COALESCE(s.login_name, ''),
COALESCE(s.program_name, ''), COALESCE(c.client_net_address, ''), r.status, COALESCE(r.wait_type, ''), r.total_elapsed_time,
CAST(r.blocking_session_id AS varchar(10)), COALESCE(LEFT(SUBSTRING(t.text, (r.statement_start_offset / 2) + 1,
  ((CASE r.statement_end_offset WHEN -1 THEN DATALENGTH(t.text) ELSE r.statement_end_offset END - r.statement_start_offset) / 2) + 1), 4000), ''),
COALESCE(CONVERT(varchar(20), r.query_hash, 1), '')
FROM sys.dm_exec_requests r JOIN sys.dm_exec_sessions s ON s.session_id = r.session_id
LEFT JOIN sys.dm_exec_connections c ON c.session_id = r.session_id
OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
WHERE s.is_user_process = 1 AND r.session_id <> @@SPID
ORDER BY r.total_elapsed_time DESC`
	SleepingBlockersQuery = `SELECT CAST(s.session_id AS varchar(10)), COALESCE(DB_NAME(s.database_id), ''), COALESCE(s.login_name, ''),
COALESCE(s.program_name, ''), COALESCE(s.host_name, ''), DATEDIFF_BIG(MILLISECOND, s.last_request_end_time, SYSDATETIME())
FROM sys.dm_exec_sessions s
WHERE s.session_id IN (SELECT blocking_session_id FROM sys.dm_exec_requests WHERE blocking_session_id > 0)
AND s.session_id NOT IN (SELECT session_id FROM sys.dm_exec_requests)`
)

var planHandleRe = regexp.MustCompile(`^0x[0-9A-Fa-f]{2,128}$`)

type queryMon struct {
	stmts      dbmon.Tracker
	plans      dbmon.Plans
	lastDeltas []dbmon.Stat
	handles    map[string]string // statement key → plan handle
}

// collectQueryStats records the statement deltas and captures due plans; errors make the collection partial.
func (c *collector) collectQueryStats(ctx context.Context, b *integrations.Batch) error {
	qs := c.inst.Settings.QueryStats
	topN := qs.TopN
	if topN <= 0 {
		topN = 20
	}
	rows, err := c.q.Rows(ctx, c.db, QueryStatsQuery(min(max(topN*5, 200), 1000)))
	if err != nil {
		return fmt.Errorf("sys.dm_exec_query_stats: %w", c.classify(err))
	}
	cur := make([]dbmon.Stat, 0, len(rows))
	c.mon.handles = map[string]string{}
	for _, r := range rows {
		if len(r) < 9 || r[0] == nil {
			continue
		}
		v := func(i int) string {
			if r[i] == nil {
				return ""
			}
			return *r[i]
		}
		n := func(i int) int64 { x, _ := strconv.ParseInt(v(i), 10, 64); return x }
		calls := n(2)
		if calls < max(int64(qs.MinCalls), 1) {
			continue
		}
		logical, physical := n(5), n(6)
		s := dbmon.Stat{QueryID: v(0), DB: v(1), Text: v(7), Calls: calls, TimeMs: float64(n(3)) / 1000, Rows: n(4),
			BlocksHit: max(logical-physical, 0), BlocksRead: physical}
		s.Key = s.QueryID + "\x00" + s.DB
		c.mon.handles[s.Key] = v(8)
		cur = append(cur, s)
	}
	iv, deltas := c.mon.stmts.Deltas(b.Now(), cur, topN)
	dbmon.RecordStats(b, dbmon.SystemMSSQL, iv, deltas)
	c.mon.lastDeltas = deltas
	return c.capturePlans(ctx, b)
}

// capturePlans reads the cached showplan of due top statements. The XML carries literal values (compiled and
// runtime parameter values, constants in scalar operators and the statement text): they are redacted before the
// plan leaves the host.
func (c *collector) capturePlans(ctx context.Context, b *integrations.Batch) error {
	qs := c.inst.Settings.QueryStats
	if !qs.ExplainEnabled() {
		return nil
	}
	c.mon.plans.Interval = qs.EffectiveExplainInterval()
	now := b.Now()
	var errs []string
	done := 0
	for _, d := range c.mon.lastDeltas {
		if done >= dbmon.MaxExplainsPerCollection || ctx.Err() != nil {
			break
		}
		if !c.mon.plans.Due(d.Key, now) {
			continue
		}
		h := c.mon.handles[d.Key]
		if !planHandleRe.MatchString(h) {
			c.mon.plans.Explained(d.Key, "", now)
			continue
		}
		done++
		rows, err := c.q.Rows(ctx, c.db, "SELECT CAST(query_plan AS nvarchar(max)) FROM sys.dm_exec_query_plan("+h+")")
		if err != nil || len(rows) != 1 || rows[0][0] == nil {
			c.mon.plans.Explained(d.Key, "", now)
			if err != nil {
				errs = append(errs, "showplan: "+c.classify(err).Error())
			}
			continue
		}
		plan := RedactShowplan(*rows[0][0])
		hash, cost := ShowplanShape(plan)
		if c.mon.plans.Explained(d.Key, hash, now) {
			dbmon.RecordPlan(b, dbmon.SystemMSSQL, d.DB, d.Text, "xml", plan, hash, cost)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

var (
	xmlAttrRe = regexp.MustCompile(`\s([A-Za-z]+)="([^"]*)"`)
	// Attributes that hold values or SQL text with values.
	literalAttrs = map[string]bool{"ParameterCompiledValue": true, "ParameterRuntimeValue": true}
	sqlAttrs     = map[string]bool{"ScalarString": true, "StatementText": true, "ConstValue": true}
	// Attributes that change with data volume or compilation: removed for the shape hash.
	volatileAttrs = map[string]bool{"StatementSubTreeCost": true, "EstimateRows": true, "EstimateIO": true, "EstimateCPU": true,
		"AvgRowSize": true, "EstimatedTotalSubtreeCost": true, "TableCardinality": true, "EstimatedRowsRead": true,
		"EstimateRowsWithoutRowGoal": true, "CachedPlanSize": true, "CompileTime": true, "CompileCPU": true, "CompileMemory": true,
		"EstimateRebinds": true, "EstimateRewinds": true, "EstimatedExecutionMode": true, "StatementEstRows": true,
		"SerialRequiredMemory": true, "SerialDesiredMemory": true, "GrantedMemory": true, "MaxUsedMemory": true,
		"ParameterCompiledValue": true, "ParameterRuntimeValue": true, "QueryPlanHash": true, "RetrievedFromCache": true}
	subtreeCostRe = regexp.MustCompile(`StatementSubTreeCost="([0-9.eE+-]+)"`)
)

// RedactShowplan replaces literal values in a showplan XML document.
func RedactShowplan(xml string) string {
	return xmlAttrRe.ReplaceAllStringFunc(xml, func(m string) string {
		sm := xmlAttrRe.FindStringSubmatch(m)
		name, val := sm[1], sm[2]
		switch {
		case literalAttrs[name]:
			return ` ` + name + `="?"`
		case sqlAttrs[name]:
			return ` ` + name + `="` + html.EscapeString(sqlredact.Redact(html.UnescapeString(val))) + `"`
		}
		return m
	})
}

// ShowplanShape returns the hash of the plan without volatile attributes and the first statement's subtree cost.
func ShowplanShape(xml string) (string, float64) {
	shape := xmlAttrRe.ReplaceAllStringFunc(xml, func(m string) string {
		if volatileAttrs[xmlAttrRe.FindStringSubmatch(m)[1]] {
			return ""
		}
		return m
	})
	sum := sha256.Sum256([]byte(shape))
	cost := 0.0
	if m := subtreeCostRe.FindStringSubmatch(xml); m != nil {
		cost, _ = strconv.ParseFloat(m[1], 64)
	}
	return hex.EncodeToString(sum[:8]), cost
}

// SampleInterval implements integrations.Sampler.
func (c *collector) SampleInterval() time.Duration {
	qs := c.inst.Settings.QueryStats
	if !qs.SessionsEnabled() {
		return 0
	}
	return qs.EffectiveSampleInterval()
}

// Sample implements integrations.Sampler.
func (c *collector) Sample(ctx context.Context, b *integrations.Batch) error {
	if c.db == nil {
		return nil
	}
	rows, err := c.q.Rows(ctx, c.db, RequestsQuery)
	if err != nil {
		return c.classify(err)
	}
	sleeping, err := c.q.Rows(ctx, c.db, SleepingBlockersQuery)
	if err != nil {
		sleeping = nil // the requests alone are still a sample
	}
	RecordSessions(b, rows, sleeping)
	return nil
}

// RecordSessions records the sampled requests (rows of RequestsQuery) and sleeping blockers (SleepingBlockersQuery).
func RecordSessions(b *integrations.Batch, requests, sleeping [][]*string) {
	sessions := make([]dbmon.Session, 0, len(requests)+len(sleeping))
	for _, r := range requests {
		if len(r) < 11 || r[0] == nil {
			continue
		}
		v := func(i int) string {
			if r[i] == nil {
				return ""
			}
			return *r[i]
		}
		ms, _ := strconv.ParseFloat(v(7), 64)
		s := dbmon.Session{ID: v(0), DB: v(1), User: v(2), Application: v(3), Client: v(4), State: v(5), DurationMs: ms,
			Text: v(9), QueryID: v(10)}
		if w := v(6); w != "" {
			s.WaitType, s.WaitEvent = WaitCategory(w), w
		}
		if bl := v(8); bl != "" && bl != "0" {
			s.BlockedBy = []string{bl}
		}
		sessions = append(sessions, s)
	}
	for _, r := range sleeping {
		if len(r) < 6 || r[0] == nil {
			continue
		}
		v := func(i int) string {
			if r[i] == nil {
				return ""
			}
			return *r[i]
		}
		ms, _ := strconv.ParseFloat(v(5), 64)
		sessions = append(sessions, dbmon.Session{ID: v(0), DB: v(1), User: v(2), Application: v(3), Client: v(4),
			State: "idle in transaction", DurationMs: ms})
	}
	dbmon.RecordSessions(b, dbmon.SystemMSSQL, sessions)
}
