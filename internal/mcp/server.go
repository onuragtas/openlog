package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/onuragtas/openlog/internal/version"
)

// instructions are sent to the model on initialize: what this server can answer and what it cannot.
const instructions = `openlog is an observability platform (logs, traces, metrics, APM, alerting, SLOs).
These tools are read-only and always answer for one organization: the one the API key belongs to.
Start from oql_schema when you need attribute names, run_oql for aggregates over any signal, and
search_logs for individual log records. Times are UTC; durations are milliseconds unless a field name
says otherwise. Query ranges are limited (31 days for raw data) and results are capped, so narrow the
range or add filters when a call reports a limit.`

// shutdownTimeout bounds the graceful shutdown of the HTTP transport (app.ShutdownTimeout).
const shutdownTimeout = 25 * time.Second

// Service serves the MCP tools of one openlog API. It is safe for concurrent use: each caller gets its
// own MCP server bound to that caller's credentials, and they share the HTTP client and metrics.
type Service struct {
	cfg    Config
	client *Client
	log    *slog.Logger

	calls   *prometheus.CounterVec
	latency *prometheus.HistogramVec
}

// NewService creates the service. reg may be nil (no metrics, e.g. the stdio transport).
func NewService(cfg Config, log *slog.Logger, reg prometheus.Registerer) *Service {
	s := &Service{
		cfg: cfg, client: NewClient(cfg), log: log,
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_mcp_tool_calls_total", Help: "MCP tool calls by tool and result.",
		}, []string{"tool", "result"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "openlog_mcp_tool_duration_seconds", Help: "MCP tool call latency by tool.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
		}, []string{"tool"}),
	}
	if reg != nil {
		reg.MustRegister(s.calls, s.latency)
	}
	return s
}

// Client exposes the API client (readiness checks).
func (s *Service) Client() *Client { return s.client }

// Server builds an MCP server whose tools act as cr. Credentials are bound here, once per session, so a
// tool handler can never be asked to use another caller's key.
func (s *Service) Server(cr Creds) *mcpsdk.Server {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name: "openlog", Title: "openlog observability", Version: version.String(),
	}, &mcpsdk.ServerOptions{Instructions: instructions, Logger: s.log})

	addTool(s, srv, cr, &mcpsdk.Tool{
		Name: "run_oql",
		Description: "Run an OQL query (openlog's NRQL-like query language) over logs, spans, transactions, metrics, " +
			"hosts or containers and return aggregates: a single row, one row per FACET group, a timeseries or a histogram. " +
			"Use it for counts, rates, percentiles and breakdowns. Limits: at most 31 days of raw data (400 days for the " +
			"metric rollup), 5 facets, 20 select items; a query that exceeds the organization's ClickHouse limits fails " +
			"with resource_exhausted, a slow one with timeout.",
	}, s.runOQL)

	addTool(s, srv, cr, &mcpsdk.Tool{
		Name: "oql_schema",
		Description: "List what OQL can query: event types with their attributes and types, the aggregate functions and " +
			"the keywords. With event_type it also samples the most frequent attribute and resource keys of the last hour " +
			"(and metric names for Metric), which is how to discover attributes that are not part of the fixed schema. " +
			"Call this before writing a query against unfamiliar data.",
	}, s.oqlSchema)

	addTool(s, srv, cr, &mcpsdk.Tool{
		Name: "list_services",
		Description: "List the APM services that reported traces in the range with their RED metrics: requests, " +
			"throughput per minute, errors, error rate, average and p50/p95/p99 latency in milliseconds, Apdex and a " +
			"throughput sparkline. Use it to find service names and to see which service is unhealthy.",
	}, s.listServices)

	addTool(s, srv, cr, &mcpsdk.Tool{
		Name: "service_summary",
		Description: "RED metrics of one APM service over the range (totals and a per-bucket series) together with its " +
			"transactions ordered by time consumed. Use it after list_services to see which endpoint of a service is slow " +
			"or failing. All counts are weighted by the trace sampling probability.",
	}, s.serviceSummary)

	addTool(s, srv, cr, &mcpsdk.Tool{
		Name: "list_incidents",
		Description: "List alert incidents of the organization, newest first, with their rule, severity, state, labels, " +
			"summary, value and threshold, plus counts by state. Use it to see what is currently firing.",
	}, s.listIncidents)

	addTool(s, srv, cr, &mcpsdk.Tool{
		Name: "get_incident",
		Description: "One alert incident with its timeline (opened, acknowledged, notes, resolved) and its notification " +
			"deliveries. Use it after list_incidents to see how an incident developed and who acted on it.",
	}, s.getIncident)

	addTool(s, srv, cr, &mcpsdk.Tool{
		Name: "search_logs",
		Description: "Search individual log records with structured filters, a body substring and paging, newest first. " +
			"Returns the timestamp, severity, body, service, host, trace and span ids of each record, plus any extra keys " +
			"asked for in 'columns'. Use it to read actual log lines; use run_oql to count or group them.",
	}, s.searchLogs)

	addTool(s, srv, cr, &mcpsdk.Tool{
		Name: "list_slos",
		Description: "List the service level objectives of the organization with, unless status is false, each one's error " +
			"budget over its rolling window: requests, good, bad, the SLI, the budget and how much of it is left, the burn " +
			"rate and whether the objective is met. Needs an openlog running with PostgreSQL authentication.",
	}, s.listSLOs)

	addTool(s, srv, cr, &mcpsdk.Tool{
		Name: "slo_status",
		Description: "Error budget of one SLO in detail: the budget over its rolling window, the multi-window burn rates " +
			"(14.4x over 1h/5m and 6x over 6h/30m, each with whether it is breaching) and the burndown series. Use it to " +
			"answer how fast a service is spending its budget.",
	}, s.sloStatus)

	return srv
}

// addTool registers one read-only tool: the JSON schema comes from In, the handler is bound to cr, and
// every call is counted and timed. Errors become tool errors (the model sees the message) rather than
// protocol errors, so a model can correct a bad argument or a too-wide range itself.
func addTool[In any](s *Service, srv *mcpsdk.Server, cr Creds, t *mcpsdk.Tool, h func(context.Context, Creds, In) (json.RawMessage, error)) {
	if t.Annotations == nil {
		t.Annotations = &mcpsdk.ToolAnnotations{}
	}
	t.Annotations.ReadOnlyHint = true
	mcpsdk.AddTool(srv, t, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, any, error) {
		start := time.Now()
		raw, err := h(ctx, cr, in)
		s.observe(t.Name, start, err)
		if err != nil {
			return nil, nil, err
		}
		return toolResult(raw)
	})
}

// observe records one tool call. The result label stays low-cardinality: "ok", the API's error code, or
// "error" for a local failure.
func (s *Service) observe(tool string, start time.Time, err error) {
	result := "ok"
	var ae *APIError
	switch {
	case errors.As(err, &ae):
		result = ae.Code
	case err != nil:
		result = "error"
	}
	s.calls.WithLabelValues(tool, result).Inc()
	s.latency.WithLabelValues(tool).Observe(time.Since(start).Seconds())
	if err != nil {
		s.log.Warn("mcp tool call failed", "tool", tool, "result", result, "err", err)
	}
}

// toolResult returns the API's JSON as both structured content and text, so clients that understand
// structured results and those that only read text both get the whole answer.
func toolResult(raw json.RawMessage) (*mcpsdk.CallToolResult, any, error) {
	var structured map[string]any
	if err := json.Unmarshal(raw, &structured); err != nil {
		// Every endpoint these tools call answers with a JSON object; anything else goes out as text.
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(raw)}}}, nil, nil
	}
	return &mcpsdk.CallToolResult{
		Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: string(raw)}},
		StructuredContent: structured,
	}, nil, nil
}

// RunStdio serves MCP on stdin/stdout until ctx is cancelled. Nothing else may write to stdout in this
// mode: the stream carries JSON-RPC messages (the binary logs to stderr).
func (s *Service) RunStdio(ctx context.Context) error {
	s.log.Info("mcp server ready", "transport", TransportStdio, "api_url", s.cfg.APIURL)
	err := s.Server(s.client.Creds("", "")).Run(ctx, &mcpsdk.StdioTransport{})
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || closedByPeer(err) {
		return nil
	}
	return err
}

// JSON-RPC codes of the SDK's "server is closing" and "client is closing" errors. The sentinels
// themselves live in an internal package of the SDK, so they are matched through the exported
// jsonrpc.Error wire code instead.
const (
	codeServerClosing = -32004
	codeClientClosing = -32003
)

// closedByPeer reports whether err is the stream being closed by the other side. That is how a stdio
// session normally ends — the editor that started the server exits — so it must not be reported as a
// failure, which would look like a crash in the client.
func closedByPeer(err error) bool {
	if errors.Is(err, mcpsdk.ErrConnectionClosed) {
		return true
	}
	var werr *jsonrpc.Error
	return errors.As(err, &werr) && (werr.Code == codeServerClosing || werr.Code == codeClientClosing)
}

// HTTPHandler serves the streamable HTTP transport at /mcp.
//
// The caller's own "Authorization: Bearer <key>" is used for every tool call of that session, so one
// deployment serves many organizations without ever mixing them; OPENLOG_MCP_API_KEY is only the
// fallback for requests that bring no key. A request with neither is refused here, before a session
// starts, instead of being sent to the API without credentials.
func (s *Service) HTTPHandler() http.Handler {
	streamable := mcpsdk.NewStreamableHTTPHandler(func(r *http.Request) *mcpsdk.Server {
		return s.Server(s.credsOf(r))
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true, Logger: s.log})

	mux := http.NewServeMux()
	mux.Handle("/mcp", s.requireKey(streamable))
	mux.Handle("/mcp/", s.requireKey(streamable))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONError(w, http.StatusNotFound, "not_found", "the MCP endpoint of this server is /mcp")
	})
	return version.Middleware(mux)
}

// credsOf reads the credentials of one HTTP request, falling back to the configured ones.
func (s *Service) credsOf(r *http.Request) Creds {
	return s.client.Creds(bearer(r.Header.Get("Authorization")), r.Header.Get("X-Openlog-Org-Id"))
}

func (s *Service) requireKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.credsOf(r).Key == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="openlog"`)
			writeJSONError(w, http.StatusUnauthorized, "unauthenticated",
				"send an openlog API key as \"Authorization: Bearer ola_…\"")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearer extracts the token of an Authorization header.
func bearer(header string) string {
	const prefix = "bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// RunHTTP serves the streamable HTTP transport until ctx is cancelled, then shuts down gracefully.
func (s *Service) RunHTTP(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.HTTPHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.log.Info("mcp server listening", "transport", TransportHTTP, "addr", ln.Addr().String(), "path", "/mcp", "api_url", s.cfg.APIURL)
	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(sctx)
	return err
}
