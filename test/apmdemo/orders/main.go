// Command orders is the Go service of the openlog APM demo: net/http + database/sql (pgx)
// instrumented with OpenTelemetry (otelhttp, otelsql, otelslog), exporting OTLP http/protobuf.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var stdout = log.New(os.Stderr, "orders ", log.LstdFlags|log.Lmsgprefix)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown, err := setupOTel(ctx)
	if err != nil {
		stdout.Fatalf("otel setup: %v", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdown(sctx)
	}()

	db, err := openDB(ctx)
	if err != nil {
		stdout.Fatalf("database: %v", err)
	}
	defer db.Close()

	s := &server{db: db}
	mux := http.NewServeMux()
	s.handle(mux, "POST", "/orders", s.createOrder)
	s.handle(mux, "GET", "/orders/report", s.report)
	s.handle(mux, "GET", "/orders/recent", s.recent)
	s.handle(mux, "GET", "/orders/{id}", s.getOrder)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	addr := ":" + envOr("PORT", "8080")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	stdout.Printf("listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		stdout.Fatalf("http: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func setupOTel(ctx context.Context) (func(context.Context), error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_HEADERS") == "" && os.Getenv("OPENLOG_LICENSE_KEY") != "" {
		os.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "openlog-license-key="+os.Getenv("OPENLOG_LICENSE_KEY"))
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://openlog:4318")
	}

	// OTEL_RESOURCE_ATTRIBUTES / OTEL_SERVICE_NAME + telemetry.sdk.*.
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil {
		return nil, err
	}

	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(traceExp),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	logExp, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	global.SetLoggerProvider(lp)
	slog.SetDefault(otelslog.NewLogger("orders", otelslog.WithLoggerProvider(lp)))

	return func(ctx context.Context) {
		_ = tp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)
	}, nil
}

func openDB(ctx context.Context) (*sql.DB, error) {
	dsn := envOr("DATABASE_URL", "postgres://orders:orders@orders-db:5432/orders?sslmode=disable")
	dbHost, dbPort := envOr("DB_HOST", "orders-db"), envOr("DB_PORT", "5432")
	port, _ := strconv.Atoi(dbPort)

	db, err := otelsql.Open("pgx", dsn,
		otelsql.WithAttributes(
			semconv.DBSystemPostgreSQL,
			attribute.String("db.name", "orders"),
			attribute.String("db.namespace", "orders"),
			attribute.String("db.system.name", "postgresql"),
			semconv.ServerAddress(dbHost),
			semconv.ServerPort(port),
		),
		// otelsql >= 0.40 only emits db.query.text; keep the legacy db.statement as well.
		otelsql.WithAttributesGetter(func(_ context.Context, _ otelsql.Method, query string, _ []driver.NamedValue) []attribute.KeyValue {
			if query == "" {
				return nil
			}
			return []attribute.KeyValue{attribute.String("db.statement", query)}
		}),
		otelsql.WithSpanNameFormatter(spanName),
		otelsql.WithSpanOptions(otelsql.SpanOptions{
			OmitConnResetSession: true,
			OmitRows:             true,
			DisableErrSkip:       true,
			// Only DB calls inside a request: no root spans for startup/schema/pool work.
			SpanFilter: func(ctx context.Context, _ otelsql.Method, _ string, _ []driver.NamedValue) bool {
				return trace.SpanContextFromContext(ctx).IsValid()
			},
		}),
	)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)

	for attempt := 1; ; attempt++ {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = db.PingContext(pctx)
		cancel()
		if err == nil {
			break
		}
		if attempt >= 90 || ctx.Err() != nil {
			return nil, fmt.Errorf("database not reachable: %w", err)
		}
		stdout.Printf("waiting for database (%d): %v", attempt, err)
		time.Sleep(2 * time.Second)
	}

	schema := []string{
		`CREATE TABLE IF NOT EXISTS orders (
			id bigserial PRIMARY KEY,
			customer_id int NOT NULL,
			product_id int NOT NULL,
			quantity int NOT NULL,
			status text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`INSERT INTO orders (customer_id, product_id, quantity, status)
		 SELECT 1 + (g % 50), 1 + ((g * 7) % 20), 1 + (g % 4), (ARRAY['new', 'paid', 'shipped'])[1 + (g % 3)]
		 FROM generate_series(1, 100) AS g
		 WHERE NOT EXISTS (SELECT 1 FROM orders)`,
	}
	for _, q := range schema {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return nil, fmt.Errorf("schema: %w", err)
		}
	}
	stdout.Printf("database ready")
	return db, nil
}

// spanName turns "SELECT id, ... FROM orders ..." into "SELECT orders".
func spanName(_ context.Context, method otelsql.Method, query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return string(method)
	}
	op := strings.ToUpper(fields[0])
	for i, f := range fields {
		up := strings.ToUpper(f)
		if (up == "FROM" || up == "INTO" || up == "UPDATE") && i+1 < len(fields) {
			return op + " " + strings.Trim(fields[i+1], "(),;")
		}
	}
	return op
}

type server struct {
	db *sql.DB
}

// handle registers a route whose SERVER span is named after the route template
// ("GET /orders/{id}") and carries http.route.
func (s *server) handle(mux *http.ServeMux, method, route string, h http.HandlerFunc) {
	pattern := method + " " + route
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace.SpanFromContext(r.Context()).SetAttributes(semconv.HTTPRoute(route))
		h(w, r)
	})
	mux.Handle(pattern, otelhttp.NewHandler(inner, pattern,
		otelhttp.WithSpanNameFormatter(func(string, *http.Request) string { return pattern })))
}

type order struct {
	ID         int64  `json:"id"`
	CustomerID int    `json:"customer_id"`
	ProductID  int    `json:"product_id"`
	Quantity   int    `json:"quantity"`
	Status     string `json:"status"`
}

func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in order
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.CustomerID <= 0 || in.ProductID <= 0 || in.Quantity <= 0 {
		slog.WarnContext(ctx, "invalid order payload", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "customer_id, product_id and quantity are required"})
		return
	}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO orders (customer_id, product_id, quantity, status) VALUES ($1, $2, $3, 'new') RETURNING id`,
		in.CustomerID, in.ProductID, in.Quantity).Scan(&in.ID)
	if err != nil {
		fail(ctx, w, err)
		return
	}
	in.Status = "new"
	slog.InfoContext(ctx, "order created", "order.id", in.ID, "customer.id", in.CustomerID, "product.id", in.ProductID)
	writeJSON(w, http.StatusCreated, in)
}

func (s *server) getOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid order id"})
		return
	}
	if err := loadInventory(ctx, id); err != nil {
		slog.ErrorContext(ctx, "inventory lookup failed", "order.id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var o order
	err = s.db.QueryRowContext(ctx,
		`SELECT id, customer_id, product_id, quantity, status FROM orders WHERE id = $1`, id).
		Scan(&o.ID, &o.CustomerID, &o.ProductID, &o.Quantity, &o.Status)
	if errors.Is(err, sql.ErrNoRows) {
		slog.InfoContext(ctx, "order not found", "order.id", id)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "order not found"})
		return
	}
	if err != nil {
		fail(ctx, w, err)
		return
	}
	slog.InfoContext(ctx, "order loaded", "order.id", id)
	writeJSON(w, http.StatusOK, o)
}

// loadInventory simulates a sharded inventory backend; every 17th order hits an unavailable shard.
func loadInventory(ctx context.Context, id int64) error {
	if id%17 != 0 {
		return nil
	}
	err := fmt.Errorf("order %d: inventory shard %d unavailable", id, id%4)
	span := trace.SpanFromContext(ctx)
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, err.Error())
	return err
}

func (s *server) report(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var slept sql.NullString
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT pg_sleep(1.2 + random() * 0.6), count(*) FROM orders`).Scan(&slept, &count); err != nil {
		fail(ctx, w, err)
		return
	}
	slog.InfoContext(ctx, "report generated", "orders.count", count)
	writeJSON(w, http.StatusOK, map[string]int64{"orders": count})
}

func (s *server) recent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, status FROM orders WHERE customer_id = 42 AND status IN ('new', 'paid') ORDER BY created_at DESC LIMIT 10`)
	if err != nil {
		fail(ctx, w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			fail(ctx, w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "status": status})
	}
	if err := rows.Err(); err != nil {
		fail(ctx, w, err)
		return
	}
	slog.InfoContext(ctx, "recent orders listed", "count", len(out))
	writeJSON(w, http.StatusOK, out)
}

func fail(ctx context.Context, w http.ResponseWriter, err error) {
	span := trace.SpanFromContext(ctx)
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, err.Error())
	slog.ErrorContext(ctx, "request failed", "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
