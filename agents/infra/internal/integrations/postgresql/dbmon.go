package postgresql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/dbmon"
)

// Query performance monitoring of PostgreSQL (db-monitoring.md §3.1): statement deltas from pg_stat_statements,
// active session samples from pg_stat_activity and generic plans of the top statements.

// fetchStatements is how many statements (by cumulative time) a collection reads to compute interval deltas: more
// than top_n, because the statements that were heaviest in the last interval are not necessarily the heaviest
// since the last reset.
func fetchStatements(topN int) int { return min(max(topN*5, 200), 1000) }

// ownStatement recognizes the monitoring statements of this integration (and of other monitoring tools): statements
// over the statistics views and catalogs are not the application's workload. Matching the text rather than the
// role keeps an application's statements visible when the agent is configured with the application's role.
var ownStatement = regexp.MustCompile(`(?i)\bpg_(stat_\w+|catalog\.|database\b|extension\b|locks\b|class\b|roles\b|blocking_pids|settings\b)|^\s*(show|explain|begin|rollback|set local)\b`)

// recordStatementEvents turns the fetched pg_stat_statements rows into openlog.db.query_stats events.
func (c *collector) recordStatementEvents(b *integrations.Batch, rows [][]any, topN int) {
	var cur []dbmon.Stat
	seen := map[string]bool{}
	for _, r := range rows {
		if len(r) < 9 || r[0] == nil || str(r[8]) == insufficientPrivilege || ownStatement.MatchString(str(r[8])) {
			continue
		}
		s := dbmon.Stat{QueryID: str(r[0]), DB: str(r[1]), User: str(r[2]), Calls: i64(r[3]), Rows: i64(r[4]), TimeMs: f64(r[5]),
			BlocksHit: i64(r[6]), BlocksRead: i64(r[7]), Text: str(r[8])}
		s.Key = s.QueryID + "\x00" + s.DB + "\x00" + s.User
		if seen[s.Key] { // track = all: the top-level row (larger total) comes first
			continue
		}
		seen[s.Key] = true
		cur = append(cur, s)
	}
	iv, deltas := c.stmts.Deltas(b.Now(), cur, topN)
	dbmon.RecordStats(b, dbmon.SystemPostgreSQL, iv, deltas)
	c.lastDeltas = deltas
}

// explainable accepts read statements: SELECT, WITH, VALUES, TABLE. PostgreSQL checks the statement's privileges
// when it plans it, so explaining an UPDATE needs UPDATE on the table — a read-only monitoring role (pg_monitor +
// pg_read_all_data) can explain reads only. Utility statements (CREATE, VACUUM, SET, …) are never explained.
var explainable = regexp.MustCompile(`(?is)^\s*(select|with|values|table)\b`)

var hasParam = regexp.MustCompile(`\$[0-9]`)

// pgVolatile are the plan keys that change with data volume and timing.
var pgVolatile = map[string]bool{"Startup Cost": true, "Total Cost": true, "Plan Rows": true, "Plan Width": true,
	"Actual Startup Time": true, "Actual Total Time": true, "Actual Rows": true, "Actual Loops": true}

// explainTop explains up to dbmon.MaxExplainsPerCollection of the last interval's heaviest statements that are due.
// PostgreSQL 16+ plans parameterized statements with GENERIC_PLAN; older servers only statements without $n.
// Every EXPLAIN runs in a READ ONLY transaction that is rolled back, with a statement timeout: EXPLAIN without
// ANALYZE never executes the statement, the transaction guards against anything that would.
func (c *collector) explainTop(ctx context.Context, b *integrations.Batch) error {
	qs := c.inst.Settings.QueryStats
	if !qs.ExplainEnabled() {
		return nil
	}
	c.plans.Interval = qs.EffectiveExplainInterval()
	now := b.Now()
	done := 0
	var errs []string
	for _, d := range c.lastDeltas {
		if done >= dbmon.MaxExplainsPerCollection || ctx.Err() != nil {
			break
		}
		key := d.QueryID + "\x00" + d.DB
		if !c.plans.Due(key, now) {
			continue
		}
		text := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(d.Text), ";"))
		generic := c.version >= 160000
		if !explainable.MatchString(text) || strings.Contains(text, ";") || (!generic && hasParam.MatchString(text)) {
			c.plans.Explained(key, "", now)
			continue
		}
		done++
		plan, err := c.explain(ctx, d.DB, text, generic)
		if err != nil {
			c.plans.Explained(key, "", now)
			errs = append(errs, fmt.Sprintf("EXPLAIN in %s: %v", d.DB, err))
			continue
		}
		hash := dbmon.JSONPlanHash([]byte(plan), pgVolatile)
		if c.plans.Explained(key, hash, now) {
			dbmon.RecordPlan(b, dbmon.SystemPostgreSQL, d.DB, d.Text, "json", plan, hash, pgPlanCost(plan))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c *collector) explain(ctx context.Context, db, text string, generic bool) (string, error) {
	cn, err := c.conn(ctx, db)
	if err != nil {
		return "", err
	}
	if db != c.mainDB() {
		defer func() {
			_ = cn.Close(ctx)
			delete(c.conns, db)
		}()
	}
	if _, err := cn.Query(ctx, "BEGIN TRANSACTION READ ONLY"); err != nil {
		return "", err
	}
	defer func() { _, _ = cn.Query(context.WithoutCancel(ctx), "ROLLBACK") }()
	ms := max(int(min(c.inst.Timeout, 5*time.Second)/time.Millisecond), 100)
	if _, err := cn.Query(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", ms)); err != nil {
		return "", err
	}
	opts := "FORMAT JSON"
	if generic {
		opts = "GENERIC_PLAN, FORMAT JSON"
	}
	stmt := "EXPLAIN (" + opts + ") " + text
	if rt, ok := cn.(rawTexter); ok {
		rows, err := rt.RawText(ctx, stmt)
		if err != nil {
			return "", err
		}
		if len(rows) != 1 || len(rows[0]) != 1 {
			return "", errors.New("unexpected EXPLAIN result")
		}
		return rows[0][0], nil
	}
	rows, err := cn.Query(ctx, stmt)
	if err != nil {
		return "", err
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return "", errors.New("unexpected EXPLAIN result")
	}
	return str(rows[0][0]), nil
}

// pgPlanCost reads the root node's Total Cost of an EXPLAIN (FORMAT JSON) document.
func pgPlanCost(plan string) float64 {
	var doc []struct {
		Plan struct {
			TotalCost float64 `json:"Total Cost"`
		} `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(plan), &doc); err != nil || len(doc) == 0 {
		return 0
	}
	return doc[0].Plan.TotalCost
}

// SessionsQuery samples the client sessions that are not idle. query_id exists from PostgreSQL 14 (and is only
// filled with compute_query_id). The duration is the running statement's, or for idle in transaction the time
// since the transaction's last statement ended.
func SessionsQuery(version int) string {
	qid := "''"
	if version >= 140000 {
		qid = "COALESCE(query_id::text, '')"
	}
	return `SELECT pid::text, COALESCE(datname, ''), COALESCE(usename, ''), COALESCE(application_name, ''),
COALESCE(host(client_addr), ''), COALESCE(state, ''), COALESCE(wait_event_type, ''), COALESCE(wait_event, ''),
COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - CASE WHEN state = 'active' THEN query_start ELSE state_change END)) * 1000, 0)::float8,
array_to_string(pg_blocking_pids(pid), ','), left(COALESCE(query, ''), 4096), ` + qid + `
FROM pg_stat_activity
WHERE backend_type = 'client backend' AND state IS NOT NULL AND state <> 'idle' AND pid <> pg_backend_pid()
ORDER BY 9 DESC LIMIT ` + fmt.Sprint(dbmon.MaxSessions)
}

// SampleInterval implements integrations.Sampler.
func (c *collector) SampleInterval() time.Duration {
	qs := c.inst.Settings.QueryStats
	if !qs.SessionsEnabled() {
		return 0
	}
	return qs.EffectiveSampleInterval()
}

// Sample implements integrations.Sampler: one sample of the active sessions on the main connection.
func (c *collector) Sample(ctx context.Context, b *integrations.Batch) error {
	main, ok := c.conns[c.mainDB()]
	if !ok || c.version == 0 {
		return nil // no successful collection yet
	}
	rows, err := main.Query(ctx, SessionsQuery(c.version))
	if err != nil {
		return c.classify(err, "")
	}
	RecordSessions(b, rows)
	return nil
}

// RecordSessions records the sampled sessions (rows of SessionsQuery).
func RecordSessions(b *integrations.Batch, rows [][]any) {
	sessions := make([]dbmon.Session, 0, len(rows))
	for _, r := range rows {
		if len(r) < 12 {
			continue
		}
		s := dbmon.Session{ID: str(r[0]), DB: str(r[1]), User: str(r[2]), Application: str(r[3]), Client: str(r[4]),
			State: str(r[5]), WaitType: str(r[6]), WaitEvent: str(r[7]), DurationMs: f64(r[8]), Text: str(r[10]), QueryID: str(r[11])}
		if s.State != "active" && s.State != "idle in transaction" && s.State != "idle in transaction (aborted)" {
			// fastpath function call, disabled: no statement of interest
			s.Text = ""
		}
		if ids := str(r[9]); ids != "" {
			s.BlockedBy = strings.Split(ids, ",")
		}
		sessions = append(sessions, s)
	}
	dbmon.RecordSessions(b, dbmon.SystemPostgreSQL, sessions)
}
