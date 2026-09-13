// Package admin implements the admin HTTP server: /healthz (liveness),
// /readyz (readiness, driven by registered checks) and /metrics (Prometheus).
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/onuragtas/openlog/internal/version"
)

// CheckFunc reports nil when a dependency is ready.
type CheckFunc func(ctx context.Context) error

// Server is the admin HTTP server. It is safe for concurrent use.
type Server struct {
	addr     string
	log      *slog.Logger
	registry *prometheus.Registry

	mu       sync.RWMutex
	checks   map[string]CheckFunc
	draining atomic.Bool

	srv *http.Server
}

// New creates an admin server with a fresh Prometheus registry that already
// includes Go runtime and process collectors.
func New(addr string, log *slog.Logger) *Server {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	s := &Server{addr: addr, log: log, registry: reg, checks: map[string]CheckFunc{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	s.srv = &http.Server{Addr: addr, Handler: version.Middleware(mux), ReadHeaderTimeout: 10 * time.Second}
	return s
}

// Registry is the registry services register their metrics on.
func (s *Server) Registry() *prometheus.Registry { return s.registry }

// Handler exposes the HTTP handler (used by tests).
func (s *Server) Handler() http.Handler { return s.srv.Handler }

// AddCheck registers a readiness check.
func (s *Server) AddCheck(name string, fn CheckFunc) {
	s.mu.Lock()
	s.checks[name] = fn
	s.mu.Unlock()
}

// SetDraining makes /readyz fail so load balancers stop sending traffic
// before the service shuts down.
func (s *Server) SetDraining() { s.draining.Store(true) }

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	// "version" is the product version (docs/contracts/releases-updates.md §1), not a check.
	status := map[string]string{"version": version.String()}
	ok := true
	if s.draining.Load() {
		status["shutdown"] = "draining"
		ok = false
	}
	s.mu.RLock()
	names := make([]string, 0, len(s.checks))
	for n := range s.checks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := s.checks[n](ctx); err != nil {
			status[n] = err.Error()
			ok = false
		} else {
			status[n] = "ok"
		}
	}
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(status)
}

// Start listens in the background. It returns an error if the address cannot be bound.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("admin server failed", "err", err)
		}
	}()
	s.log.Info("admin server listening", "addr", ln.Addr().String())
	return nil
}

// Shutdown stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }
