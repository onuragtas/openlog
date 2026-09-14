//go:build !darwin && !windows

package update

import (
	"context"
	"errors"
)

// native is never reached on Linux: -reconcile uses Reconcile there.
func (r *reconciler) native(context.Context, *ReconcileStatus) {
	r.fail("reconcile", errors.New("the native reconcile step is only available on macOS and Windows"))
}

// UninstallService is only available on Windows and macOS; Linux packages manage the systemd unit.
func UninstallService(context.Context) error {
	return errors.New("use the package manager or systemctl to remove the service on Linux")
}
