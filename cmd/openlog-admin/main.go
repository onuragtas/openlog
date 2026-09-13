// Command openlog-admin manages PostgreSQL-backed tenancy from the command line:
//
//	openlog-admin migrate                      apply migrations/postgres
//	openlog-admin bootstrap                    ensure the organization, owner and keys from OPENLOG_BOOTSTRAP_*
//	openlog-admin create-owner --email E --org NAME [--tenant-id ID] [--name N] [--password-stdin] [--no-license-key]
//	openlog-admin reset-password --email E [--password-stdin]
//
// Connection settings come from the environment (OPENLOG_POSTGRES_DSN,
// OPENLOG_POSTGRES_PASSWORD; docs/contracts/config.md). Every command applies
// pending PostgreSQL migrations first.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/logging"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/version"
)

const usage = `usage:
  openlog-admin migrate
  openlog-admin bootstrap
  openlog-admin create-owner --email EMAIL --org NAME [--tenant-id ID] [--name NAME] [--password-stdin] [--no-license-key]
  openlog-admin reset-password --email EMAIL [--password-stdin]
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if a := os.Args[1]; a == "-version" || a == "--version" || a == "version" {
		fmt.Printf("openlog-admin %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "openlog-admin: configuration error: %v\n", err)
		os.Exit(2)
	}
	log := logging.New(cfg.LogLevel, "openlog-admin")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, log, os.Args[1], os.Args[2:], os.Stdin, os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "openlog-admin %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, log *slog.Logger, cmd string, args []string, stdin io.Reader, stdout io.Writer) error {
	switch cmd {
	case "migrate", "bootstrap", "create-owner", "reset-password":
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
	pool, err := app.OpenPostgres(ctx, cfg, "openlog-admin")
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := app.MigratePostgres(ctx, pool, log); err != nil {
		return err
	}
	svc := auth.NewService(postgres.NewStore(pool), auth.Config{}, log)

	switch cmd {
	case "migrate":
		return nil
	case "bootstrap":
		return bootstrap(ctx, svc, cfg.Bootstrap, log, stdout)
	case "create-owner":
		return createOwner(ctx, svc, args, stdin, stdout)
	default:
		return resetPassword(ctx, svc, args, stdin, stdout)
	}
}

func bootstrap(ctx context.Context, svc *auth.Service, b config.Bootstrap, log *slog.Logger, stdout io.Writer) error {
	if b.OwnerEmail == "" && b.LicenseKey == "" && b.APIKey == "" {
		log.Info("OPENLOG_BOOTSTRAP_OWNER_EMAIL is not set; nothing to bootstrap")
		return nil
	}
	for name, key := range map[string]string{"license key": b.LicenseKey, "API key": b.APIKey} {
		if key != "" && len(key) < 32 {
			log.Warn("bootstrap "+name+" is short; use generated keys outside development", "length", len(key))
		}
	}
	res, err := svc.Bootstrap(ctx, auth.BootstrapSpec{
		TenantID: b.TenantID, OrgName: b.OrgName, OwnerEmail: b.OwnerEmail, OwnerPassword: b.OwnerPassword,
		OwnerName: b.OwnerName, LicenseKey: b.LicenseKey, APIKey: b.APIKey,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "bootstrap: organization %q (tenant_id=%s, id=%s) created_org=%t created_user=%t added_owner=%t created_license_key=%t created_api_key=%t\n",
		res.Org.Name, res.Org.TenantID, res.Org.ID, res.CreatedOrg, res.CreatedUser, res.AddedOwner, res.CreatedLicenseKey, res.CreatedAPIKey)
	return nil
}

func readPassword(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func generatePassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func createOwner(ctx context.Context, svc *auth.Service, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("create-owner", flag.ContinueOnError)
	email := fs.String("email", "", "owner email (required)")
	org := fs.String("org", "", "organization name (required)")
	tenantID := fs.String("tenant-id", "", "tenant id (ClickHouse key); generated when empty")
	name := fs.String("name", "", "owner display name")
	pwStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin (otherwise one is generated and printed)")
	noKey := fs.Bool("no-license-key", false, "do not create an ingest license key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || *org == "" {
		fs.Usage()
		return errors.New("--email and --org are required")
	}
	if *tenantID == "" {
		id, err := auth.NewTenantID()
		if err != nil {
			return err
		}
		*tenantID = id
	}
	password, generated := "", false
	if *pwStdin {
		p, err := readPassword(stdin)
		if err != nil {
			return err
		}
		password = p
	} else {
		p, err := generatePassword()
		if err != nil {
			return err
		}
		password, generated = p, true
	}
	var key string
	if !*noKey {
		k, err := auth.NewSecret(auth.PrefixLicenseKey)
		if err != nil {
			return err
		}
		key = k
	}
	res, err := svc.Bootstrap(ctx, auth.BootstrapSpec{TenantID: *tenantID, OrgName: *org, OwnerEmail: *email,
		OwnerPassword: password, OwnerName: *name, LicenseKey: key})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "organization: %s (tenant_id=%s, id=%s, created=%t)\n", res.Org.Name, res.Org.TenantID, res.Org.ID, res.CreatedOrg)
	switch {
	case res.CreatedUser && generated:
		fmt.Fprintf(stdout, "owner: %s password=%s (shown once; change it after signing in)\n", auth.NormalizeEmail(*email), password)
	case res.CreatedUser:
		fmt.Fprintf(stdout, "owner: %s (password from stdin)\n", auth.NormalizeEmail(*email))
	default:
		fmt.Fprintf(stdout, "owner: %s (existing user, password unchanged, added_owner=%t)\n", auth.NormalizeEmail(*email), res.AddedOwner)
	}
	if res.CreatedLicenseKey {
		fmt.Fprintf(stdout, "ingest license key: %s (shown once)\n", key)
	}
	return nil
}

func resetPassword(ctx context.Context, svc *auth.Service, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	email := fs.String("email", "", "user email (required)")
	pwStdin := fs.Bool("password-stdin", false, "read the new password from stdin (otherwise one is generated and printed)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		fs.Usage()
		return errors.New("--email is required")
	}
	password, generated := "", false
	if *pwStdin {
		p, err := readPassword(stdin)
		if err != nil {
			return err
		}
		password = p
	} else {
		p, err := generatePassword()
		if err != nil {
			return err
		}
		password, generated = p, true
	}
	if err := svc.ResetPassword(ctx, *email, password); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return fmt.Errorf("no user with email %s", *email)
		}
		return err
	}
	if generated {
		fmt.Fprintf(stdout, "password for %s reset to %s (shown once); all sessions revoked\n", auth.NormalizeEmail(*email), password)
	} else {
		fmt.Fprintf(stdout, "password for %s reset; all sessions revoked\n", auth.NormalizeEmail(*email))
	}
	return nil
}
