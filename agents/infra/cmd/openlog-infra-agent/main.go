// Command openlog-infra-agent is the openlog Linux host agent.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/agent"
	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/phpaccess"
	"github.com/onuragtas/openlog/agents/infra/internal/release"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
	"github.com/onuragtas/openlog/agents/infra/internal/version"
)

// selfTestBudget keeps -self-test well inside the 30 s limit of the updater.
const selfTestBudget = 25 * time.Second

// beforeRun is nil in normal builds. The update end-to-end scenario's broken release
// (-tags openlog_e2e_broken) sets it to fail after the startup check.
var beforeRun func()

func main() {
	if code, ok := runWindowsService(); ok { // service_windows.go
		os.Exit(code)
	}
	os.Exit(run())
}

func run() int {
	configPath := flag.String("config", config.DefaultPath, "path to the configuration file")
	showVersion := flag.Bool("version", false, "print version, commit, build date and install method, then exit")
	once := flag.Bool("once", false, "collect one round of metrics, inventory and discovery, print the OTLP payload as JSON and exit (nothing is sent)")
	validateRules := flag.Bool("validate-rules", false, "validate the embedded and configured discovery rules and exit")
	selfTest := flag.Bool("self-test", false, "parse the configuration, run every collector once without sending and check the state directory is writable; exit 0 on success")
	apply := flag.Bool("apply", false, "privileged pre-start step run as root by the systemd unit (ExecStartPre=+): install a staged update after verifying it again, roll back an unconfirmed one, reconcile the installation; always exits 0")
	reconcile := flag.Bool("reconcile", false, "as root: make the installation match this release (systemd unit, service account, docker group, ownership); prints restart-required when the service must restart")
	reconcileContext := flag.String("reconcile-context", update.ReconcileManual, "who runs -reconcile: apply, package, install or manual")
	configure := flag.Bool("configure", false, "create the configuration file if missing (restricted permissions) and set -license-key and -endpoint in it, keeping everything else; then exit")
	licenseKey := flag.String("license-key", "", "license key for -configure")
	endpointFlag := flag.String("endpoint", "", "OTLP/HTTP endpoint for -configure")
	verifyRelease := flag.String("verify-release", "", "verify the Ed25519 signature (<manifest>.sig) of a release manifest with this binary's trusted keys, then exit")
	artifact := flag.String("artifact", "", "with -verify-release: also check this file's size and sha256 against the manifest artifact of the same name")
	uninstallService := flag.Bool("uninstall-service", false, "macOS: boot out and remove the LaunchDaemon; Windows: stop and delete the service; then exit")
	flag.Parse()

	explicit := false
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		set[f.Name] = true
		if f.Name == "config" {
			explicit = true
		}
	})
	ver := version.Current()
	switch {
	case *uninstallService:
		return runUninstallService() // native_service.go
	case *verifyRelease != "":
		return runVerifyRelease(*configPath, explicit, *verifyRelease, *artifact) // verifyrelease.go
	case *configure:
		return runConfigure(*configPath, *licenseKey, *endpointFlag, set["license-key"], set["endpoint"]) // configure.go
	}
	if *apply {
		return runApply(*configPath, explicit, ver)
	}
	if *reconcile {
		return runReconcile(*configPath, explicit, ver, *reconcileContext)
	}

	// Without an explicit -config a missing default file is fine: the agent can
	// be configured through OPENLOG_* environment variables alone.
	cfg, loadErr := config.Load(*configPath, !explicit)

	if *showVersion {
		if cfg == nil {
			cfg = config.Default()
		}
		printVersion(os.Stdout, cfg)
		return 0
	}
	if *selfTest {
		if loadErr != nil {
			fmt.Fprintln(os.Stderr, "self-test:", loadErr)
			return 1
		}
		return runSelfTest(cfg, ver)
	}
	if *validateRules || *once {
		if loadErr != nil {
			fmt.Fprintln(os.Stderr, loadErr)
			return 2
		}
		if *validateRules {
			return runValidateRules(cfg)
		}
	}

	logCfg := cfg
	if logCfg == nil {
		logCfg = config.Default()
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(logCfg.LogLevel))
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if loadErr == nil && cfg.Kubernetes.ClusterMode(os.Getenv) {
		return runCluster(cfg, ver, log, *once) // cluster.go: Kubernetes cluster collector, no updates or sync
	}

	var mgr *update.Manager
	var install update.Install
	if !*once {
		if nativeServiceMode() && runNativeStartApply(*configPath, explicit, ver) { // native_service.go: launchd, Windows SCM
			return 0
		}
		// Update startup check first: a candidate that cannot even load its configuration must
		// still count its start attempts and roll back.
		cfgPath := ""
		if explicit {
			cfgPath = *configPath
		} else if _, err := os.Stat(*configPath); err == nil {
			cfgPath = *configPath
		}
		mgr, install = newUpdateManager(logCfg, cfgPath, ver, log)
		if mgr.Startup() {
			log.Warn("exiting after rollback; the service manager restarts the previous version")
			return 0
		}
		if restartForUnit(install) {
			log.Warn("the systemd unit was updated before this start; exiting once so the service manager starts under it",
				"unit", install.Reconcile.UnitPath)
			return 0
		}
		if beforeRun != nil {
			beforeRun()
		}
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, loadErr)
		return 2
	}

	if err := cfg.Validate(!*once); err != nil {
		fmt.Fprintln(os.Stderr, "invalid configuration:\n"+err.Error())
		return 2
	}

	ctx, stop := signal.NotifyContext(baseContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := agent.New(cfg, ver, log, !*once)
	if err != nil {
		log.Error("startup failed", "error", err)
		return 1
	}
	if *once {
		if err := a.Once(ctx, os.Stdout); err != nil {
			log.Error("collection failed", "error", err)
			return 1
		}
		return 0
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mgr.SetStats(a.Stats())
	mgr.SetRestart(cancel)
	a.OnExportSuccess(mgr.Confirm)
	php := newPHPAgentManager(cfg, ver, install, mgr, a, cancel, log) // phpagent.go
	php.SetStats(a.Stats())
	php.Startup()
	java := newJavaAgentManager(cfg, ver, install, mgr, cancel, log) // javaagent.go
	java.Startup()
	syncer := &update.Syncer{
		Endpoint: cfg.Endpoint, LicenseKey: cfg.LicenseKey, UserAgent: update.AgentName + "/" + ver,
		Client: &http.Client{Timeout: 30 * time.Second}, Log: log.With("component", "sync"),
		Handle: mgr.Handle, Kick: mergeKicks(ctx, mgr.Kick(), php.Kick(), java.Kick()), InitialDelay: -1,
		Integrations: a.ApplyRemoteIntegrations,
		PHPAgent:     php.SetRemote,
		JavaAgent:    java.SetRemote,
		Request: func() update.SyncRequest {
			return update.SyncRequest{
				HostID: a.HostID(), HostName: a.HostName(), Agent: mgr.AgentInfo(),
				Update: mgr.Report(), ConfigHash: configHash(*configPath),
				IntegrationsConfigRevision: a.IntegrationsConfigRevision(),
				Reconcile:                  install.Reconcile.Report(),
				PHPAgent:                   php.Report(),
				JavaAgent:                  java.Report(),
				PHPAccess:                  a.PHPAccess(update.AgentUser, filepath.Join(filepath.Dir(*configPath), phpaccess.OptOutFile)),
			}
		},
	}
	mgr.SetReporter(func(ctx context.Context) {
		if _, err := syncer.Once(ctx); err != nil {
			log.Debug("pre-restart sync failed", "error", err)
		}
	})
	var wg sync.WaitGroup
	wg.Go(func() { syncer.Run(ctx) })
	wg.Go(func() { mgr.Run(ctx) })
	wg.Go(func() { php.Run(ctx) })
	wg.Go(func() { java.Run(ctx) })

	if err := a.Run(ctx); err != nil {
		log.Error("agent failed", "error", err)
		return 1
	}
	cancel()
	wg.Wait()
	if mgr.Restarting() {
		log.Info("agent exited to restart into another version", "state", mgr.State().Status)
	}
	return 0
}

func newUpdateManager(cfg *config.Config, cfgPath, ver string, log *slog.Logger) (*update.Manager, update.Install) {
	keys, err := release.TrustedKeys(cfg.Release.TrustedKeysFile)
	if err != nil {
		log.Warn("release keys unusable; updates cannot be applied", "error", err)
		keys = nil
	}
	exe, _ := os.Executable()
	install := update.Detect(update.Env{
		Executable: exe, InstallRoot: cfg.Update.InstallRoot,
		UpdatesEnabled: cfg.Update.Enabled, HaveTrustedKeys: len(keys) > 0,
		InvocationID: os.Getenv("INVOCATION_ID"),
	})
	log.Info("install detected", "version", ver, "install_method", install.Method, "update_capable", install.Capable,
		"update_mode", install.Mode, "reason", install.Reason, "version_dir", install.VersionDir)
	if install.Notice != "" && install.VersionDir != "" && install.Method != update.MethodContainer {
		log.Warn(install.Notice)
	}
	return update.NewManager(update.Options{
		StateDir: cfg.StateDir, ConfigPath: cfgPath, Version: ver, Commit: version.Commit,
		Install: install, Trusted: keys, Endpoint: cfg.Endpoint, LicenseKey: cfg.LicenseKey, Log: log,
	}), install
}

func printVersion(w io.Writer, cfg *config.Config) {
	keys, keyErr := release.TrustedKeys(cfg.Release.TrustedKeysFile)
	exe, _ := os.Executable()
	install := update.Detect(update.Env{
		Executable: exe, InstallRoot: cfg.Update.InstallRoot,
		UpdatesEnabled: cfg.Update.Enabled, HaveTrustedKeys: keyErr == nil && len(keys) > 0,
	})
	orUnknown := func(s string) string {
		if s == "" {
			return "unknown"
		}
		return s
	}
	fmt.Fprintln(w, "openlog-infra-agent", version.Current())
	fmt.Fprintln(w, "commit:", orUnknown(version.Commit))
	fmt.Fprintln(w, "date:", orUnknown(version.Date))
	method := install.Method
	if install.PackageVersion != "" {
		method += " (package version " + install.PackageVersion + ")"
	}
	fmt.Fprintln(w, "install method:", method)
	if install.Capable {
		fmt.Fprintln(w, "update capable: yes ("+install.Mode+")")
	} else {
		fmt.Fprintln(w, "update capable: no ("+install.Reason+")")
	}
	fmt.Fprintf(w, "trusted release keys: %d compiled in, %d total\n", release.CompiledKeyCount(), len(keys))
}

func runValidateRules(cfg *config.Config) int {
	rs, err := agent.LoadRules(cfg.Discovery.RulesDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid discovery rules:\n"+err.Error())
		return 1
	}
	fmt.Printf("%d discovery rules OK (embedded + %s)\n", len(rs), cfg.Discovery.RulesDir)
	return 0
}

// runSelfTest is "-self-test": run by the updater on a freshly extracted binary before switching.
func runSelfTest(cfg *config.Config, ver string) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "self-test failed: "+format+"\n", a...)
		return 1
	}
	if err := cfg.Validate(true); err != nil {
		return fail("invalid configuration: %v", err)
	}
	if err := os.MkdirAll(cfg.StateDir, 0o750); err != nil {
		return fail("state dir: %v", err)
	}
	f, err := os.CreateTemp(cfg.StateDir, ".self-test-*")
	if err != nil {
		return fail("state dir %s not writable: %v", cfg.StateDir, err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return fail("state dir %s: %v", cfg.StateDir, err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a, err := agent.New(cfg, ver, log, false)
	if err != nil {
		return fail("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), selfTestBudget)
	defer cancel()
	if err := a.SelfTest(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fail("collectors did not finish within %s", selfTestBudget)
		}
		return fail("%v", err)
	}
	fmt.Println("self-test OK", ver)
	return 0
}

// configHash is "sha256:<hex>" of the configuration file bytes (empty input when there is no file).
func configHash(path string) string {
	b, _ := os.ReadFile(filepath.Clean(path))
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
