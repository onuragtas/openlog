package main

// "-apply" and "-reconcile": the steps that run as root (docs/contracts/releases-updates.md §3,
// "Privileged apply and reconcile"). Only root-controlled inputs may steer them: the configuration
// and release.trusted_keys_file are ignored unless only root can change them.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/release"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
	"github.com/onuragtas/openlog/agents/infra/packaging"
)

const (
	// applyBudget keeps -apply well inside the unit's TimeoutStartSec=180.
	applyBudget     = 150 * time.Second
	reconcileBudget = 2 * time.Minute
)

// systemdUnit is the unit -reconcile installs (replaced by the update end-to-end scenario's builds).
var systemdUnit = packaging.SystemdUnit

func privilegedConfig(sys *update.Sys, path string, explicit bool, log *slog.Logger) (*config.Config, []ed25519.PublicKey) {
	cfg := config.Default()
	if err := sys.TrustedFile(path); err != nil {
		if explicit || !errors.Is(err, fs.ErrNotExist) {
			log.Error("configuration ignored by the privileged step (it must be changeable by root only); using defaults", "config", path, "error", err)
		}
	} else if c, err := config.Load(path, !explicit); err != nil {
		log.Error("configuration unreadable; using defaults", "config", path, "error", err)
	} else {
		cfg = c
	}
	keysFile := cfg.Release.TrustedKeysFile
	if keysFile != "" {
		if err := sys.TrustedFile(keysFile); err != nil {
			log.Error("release.trusted_keys_file ignored by the privileged step (it must be changeable by root only)", "file", keysFile, "error", err)
			keysFile = ""
		}
	}
	keys, err := release.TrustedKeys(keysFile)
	if err != nil {
		log.Error("release keys unusable", "error", err)
		keys = nil
	}
	return cfg, keys
}

func privilegedInstall(cfg *config.Config, keys []ed25519.PublicKey) update.Install {
	exe, _ := os.Executable()
	return update.Detect(update.Env{
		Executable: exe, InstallRoot: cfg.Update.InstallRoot, UpdatesEnabled: cfg.Update.Enabled,
		HaveTrustedKeys: len(keys) > 0, Privileged: true,
	})
}

func reconcileOptions(sys *update.Sys, cfg *config.Config, install update.Install, configPath, ver, rctx string, log *slog.Logger) update.ReconcileOptions {
	return update.ReconcileOptions{
		Sys: sys, Install: install, StateDir: cfg.StateDir, ConfigPath: configPath, Version: ver,
		Unit: systemdUnit, Context: rctx, InvocationID: os.Getenv("INVOCATION_ID"), Log: log,
		// cfg is the defaults unless only root can change the file (privilegedConfig).
		PHPGrantsDisabled: cfg.PHPForwarder.GrantPoolUsers != nil && !*cfg.PHPForwarder.GrantPoolUsers,
	}
}

// runApply is "-apply". It always exits 0: the service must start the current version whatever happens.
func runApply(configPath string, explicit bool, ver string) (code int) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("step", "apply")
	defer func() {
		if r := recover(); r != nil {
			log.Error("apply panicked; starting the current version", "panic", fmt.Sprint(r))
			code = 0
		}
	}()
	sys := update.HostSys()
	cfg, keys := privilegedConfig(sys, configPath, explicit, log)
	install := privilegedInstall(cfg, keys)
	selfTestConfig := configPath
	if _, err := os.Stat(configPath); err != nil && !explicit {
		selfTestConfig = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), applyBudget)
	defer cancel()
	update.Apply(ctx, update.ApplyOptions{
		Sys: sys, Install: install, StateDir: cfg.StateDir, Version: ver, Trusted: keys,
		UpdatesEnabled: cfg.Update.Enabled, InvocationID: os.Getenv("INVOCATION_ID"), Log: log,
		SelfTest: func(ctx context.Context, bin string, uid, gid int) error {
			return update.RunSelfTestAs(ctx, bin, selfTestConfig, update.SelfTestTimeout, uid, gid)
		},
		Reconcile: func(ctx context.Context, dir string) error {
			if dir == "" {
				_, err := update.Reconcile(ctx, reconcileOptions(sys, cfg, install, configPath, ver, update.ReconcileApply, log))
				return err
			}
			// After a switch the release that starts reconciles with its own code. It is root-owned and was verified.
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
	runPHPAgentApply(sys, cfg, install, keys, log) // phpagent.go
	return 0
}

// runReconcile is "-reconcile". It prints "restart-required" when a running service must restart
// (new unit or new groups); installers use it.
func runReconcile(configPath string, explicit bool, ver, rctx string) int {
	switch rctx {
	case update.ReconcileApply, update.ReconcilePackage, update.ReconcileInstall, update.ReconcileManual:
	default:
		fmt.Fprintln(os.Stderr, "-reconcile-context must be apply, package, install or manual")
		return 2
	}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, nil)
	if rctx == update.ReconcileApply {
		h = slog.NewJSONHandler(os.Stderr, nil)
	}
	log := slog.New(h).With("step", "reconcile")
	sys := update.HostSys()
	cfg, keys := privilegedConfig(sys, configPath, explicit, log)
	install := privilegedInstall(cfg, keys)
	ctx, cancel := context.WithTimeout(context.Background(), reconcileBudget)
	defer cancel()
	st, err := update.Reconcile(ctx, reconcileOptions(sys, cfg, install, configPath, ver, rctx, log))
	if st != nil && st.RestartRequired && rctx != update.ReconcileApply {
		fmt.Println("restart-required")
	}
	if err != nil {
		log.Error("reconcile incomplete", "error", err)
		return 1
	}
	return 0
}

// restartForUnit reports whether this start's "-apply" installed a new unit: the agent exits once so
// that systemd starts it under the new definition (daemon-reload already ran).
func restartForUnit(in update.Install) bool {
	a, r := in.Apply, in.Reconcile
	return in.Mode == update.ModeStaged && a != nil && r != nil && r.RestartRequired &&
		r.Context == update.ReconcileApply && r.InvocationID != "" && r.InvocationID == a.InvocationID
}
