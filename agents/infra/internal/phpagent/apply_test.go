package phpagent

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
)

type applyEnv struct {
	t        *testing.T
	key      testKey
	stateDir string
	status   string
	root     string
	fpm, cli string
	host     *fakeHost
	cfg      config.PHPAgentConfig
	n        int
}

func newApplyEnv(t *testing.T) *applyEnv {
	base := t.TempDir()
	e := &applyEnv{t: t, key: newKey(t), stateDir: filepath.Join(base, "state"), status: filepath.Join(base, "infra"),
		root: filepath.Join(base, "php-agent")}
	for _, d := range []string{e.stateDir, e.status, filepath.Join(base, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Real files: --php arguments must be binaries only root can change (TrustedFile).
	for _, p := range []*string{&e.fpm, &e.cli} {
		name := "php-fpm8.2"
		if p == &e.cli {
			name = "php8.2"
		}
		path := filepath.Join(base, "bin", name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		*p = path
	}
	e.host = newFakeHost(e.root, e.fpm, e.cli)
	e.cfg = config.Default().PHPAgent
	e.cfg.InstallRoot = e.root
	return e
}

func (e *applyEnv) stage(version, so, floor string) {
	archive := phpTarball(e.t, version, so)
	manifest := phpManifest(e.t, version, "https://example.com/r", archive, floor)
	stageRelease(e.t, e.stateDir, version, archive, manifest, signLine(manifest, e.key))
}

func (e *applyEnv) request(action, version string, mutate ...func(*Request)) {
	e.n++
	req := Request{ID: strings.Repeat("0", 22) + string(rune('a'+e.n%6)) + string(rune('0'+e.n%10)), Action: action,
		Version: version, Reload: config.PHPAgentReloadGraceful, At: time.Now()}
	for _, f := range mutate {
		f(&req)
	}
	writeJSON(e.t, filepath.Join(e.stateDir, StateSubdir, RequestFile), req)
}

func (e *applyEnv) apply() *Status {
	return Apply(context.Background(), ApplyOptions{Sys: testSys(), StateDir: e.stateDir, StatusDir: e.status, Config: e.cfg,
		Trusted: []ed25519.PublicKey{e.key.pub}, OS: "linux", Arch: testArch, Run: e.host.run})
}

func (e *applyEnv) want(st *Status, result, version string) {
	e.t.Helper()
	if st == nil {
		e.t.Fatal("no status")
	}
	if st.Result != result || st.Version != version {
		e.t.Fatalf("status = %+v, want result %s version %q", st, result, version)
	}
	if got := InstalledVersion(e.root); got != version {
		e.t.Fatalf("current = %q, want %q", got, version)
	}
	saved, err := LoadStatus(e.status)
	if err != nil || saved.RequestID != st.RequestID || saved.Result != result {
		e.t.Fatalf("saved status = %+v, %v", saved, err)
	}
}

func TestApplyInstallUpgradeRollbackUninstall(t *testing.T) {
	e := newApplyEnv(t)

	e.stage("0.9.1", goodSO, "")
	e.request(ActionInstall, "0.9.1")
	st := e.apply()
	e.want(st, ResultApplied, "0.9.1")
	if st.Operation != OpInstall || len(st.Runtimes) != 2 || st.Previous != "" {
		t.Errorf("install status = %+v", st)
	}
	if len(st.Units) != 1 || st.Units[0] != "php8.2-fpm.service" {
		t.Errorf("units = %v", st.Units)
	}
	if calls := e.host.callsWith("openlog-php-install install"); len(calls) != 1 || !strings.Contains(calls[0], "--reload") {
		t.Errorf("install calls = %v", calls)
	}
	for _, f := range []string{MarkerFile, "versions/0.9.1/manifest.json", "versions/0.9.1/manifest.json.sig", "versions/0.9.1/bin/openlog-php-install"} {
		if _, err := os.Stat(filepath.Join(e.root, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if e.apply() != nil {
		t.Error("the same request must be handled once")
	}

	// A broken module: openlog-php-install cannot load it, the previous version is restored.
	e.stage("0.9.2", badSO, "0.9.1")
	e.request(ActionInstall, "0.9.2")
	st = e.apply()
	e.want(st, ResultRolledBack, "0.9.1")
	if st.Operation != OpUpgrade || !strings.Contains(st.Error, "loads into no PHP runtime") {
		t.Errorf("broken upgrade status = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(e.root, "versions", "0.9.2")); !os.IsNotExist(err) {
		t.Errorf("broken version kept: %v", err)
	}
	if !e.host.enabled[e.fpm] || !e.host.moduleLoads() {
		t.Error("0.9.1 not enabled again after the rollback")
	}

	// A good upgrade keeps the previous version for a health check rollback.
	e.stage("0.9.3", goodSO, "0.9.1")
	e.request(ActionInstall, "0.9.3")
	st = e.apply()
	e.want(st, ResultApplied, "0.9.3")
	if st.Previous != "0.9.1" {
		t.Errorf("previous = %q", st.Previous)
	}

	// The agent's health check failed: back to 0.9.1.
	e.request(ActionRollback, "0.9.3")
	st = e.apply()
	e.want(st, ResultRolledBack, "0.9.1")
	if st.Operation != OpRollback {
		t.Errorf("rollback status = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(e.root, "versions", "0.9.3")); !os.IsNotExist(err) {
		t.Errorf("rolled back version kept: %v", err)
	}

	e.request(ActionUninstall, "0.9.1")
	st = e.apply()
	if st == nil || st.Result != ResultUninstalled || st.Version != "" {
		t.Fatalf("uninstall status = %+v", st)
	}
	if _, err := os.Stat(e.root); !os.IsNotExist(err) {
		t.Errorf("install root kept: %v", err)
	}
	if e.host.enabled[e.fpm] {
		t.Error("runtime still enabled after uninstall")
	}
}

func TestApplyBrokenFirstInstallRemovesEverything(t *testing.T) {
	e := newApplyEnv(t)
	e.stage("0.9.1", badSO, "")
	e.request(ActionInstall, "0.9.1")
	st := e.apply()
	e.want(st, ResultFailed, "")
	if _, err := os.Stat(e.root); !os.IsNotExist(err) {
		t.Errorf("install root kept after a failed first installation: %v", err)
	}
	if len(e.host.callsWith("openlog-php-install uninstall")) != 1 {
		t.Errorf("calls = %v", e.host.calls)
	}
}

func TestApplyRejects(t *testing.T) {
	cases := map[string]struct {
		prepare func(e *applyEnv)
		want    string
	}{
		"forged signature": {func(e *applyEnv) {
			archive := phpTarball(e.t, "0.9.1", goodSO)
			manifest := phpManifest(e.t, "0.9.1", "https://example.com/r", archive, "")
			stageRelease(e.t, e.stateDir, "0.9.1", archive, manifest, signLine(manifest, newKey(e.t)))
			e.request(ActionInstall, "0.9.1")
		}, "signature"},
		"version mismatch": {func(e *applyEnv) {
			e.stage("0.9.1", goodSO, "")
			e.request(ActionInstall, "0.9.1")
			os.Rename(filepath.Join(e.stateDir, StateSubdir, StagedDir, "0.9.1"), filepath.Join(e.stateDir, StateSubdir, StagedDir, "0.9.9"))
			e.request(ActionInstall, "0.9.9")
		}, "does not match"},
		"archive symlinked out of the state dir": {func(e *applyEnv) {
			e.stage("0.9.1", goodSO, "")
			p := filepath.Join(e.stateDir, StateSubdir, StagedDir, "0.9.1", ArchiveFile)
			os.Remove(p)
			os.Symlink("/etc/passwd", p)
			e.request(ActionInstall, "0.9.1")
		}, "staged archive"},
		"package installation": {func(e *applyEnv) {
			os.MkdirAll(filepath.Join(e.root, "versions", "0.9.0"), 0o755)
			e.stage("0.9.1", goodSO, "")
			e.request(ActionInstall, "0.9.1")
		}, "not managed by the infra agent"},
		"mode off in the trusted configuration": {func(e *applyEnv) {
			e.cfg.Mode, e.cfg.RemoteConfig = config.PHPAgentModeOff, false
			e.stage("0.9.1", goodSO, "")
			e.request(ActionInstall, "0.9.1")
		}, "mode is off"},
		"invalid exclude glob": {func(e *applyEnv) {
			e.stage("0.9.1", goodSO, "")
			e.request(ActionInstall, "0.9.1", func(r *Request) { r.ExcludeBins = []string{"["} })
		}, "exclude_bins"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newApplyEnv(t)
			c.prepare(e)
			st := e.apply()
			if st == nil || st.Result != ResultRejected || !strings.Contains(st.Error, c.want) {
				t.Fatalf("status = %+v, want rejected with %q", st, c.want)
			}
			if len(e.host.callsWith("openlog-php-install")) != 0 {
				t.Errorf("installer ran: %v", e.host.calls)
			}
		})
	}
}

func TestApplyDowngradeRespectsRollbackFloor(t *testing.T) {
	e := newApplyEnv(t)
	e.stage("0.9.5", goodSO, "0.9.3")
	e.request(ActionInstall, "0.9.5")
	e.want(e.apply(), ResultApplied, "0.9.5")

	e.stage("0.9.2", goodSO, "")
	e.request(ActionInstall, "0.9.2")
	st := e.apply()
	if st.Result != ResultRejected || !strings.Contains(st.Error, "rollback_floor 0.9.3") {
		t.Fatalf("status = %+v", st)
	}
	e.stage("0.9.4", goodSO, "")
	e.request(ActionInstall, "0.9.4")
	e.want(e.apply(), ResultApplied, "0.9.4")
}

func TestApplyExcludeBins(t *testing.T) {
	e := newApplyEnv(t)
	e.stage("0.9.1", goodSO, "")
	e.request(ActionInstall, "0.9.1", func(r *Request) { r.ExcludeBins = []string{filepath.Dir(e.cli) + "/php8*"}; r.Reload = "" })
	st := e.apply()
	e.want(st, ResultApplied, "0.9.1")
	if e.host.enabled[e.cli] || !e.host.enabled[e.fpm] {
		t.Errorf("enabled = %v", e.host.enabled)
	}
	calls := e.host.callsWith("openlog-php-install install")
	if len(calls) != 1 || strings.Contains(calls[0], "--reload") || !strings.Contains(calls[0], "--php "+e.fpm) || strings.Contains(calls[0], e.cli) {
		t.Errorf("install calls = %v", calls)
	}
	if len(st.Units) != 0 {
		t.Errorf("units without reload = %v", st.Units)
	}
}

func TestApplyNotRoot(t *testing.T) {
	e := newApplyEnv(t)
	e.stage("0.9.1", goodSO, "")
	e.request(ActionInstall, "0.9.1")
	sys := testSys()
	sys.IsRoot = func() bool { return false }
	if st := Apply(context.Background(), ApplyOptions{Sys: sys, StateDir: e.stateDir, StatusDir: e.status, Config: e.cfg, Run: e.host.run}); st != nil {
		t.Fatalf("status = %+v", st)
	}
}
