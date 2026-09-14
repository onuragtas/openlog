//go:build darwin

package update

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
)

// UninstallService boots the LaunchDaemon out of the system domain and removes its definition.
func UninstallService(ctx context.Context) error {
	// bootout fails when the job is not loaded; that is fine.
	_ = exec.CommandContext(ctx, "/bin/launchctl", "bootout", "system/"+LaunchdLabel).Run()
	if err := os.Remove(LaunchdPlistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
