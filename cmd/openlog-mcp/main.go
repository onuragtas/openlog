// Command openlog-mcp serves openlog's read-only Model Context Protocol tools, so AI tools such as
// Claude Code and Cursor can query an openlog installation (docs/operations/mcp.md).
//
//	openlog-mcp                 stdio (default): one local client, key from OPENLOG_MCP_API_KEY
//	openlog-mcp -transport http streamable HTTP at /mcp, key from each request's Authorization header
//
// It is a front end of the query API: every tool call is a request to /api/v1 with the caller's API
// key, so the tenant scope, the role gate and the read-only ClickHouse user of openlog-api apply
// unchanged. It needs no ClickHouse, PostgreSQL or Kafka access of its own.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/onuragtas/openlog/internal/admin"
	"github.com/onuragtas/openlog/internal/mcp"
	"github.com/onuragtas/openlog/internal/version"
)

func main() {
	transport := flag.String("transport", "", "transport: stdio or http (default OPENLOG_MCP_TRANSPORT, else stdio)")
	addr := flag.String("addr", "", "listen address of the http transport (default OPENLOG_MCP_HTTP_ADDR)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("openlog-mcp %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	cfg, err := mcp.LoadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "openlog-mcp: configuration error: %v\n", err)
		os.Exit(2)
	}
	if *transport != "" {
		cfg.Transport = *transport
	}
	if *addr != "" {
		cfg.HTTPAddr = *addr
	}
	if cfg.Transport != mcp.TransportStdio && cfg.Transport != mcp.TransportHTTP {
		fmt.Fprintf(os.Stderr, "openlog-mcp: -transport must be %s or %s, got %q\n", mcp.TransportStdio, mcp.TransportHTTP, cfg.Transport)
		os.Exit(2)
	}
	// stdio carries JSON-RPC on stdout, so logs go to stderr in both transports (unlike the other
	// binaries, which log to stdout through internal/logging).
	log := newLogger(os.Getenv("OPENLOG_LOG_LEVEL"))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, log); err != nil && !isCancelled(err) {
		log.Error("service failed", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

func run(ctx context.Context, cfg mcp.Config, log *slog.Logger) error {
	if cfg.Transport == mcp.TransportStdio {
		// No admin server: a stdio server is a child process of one editor, not a deployment.
		return mcp.NewService(cfg, log, nil).RunStdio(ctx)
	}
	adm := admin.New(cfg.AdminAddr, log)
	svc := mcp.NewService(cfg, log, adm.Registry())
	adm.AddCheck("openlog_api", svc.Client().Ping)
	adm.AddCheck("mcp_listener", admin.ListenerCheck(cfg.HTTPAddr))
	if err := adm.Start(); err != nil {
		return fmt.Errorf("cannot start the admin server: %w", err)
	}
	go func() {
		<-ctx.Done()
		log.Info("shutdown signal received")
		adm.SetDraining()
	}()
	err := svc.RunHTTP(ctx, cfg.HTTPAddr)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = adm.Shutdown(sctx)
	cancel()
	return err
}

// newLogger is internal/logging.New writing to stderr.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	l := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})).With("service", "openlog-mcp")
	slog.SetDefault(l)
	return l
}

func isCancelled(err error) bool { return errors.Is(err, context.Canceled) }
