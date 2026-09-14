//go:build openlog_testmigrations && openlog_testmigrations_next

package migrations

import (
	"embed"
	"io/fs"

	"github.com/onuragtas/openlog/internal/migrate/phase"
)

// testdata/next holds the migrations of a test release after the one built with openlog_testmigrations
// (test/autoupdate: the broken release whose expand migration stays applied after the rollback).
//
//go:embed testdata/next/postgres/*.sql
var testNextFiles embed.FS

// Runs after testextra.go's init (files are initialized in file name order).
func init() {
	sub, err := fs.Sub(testNextFiles, "testdata/next")
	if err != nil {
		panic(err)
	}
	testExtra = phase.Union(testExtra, sub)
}
