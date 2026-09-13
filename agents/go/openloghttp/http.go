// Package openloghttp instruments net/http servers and clients (OpenTelemetry otelhttp
// underneath) and records http.route so openlog can group transactions by route.
package openloghttp

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// RouteKey is the semantic convention attribute for the matched route template.
const RouteKey = attribute.Key("http.route")

// Option configures the instrumentation (re-exported otelhttp options: WithFilter,
// WithTracerProvider, WithMeterProvider, WithPublicEndpointFn, …).
type Option = otelhttp.Option

// Re-exported otelhttp options that are commonly needed.
var (
	WithFilter           = otelhttp.WithFilter
	WithTracerProvider   = otelhttp.WithTracerProvider
	WithMeterProvider    = otelhttp.WithMeterProvider
	WithPropagators      = otelhttp.WithPropagators
	WithPublicEndpointFn = otelhttp.WithPublicEndpointFn
)

// Handler instruments h as a server endpoint with a fixed route template, e.g.
//
//	http.Handle("/users/", openloghttp.Handler(usersHandler, "/users/{id}"))
func Handler(h http.Handler, route string, opts ...Option) http.Handler {
	return RouteMiddleware(func(*http.Request) string { return route }, opts...)(h)
}

// HandlerFunc is Handler for a function.
func HandlerFunc(f func(http.ResponseWriter, *http.Request), route string, opts ...Option) http.Handler {
	return Handler(http.HandlerFunc(f), route, opts...)
}

// Middleware instruments a whole router. With Go 1.22+ http.ServeMux patterns the route is
// taken from the matched pattern (r.Pattern), e.g. "GET /users/{id}" → http.route "/users/{id}":
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("GET /users/{id}", getUser)
//	http.ListenAndServe(":8080", openloghttp.Middleware(mux))
func Middleware(h http.Handler, opts ...Option) http.Handler {
	return RouteMiddleware(nil, opts...)(h)
}

// RouteMiddleware returns middleware whose route is resolved by route after the wrapped
// handler has run (so routers can fill in the matched template first); a nil route or an
// empty result falls back to r.Pattern. Router integrations (chi, …) are built on it.
func RouteMiddleware(route func(*http.Request) string, opts ...Option) func(http.Handler) http.Handler {
	opts = append([]Option{otelhttp.WithSpanNameFormatter(spanName)}, opts...)
	return func(next http.Handler) http.Handler {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			rt := ""
			if route != nil {
				rt = route(r)
			}
			if rt == "" {
				rt = patternRoute(r.Pattern)
			}
			if rt != "" {
				SetRoute(r, rt)
			}
		})
		return otelhttp.NewHandler(inner, "http.server", opts...)
	}
}

// SetRoute records the route template on the current server span (name "METHOD route",
// attribute http.route) and on the HTTP server metrics of this request.
func SetRoute(r *http.Request, route string) {
	if route == "" {
		return
	}
	kv := RouteKey.String(route)
	span := trace.SpanFromContext(r.Context())
	if span.IsRecording() {
		span.SetName(r.Method + " " + route)
		span.SetAttributes(kv)
	}
	if l, ok := otelhttp.LabelerFromContext(r.Context()); ok {
		l.Add(kv)
	}
}

// patternRoute strips the optional method and host from a ServeMux pattern.
func patternRoute(p string) string {
	if p == "" {
		return ""
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[i:]
	}
	return ""
}

// spanName follows the HTTP semconv: "{method} {route}" when a route is known, else "{method}".
func spanName(_ string, r *http.Request) string {
	m := r.Method
	if m == "" {
		m = http.MethodGet
	}
	if rt := patternRoute(r.Pattern); rt != "" {
		return m + " " + rt
	}
	return m
}

// Transport wraps base (nil = http.DefaultTransport) so outgoing requests create client
// spans and carry W3C trace context headers.
func Transport(base http.RoundTripper, opts ...Option) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base, opts...)
}

// Client returns an *http.Client using Transport(http.DefaultTransport).
func Client(opts ...Option) *http.Client {
	return &http.Client{Transport: Transport(nil, opts...)}
}
