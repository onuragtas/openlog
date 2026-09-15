//go:build darwin

package update

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"time"
)

// launchdCtl runs launchctl; tests replace it.
var launchdCtl = func(ctx context.Context, args ...string) error {
	return exec.CommandContext(ctx, "/bin/launchctl", args...).Run()
}

// launchdUnloadTimeout covers the LaunchDaemon's ExitTimeOut (30s): bootout returns while a job that is still
// flushing on SIGTERM can stay listed in the system domain.
var launchdUnloadTimeout = 40 * time.Second

// UninstallService boots the LaunchDaemon out of the system domain, waits until launchd no longer lists it (a second
// bootout is sent once halfway) and removes its definition. It fails when the job is still loaded after the timeout.
func UninstallService(ctx context.Context) error {
	target := "system/" + LaunchdLabel
	loaded := func() bool { return launchdCtl(ctx, "print", target) == nil }
	// bootout fails when the job is not loaded; that is fine.
	_ = launchdCtl(ctx, "bootout", target)
	deadline := time.Now().Add(launchdUnloadTimeout)
	retried := false
	for loaded() {
		if !retried && time.Now().After(deadline.Add(-launchdUnloadTimeout/2)) {
			_ = launchdCtl(ctx, "bootout", target)
			retried = true
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("launchd still lists %s after %s", target, launchdUnloadTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	if err := os.Remove(LaunchdPlistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
