package openloggin_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	openloggin "github.com/onuragtas/openlog/agents/go/instrumentation/gin"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	r := gin.New()
	r.Use(openloggin.Middleware(openloggin.WithTracerProvider(tp)))
	r.GET("/users/:id", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/5", nil))

	s := rec.Ended()[0]
	route := ""
	for _, a := range s.Attributes() {
		if a.Key == "http.route" {
			route = a.Value.AsString()
		}
	}
	if route != "/users/:id" || s.SpanKind() != trace.SpanKindServer || s.Name() != "GET /users/:id" {
		t.Errorf("name %q kind %v route %q", s.Name(), s.SpanKind(), route)
	}
}
