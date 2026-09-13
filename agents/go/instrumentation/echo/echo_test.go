package openlogecho_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	openlogecho "github.com/onuragtas/openlog/agents/go/instrumentation/echo"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestRoute(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	e := echo.New()
	e.Use(openlogecho.Middleware(openlogecho.WithTracerProvider(tp)))
	e.GET("/users/:id", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/5", nil))

	s := rec.Ended()[0]
	route := ""
	for _, a := range s.Attributes() {
		if a.Key == "http.route" {
			route = a.Value.AsString()
		}
	}
	if route != "/users/:id" || s.SpanKind() != trace.SpanKindServer {
		t.Errorf("name %q kind %v route %q", s.Name(), s.SpanKind(), route)
	}
}
