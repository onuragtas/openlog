// Package openlogchi instruments github.com/go-chi/chi/v5 routers; http.route is the matched
// chi route pattern (e.g. /users/{id}).
//
//	r := chi.NewRouter()
//	r.Use(openlogchi.Middleware())
package openlogchi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/onuragtas/openlog/agents/go/openloghttp"
)

// Middleware returns chi middleware (register it with r.Use before the routes).
func Middleware(opts ...openloghttp.Option) func(http.Handler) http.Handler {
	return openloghttp.RouteMiddleware(func(r *http.Request) string {
		if rc := chi.RouteContext(r.Context()); rc != nil {
			return rc.RoutePattern()
		}
		return ""
	}, opts...)
}
