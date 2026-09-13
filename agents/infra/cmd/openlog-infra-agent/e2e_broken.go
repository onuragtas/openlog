//go:build openlog_e2e_broken

package main

// Built only with -tags openlog_e2e_broken for the update end-to-end scenario
// (test/update/): a release whose -self-test passes but which exits 1 one second
// after every normal start (after the update startup check, before anything is
// exported), so that the automatic rollback can be exercised.

import (
	"fmt"
	"os"
	"time"
)

func init() {
	beforeRun = func() {
		time.Sleep(time.Second)
		fmt.Fprintln(os.Stderr, `{"level":"ERROR","msg":"e2e broken build: exiting with status 1 before collecting"}`)
		os.Exit(1)
	}
}
