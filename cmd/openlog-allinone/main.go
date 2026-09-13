// Command openlog-allinone runs ingest, processor and api in one process (the
// `single` deployment profile), optionally applying migrations first.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/onuragtas/openlog/internal/admin"
	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/version"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("openlog-allinone %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	app.Main("openlog-allinone", func(ctx context.Context, cfg config.Config, adm *admin.Server, log *slog.Logger) error {
		if cfg.MigrateOnStart {
			if err := app.RunMigrate(ctx, cfg, log); err != nil {
				return err
			}
		}
		return app.RunAll(ctx,
			func(ctx context.Context) error { return app.RunIngest(ctx, cfg, adm, log) },
			func(ctx context.Context) error { return app.RunProcessor(ctx, cfg, adm, log) },
			func(ctx context.Context) error { return app.RunAPI(ctx, cfg, adm, log) },
		)
	})
}
