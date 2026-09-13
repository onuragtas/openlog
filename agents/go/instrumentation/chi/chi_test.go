package openlogchi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	openlogchi "github.com/onuragtas/openlog/agents/go/instrumentation/chi"
	"github.com/onuragtas/openlog/agents/go/openloghttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRoutePattern(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	r := chi.NewRouter()
	r.Use(openlogchi.Middleware(openloghttp.WithTracerProvider(tp)))
	r.Route("/api", func(r chi.Router) {
		r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {})
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/users/9", nil))

	s := rec.Ended()[0]
	route := ""
	for _, a := range s.Attributes() {
		if a.Key == "http.route" {
			route = a.Value.AsString()
		}
	}
	if s.Name() != "GET /api/users/{id}" || route != "/api/users/{id}" {
		t.Errorf("name %q route %q", s.Name(), route)
	}
}
