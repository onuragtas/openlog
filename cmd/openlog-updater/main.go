// Command openlog-updater keeps a self-hosted openlog installation up to date
// (docs/operations/upgrading.md).
//
//	openlog-updater            Docker Compose: check every OPENLOG_UPDATER_INTERVAL (Docker socket required)
//	openlog-updater -k8s -once Kubernetes CronJob: one check/update of the chart's Deployments
//
// OPENLOG_UPDATER_MODE=off|notify|auto (default notify). Only releases whose index and manifest
// verify with a trusted key are considered.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/onuragtas/openlog/internal/admin"
	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/release"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/updater"
	"github.com/onuragtas/openlog/internal/updatereq"
	"github.com/onuragtas/openlog/internal/version"
)

func main() {
	k8s := flag.Bool("k8s", false, "update the Kubernetes Deployments of the Helm chart (in-cluster service account)")
	once := flag.Bool("once", false, "run one check/update and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("openlog-updater %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	app.Main("openlog-updater", func(ctx context.Context, cfg config.Config, _ *admin.Server, log *slog.Logger) error {
		return run(ctx, cfg, log, *k8s, *once)
	})
}

func run(ctx context.Context, cfg config.Config, log *slog.Logger, k8s, once bool) error {
	ucfg, err := updater.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	keys, err := release.TrustedKeys(ucfg.TrustedKeysFile)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		log.Warn("no trusted release keys: this build cannot verify releases (set OPENLOG_RELEASE_TRUSTED_KEYS_FILE or use a release build)")
	}

	var stores []updater.StatusStore
	var audit updater.Auditor
	var requests updatereq.Poller
	if !k8s {
		stores = append(stores, updater.FileStore{Path: filepath.Join(ucfg.BackupDir, updater.StatusFileName)})
	}
	if strings.TrimSpace(cfg.Postgres.DSN) != "" {
		pool, err := app.OpenPostgres(ctx, cfg, "openlog-updater")
		if err != nil {
			return err
		}
		defer pool.Close()
		stores = append(stores, updater.PostgresStore{Pool: pool})
		audit = updater.PostgresAuditor{Store: postgres.NewStore(pool), Log: log}
		// "Check now" / "Update now" from the UI (update_requests, D-041).
		requests = updatereq.PGQueue{Pool: pool}
	} else {
		log.Warn("OPENLOG_POSTGRES_DSN is not set: the updater status is not shown in GET /api/v1/version, not audited, and UI update requests are not received")
	}

	var engine updater.Engine
	if k8s {
		kube, err := updater.InClusterKube()
		if err != nil {
			return err
		}
		engine = &updater.K8sEngine{Cfg: ucfg, Kube: kube, Log: log}
	} else {
		dc, err := updater.NewDockerClient(ucfg.DockerHost)
		if err != nil {
			return err
		}
		ce := &updater.ComposeEngine{Cfg: ucfg, Docker: dc, Log: log}
		if err := ce.Init(ctx); err != nil {
			return err
		}
		log.Info("compose updater", "project", ce.Cfg.Project, "services", ucfg.Services, "mode", ucfg.Mode, "channel", ucfg.Channel)
		engine = ce
	}
	runner := &updater.Runner{
		Cfg: ucfg, Engine: engine, Source: release.NewFetcher(keys),
		Store: updater.MultiStore{Stores: stores, Log: log}, Audit: audit, Log: log, Requests: requests,
	}
	if once {
		if err := engine.Recover(ctx); err != nil {
			return err
		}
		// Kubernetes CronJob: requests made since the last run are handled first; a handled request
		// already includes a check.
		n, err := runner.RunRequests(ctx)
		if n > 0 {
			return err
		}
		if err != nil {
			log.Warn("cannot read update requests", "err", err)
		}
		return runner.RunOnce(ctx)
	}
	// The long-running Compose updater may replace its own container after an update (D-120); at start it completes
	// or undoes such a handover before acting.
	runner.SelfUpdate = !k8s
	return runner.Loop(ctx)
}
