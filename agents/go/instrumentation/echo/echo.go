// Package openlogecho instruments github.com/labstack/echo/v4 (otelecho underneath);
// http.route is the echo route template (c.Path(), e.g. /users/:id).
//
//	e := echo.New()
//	e.Use(openlogecho.Middleware())
package openlogecho

import (
	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
)

// Option configures the instrumentation (otelecho options).
type Option = otelecho.Option

// Re-exported otelecho options.
var (
	WithTracerProvider = otelecho.WithTracerProvider
	WithMeterProvider  = otelecho.WithMeterProvider
	WithPropagators    = otelecho.WithPropagators
	WithSkipper        = otelecho.WithSkipper
)

// Middleware returns echo middleware; register it before the routes.
func Middleware(opts ...Option) echo.MiddlewareFunc {
	return otelecho.Middleware("", opts...)
}
