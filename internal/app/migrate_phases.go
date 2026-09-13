package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/migrate/phase"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/version"
	"github.com/onuragtas/openlog/migrations"
	"github.com/onuragtas/openlog/schema"
)

// MigrateOptions control openlog-migrate (flags -plan and -force-contract).
type MigrateOptions struct {
	// Plan prints what would run and changes nothing (no Kafka topics either).
	Plan bool
	// ForceContract applies contract migrations regardless of running component versions.
	ForceContract bool
	// Out receives the plan (default stdout).
	Out io.Writer
}

func (o MigrateOptions) out() io.Writer {
	if o.Out == nil {
		return os.Stdout
	}
	return o.Out
}

func postgresGate(force bool) postgres.GateFunc {
	return func(ctx context.Context, q postgres.Querier) *phase.Gate {
		in, err := postgres.LiveInstances(ctx, q)
		return &phase.Gate{Self: version.Version, Instances: in, Known: err == nil, Err: err, Force: force}
	}
}

func migratePostgres(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, opts MigrateOptions) error {
	ms, err := postgres.LoadMigrations(migrations.PostgresFS(), "postgres")
	if err != nil {
		return err
	}
	if err := postgres.WaitReady(ctx, pool, log); err != nil {
		return err
	}
	if opts.Plan {
		steps, err := postgres.PlanMigrations(ctx, pool, ms, postgresGate(opts.ForceContract))
		if err != nil {
			return err
		}
		in, ierr := postgres.LiveInstances(ctx, pool)
		printInstances(opts.out(), in, ierr)
		PrintPlan(opts.out(), steps)
		return nil
	}
	_, err = postgres.MigrateGated(ctx, pool, ms, postgresGate(opts.ForceContract), log)
	return err
}

// clickhouseGate reads the live instances from PostgreSQL for ClickHouse contract migrations.
func clickhouseGate(ctx context.Context, cfg config.Config, force bool) *phase.Gate {
	g := &phase.Gate{Self: version.Version, Force: force}
	if strings.TrimSpace(cfg.Postgres.DSN) == "" {
		g.Err = errors.New("OPENLOG_POSTGRES_DSN is not set")
		return g
	}
	pool, err := OpenPostgres(ctx, cfg, "openlog-migrate")
	if err != nil {
		g.Err = err
		return g
	}
	defer pool.Close()
	qctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	g.Instances, g.Err = postgres.LiveInstances(qctx, pool)
	g.Known = g.Err == nil
	return g
}

func migrateClickHouse(ctx context.Context, cfg config.Config, conn clickhouse.Conn, log *slog.Logger, opts MigrateOptions) error {
	ms, err := migrate.Load(schema.ClickHouseFS(), "clickhouse", cfg.ClickHouseCluster)
	if err != nil {
		return err
	}
	gate := clickhouseGate(ctx, cfg, opts.ForceContract)
	if opts.Plan {
		steps, err := migrate.Plan(ctx, conn, ms, gate)
		if err != nil {
			return err
		}
		PrintPlan(opts.out(), steps)
		if cur, err := migrate.AppliedAPMRetention(ctx, conn); err != nil {
			fmt.Fprintf(opts.out(), "apm retention: unknown (%v)\n\n", err)
		} else if cur != cfg.APM.RetentionDays {
			fmt.Fprintf(opts.out(), "apm retention: %d -> %d days (ALTER TABLE ... ON CLUSTER MODIFY TTL on the APM tables)\n\n", cur, cfg.APM.RetentionDays)
		} else {
			fmt.Fprintf(opts.out(), "apm retention: %d days (unchanged)\n\n", cur)
		}
		return nil
	}
	if _, err = migrate.RunGated(ctx, conn, ms, gate, log); err != nil {
		return err
	}
	// Settings applied outside migration files (apm.md §8 "Retention"): after the migrations, idempotent.
	_, err = migrate.ApplyAPMRetention(ctx, conn, cfg.ClickHouseCluster, cfg.APM.RetentionDays, log)
	return err
}

func printInstances(w io.Writer, in []phase.Instance, err error) {
	if err != nil {
		fmt.Fprintf(w, "live instances: unknown (%v)\n\n", err)
		return
	}
	fmt.Fprintf(w, "live instances (seen within %s): %d\n", phase.LiveWindow, len(in))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, i := range in {
		fmt.Fprintf(tw, "  %s\t%s\t%s\tlast seen %s\n", i.Component, i.InstanceID, i.Version, i.LastSeen.UTC().Format(time.RFC3339))
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
}

// PrintPlan writes migration steps as a table.
func PrintPlan(w io.Writer, steps []phase.Step) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DATABASE\tVERSION\tNAME\tPHASE\tACTION\tREASON")
	for _, s := range steps {
		ph := string(s.Header.Phase)
		if s.Header.RequiresAllAtLeast != "" {
			ph += " (>= " + s.Header.RequiresAllAtLeast + ")"
		}
		fmt.Fprintf(tw, "%s\t%04d\t%s\t%s\t%s\t%s\n", s.Database, s.Version, s.Name, ph, s.Action, s.Reason)
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
}
