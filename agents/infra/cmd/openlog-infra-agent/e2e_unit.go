//go:build openlog_e2e_unit

package main

// Built only with -tags openlog_e2e_unit for the update end-to-end scenario (test/update/): a
// release whose embedded systemd unit differs, so that the scenario can check that a self-update
// installs the new unit and restarts the service under it.

import "bytes"

func init() {
	systemdUnit = bytes.Replace(systemdUnit, []byte("[Service]\n"), []byte("[Service]\nEnvironment=OPENLOG_E2E_UNIT=changed\n"), 1)
}
