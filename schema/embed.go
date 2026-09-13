// Package schema embeds the ClickHouse schema files so that openlog-migrate and
// openlog-allinone can apply them without any files on disk.
package schema

import (
	"embed"
	"io/fs"

	"github.com/onuragtas/openlog/internal/migrate/phase"
)

// ClickHouse holds schema/clickhouse/*.sql. Files are applied in lexical order.
//
//go:embed clickhouse/*.sql
var ClickHouse embed.FS

// testExtra is set only in binaries built with the tag openlog_testmigrations (test/mixedversion).
var testExtra fs.FS

// ClickHouseFS returns the schema files to apply: ClickHouse plus test-only files in test builds.
func ClickHouseFS() fs.FS {
	if testExtra == nil {
		return ClickHouse
	}
	return phase.Union(ClickHouse, testExtra)
}
