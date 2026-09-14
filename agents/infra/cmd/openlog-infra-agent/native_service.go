package main

// macOS (launchd) and Windows (SCM) service starts (D-104): the service process is privileged, so it runs the
// update apply step itself before the agent starts, with the same verification and rollback rules as the systemd
// pre-start step. After switching versions it exits once; the service manager starts the new binary.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/ids"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
)

var (
	// svcCtx is cancelled when the Windows service is asked to stop (service_windows.go).
	svcCtx = context.Background()
	// runningAsWindowsService is set by service_windows.go.
	runningAsWindowsService bool
)

func baseContext() context.Context { return svcCtx }

// nativeServiceMode reports whether this process was started by launchd (LaunchDaemon, as root) or the Windows SCM.
func nativeServiceMode() bool {
	switch runtime.GOOS {
	case "darwin":
		return os.Getenv(update.ServiceManagerEnv) == "launchd" && os.Geteuid() == 0
	case "windows":
		return runningAsWindowsService
	}
	return false
}

// runNativeStartApply runs the apply step of a service start. exit is true when the process must exit so that the
// service manager starts another version (switched or rolled back).
func runNativeStartApply(configPath string, explicit bool, ver string) (exit bool) {
	inv := ids.NewV4()
	os.Setenv("INVOCATION_ID", inv) // matched by update.Detect for staged mode, like systemd's $INVOCATION_ID
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("step", "apply")
	defer func() {
		if r := recover(); r != nil {
			log.Error("apply panicked; starting the current version", "panic", fmt.Sprint(r))
			exit = false
		}
	}()
	sys := update.HostSys()
	cfg, keys := privilegedConfig(sys, configPath, explicit, log)
	install := privilegedInstall(cfg, keys)
	if install.VersionDir == "" {
		return false
	}
	selfTestConfig := configPath
	if _, err := os.Stat(configPath); err != nil && !explicit {
		selfTestConfig = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), applyBudget)
	defer cancel()
	st := update.Apply(ctx, update.ApplyOptions{
		Sys: sys, Install: install, StateDir: cfg.StateDir, Version: ver, Trusted: keys,
		UpdatesEnabled: cfg.Update.Enabled, InvocationID: inv, Log: log, DeferAttemptCount: true,
		SelfTest: func(ctx context.Context, bin string, _, _ int) error {
			return update.RunSelfTest(ctx, bin, selfTestConfig, update.SelfTestTimeout)
		},
		Reconcile: func(ctx context.Context, dir string) error {
			if dir == "" {
				_, err := update.ReconcileNative(ctx, nativeReconcileOptions(sys, cfg, install, configPath, ver, update.ReconcileApply, log))
				return err
			}
			args := []string{"-reconcile", "-reconcile-context", update.ReconcileApply}
			if explicit {
				args = append(args, "-config", configPath)
			}
			cmd := exec.CommandContext(ctx, filepath.Join(install.InstallRoot, "versions", dir, update.BinaryName), args...)
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			cmd.WaitDelay = 2 * time.Second
			return cmd.Run()
		},
	})
	if st != nil && !st.Carried && (st.Result == update.ApplySwitched || st.Result == update.ApplyRolledBack) {
		log.Warn("exiting so that the service manager starts the other version", "result", st.Result, "from", st.FromVersion, "to", st.ToVersion)
		return true
	}
	return false
}

func nativeReconcileOptions(sys *update.Sys, cfg *config.Config, install update.Install, configPath, ver, rctx string, log *slog.Logger) update.NativeReconcileOptions {
	return update.NativeReconcileOptions{
		Sys: sys, Install: install, StateDir: cfg.StateDir, ConfigPath: configPath, Version: ver,
		Context: rctx, InvocationID: os.Getenv("INVOCATION_ID"), Log: log,
	}
}

// runUninstallService is "-uninstall-service" (macOS, Windows).
func runUninstallService() int {
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "uninstall-service: run as root (sudo)")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := update.UninstallService(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "uninstall-service:", err)
		return 1
	}
	fmt.Println("service removed")
	return 0
}
