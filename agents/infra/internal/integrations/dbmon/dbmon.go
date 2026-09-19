// Package dbmon is the part of query performance monitoring shared by the database integrations (PostgreSQL,
// MySQL/MariaDB, SQL Server; db-monitoring.md §3): turning a server's cumulative per-statement counters into
// per-interval deltas, recording the three event kinds the backend routes to its db_* tables, and deciding when a
// statement's plan is explained and sent.
package dbmon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/sqlredact"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// Event names (the backend's processor.EventDB*).
const (
	EventQueryStats    = "openlog.db.query_stats"
	EventSessionSample = "openlog.db.session_sample"
	EventQueryPlan     = "openlog.db.query_plan"
)

// DB systems (OTel db.system.name).
const (
	SystemPostgreSQL = "postgresql"
	SystemMySQL      = "mysql"
	SystemMSSQL      = "mssql"
)

// Limits.
const (
	// MaxSessions bounds the sessions of one sample.
	MaxSessions = 200
	// MaxExplainsPerCollection bounds the EXPLAINs of one collection, so a collection stays within its timeout.
	MaxExplainsPerCollection = 3
	// PlanResendAfter sends an unchanged plan again, so the UI can tell a current plan from a stale one.
	PlanResendAfter = 24 * time.Hour
	// MaxPlanBytes: larger plans are not sent (the backend drops them anyway; half a document cannot be shown).
	MaxPlanBytes = 512 << 10
)

// Stat is one statement's cumulative counters as the server reports them.
type Stat struct {
	// Key identifies the statement across collections (e.g. queryid + database + role).
	Key                       string
	QueryID, DB, User, Text   string
	Calls, Rows, RowsExamined int64
	Errors, NoIndexUsed       int64
	BlocksHit, BlocksRead     int64
	TimeMs                    float64
}

// Tracker turns cumulative counters into deltas between two collections.
type Tracker struct {
	prev   map[string]Stat
	prevAt time.Time
}

// Deltas returns the interval length and the statements that ran in it, heaviest (time) first, at most topN.
// The first call only records a baseline. A statement whose calls went down was reset (pg_stat_statements_reset,
// eviction, a server restart) and is skipped for this interval.
func (t *Tracker) Deltas(now time.Time, cur []Stat, topN int) (time.Duration, []Stat) {
	prev, prevAt := t.prev, t.prevAt
	t.prev = make(map[string]Stat, len(cur))
	for _, s := range cur {
		t.prev[s.Key] = s
	}
	t.prevAt = now
	if prev == nil || prevAt.IsZero() || !now.After(prevAt) {
		return 0, nil
	}
	var out []Stat
	for _, s := range cur {
		p, ok := prev[s.Key]
		if !ok {
			// New since the last collection (or it entered the fetched top): its whole count is not this interval's.
			continue
		}
		d := Stat{Key: s.Key, QueryID: s.QueryID, DB: s.DB, User: s.User, Text: s.Text,
			Calls: s.Calls - p.Calls, Rows: s.Rows - p.Rows, RowsExamined: s.RowsExamined - p.RowsExamined, Errors: s.Errors - p.Errors,
			NoIndexUsed: s.NoIndexUsed - p.NoIndexUsed, BlocksHit: s.BlocksHit - p.BlocksHit, BlocksRead: s.BlocksRead - p.BlocksRead,
			TimeMs: s.TimeMs - p.TimeMs}
		if d.Calls <= 0 || d.TimeMs < 0 || d.Rows < 0 {
			continue
		}
		d.RowsExamined, d.Errors, d.NoIndexUsed = max(d.RowsExamined, 0), max(d.Errors, 0), max(d.NoIndexUsed, 0)
		d.BlocksHit, d.BlocksRead = max(d.BlocksHit, 0), max(d.BlocksRead, 0)
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TimeMs > out[j].TimeMs })
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return now.Sub(prevAt), out
}

// RecordStats records one openlog.db.query_stats event per delta.
func RecordStats(b *integrations.Batch, system string, interval time.Duration, deltas []Stat) {
	secs := int64(interval.Round(time.Second) / time.Second)
	if secs <= 0 {
		secs = 1
	}
	for _, d := range deltas {
		attrs := []*commonpb.KeyValue{
			otlputil.Str("db.system.name", system), otlputil.Str("db.query.text", sqlredact.Redact(d.Text)),
			otlputil.Int("openlog.db.interval_seconds", secs), otlputil.Int("openlog.db.calls", d.Calls),
			otlputil.Double("openlog.db.total_time_ms", d.TimeMs), otlputil.Int("openlog.db.rows", d.Rows),
		}
		attrs = appendStr(attrs, "db.namespace", d.DB)
		attrs = appendStr(attrs, "openlog.db.user", d.User)
		attrs = appendStr(attrs, "openlog.db.query.id", d.QueryID)
		attrs = appendInt(attrs, "openlog.db.rows_examined", d.RowsExamined)
		attrs = appendInt(attrs, "openlog.db.errors", d.Errors)
		attrs = appendInt(attrs, "openlog.db.no_index_used", d.NoIndexUsed)
		attrs = appendInt(attrs, "openlog.db.blocks_hit", d.BlocksHit)
		attrs = appendInt(attrs, "openlog.db.blocks_read", d.BlocksRead)
		b.Event(EventQueryStats, "", attrs...)
	}
}

// Session is one non-idle session at a sampling instant.
type Session struct {
	ID, DB, User, State, WaitType, WaitEvent string
	QueryID, Text, Application, Client       string
	DurationMs                               float64
	BlockedBy                                []string
}

// RecordSessions records one openlog.db.session_sample event per session (at most MaxSessions).
func RecordSessions(b *integrations.Batch, system string, sessions []Session) {
	for i, s := range sessions {
		if i >= MaxSessions {
			break
		}
		attrs := []*commonpb.KeyValue{otlputil.Str("db.system.name", system), otlputil.Str("openlog.db.session.id", s.ID)}
		attrs = appendStr(attrs, "db.namespace", s.DB)
		attrs = appendStr(attrs, "openlog.db.user", s.User)
		attrs = appendStr(attrs, "openlog.db.session.state", s.State)
		attrs = appendStr(attrs, "openlog.db.wait.type", s.WaitType)
		attrs = appendStr(attrs, "openlog.db.wait.event", s.WaitEvent)
		attrs = appendStr(attrs, "openlog.db.query.id", s.QueryID)
		if s.Text != "" {
			attrs = appendStr(attrs, "db.query.text", sqlredact.Redact(s.Text))
		}
		attrs = appendStr(attrs, "openlog.db.application", s.Application)
		attrs = appendStr(attrs, "client.address", s.Client)
		if s.DurationMs > 0 {
			attrs = append(attrs, otlputil.Double("openlog.db.duration_ms", s.DurationMs))
		}
		if len(s.BlockedBy) > 0 {
			attrs = append(attrs, otlputil.StrSlice("openlog.db.blocking_session_ids", s.BlockedBy))
		}
		b.Event(EventSessionSample, "", attrs...)
	}
}

// Plans decides when statements are explained and when a plan is sent.
type Plans struct {
	// Interval is how often one statement is explained again.
	Interval time.Duration
	state    map[string]planState
}

type planState struct {
	explainedAt time.Time
	hash        string
	sentAt      time.Time
}

// Due reports whether the statement with key should be explained now.
func (p *Plans) Due(key string, now time.Time) bool {
	st, ok := p.state[key]
	return !ok || now.Sub(st.explainedAt) >= p.Interval
}

// Explained records an attempt (successful or not) and reports whether a plan with hash must be sent: a new
// shape, or the same shape last sent PlanResendAfter ago. An empty hash (the statement cannot be explained) is
// only recorded, so it is not retried before Interval.
func (p *Plans) Explained(key, hash string, now time.Time) bool {
	if p.state == nil {
		p.state = map[string]planState{}
	}
	st := p.state[key]
	st.explainedAt = now
	send := hash != "" && (hash != st.hash || now.Sub(st.sentAt) >= PlanResendAfter)
	if send {
		st.hash, st.sentAt = hash, now
	}
	p.state[key] = st
	// Forget statements not explained for a long time (they left the top).
	if len(p.state) > 2000 {
		for k, s := range p.state {
			if now.Sub(s.explainedAt) > 2*PlanResendAfter {
				delete(p.state, k)
			}
		}
	}
	return send
}

// RecordPlan records an openlog.db.query_plan event.
func RecordPlan(b *integrations.Batch, system, db, text, format, plan, hash string, cost float64) {
	if len(plan) > MaxPlanBytes {
		return
	}
	attrs := []*commonpb.KeyValue{otlputil.Str("db.system.name", system), otlputil.Str("db.query.text", sqlredact.Redact(text)),
		otlputil.Str("openlog.db.plan.format", format), otlputil.Str("openlog.db.plan.hash", hash), otlputil.Double("openlog.db.plan.cost", cost)}
	attrs = appendStr(attrs, "db.namespace", db)
	b.Event(EventQueryPlan, plan, attrs...)
}

// JSONPlanHash hashes a JSON plan without the keys that change with data volume (costs, row and width estimates,
// actual times): what remains is the plan's shape. It returns "" when the document is not JSON.
func JSONPlanHash(plan []byte, volatile map[string]bool) string {
	var v any
	if err := json.Unmarshal(plan, &v); err != nil {
		return ""
	}
	v = stripKeys(v, volatile)
	norm, err := json.Marshal(v) // maps marshal with sorted keys
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(norm)
	return hex.EncodeToString(sum[:8])
}

func stripKeys(v any, drop map[string]bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if !drop[k] {
				out[k] = stripKeys(e, drop)
			}
		}
		return out
	case []any:
		for i := range x {
			x[i] = stripKeys(x[i], drop)
		}
		return x
	}
	return v
}

func appendStr(attrs []*commonpb.KeyValue, k, v string) []*commonpb.KeyValue {
	if v == "" {
		return attrs
	}
	return append(attrs, otlputil.Str(k, v))
}

func appendInt(attrs []*commonpb.KeyValue, k string, v int64) []*commonpb.KeyValue {
	if v == 0 {
		return attrs
	}
	return append(attrs, otlputil.Int(k, v))
}

// FormatInt is strconv.FormatInt for ids read as integers.
func FormatInt(v int64) string { return strconv.FormatInt(v, 10) }
