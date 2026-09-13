//go:build openlog_testmigrations

package schema

import (
	"embed"
	"io/fs"
)

//go:embed testdata/clickhouse/*.sql
var testExtraFiles embed.FS

func init() {
	sub, err := fs.Sub(testExtraFiles, "testdata")
	if err != nil {
		panic(err)
	}
	testExtra = sub
}
