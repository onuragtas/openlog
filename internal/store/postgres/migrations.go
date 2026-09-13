package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/migrate/phase"
)

// Querier is satisfied by *pgxpool.Pool, *pgxpool.Conn, *pgx.Conn and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Migration is one schema file.
type Migration struct {
	Version int
	Name    string
	SQL     string
	Header  phase.Header
}

var fileRe = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)\.sql$`)

// LoadMigrations reads dir/NNNN_name.sql from fsys in version order. Every file must start with
// a phase header (docs/contracts/releases-updates.md §6).
func LoadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q does not match NNNN_name.sql", e.Name())
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, err
		}
		if other, dup := seen[v]; dup {
			return nil, fmt.Errorf("duplicate migration version %d (%s, %s)", v, other, e.Name())
		}
		seen[v] = e.Name()
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		h, err := phase.Parse(string(b))
		if err != nil {
			return nil, fmt.Errorf("postgres migration %s: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: v, Name: m[2], SQL: string(b), Header: h})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// migrationLock is the pg_advisory_lock key serializing concurrent migrators.
const migrationLock int64 = 0x6f70656e6c6f67 // "openlog"

// GateFunc builds the contract-migration gate. It is called at most once per run, only when a
// contract migration is pending, on the connection holding the migration lock.
type GateFunc func(ctx context.Context, q Querier) *phase.Gate

// Migrate applies pending expand migrations; contract migrations are skipped. See MigrateGated.
func Migrate(ctx context.Context, pool *pgxpool.Pool, migrations []Migration, log *slog.Logger) error {
	_, err := MigrateGated(ctx, pool, migrations, nil, log)
	return err
}

// MigrateGated applies migrations not yet recorded in schema_migrations: every expand migration,
// and contract migrations the gate allows (a nil gate skips them). Each migration runs in its own
// transaction; concurrent callers (several pods, allinone + openlog-admin) are serialized with an
// advisory lock. A skipped contract migration stays pending and is retried by the next run.
func MigrateGated(ctx context.Context, pool *pgxpool.Pool, migrations []Migration, gate GateFunc, log *slog.Logger) ([]phase.Step, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, "SELECT pg_advisory_unlock($1)", migrationLock)
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := appliedPostgres(ctx, conn)
	if err != nil {
		return nil, err
	}
	steps := planSteps(ctx, conn, migrations, applied, gate)
	for i, st := range steps {
		m := migrations[i]
		switch st.Action {
		case phase.ActionApplied:
			continue
		case phase.ActionSkip:
			log.Warn("skipping postgres contract migration", "version", m.Version, "name", m.Name, "reason", st.Reason)
			continue
		}
		log.Info("applying postgres migration", "version", m.Version, "name", m.Name, "phase", m.Header.Phase)
		tx, err := conn.Begin(ctx)
		if err != nil {
			return steps, err
		}
		// No arguments: pgx uses the simple protocol, so a file may hold many statements.
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return steps, fmt.Errorf("postgres migration %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.Version, m.Name); err != nil {
			_ = tx.Rollback(ctx)
			return steps, err
		}
		if err := tx.Commit(ctx); err != nil {
			return steps, err
		}
	}
	log.Info("postgres schema up to date", "migrations", len(migrations))
	return steps, nil
}

// PlanMigrations reports what MigrateGated would do without changing anything.
func PlanMigrations(ctx context.Context, q Querier, migrations []Migration, gate GateFunc) ([]phase.Step, error) {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return nil, err
	}
	applied := map[int]bool{}
	if exists {
		var err error
		if applied, err = appliedPostgres(ctx, q); err != nil {
			return nil, err
		}
	}
	return planSteps(ctx, q, migrations, applied, gate), nil
}

func appliedPostgres(ctx context.Context, q Querier) (map[int]bool, error) {
	applied := map[int]bool{}
	rows, err := q.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// planSteps decides every migration. The gate is evaluated before any migration of this run is
// applied only if needed; expand migrations earlier in the list (e.g. the heartbeat table) are
// applied by then in MigrateGated because the gate is built lazily.
func planSteps(ctx context.Context, q Querier, migrations []Migration, applied map[int]bool, gateFn GateFunc) []phase.Step {
	steps := make([]phase.Step, len(migrations))
	var (
		gate  *phase.Gate
		built bool
	)
	for i, m := range migrations {
		st := phase.Step{Database: "postgres", Version: m.Version, Name: m.Name, Header: m.Header}
		switch {
		case applied[m.Version]:
			st.Action, st.Reason = phase.ActionApplied, ""
		case m.Header.Phase != phase.Contract:
			st.Action, st.Reason = phase.ActionApply, "expand"
		default:
			if !built && gateFn != nil {
				gate, built = gateFn(ctx, q), true
			}
			d := gate.Decide(m.Header)
			st.Reason = d.Reason
			if d.Apply {
				st.Action = phase.ActionApply
			} else {
				st.Action = phase.ActionSkip
			}
		}
		steps[i] = st
	}
	return steps
}
