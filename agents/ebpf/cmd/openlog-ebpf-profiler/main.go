// Command openlog-ebpf-profiler samples the CPU of a whole machine and exports OTLP profiles
// (docs/contracts/ebpf-profiler.md).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/onuragtas/openlog/agents/ebpf/internal/aggregate"
	"github.com/onuragtas/openlog/agents/ebpf/internal/attribute"
	"github.com/onuragtas/openlog/agents/ebpf/internal/config"
	"github.com/onuragtas/openlog/agents/ebpf/internal/export"
	"github.com/onuragtas/openlog/agents/ebpf/internal/profiler"
	"github.com/onuragtas/openlog/agents/ebpf/internal/sampler"
	"github.com/onuragtas/openlog/agents/ebpf/internal/symbol"
	"github.com/onuragtas/openlog/agents/ebpf/internal/version"
)

// exitConfig is returned for a failure that will not fix itself: the configuration is wrong, or this
// kernel refuses to be sampled (capabilities, perf_event_paranoid, a guest without perf events). The unit
// stops restarting on it (RestartPreventExitStatus), because retrying every ten seconds forever only
// costs the host: an operator has to act first.
const exitConfig = 78 // EX_CONFIG

func main() { os.Exit(run()) }

func run() int {
	showVersion := flag.Bool("version", false, "print version, commit and build date, then exit")
	selfTest := flag.Bool("self-test", false, "check the configuration and whether this kernel can be sampled, then exit; 0 on success")
	once := flag.Bool("once", false, "sample one interval, print what would be sent as text and exit (nothing is sent)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("openlog-ebpf-profiler %s (commit %s, built %s)\n", version.Current(), orUnknown(version.Commit), orUnknown(version.Date))
		return 0
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		// Refused rather than started: a profiler that samples a whole machine and throws every profile
		// away costs the host and delivers nothing.
		fmt.Fprintf(os.Stderr, "configuration: %v\n", err)
		return exitConfig
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level(cfg.LogLevel)}))

	s, err := sampler.New(cfg)
	if err != nil {
		// Says what is missing and exits non-zero rather than degrading into sampling nothing while
		// looking healthy (contract §3).
		fmt.Fprintf(os.Stderr, "cannot sample this machine: %v\n", err)
		return exitConfig
	}

	fs := attribute.OS{Root: cfg.HostRoot}
	namer := attribute.New(fs, cfg.RuntimeDir)
	hostID := attribute.ReadHostID(fs, cfg.RuntimeDir)

	if *selfTest {
		_ = s.Close()
		fmt.Printf("ok: %s, sampling at %d Hz every %s, host %s\n",
			cfg.Endpoint, cfg.Frequency, cfg.Interval, orUnknown(hostID))
		return 0
	}

	if *once {
		return runOnce(s, cfg, namer)
	}

	exp, err := export.New(export.Options{
		Endpoint: cfg.Endpoint,
		Headers:  map[string]string{"openlog-license-key": cfg.LicenseKey},
		Gzip:     cfg.Gzip,
		Timeout:  cfg.Timeout,
		Elapsed:  cfg.Interval,
		Initial:  time.Second,
		Max:      cfg.Interval / 4,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "exporter: %v\n", err)
		_ = s.Close()
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	log.Info("profiling started", "endpoint", cfg.Endpoint, "frequency_hz", cfg.Frequency,
		"interval", cfg.Interval, "host_id", hostID, "version", version.Current())

	if err := profiler.Run(ctx, profiler.Options{
		Config: cfg, Sampler: s, Exporter: exp,
		NewSymbolizer: func() aggregate.Symbolizer { return symbol.NewResolver(cfg.HostRoot) },
		Namer:         namer,
		HostID:        hostID, Version: version.Current(), Log: log,
	}); err != nil {
		log.Error("profiling stopped", "error", err)
		return 1
	}
	log.Info("profiling stopped")
	return 0
}

// runOnce prints one interval instead of sending it, so an operator can see what would be stored before
// pointing this at an ingest.
func runOnce(s sampler.Sampler, cfg config.Config, namer aggregate.Namer) int {
	defer func() { _ = s.Close() }()
	raw, err := s.Sample(context.Background(), cfg.Interval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sampling: %v\n", err)
		return 1
	}
	byService := aggregate.Run(raw, symbol.NewResolver(cfg.HostRoot), namer, cfg.PeriodNanos())
	if len(byService) == 0 {
		fmt.Println("no samples: nothing ran on a CPU during the interval")
		return 0
	}
	names := make([]string, 0, len(byService))
	for n := range byService {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		var total int64
		for _, sm := range byService[n] {
			total += sm.Value
		}
		fmt.Printf("%-32s %6d stacks  %10.3f CPU seconds\n", n, len(byService[n]), float64(total)/1e9)
	}
	return 0
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func level(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
