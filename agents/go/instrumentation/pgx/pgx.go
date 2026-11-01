// Package openlogpgx instruments github.com/jackc/pgx/v5, which pgxpool runs on and which openlogsql cannot see because it
// does not go through database/sql. Statements become the same client spans:
//
//	cfg, err := pgxpool.ParseConfig(dsn)
//	cfg.ConnConfig.Tracer = openlogpgx.NewTracer()
//	pool, err := pgxpool.NewWithConfig(ctx, cfg)
//
// Spans are only created inside an existing span (a request, a job) unless WithRootSpans is used. WithExplain also
// captures the execution plans of slow statements.
package openlogpgx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/onuragtas/openlog/agents/go/openlogsql"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope of database spans.
const ScopeName = "github.com/onuragtas/openlog/agents/go/instrumentation/pgx"

const system = "postgresql"

// pingStatement is what pgx sends for Ping, on every health check, and says nothing about the application.
const pingStatement = "-- ping"

type config struct {
	queryText openlogsql.QueryTextMode
	rootSpans bool
	tp        trace.TracerProvider
	explain   time.Duration
}

// Option configures the tracer.
type Option func(*config)

// WithQueryText selects how db.query.text is recorded. The default is sanitized, or OPENLOG_DB_QUERY_TEXT=sanitized|raw|off.
func WithQueryText(m openlogsql.QueryTextMode) Option { return func(c *config) { c.queryText = m } }

// WithRootSpans creates spans for statements executed without a parent span.
func WithRootSpans() Option { return func(c *config) { c.rootSpans = true } }

// WithExplain captures the execution plan of a SELECT slower than threshold, at most once an hour per statement. It runs a query
// the application did not ask for, so it is off unless asked for here and given a pool with ExplainWith.
func WithExplain(threshold time.Duration) Option { return func(c *config) { c.explain = threshold } }

// WithTracerProvider uses tp instead of the global provider.
func WithTracerProvider(tp trace.TracerProvider) Option { return func(c *config) { c.tp = tp } }

// Tracer implements pgx.QueryTracer, pgx.BatchTracer and pgx.CopyFromTracer. Set it as ConnConfig.Tracer.
type Tracer struct {
	cfg      config
	once     sync.Once
	otel     trace.Tracer
	explains explainer
}

var (
	_ pgx.QueryTracer    = (*Tracer)(nil)
	_ pgx.BatchTracer    = (*Tracer)(nil)
	_ pgx.CopyFromTracer = (*Tracer)(nil)
)

// NewTracer returns a tracer. One can be shared by every connection of a pool.
func NewTracer(opts ...Option) *Tracer {
	t := &Tracer{cfg: config{queryText: openlogsql.QueryTextModeFromEnv()}}
	for _, o := range opts {
		o(&t.cfg)
	}
	return t
}

func (t *Tracer) tracer() trace.Tracer {
	t.once.Do(func() {
		tp := t.cfg.tp
		if tp == nil {
			tp = otel.GetTracerProvider()
		}
		t.otel = tp.Tracer(ScopeName)
	})
	return t.otel
}

// A span of ours travels under its own key. Without one the end hooks have nothing to end, and trace.SpanFromContext there
// would hand them the caller's own span, ending the request.
type spanKey struct{}

type started struct {
	span  trace.Span
	start time.Time
	query string
	conn  *pgx.Conn
	// statements and failure collect a batch's statements as they come back.
	statements []string
	failure    error
}

func fromContext(ctx context.Context) *started {
	s, _ := ctx.Value(spanKey{}).(*started)
	return s
}

func (t *Tracer) start(ctx context.Context, conn *pgx.Conn, op string, extra ...attribute.KeyValue) (context.Context, *started) {
	if !t.cfg.rootSpans && !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx, nil
	}
	attrs := []attribute.KeyValue{attribute.String("db.system.name", system), attribute.String("db.system", system)}
	name := op
	if name == "" {
		name = system
	}
	if conn != nil {
		c := conn.Config()
		if c.Database != "" {
			attrs = append(attrs, attribute.String("db.namespace", c.Database))
			if op != "" {
				name = op + " " + c.Database
			}
		}
		// A leading slash is a unix socket, which is no address to draw a dependency to.
		if c.Host != "" && !strings.HasPrefix(c.Host, "/") {
			attrs = append(attrs, attribute.String("server.address", c.Host))
			if c.Port > 0 {
				attrs = append(attrs, attribute.Int("server.port", int(c.Port)))
			}
		}
	}
	if op != "" {
		attrs = append(attrs, attribute.String("db.operation.name", op))
	}
	attrs = append(attrs, extra...)
	ctx, span := t.tracer().Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	s := &started{span: span}
	return context.WithValue(ctx, spanKey{}, s), s
}

func (t *Tracer) queryAttr(query string) []attribute.KeyValue {
	if text := openlogsql.QueryText(query, system, t.cfg.queryText); text != "" {
		return []attribute.KeyValue{attribute.String("db.query.text", text)}
	}
	return nil
}

func end(s *started, err error) {
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.span.RecordError(err)
		s.span.SetStatus(codes.Error, err.Error())
		s.span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
	}
	s.span.End()
}

// TraceQueryStart implements pgx.QueryTracer.
func (t *Tracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == pingStatement {
		return ctx
	}
	ctx, s := t.start(ctx, conn, openlogsql.OperationName(data.SQL), t.queryAttr(data.SQL)...)
	if s != nil {
		s.start, s.query, s.conn = time.Now(), data.SQL, conn
	}
	return ctx
}

// TraceQueryEnd implements pgx.QueryTracer.
func (t *Tracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if s := fromContext(ctx); s != nil {
		t.maybeExplain(s, data.Err)
		end(s, data.Err)
	}
}

// TraceBatchStart implements pgx.BatchTracer.
func (t *Tracer) TraceBatchStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	var extra []attribute.KeyValue
	if data.Batch != nil {
		extra = append(extra, attribute.Int("db.operation.batch.size", data.Batch.Len()))
	}
	ctx, _ = t.start(ctx, conn, "BATCH", extra...)
	return ctx
}

// TraceBatchQuery implements pgx.BatchTracer. It runs once per statement, as its result is read, so a span each would
// measure the reading and not the statement. They go on the batch's one span instead.
func (t *Tracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	s := fromContext(ctx)
	if s == nil {
		return
	}
	s.statements = append(s.statements, data.SQL)
	if data.Err != nil && s.failure == nil {
		s.failure = data.Err
	}
}

// TraceBatchEnd implements pgx.BatchTracer.
func (t *Tracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	s := fromContext(ctx)
	if s == nil {
		return
	}
	s.span.SetAttributes(t.queryAttr(strings.Join(s.statements, "; "))...)
	err := data.Err
	if err == nil {
		err = s.failure
	}
	end(s, err)
}

// TraceCopyFromStart implements pgx.CopyFromTracer.
func (t *Tracer) TraceCopyFromStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	table := data.TableName.Sanitize()
	extra := append(t.queryAttr("COPY "+table+" FROM STDIN"), attribute.String("db.collection.name", table))
	ctx, _ = t.start(ctx, conn, "COPY", extra...)
	return ctx
}

// TraceCopyFromEnd implements pgx.CopyFromTracer.
func (t *Tracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	if s := fromContext(ctx); s != nil {
		end(s, data.Err)
	}
}
