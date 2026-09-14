package update

// Installation and service management of macOS and Windows hosts (D-104). launchd and the Windows Service
// Control Manager have no privileged pre-start hook like systemd's ExecStartPre=+, but the agent itself runs
// privileged there (root / LocalSystem): the service process runs Apply (same verification and rollback rules)
// before it starts collecting, and exits once after a switch so that the service manager starts the new binary.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Service identities.
const (
	// ServiceName is the Windows service name.
	ServiceName = "openlog-infra-agent"
	// ServiceDisplayName is the Windows service display name.
	ServiceDisplayName = "openlog infrastructure agent"
	// LaunchdLabel is the macOS LaunchDaemon label.
	LaunchdLabel = "org.openlog.infra-agent"
	// LaunchdPlistPath is where -reconcile installs the LaunchDaemon.
	LaunchdPlistPath = "/Library/LaunchDaemons/" + LaunchdLabel + ".plist"
	// LaunchdLogPath receives the agent's stdout/stderr on macOS.
	LaunchdLogPath = "/var/log/openlog-infra-agent.log"
	// ServiceManagerEnv is set by the LaunchDaemon ("launchd"); the Windows service is detected through the SCM.
	ServiceManagerEnv = "OPENLOG_SERVICE_MANAGER"
)

// Install methods of macOS and Windows hosts.
const (
	// MethodZip is an install.ps1 installation from the Windows zip archive.
	MethodZip = "zip"
	// MethodMSI is an MSI package installation (registry value InstallMethod=msi).
	MethodMSI = "msi"
)

// RenderLaunchdPlist renders the LaunchDaemon for an install root and configuration path (packaging/launchd has the
// default rendering).
func RenderLaunchdPlist(installRoot, configPath string) []byte {
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- openlog infrastructure agent (macOS LaunchDaemon, D-104). Installed and kept up to date by
     "openlog-infra-agent -reconcile" (install.sh). The agent runs as root: launchd has no privileged
     pre-start hook, so the root process verifies and installs staged updates itself before it starts. -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>-config</string>
		<string>%s</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>%s</key>
		<string>launchd</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ExitTimeOut</key>
	<integer>30</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>Nice</key>
	<integer>10</integer>
	<key>WorkingDirectory</key>
	<string>/</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, LaunchdLabel, esc(filepath.Join(installRoot, "current", BinaryName)), esc(configPath), ServiceManagerEnv, LaunchdLogPath, LaunchdLogPath)
}

// NativeReconcileOptions configures ReconcileNative.
type NativeReconcileOptions struct {
	Sys        *Sys
	Install    Install
	StateDir   string
	ConfigPath string
	Version    string
	// Context is ReconcileApply, ReconcilePackage, ReconcileInstall or ReconcileManual.
	Context      string
	InvocationID string
	Log          *slog.Logger
	Now          func() time.Time
}

// ReconcileNative is "-reconcile" on macOS (LaunchDaemon, root-owned layout and directories, CLI link, log rotation)
// and Windows (service registration with restart recovery, protected ACLs on configuration and state, and for
// packages the current link). It prints nothing; RestartRequired is set when the service definition changed outside
// the apply context.
func ReconcileNative(ctx context.Context, o NativeReconcileOptions) (*ReconcileStatus, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	root := o.Install.InstallRoot
	if o.Install.VersionDir == "" || root == "" {
		return nil, fmt.Errorf("binary is not in the versions layout: %s", o.Install.Reason)
	}
	if !o.Sys.IsRoot() {
		return nil, errors.New("-reconcile needs root (macOS) or an elevated administrator (Windows)")
	}
	r := &reconciler{
		o: ReconcileOptions{Sys: o.Sys, Install: o.Install, StateDir: o.StateDir, ConfigPath: o.ConfigPath, Version: o.Version,
			Context: o.Context, InvocationID: o.InvocationID, Log: o.Log, Now: o.Now},
		log: o.Log.With("component", "reconcile"),
		st:  &ReconcileStatus{InvocationID: o.InvocationID, At: o.Now().UTC(), Version: o.Version, Context: o.Context},
	}
	var prev ReconcileStatus
	if err := o.Sys.loadTrustedJSON(filepath.Join(root, ReconcileStatusFile), &prev); err != nil {
		prev = ReconcileStatus{}
	}
	notes, err := o.Sys.secureLayout(root, r.log)
	r.st.Notes = append(r.st.Notes, notes...)
	if err != nil {
		r.fail("install root", err)
	}
	r.native(ctx, &prev) // reconcile_darwin.go, reconcile_windows.go
	if err := saveStatus(filepath.Join(root, ReconcileStatusFile), r.st); err != nil {
		r.fail("status", err)
	}
	if len(r.st.Errors) > 0 {
		return r.st, errors.Join(errors.New(joinErrors(r.st.Errors)))
	}
	return r.st, nil
}

func joinErrors(errs []string) string {
	var b bytes.Buffer
	for i, e := range errs {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(e)
	}
	return b.String()
}

// writeIfChanged writes data to path (atomically, mode) when the content differs and reports whether it changed.
func writeIfChanged(path string, data []byte, mode os.FileMode) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, writeFileAtomic(path, data, mode)
}
