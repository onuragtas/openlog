// Package migrations embeds the PostgreSQL schema migrations
// (docs/contracts/postgres.md). openlog-migrate, openlog-allinone and
// openlog-admin apply them before the ClickHouse schema.
package migrations

import (
	"embed"
	"io/fs"

	"github.com/onuragtas/openlog/internal/migrate/phase"
)

// Postgres holds postgres/NNNN_name.sql, applied in version order.
//
//go:embed postgres/*.sql
var Postgres embed.FS

// testExtra is set only in binaries built with the tag openlog_testmigrations
// (test/mixedversion): extra migrations under postgres/ that exercise the expand/contract rules.
var testExtra fs.FS

// PostgresFS returns the migrations to apply: Postgres plus test-only migrations in test builds.
func PostgresFS() fs.FS {
	if testExtra == nil {
		return Postgres
	}
	return phase.Union(Postgres, testExtra)
}
