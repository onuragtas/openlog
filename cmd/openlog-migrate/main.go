// Command openlog-migrate applies the embedded PostgreSQL and ClickHouse schema and creates the
// Kafka topics, then exits.
//
// Expand migrations always run. Contract migrations run only when every openlog instance seen in
// component_heartbeats during the last 5 minutes reports at least the version the migration
// requires (docs/contracts/releases-updates.md §6); otherwise they are skipped and retried by the
// next run.
//
//	openlog-migrate                  apply
//	openlog-migrate -plan            print what would run (and why), change nothing
//	openlog-migrate -force-contract  also apply contract migrations blocked by older instances
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"

	"github.com/onuragtas/openlog/internal/admin"
	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/version"
)

func main() {
	plan := flag.Bool("plan", false, "print which migrations would run and why contract migrations are skipped; change nothing")
	force := flag.Bool("force-contract", false, "apply contract migrations even when older instances are running (breaks them)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("openlog-migrate %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	app.Main("openlog-migrate", func(ctx context.Context, cfg config.Config, _ *admin.Server, log *slog.Logger) error {
		return app.RunMigrateWith(ctx, cfg, log, app.MigrateOptions{Plan: *plan, ForceContract: *force})
	})
}
