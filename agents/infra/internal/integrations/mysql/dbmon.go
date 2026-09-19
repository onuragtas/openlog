package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/dbmon"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/sqlredact"
)

// Query performance monitoring of MySQL and MariaDB (db-monitoring.md §3.2): statement deltas from
// performance_schema.events_statements_summary_by_digest, active session samples from performance_schema.threads
// with their current wait and InnoDB lock waits, and EXPLAIN FORMAT=JSON plans of the top statements.
//
// Required grants beyond the integration's: SELECT ON performance_schema.* (already required) — the digest table,
// threads, events_waits_current and data_lock_waits are all there. Statement digests must be enabled
// (performance_schema_consumer_statements_digest, ON by default in MySQL 8).

// DigestQuery reads the statements with the most cumulative time, without monitoring statements (over
// performance_schema/information_schema, SHOW, EXPLAIN: the agent's own and other tools'). QUERY_SAMPLE_TEXT (MySQL 8.0.3+) is a real
// execution of the digest: redacted and normalized it matches what APM stores for the same statement, which the
// DIGEST_TEXT (backticked identifiers, ? for every value) does not. MariaDB has no sample column.
func DigestQuery(limit int, sample bool) string {
	sampleCol := "''"
	if sample {
		sampleCol = "COALESCE(QUERY_SAMPLE_TEXT, '')"
	}
	return `SELECT COALESCE(SCHEMA_NAME, ''), DIGEST, COALESCE(DIGEST_TEXT, ''), ` + sampleCol + `, COUNT_STAR, SUM_TIMER_WAIT,
SUM_ROWS_SENT + SUM_ROWS_AFFECTED, SUM_ROWS_EXAMINED, SUM_ERRORS, SUM_NO_INDEX_USED + SUM_NO_GOOD_INDEX_USED
FROM performance_schema.events_statements_summary_by_digest
WHERE DIGEST IS NOT NULL AND COALESCE(SCHEMA_NAME, '') NOT IN ('performance_schema', 'information_schema', 'mysql', 'sys')
AND DIGEST_TEXT NOT LIKE '%performance_schema%' AND DIGEST_TEXT NOT LIKE '%information_schema%'
AND DIGEST_TEXT NOT LIKE 'SHOW %' AND DIGEST_TEXT NOT LIKE 'EXPLAIN %' 
ORDER BY SUM_TIMER_WAIT DESC LIMIT ` + strconv.Itoa(limit)
}

// SessionsQuery samples the foreground threads that are running something, with their current wait event.
const SessionsQuery = `SELECT t.PROCESSLIST_ID, COALESCE(t.PROCESSLIST_USER, ''), COALESCE(t.PROCESSLIST_HOST, ''), COALESCE(t.PROCESSLIST_DB, ''),
COALESCE(t.PROCESSLIST_COMMAND, ''), COALESCE(t.PROCESSLIST_STATE, ''), COALESCE(t.PROCESSLIST_TIME, 0), LEFT(COALESCE(t.PROCESSLIST_INFO, ''), 4096),
COALESCE(w.EVENT_NAME, ''), t.THREAD_ID
FROM performance_schema.threads t LEFT JOIN performance_schema.events_waits_current w ON w.THREAD_ID = t.THREAD_ID AND w.END_EVENT_ID IS NULL
WHERE t.TYPE = 'FOREGROUND' AND t.PROCESSLIST_ID IS NOT NULL AND t.PROCESSLIST_ID <> CONNECTION_ID()
AND t.PROCESSLIST_COMMAND NOT IN ('Sleep', 'Daemon', 'Binlog Dump', 'Binlog Dump GTID')
ORDER BY t.PROCESSLIST_TIME DESC LIMIT 200`

// LockWaitsQuery lists who waits for whom on InnoDB row locks (MySQL 8; older servers answer an error and the
// sample has no blocking edges).
const LockWaitsQuery = `SELECT r.PROCESSLIST_ID, b.PROCESSLIST_ID FROM performance_schema.data_lock_waits w
JOIN performance_schema.threads r ON r.THREAD_ID = w.REQUESTING_THREAD_ID
JOIN performance_schema.threads b ON b.THREAD_ID = w.BLOCKING_THREAD_ID`

// mysqlVolatile are the plan keys that change with data volume.
var mysqlVolatile = map[string]bool{"cost_info": true, "rows_examined_per_scan": true, "rows_produced_per_join": true,
	"filtered": true, "query_cost": true, "rows_examined_per_join": true}

// mysqlExplainable accepts read statements only: MySQL requires the statement's own privileges for EXPLAIN, and
// the monitoring user must not hold write privileges.
var mysqlExplainable = regexp.MustCompile(`(?is)^\s*(select|with|table)\b`)

type queryMon struct {
	stmts      dbmon.Tracker
	plans      dbmon.Plans
	lastDeltas []dbmon.Stat
	// sample: nil = unknown, then whether QUERY_SAMPLE_TEXT exists.
	sample *bool
	// samples hold the last QUERY_SAMPLE_TEXT per digest (for EXPLAIN; it is never sent unredacted).
	samples map[string]string
}

// collectQueryStats records the statement deltas and explains due top statements; errors make the collection
// partial.
func (c *collector) collectQueryStats(ctx context.Context, b *integrations.Batch) error {
	qs := c.inst.Settings.QueryStats
	topN := qs.TopN
	if topN <= 0 {
		topN = 20
	}
	if c.mon.sample == nil {
		_, rows, err := c.q.Rows(ctx, c.db, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = 'performance_schema' "+
			"AND TABLE_NAME = 'events_statements_summary_by_digest' AND COLUMN_NAME = 'QUERY_SAMPLE_TEXT'")
		if err != nil {
			return c.classify(err)
		}
		has := len(rows) == 1 && rows[0][0] != nil && *rows[0][0] != "0"
		c.mon.sample = &has
	}
	_, rows, err := c.q.Rows(ctx, c.db, DigestQuery(min(max(topN*5, 200), 1000), *c.mon.sample))
	if err != nil {
		return fmt.Errorf("statement digests: %w", c.classify(err))
	}
	cur := make([]dbmon.Stat, 0, len(rows))
	c.mon.samples = map[string]string{}
	for _, r := range rows {
		if len(r) < 10 {
			continue
		}
		v := func(i int) string {
			if r[i] == nil {
				return ""
			}
			return *r[i]
		}
		n := func(i int) int64 { x, _ := intOf(r[i]); return x }
		calls := n(4)
		if calls < max(int64(qs.MinCalls), 1) {
			continue
		}
		digest, text, sample := v(1), v(2), v(3)
		if sample != "" && !strings.HasSuffix(sample, "...") { // a truncated sample is not the statement
			text = sample
			c.mon.samples[digest+"\x00"+v(0)] = sample
		}
		pico, _ := strconv.ParseFloat(v(5), 64)
		cur = append(cur, dbmon.Stat{Key: digest + "\x00" + v(0), QueryID: digest, DB: v(0), Text: text, Calls: calls,
			TimeMs: pico / 1e9, Rows: n(6), RowsExamined: n(7), Errors: n(8), NoIndexUsed: n(9)})
	}
	iv, deltas := c.mon.stmts.Deltas(b.Now(), cur, topN)
	dbmon.RecordStats(b, dbmon.SystemMySQL, iv, deltas)
	c.mon.lastDeltas = deltas
	return c.explainTop(ctx, b)
}

// explainTop explains due top statements from their QUERY_SAMPLE_TEXT. MySQL's EXPLAIN FORMAT=JSON repeats the
// statement's conditions with their literal values, so every *condition string of the plan is redacted before it
// leaves the host.
func (c *collector) explainTop(ctx context.Context, b *integrations.Batch) error {
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
		sample := strings.TrimSuffix(strings.TrimSpace(c.mon.samples[d.Key]), ";")
		if sample == "" || !mysqlExplainable.MatchString(sample) || strings.Contains(sample, ";") {
			c.mon.plans.Explained(d.Key, "", now)
			continue
		}
		done++
		plan, err := c.explain(ctx, d.DB, sample)
		if err != nil {
			c.mon.plans.Explained(d.Key, "", now)
			errs = append(errs, "EXPLAIN: "+c.classify(err).Error())
			continue
		}
		plan, cost := redactMySQLPlan(plan)
		hash := dbmon.JSONPlanHash([]byte(plan), mysqlVolatile)
		if c.mon.plans.Explained(d.Key, hash, now) {
			dbmon.RecordPlan(b, dbmon.SystemMySQL, d.DB, d.Text, "json", plan, hash, cost)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// explain runs EXPLAIN FORMAT=JSON in the statement's schema. The connection is pinned for the USE: the pool has
// one connection (SetMaxOpenConns(1)), so the collection's other queries are unaffected once it is back.
func (c *collector) explain(ctx context.Context, schema, stmt string) (string, error) {
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if schema != "" {
		if _, err := conn.ExecContext(ctx, "USE `"+strings.ReplaceAll(schema, "`", "``")+"`"); err != nil {
			return "", err
		}
	}
	var plan string
	if err := conn.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+stmt).Scan(&plan); err != nil {
		return "", err
	}
	return plan, nil
}

// redactMySQLPlan redacts the literal values of every *condition string in the plan and returns the plan with the
// query_block's query cost.
func redactMySQLPlan(plan string) (string, float64) {
	var v any
	if err := json.Unmarshal([]byte(plan), &v); err != nil {
		return "", 0
	}
	v = redactConditions(v, "")
	out, err := json.Marshal(v)
	if err != nil {
		return "", 0
	}
	cost := 0.0
	if m, ok := v.(map[string]any); ok {
		if qb, ok := m["query_block"].(map[string]any); ok {
			if ci, ok := qb["cost_info"].(map[string]any); ok {
				if s, ok := ci["query_cost"].(string); ok {
					cost, _ = strconv.ParseFloat(s, 64)
				}
			}
		}
	}
	return string(out), cost
}

func redactConditions(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = redactConditions(e, k)
		}
	case []any:
		for i := range x {
			x[i] = redactConditions(x[i], key)
		}
	case string:
		if strings.HasSuffix(key, "condition") || key == "message" {
			return sqlredact.Redact(x)
		}
	}
	return v
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
	_, rows, err := c.q.Rows(ctx, c.db, SessionsQuery)
	if err != nil {
		return c.classify(err)
	}
	blockers := map[string][]string{}
	if _, lw, err := c.q.Rows(ctx, c.db, LockWaitsQuery); err == nil {
		for _, r := range lw {
			if len(r) == 2 && r[0] != nil && r[1] != nil {
				blockers[*r[0]] = append(blockers[*r[0]], *r[1])
			}
		}
	}
	RecordSessions(b, rows, blockers)
	return nil
}

// RecordSessions records the sampled threads (rows of SessionsQuery); blockers maps a processlist id to the ids
// holding the locks it waits for.
func RecordSessions(b *integrations.Batch, rows [][]*string, blockers map[string][]string) {
	sessions := make([]dbmon.Session, 0, len(rows))
	for _, r := range rows {
		if len(r) < 10 || r[0] == nil {
			continue
		}
		v := func(i int) string {
			if r[i] == nil {
				return ""
			}
			return *r[i]
		}
		secs, _ := intOf(r[6])
		s := dbmon.Session{ID: v(0), User: v(1), Client: hostOnly(v(2)), DB: v(3), State: strings.ToLower(v(4)),
			Text: v(7), DurationMs: float64(secs) * 1000, BlockedBy: blockers[v(0)]}
		// wait/io/table/sql/handler → type io, event table/sql/handler; wait/lock/table/sql/handler → lock, …
		if ev := v(8); strings.HasPrefix(ev, "wait/") {
			parts := strings.SplitN(strings.TrimPrefix(ev, "wait/"), "/", 2)
			s.WaitType = parts[0]
			if len(parts) == 2 {
				s.WaitEvent = parts[1]
			}
		}
		if len(s.BlockedBy) > 0 && s.WaitType == "" {
			s.WaitType, s.WaitEvent = "lock", "innodb/row"
		}
		sessions = append(sessions, s)
	}
	dbmon.RecordSessions(b, dbmon.SystemMySQL, sessions)
}

// hostOnly drops the client port of PROCESSLIST_HOST (10.0.0.7:51234 → 10.0.0.7).
func hostOnly(h string) string {
	if i := strings.LastIndexByte(h, ':'); i > 0 && !strings.Contains(h[i+1:], ".") {
		return h[:i]
	}
	return h
}
