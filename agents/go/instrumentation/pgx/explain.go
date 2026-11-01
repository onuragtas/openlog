package openlogpgx

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/onuragtas/openlog/agents/go/openlogsql"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// The plan of a slow statement, from the application's own pool (apm.md §7.1). It is how a plan can exist with no credentials
// given to openlog, and the price is a query the application did not ask for, so everything here is bounded.

const (
	// explainEvery is how often one statement is explained. Its plan changes far less often than it runs.
	explainEvery = time.Hour
	// explainTimeout bounds waiting for a connection as well as planning.
	explainTimeout = 2 * time.Second
	// maxExplainStatements bounds what is remembered. Forgetting costs one more EXPLAIN.
	maxExplainStatements = 1024
	// maxPlanBytes bounds the attribute. A plan past this is a very large statement, and its top is what matters.
	maxPlanBytes = 16 << 10

	explainSpanName = "db.explain"
)

// Pool is what plans are read through. A *pgxpool.Pool is one.
type Pool interface {
	AcquireFunc(ctx context.Context, f func(*pgxpool.Conn) error) error
}

type explainer struct {
	mu        sync.Mutex
	querier   Pool
	seen      map[string]time.Time
	running   bool
	noGeneric bool
}

// ExplainWith gives the tracer the pool plans are read through. It takes a call of its own because the tracer has to be in the
// pool's configuration before the pool exists.
func (t *Tracer) ExplainWith(q Pool) {
	t.explains.mu.Lock()
	defer t.explains.mu.Unlock()
	t.explains.querier = q
}

// explainable refuses what must not reach EXPLAIN. A semicolon may only be inside a literal, but the protocol this is sent over runs
// everything it is given, so a statement holding one is not worth the doubt.
func explainable(sql string) bool {
	return openlogsql.OperationName(sql) == "SELECT" && !strings.Contains(strings.TrimRight(strings.TrimSpace(sql), ";"), ";")
}

var (
	placeholderRe = regexp.MustCompile(`\$\d`)
	planStringRe  = regexp.MustCompile(`'(?:[^']|'')*'`)
	// A number that is a value: not part of an identifier, and not the 1 of $1.
	planNumberRe = regexp.MustCompile(`(^|[^\w$.])\d+(?:\.\d+)?`)
)

// redactPlan removes literals from a text plan. Strings go everywhere. Numbers go only from the lines holding a condition,
// because a line's cost, rows and width are numbers too and are the point of having a plan.
func redactPlan(plan string) string {
	lines := strings.Split(plan, "\n")
	for i, line := range lines {
		line = planStringRe.ReplaceAllString(line, "'?'")
		if at := strings.Index(line, "Cond:"); at >= 0 {
			line = line[:at] + planNumberRe.ReplaceAllString(line[at:], "${1}?")
		} else if at := strings.Index(line, "Filter:"); at >= 0 {
			line = line[:at] + planNumberRe.ReplaceAllString(line[at:], "${1}?")
		}
		lines[i] = line
	}
	out := strings.Join(lines, "\n")
	if len(out) <= maxPlanBytes {
		return out
	}
	cut := strings.LastIndexByte(out[:maxPlanBytes], '\n')
	if cut <= 0 {
		cut = maxPlanBytes
	}
	return out[:cut]
}

// claim says whether this statement is due, and takes the one slot if so. Only one EXPLAIN runs at a time, so a database in
// trouble, when everything is slow, gets one extra query and not one per statement.
func (e *explainer) claim(key string, now time.Time) (Pool, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.querier == nil || e.running {
		return nil, false
	}
	if last, ok := e.seen[key]; ok && now.Sub(last) < explainEvery {
		return nil, false
	}
	if e.seen == nil || len(e.seen) >= maxExplainStatements {
		e.seen = map[string]time.Time{}
	}
	e.seen[key] = now
	e.running = true
	return e.querier, true
}

func (e *explainer) release() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.running = false
}

func (e *explainer) genericUnsupported() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.noGeneric
}

func (e *explainer) setGenericUnsupported() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.noGeneric = true
}

func (t *Tracer) maybeExplain(s *started, err error) {
	if t.cfg.explain <= 0 || err != nil || t.cfg.queryText == openlogsql.QueryOff || s.query == "" {
		return
	}
	if time.Since(s.start) < t.cfg.explain || !s.span.SpanContext().IsSampled() || !explainable(s.query) {
		return
	}
	generic := placeholderRe.MatchString(s.query)
	if generic && t.explains.genericUnsupported() {
		return
	}
	statement := openlogsql.Sanitize(s.query, system)
	querier, ok := t.explains.claim(statement, time.Now())
	if !ok {
		return
	}
	var database string
	if s.conn != nil {
		database = s.conn.Config().Database
	}
	parent := s.span.SpanContext()
	go func() {
		defer t.explains.release()
		t.explain(parent, querier, s.query, statement, database, generic)
	}()
}

func (t *Tracer) explain(parent trace.SpanContext, q Pool, sql, statement, database string, generic bool) {
	ctx, cancel := context.WithTimeout(context.Background(), explainTimeout)
	defer cancel()
	options := "FORMAT TEXT"
	if generic {
		options = "GENERIC_PLAN, FORMAT TEXT"
	}
	var lines []string
	// Straight to the wire, under pgx's own layer, for two reasons: pgx would want values for $1, and those are what is being kept
	// out, and it would call this tracer, and an EXPLAIN must not become a span.
	err := q.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
		results, err := c.Conn().PgConn().Exec(ctx, "EXPLAIN ("+options+") "+sql).ReadAll()
		if err != nil {
			return err
		}
		for _, result := range results {
			for _, row := range result.Rows {
				if len(row) > 0 {
					lines = append(lines, string(row[0]))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.noteExplainError(err)
		return
	}
	if len(lines) == 0 {
		return
	}
	_, span := t.tracer().Start(trace.ContextWithSpanContext(context.Background(), parent), explainSpanName,
		trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(
			attribute.String("openlog.db.plan", redactPlan(strings.Join(lines, "\n"))),
			attribute.String("openlog.db.plan.statement", statement),
			attribute.String("openlog.db.plan.system", system),
			attribute.String("openlog.db.plan.namespace", database),
			attribute.Bool("openlog.db.plan.generic", generic),
		))
	span.End()
}

// noteExplainError remembers a server too old for GENERIC_PLAN, which would otherwise be asked every hour for every statement.
// Every other failure is a plan that cannot be had, such as a temporary table on another connection, and is nobody's problem.
func (t *Tracer) noteExplainError(err error) {
	if strings.Contains(err.Error(), "GENERIC_PLAN") || strings.Contains(strings.ToLower(err.Error()), "unrecognized explain option") {
		t.explains.setGenericUnsupported()
	}
}
