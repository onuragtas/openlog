// Package openloggin instruments github.com/gin-gonic/gin engines (otelgin underneath);
// http.route is the gin route template (c.FullPath(), e.g. /users/:id).
//
//	r := gin.New()
//	r.Use(openloggin.Middleware())
package openloggin

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// Option configures the instrumentation (otelgin options).
type Option = otelgin.Option

// Re-exported otelgin options.
var (
	WithTracerProvider = otelgin.WithTracerProvider
	WithMeterProvider  = otelgin.WithMeterProvider
	WithPropagators    = otelgin.WithPropagators
	WithFilter         = otelgin.WithFilter
	WithGinFilter      = otelgin.WithGinFilter
)

// Middleware returns gin middleware; register it before the routes.
func Middleware(opts ...Option) gin.HandlerFunc {
	return otelgin.Middleware("", opts...)
}
