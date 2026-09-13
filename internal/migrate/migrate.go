// Package migrate applies the embedded ClickHouse schema and creates Kafka topics.
package migrate

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

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/migrate/phase"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// Migration is one schema file.
type Migration struct {
	Version    uint32
	Name       string
	Statements []string
	Header     phase.Header
}

var (
	fileRe    = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)\.sql$`)
	clusterRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)
)

// Load reads *.sql files from dir in fsys, ordered by version. Every
// occurrence of '{cluster}' is replaced by the quoted cluster name so both
// ON CLUSTER DDL and Distributed engines use OPENLOG_CLICKHOUSE_CLUSTER.
func Load(fsys fs.FS, dir, cluster string) ([]Migration, error) {
	if !clusterRe.MatchString(cluster) {
		return nil, fmt.Errorf("invalid cluster name %q", cluster)
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[uint32]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q does not match NNNN_name.sql", e.Name())
		}
		v, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			return nil, err
		}
		if other, dup := seen[uint32(v)]; dup {
			return nil, fmt.Errorf("duplicate migration version %d (%s, %s)", v, other, e.Name())
		}
		seen[uint32(v)] = e.Name()
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		h, err := phase.Parse(string(b))
		if err != nil {
			return nil, fmt.Errorf("clickhouse migration %s: %w", e.Name(), err)
		}
		sql := strings.ReplaceAll(string(b), "'{cluster}'", "'"+cluster+"'")
		out = append(out, Migration{Version: uint32(v), Name: m[2], Statements: SplitStatements(sql), Header: h})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// SplitStatements splits SQL text into statements on ';', ignoring semicolons
// inside quoted strings/identifiers and comments. Comments are removed and
// empty statements are dropped.
func SplitStatements(sql string) []string {
	var (
		out []string
		cur strings.Builder
	)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	n := len(sql)
	for i := 0; i < n; i++ {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
			cur.WriteByte('\n')
		case c == '/' && i+1 < n && sql[i+1] == '*':
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				i = n
			} else {
				i += 2 + end + 1
			}
			cur.WriteByte(' ')
		case c == '\'' || c == '"' || c == '`':
			quote := c
			cur.WriteByte(c)
			i++
			for i < n {
				cur.WriteByte(sql[i])
				if sql[i] == '\\' && i+1 < n {
					i++
					cur.WriteByte(sql[i])
				} else if sql[i] == quote {
					if i+1 < n && sql[i+1] == quote { // doubled quote escape
						i++
						cur.WriteByte(sql[i])
					} else {
						break
					}
				}
				i++
			}
		case c == ';':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// Run applies pending expand migrations and skips contract migrations. See RunGated.
func Run(ctx context.Context, conn clickhouse.Conn, migrations []Migration, log *slog.Logger) error {
	_, err := RunGated(ctx, conn, migrations, nil, log)
	return err
}

// RunGated applies migrations not yet recorded in openlog.schema_migrations: all expand
// migrations and the contract migrations gate allows (nil skips them; they stay pending). Every
// statement is idempotent (IF NOT EXISTS), so re-applying after a partial failure is safe. conn
// must not be bound to the openlog database, since it may not exist yet.
func RunGated(ctx context.Context, conn clickhouse.Conn, migrations []Migration, gate *phase.Gate, log *slog.Logger) ([]phase.Step, error) {
	steps, err := Plan(ctx, conn, migrations, gate)
	if err != nil {
		return nil, err
	}
	for i, m := range migrations {
		switch steps[i].Action {
		case phase.ActionApplied:
			log.Debug("migration already applied", "version", m.Version, "name", m.Name)
			continue
		case phase.ActionSkip:
			log.Warn("skipping clickhouse contract migration", "version", m.Version, "name", m.Name, "reason", steps[i].Reason)
			continue
		}
		log.Info("applying migration", "version", m.Version, "name", m.Name, "phase", m.Header.Phase, "statements", len(m.Statements))
		for i, stmt := range m.Statements {
			if err := conn.Exec(ctx, stmt); err != nil {
				return steps, fmt.Errorf("migration %04d_%s statement %d: %w\n%s", m.Version, m.Name, i+1, err, stmt)
			}
		}
		ictx := ch.Context(ctx, ch.WithSettings(ch.Settings{"distributed_foreground_insert": 1}))
		if err := conn.Exec(ictx, "INSERT INTO openlog.schema_migrations (version, name) VALUES (?, ?)", m.Version, m.Name); err != nil {
			return steps, fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}
	log.Info("clickhouse schema up to date", "migrations", len(migrations))
	return steps, nil
}

// Plan reports what RunGated would do without changing anything.
func Plan(ctx context.Context, conn clickhouse.Conn, migrations []Migration, gate *phase.Gate) ([]phase.Step, error) {
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}
	return PlanSteps(migrations, applied, gate), nil
}

// PlanSteps decides every migration given the applied versions.
func PlanSteps(migrations []Migration, applied map[uint32]bool, gate *phase.Gate) []phase.Step {
	steps := make([]phase.Step, len(migrations))
	for i, m := range migrations {
		st := phase.Step{Database: "clickhouse", Version: int(m.Version), Name: m.Name, Header: m.Header}
		if applied[m.Version] {
			st.Action = phase.ActionApplied
		} else if d := gate.Decide(m.Header); d.Apply {
			st.Action, st.Reason = phase.ActionApply, d.Reason
		} else {
			st.Action, st.Reason = phase.ActionSkip, d.Reason
		}
		steps[i] = st
	}
	return steps
}

func appliedVersions(ctx context.Context, conn clickhouse.Conn) (map[uint32]bool, error) {
	out := map[uint32]bool{}
	var exists uint8
	if err := conn.QueryRow(ctx, "EXISTS TABLE openlog.schema_migrations").Scan(&exists); err != nil {
		return nil, fmt.Errorf("check schema_migrations: %w", err)
	}
	if exists == 0 {
		return out, nil
	}
	rows, err := conn.Query(ctx, "SELECT DISTINCT version FROM openlog.schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v uint32
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}
