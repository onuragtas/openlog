package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/migrate/phase"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/version"
)

// HeartbeatInterval is how often a service records itself in component_heartbeats.
const HeartbeatInterval = 30 * time.Second

// heartbeatServices are the long-running services that must be counted before a contract
// migration may run (docs/contracts/releases-updates.md §6).
var heartbeatServices = map[string]bool{
	"openlog-ingest": true, "openlog-processor": true, "openlog-api": true, "openlog-allinone": true, "openlog-alert": true,
}

// startHeartbeat records this process in component_heartbeats every HeartbeatInterval when
// OPENLOG_POSTGRES_DSN is set. It uses its own one-connection pool and never blocks the service:
// failures (PostgreSQL down, table not migrated yet) are logged once and retried. The returned
// stop function removes the row so a stopped instance stops blocking contract migrations at once.
func startHeartbeat(cfg config.Config, service string, log *slog.Logger) (stop func()) {
	if !heartbeatServices[service] || strings.TrimSpace(cfg.Postgres.DSN) == "" {
		return func() {}
	}
	pool, err := postgres.Open(context.Background(), postgres.Options{
		DSN: cfg.Postgres.DSN, Password: cfg.Postgres.Password, MaxConns: 1, Application: service + "-heartbeat",
		TLSCAFile: cfg.Postgres.TLS.CAFile, TLSCertFile: cfg.Postgres.TLS.CertFile, TLSKeyFile: cfg.Postgres.TLS.KeyFile,
	})
	if err != nil {
		log.Warn("component heartbeat disabled", "err", err)
		return func() {}
	}
	inst := phase.Instance{Component: service, InstanceID: instanceID(), Version: version.String(), StartedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(ctx, pool, inst, log)
	}()
	return func() {
		cancel()
		<-done
		dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := postgres.DeleteHeartbeat(dctx, pool, inst.Component, inst.InstanceID); err != nil {
			log.Debug("cannot remove component heartbeat", "err", err)
		}
		dcancel()
		pool.Close()
	}
}

func runHeartbeat(ctx context.Context, pool *pgxpool.Pool, inst phase.Instance, log *slog.Logger) {
	failing := false
	lastPrune := time.Time{}
	beat := func() {
		bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err := postgres.UpsertHeartbeat(bctx, pool, inst)
		switch {
		case err != nil && ctx.Err() == nil:
			if !failing {
				log.Warn("component heartbeat failed; retrying", "err", err, "interval", HeartbeatInterval)
			}
			failing = true
		case err == nil:
			if failing {
				log.Info("component heartbeat recorded", "instance_id", inst.InstanceID)
			}
			failing = false
			if time.Since(lastPrune) > time.Hour {
				lastPrune = time.Now()
				_, _ = postgres.PruneHeartbeats(bctx, pool, 24*time.Hour)
			}
		}
	}
	beat()
	t := time.NewTicker(HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			beat()
		}
	}
}

// instanceID is the host name (the pod or container name) plus a random suffix, so a restarted
// process is a new instance and its predecessor's row simply ages out.
func instanceID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return host + "-" + hex.EncodeToString(b)
}
