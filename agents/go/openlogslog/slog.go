// Package openlogslog bridges log/slog to openlog: records are exported as OTLP logs
// carrying the trace and span id of the context they were logged with, and can be
// duplicated to an existing handler (stdout JSON, …) with trace_id/span_id attributes.
package openlogslog

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the default instrumentation scope of exported records.
const ScopeName = "github.com/onuragtas/openlog/agents/go/openlogslog"

type config struct {
	name     string
	level    slog.Leveler
	provider log.LoggerProvider
	source   bool
}

// Option configures the bridge.
type Option func(*config)

// WithName sets the instrumentation scope name (default ScopeName).
func WithName(name string) Option { return func(c *config) { c.name = name } }

// WithLevel sets the minimum level exported to openlog (default slog.LevelInfo).
func WithLevel(l slog.Leveler) Option { return func(c *config) { c.level = l } }

// WithLoggerProvider uses p instead of the global LoggerProvider installed by openlog.Start.
func WithLoggerProvider(p log.LoggerProvider) Option { return func(c *config) { c.provider = p } }

// WithSource adds code.* source location attributes.
func WithSource(enabled bool) Option { return func(c *config) { c.source = enabled } }

func newConfig(opts []Option) config {
	c := config{name: ScopeName, level: slog.LevelInfo}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// NewHandler returns a handler that only exports to openlog.
//
//	slog.SetDefault(slog.New(openlogslog.NewHandler()))
func NewHandler(opts ...Option) slog.Handler {
	c := newConfig(opts)
	return &leveled{Handler: otelHandler(c), level: c.level}
}

func otelHandler(c config) slog.Handler {
	o := []otelslog.Option{otelslog.WithSource(c.source)}
	if c.provider != nil {
		o = append(o, otelslog.WithLoggerProvider(c.provider))
	}
	return otelslog.NewHandler(c.name, o...)
}

// Wrap returns a handler that exports records to openlog and also passes them to next with
// trace_id and span_id attributes added when the context has a valid span:
//
//	logger := slog.New(openlogslog.Wrap(slog.NewJSONHandler(os.Stdout, nil)))
//	logger.InfoContext(ctx, "order placed") // {"msg":"order placed","trace_id":"…","span_id":"…"}
func Wrap(next slog.Handler, opts ...Option) slog.Handler {
	c := newConfig(opts)
	return &fanout{next: next, otel: otelHandler(c), level: c.level}
}

// leveled applies a minimum level in front of the OTel handler.
type leveled struct {
	slog.Handler
	level slog.Leveler
}

func (h *leveled) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level.Level() && h.Handler.Enabled(ctx, l)
}

func (h *leveled) WithAttrs(as []slog.Attr) slog.Handler {
	return &leveled{Handler: h.Handler.WithAttrs(as), level: h.level}
}

func (h *leveled) WithGroup(name string) slog.Handler {
	return &leveled{Handler: h.Handler.WithGroup(name), level: h.level}
}

type fanout struct {
	next  slog.Handler
	otel  slog.Handler
	level slog.Leveler
}

func (h *fanout) otelEnabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level.Level() && h.otel.Enabled(ctx, l)
}

func (h *fanout) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l) || h.otelEnabled(ctx, l)
}

func (h *fanout) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.next.Enabled(ctx, r.Level) {
		nr := r
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			nr = r.Clone()
			nr.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
		}
		err = h.next.Handle(ctx, nr)
	}
	if h.otelEnabled(ctx, r.Level) {
		// The OTel handler takes trace/span ids from ctx itself.
		if oerr := h.otel.Handle(ctx, r); err == nil {
			err = oerr
		}
	}
	return err
}

func (h *fanout) WithAttrs(as []slog.Attr) slog.Handler {
	return &fanout{next: h.next.WithAttrs(as), otel: h.otel.WithAttrs(as), level: h.level}
}

func (h *fanout) WithGroup(name string) slog.Handler {
	return &fanout{next: h.next.WithGroup(name), otel: h.otel.WithGroup(name), level: h.level}
}
