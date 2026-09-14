package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/onuragtas/openlog/agents/infra/internal/agent"
	"github.com/onuragtas/openlog/agents/infra/internal/config"
)

// runCluster runs the Kubernetes cluster-mode agent (kubernetes.mode: cluster, D-071). It has no update manager
// and no agent sync: it is not a host.
func runCluster(cfg *config.Config, ver string, log *slog.Logger, once bool) int {
	if err := cfg.Validate(!once); err != nil {
		fmt.Fprintln(os.Stderr, "invalid configuration:\n"+err.Error())
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := agent.NewCluster(cfg, ver, log, !once)
	if err != nil {
		log.Error("startup failed", "error", err)
		return 1
	}
	if once {
		if err := c.Once(ctx, os.Stdout); err != nil {
			log.Error("collection failed", "error", err)
			return 1
		}
		return 0
	}
	if err := c.Run(ctx); err != nil {
		log.Error("agent failed", "error", err)
		return 1
	}
	return 0
}
