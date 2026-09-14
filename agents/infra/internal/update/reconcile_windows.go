//go:build windows

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	lib "github.com/onuragtas/openlog/libs/release"
)

// ServiceCommandLine is the ImagePath of the service: the current link keeps it stable across updates.
func ServiceCommandLine(installRoot, configPath string) (exe string, args []string) {
	return filepath.Join(installRoot, "current", BinaryName), []string{"-config", configPath}
}

func (r *reconciler) native(ctx context.Context, prev *ReconcileStatus) {
	root := r.o.Install.InstallRoot
	// Packages (MSI) install versions\<v> without touching current: point it at this release unless a newer one
	// (installed by a self-update) is current.
	if r.o.Context == ReconcilePackage {
		r.packageCurrent(root)
	}
	// Configuration (license key) and state: SYSTEM and Administrators only.
	for _, dir := range []string{filepath.Dir(r.o.ConfigPath), r.o.StateDir} {
		if dir == "" || dir == "." {
			continue
		}
		if err := SecureDir(dir); err != nil {
			r.fail("secure "+dir, err)
		}
	}
	if r.o.ConfigPath != "" {
		if _, err := os.Stat(r.o.ConfigPath); err == nil {
			if err := secureACL(r.o.ConfigPath); err != nil {
				r.fail("config", err)
			}
		}
	}

	exe, args := ServiceCommandLine(root, r.o.ConfigPath)
	cmdline := syscall.EscapeArg(exe)
	for _, a := range args {
		cmdline += " " + syscall.EscapeArg(a)
	}
	sum := sha256.Sum256([]byte(cmdline))
	r.st.UnitPath, r.st.UnitSHA256 = "service:"+ServiceName, hex.EncodeToString(sum[:])

	m, err := mgr.Connect()
	if err != nil {
		r.fail("service manager", err)
		return
	}
	defer m.Disconnect()
	changed := false
	sv, err := m.OpenService(ServiceName)
	if err != nil {
		sv, err = m.CreateService(ServiceName, exe, mgr.Config{
			DisplayName: ServiceDisplayName, StartType: mgr.StartAutomatic, DelayedAutoStart: true,
			Description: "Collects host metrics, inventory and logs and sends them to openlog (https://github.com/onuragtas/openlog).",
			SidType:     windows.SERVICE_SID_TYPE_UNRESTRICTED,
		}, args...)
		if err != nil {
			r.fail("create service", err)
			return
		}
		changed = true
		r.log.Info("windows service created", "service", ServiceName, "command", cmdline)
	} else {
		c, err := sv.Config()
		if err != nil {
			sv.Close()
			r.fail("service config", err)
			return
		}
		if !strings.EqualFold(c.BinaryPathName, cmdline) || c.StartType != mgr.StartAutomatic || c.ServiceStartName != "LocalSystem" {
			c.BinaryPathName, c.StartType, c.DelayedAutoStart = cmdline, mgr.StartAutomatic, true
			c.ServiceStartName = "LocalSystem"
			c.DisplayName = ServiceDisplayName
			if err := sv.UpdateConfig(c); err != nil {
				r.fail("update service", err)
			} else {
				changed = true
				r.log.Info("windows service definition updated", "service", ServiceName, "command", cmdline)
			}
		}
	}
	defer sv.Close()
	// Restart after every exit that is not a stop request: self-update and rollback exit with a service specific
	// error code (non-crash failure), like systemd's Restart=always.
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := sv.SetRecoveryActions(actions, 24*60*60); err != nil {
		r.fail("service recovery", err)
	}
	if err := sv.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		r.fail("service recovery", err)
	}
	r.st.UnitChanged = changed
	if changed && r.o.Context != ReconcileApply && !(prev.UnitSHA256 == r.st.UnitSHA256 && prev.RestartRequired) {
		if st, err := sv.Query(); err == nil && st.State == svc.Running {
			r.st.RestartRequired = true
		}
	}
	_ = ctx
}

// packageCurrent switches current to this release unless current already points at a newer valid version.
func (r *reconciler) packageCurrent(root string) {
	mine := r.o.Install.VersionDir
	if cur, err := CurrentDir(root); err == nil && cur != mine {
		cv, err1 := lib.ParseVersion(cur)
		mv, err2 := lib.ParseVersion(mine)
		if _, statErr := os.Stat(filepath.Join(root, "versions", cur, BinaryName)); err1 == nil && err2 == nil && statErr == nil && lib.Compare(cv, mv) > 0 {
			r.note("current points at the newer version " + cur + " (self-update); kept")
			return
		}
	} else if err == nil {
		return
	}
	if err := SwitchCurrent(root, mine); err != nil {
		r.fail("current", err)
		return
	}
	r.note("current switched to " + mine)
}

// UninstallService stops and deletes the Windows service.
func UninstallService(ctx context.Context) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	sv, err := m.OpenService(ServiceName)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return err
	}
	defer sv.Close()
	if st, err := sv.Query(); err == nil && st.State != svc.Stopped {
		if _, err := sv.Control(svc.Stop); err == nil {
			for st.State != svc.Stopped && ctx.Err() == nil {
				time.Sleep(500 * time.Millisecond)
				if st, err = sv.Query(); err != nil {
					break
				}
			}
		}
	}
	return sv.Delete()
}
