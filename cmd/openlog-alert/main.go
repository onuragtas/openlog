// Command openlog-alert evaluates alert rules and delivers notifications (docs/contracts/alerting.md). Run any
// number of replicas: rules are shared through PostgreSQL leases.
//
//	openlog-alert                      run the evaluator and dispatcher
//	openlog-alert rotate-secrets       re-encrypt channel secrets with OPENLOG_SECRETS_KEY
//	openlog-alert rotate-secrets -check
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/logging"
	"github.com/onuragtas/openlog/internal/version"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("openlog-alert %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "rotate-secrets" {
		os.Exit(rotateSecrets(os.Args[2:]))
	}
	app.Main("openlog-alert", app.RunAlert)
}

func rotateSecrets(args []string) int {
	fs := flag.NewFlagSet("rotate-secrets", flag.ContinueOnError)
	check := fs.Bool("check", false, "only report how many channels are not encrypted with the current key")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "openlog-alert: configuration error: %v\n", err)
		return 2
	}
	log := logging.New(cfg.LogLevel, "openlog-alert")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rotated, failed, pending, err := app.RotateAlertSecrets(ctx, cfg, *check, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "openlog-alert rotate-secrets: %v\n", err)
		return 1
	}
	if *check {
		fmt.Printf("channels not encrypted with the current key: %d\n", pending)
		if pending > 0 {
			return 3
		}
		return 0
	}
	fmt.Printf("re-encrypted %d channels; %d could not be decrypted with the configured keys\n", rotated, failed)
	if failed > 0 {
		return 1
	}
	return 0
}
