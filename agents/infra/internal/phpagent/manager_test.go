package phpagent

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
)

type mgrEnv struct {
	t        *testing.T
	key      testKey
	base     string
	stateDir string
	status   string
	root     string
	bin      string
	host     *fakeHost
	now      time.Time
	restarts int
	apm      bool
	canRst   bool
	stats    *selfmon.Stats
	srv      *httptest.Server
	files    map[string][]byte
	api      string // PHP Extension of the fake binary
}

func newMgrEnv(t *testing.T) *mgrEnv {
	base := t.TempDir()
	e := &mgrEnv{t: t, key: newKey(t), base: base, stateDir: filepath.Join(base, "state"), status: filepath.Join(base, "infra"),
		root: filepath.Join(base, "php-agent"), now: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), canRst: true,
		stats: selfmon.New(time.Now()), files: map[string][]byte{}, api: "20220829"}
	for _, d := range []string{e.stateDir, e.status, filepath.Join(base, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A PHP binary found through ExtraBins; its -i/-m output comes from the fake runner.
	e.bin = filepath.Join(base, "bin", "php-fpm8.2")
	if err := os.WriteFile(e.bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(e.bin); err == nil {
		e.bin = real
	}
	e.host = newFakeHost(e.root, e.bin)
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := e.files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *mgrEnv) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == e.bin {
		switch args[0] {
		case "-i":
			return []byte("phpinfo()\nPHP Version => 8.2.29\nPHP Extension => " + e.api + "\nThread Safety => disabled\nDebug Build => no\n" +
				"Scan this dir for additional .ini files => /nonexistent/conf.d\n"), nil
		case "-m":
			return []byte("[PHP Modules]\nCore\n"), nil
		}
	}
	if strings.HasPrefix(name, "dpkg") || name == "rpm" || name == "apk" {
		return nil, fmt.Errorf("not owned")
	}
	return e.host.run(ctx, name, args...)
}

func (e *mgrEnv) manager(mutate ...func(*Options)) *Manager {
	cfg := config.DefaultFor("linux").PHPAgent // the PHP agent installer is Linux-only
	cfg.InstallRoot = e.root
	o := Options{Config: cfg, StateDir: e.stateDir, StatusDir: e.status, AgentVersion: "0.9.1", OS: "linux", Arch: testArch,
		Trusted: []ed25519.PublicKey{e.key.pub}, Capable: true, ExtraBins: func() []string { return []string{e.bin} },
		APMActive: func() bool { return e.apm }, CanRestart: func() bool { return e.canRst }, Restart: func() { e.restarts++ },
		Run: e.run, Now: func() time.Time { return e.now }}
	for _, f := range mutate {
		f(&o)
	}
	m := NewManager(o)
	m.SetStats(e.stats)
	return m
}

// release publishes an archive on the test server and returns the fleet settings offering it.
func (e *mgrEnv) release(version, so string) Remote {
	archive := phpTarball(e.t, version, so)
	name := TopDir(version, "linux", testArch) + ".tar.gz"
	e.files[name] = archive
	manifest := phpManifest(e.t, version, e.srv.URL, archive, "")
	return Remote{Mode: config.PHPAgentModeAuto, Version: version, Reload: config.PHPAgentReloadGraceful, TargetVersion: version,
		Manifest: base64.StdEncoding.EncodeToString(manifest), Signature: string(signLine(manifest, e.key))}
}

func (e *mgrEnv) setRemote(m *Manager, r Remote) {
	b, _ := json.Marshal(r)
	m.SetRemote(b)
}

func (e *mgrEnv) ops() map[selfmon.PHPAgentOp]uint64 { return e.stats.Snapshot().PHPAgentOps }

func TestManagerInstallsWhenTheFleetSaysAuto(t *testing.T) {
	e := newMgrEnv(t)
	m := e.manager()
	ctx := context.Background()

	m.Evaluate(ctx)
	rep := m.Report()
	if rep.Mode != config.PHPAgentModeManual || rep.Source != SourceLocal || rep.ManagedBy != ManagedNone || len(rep.Runtimes) != 1 {
		t.Fatalf("report before the fleet = %+v", rep)
	}
	if r := rep.Runtimes[0]; r.Bin != e.bin || r.Module != testModule || !r.Supported || r.Loaded {
		t.Errorf("runtime = %+v", r)
	}
	if e.restarts != 0 {
		t.Fatal("manual mode must not install")
	}

	e.apm = true
	e.setRemote(m, e.release("0.9.1", goodSO))
	m.Evaluate(ctx)
	if e.restarts != 1 {
		t.Fatalf("restarts = %d; report %+v", e.restarts, m.Report().Update)
	}
	var req Request
	b, err := os.ReadFile(filepath.Join(e.stateDir, StateSubdir, RequestFile))
	if err != nil || json.Unmarshal(b, &req) != nil {
		t.Fatalf("request: %v", err)
	}
	if req.Action != ActionInstall || req.Version != "0.9.1" || req.Reload != config.PHPAgentReloadGraceful || !requestID.MatchString(req.ID) {
		t.Errorf("request = %+v", req)
	}
	for _, f := range []string{ArchiveFile, ManifestFile, SignatureFile} {
		if _, err := os.Stat(filepath.Join(e.stateDir, StateSubdir, StagedDir, "0.9.1", f)); err != nil {
			t.Errorf("staged %s: %v", f, err)
		}
	}
	if u := m.Report().Update; u == nil || u.State != StateRestarting || u.Operation != OpInstall || u.Version != "0.9.1" {
		t.Errorf("update = %+v", u)
	}

	// The next start: -apply installs, the agent confirms after the health check.
	st := Apply(ctx, ApplyOptions{Sys: testSys(), StateDir: e.stateDir, StatusDir: e.status, Config: config.PHPAgentConfig{InstallRoot: e.root, Mode: "manual", RemoteConfig: true},
		Trusted: []ed25519.PublicKey{e.key.pub}, OS: "linux", Arch: testArch, Run: e.host.run, Now: func() time.Time { return e.now }})
	if st == nil || st.Result != ResultApplied {
		t.Fatalf("apply = %+v", st)
	}
	m = e.manager()
	m.Startup()
	if u := m.Report().Update; u == nil || u.State != StateConfirming {
		t.Fatalf("after start: %+v", u)
	}
	if _, err := os.Stat(filepath.Join(e.stateDir, StateSubdir, RequestFile)); !os.IsNotExist(err) {
		t.Error("handled request not removed")
	}
	m.Evaluate(ctx)
	if u := m.Report().Update; u.State != StateConfirming {
		t.Fatalf("health check ran early: %+v", u)
	}
	e.now = e.now.Add(6 * time.Minute)
	m.Evaluate(ctx)
	rep = m.Report()
	if rep.Update.State != StateApplied || rep.Version != "0.9.1" || rep.ManagedBy != ManagedFleet || rep.Source != SourceRemote {
		t.Fatalf("after the health check: %+v %+v", rep, rep.Update)
	}
	if got := e.ops()[selfmon.PHPAgentOp{Operation: OpInstall, Result: "success"}]; got != 1 {
		t.Errorf("operations = %v", e.ops())
	}
	m.Evaluate(ctx)
	if e.restarts != 1 {
		t.Errorf("restarted again for the installed version: %d", e.restarts)
	}
}

func TestManagerHealthCheckFailureRequestsRollback(t *testing.T) {
	e := newMgrEnv(t)
	m := e.manager()
	ctx := context.Background()
	e.apm = true
	e.setRemote(m, e.release("0.9.1", goodSO))
	m.Evaluate(ctx)
	Apply(ctx, ApplyOptions{Sys: testSys(), StateDir: e.stateDir, StatusDir: e.status, Config: config.PHPAgentConfig{InstallRoot: e.root, RemoteConfig: true},
		Trusted: []ed25519.PublicKey{e.key.pub}, OS: "linux", Arch: testArch, Run: e.host.run, Now: func() time.Time { return e.now }})
	m = e.manager()
	m.Startup()

	e.apm = false // PHP services stopped sending data
	e.host.units["php8.2-fpm.service"] = "failed"
	e.now = e.now.Add(6 * time.Minute)
	m.Evaluate(ctx)
	if e.restarts != 2 {
		t.Fatalf("restarts = %d", e.restarts)
	}
	u := m.Report().Update
	if u.State != StateRestarting || u.Operation != OpRollback || !strings.Contains(u.Error, "php8.2-fpm.service is failed") || !strings.Contains(u.Error, "sent data before") {
		t.Fatalf("update = %+v", u)
	}
	if got := e.ops()[selfmon.PHPAgentOp{Operation: OpInstall, Result: "failure"}]; got != 1 {
		t.Errorf("operations = %v", e.ops())
	}

	Apply(ctx, ApplyOptions{Sys: testSys(), StateDir: e.stateDir, StatusDir: e.status, Config: config.PHPAgentConfig{InstallRoot: e.root, RemoteConfig: true},
		Trusted: []ed25519.PublicKey{e.key.pub}, OS: "linux", Arch: testArch, Run: e.host.run})
	m = e.manager()
	m.Startup()
	rep := m.Report()
	if rep.Update.State != StateRolledBack || rep.Version != "" || !strings.Contains(rep.Update.Error, "health check failed") {
		t.Fatalf("after rollback: %+v %+v", rep, rep.Update)
	}
	if got := e.ops()[selfmon.PHPAgentOp{Operation: OpRollback, Result: "success"}]; got != 1 {
		t.Errorf("operations = %v", e.ops())
	}
	// A rolled back version is never retried automatically.
	m.Evaluate(ctx)
	if e.restarts != 2 {
		t.Errorf("rolled back version retried: restarts = %d", e.restarts)
	}
}

func TestManagerModeOffUninstallsFleetInstallation(t *testing.T) {
	e := newMgrEnv(t)
	ctx := context.Background()
	m := e.manager()
	e.setRemote(m, e.release("0.9.1", goodSO))
	m.Evaluate(ctx)
	Apply(ctx, ApplyOptions{Sys: testSys(), StateDir: e.stateDir, StatusDir: e.status, Config: config.PHPAgentConfig{InstallRoot: e.root, RemoteConfig: true},
		Trusted: []ed25519.PublicKey{e.key.pub}, OS: "linux", Arch: testArch, Run: e.host.run, Now: func() time.Time { return e.now }})
	m = e.manager()
	m.Startup()
	// Past the health check of the install, on the test clock (Apply must not use the wall clock: the test failed
	// once the real time passed the fixed test time).
	e.now = e.now.Add(6 * time.Minute)

	e.setRemote(m, Remote{Mode: config.PHPAgentModeOff})
	m.canRestartOnce(e, false)
	m.Evaluate(ctx)
	if e.restarts != 1 {
		t.Fatalf("uninstall must wait for CanRestart: restarts = %d", e.restarts)
	}
	e.canRst = true
	m.Evaluate(ctx)
	if e.restarts != 2 {
		t.Fatalf("restarts = %d", e.restarts)
	}
	if u := m.Report().Update; u.Operation != OpUninstall || u.State != StateRestarting {
		t.Fatalf("update = %+v", u)
	}
	Apply(ctx, ApplyOptions{Sys: testSys(), StateDir: e.stateDir, StatusDir: e.status, Config: config.PHPAgentConfig{InstallRoot: e.root, RemoteConfig: true},
		Trusted: []ed25519.PublicKey{e.key.pub}, OS: "linux", Arch: testArch, Run: e.host.run, Now: func() time.Time { return e.now }})
	m = e.manager()
	m.Startup()
	m.Evaluate(ctx)
	rep := m.Report()
	if rep.Update.State != StateUninstalled || rep.ManagedBy != ManagedNone || rep.Version != "" || rep.Mode != config.PHPAgentModeOff {
		t.Fatalf("report = %+v %+v", rep, rep.Update)
	}
	if e.restarts != 2 {
		t.Errorf("restarts = %d", e.restarts)
	}
}

// canRestartOnce sets the CanRestart answer (test helper).
func (m *Manager) canRestartOnce(e *mgrEnv, v bool) { e.canRst = v }

func TestManagerLeavesPackageInstallationsAlone(t *testing.T) {
	e := newMgrEnv(t)
	if err := os.MkdirAll(filepath.Join(e.root, "versions", "0.9.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := e.manager()
	e.setRemote(m, Remote{Mode: config.PHPAgentModeOff})
	m.Evaluate(context.Background())
	e.setRemote(m, e.release("0.9.1", goodSO))
	m.Evaluate(context.Background())
	if e.restarts != 0 {
		t.Fatalf("restarts = %d", e.restarts)
	}
	if rep := m.Report(); rep.ManagedBy != ManagedManual {
		t.Errorf("managed_by = %s", rep.ManagedBy)
	}
}

func TestManagerFailuresBeforeTheRestart(t *testing.T) {
	cases := map[string]struct {
		remote func(e *mgrEnv) Remote
		opts   func(*Options)
		want   string
	}{
		"not capable": {func(e *mgrEnv) Remote { return e.release("0.9.1", goodSO) }, func(o *Options) { o.Capable = false }, ""},
		"forged signature": {func(e *mgrEnv) Remote {
			r := e.release("0.9.1", goodSO)
			b, _ := base64.StdEncoding.DecodeString(r.Manifest)
			r.Signature = string(signLine(b, newKey(e.t)))
			return r
		}, nil, "signature"},
		"sha256 mismatch": {func(e *mgrEnv) Remote {
			r := e.release("0.9.1", goodSO)
			name := TopDir("0.9.1", "linux", testArch) + ".tar.gz"
			e.files[name] = append([]byte(nil), e.files[name]...)
			e.files[name][10] ^= 0xff
			return r
		}, nil, "sha256 mismatch"},
		"no module for the runtime": {func(e *mgrEnv) Remote {
			e.api = "20230831" // PHP 8.3: supported, but this release only has the 8.2 module
			return e.release("0.9.1", goodSO)
		}, nil, "has no openlog.so for the PHP runtimes"},
		"local version without a manifest": {func(e *mgrEnv) Remote { return Remote{Mode: config.PHPAgentModeAuto, TargetVersion: "0.9.7"} }, nil, "no signed manifest"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newMgrEnv(t)
			var opts []func(*Options)
			if c.opts != nil {
				opts = append(opts, c.opts)
			}
			m := e.manager(opts...)
			e.setRemote(m, c.remote(e))
			m.Evaluate(context.Background())
			if e.restarts != 0 {
				t.Fatalf("restarted")
			}
			u := m.Report().Update
			if c.want == "" {
				return
			}
			if u == nil || u.State != StateFailed || !strings.Contains(u.Error, c.want) {
				t.Fatalf("update = %+v, want failed with %q", u, c.want)
			}
			// Not retried within FailedRetryDelay.
			m.Evaluate(context.Background())
			if e.restarts != 0 || m.Report().Update.ChangedAt != u.ChangedAt {
				t.Error("failed version retried immediately")
			}
		})
	}
}

func TestManagerStartupWithoutPrivilegedStep(t *testing.T) {
	e := newMgrEnv(t)
	writeJSON(t, filepath.Join(e.stateDir, StateSubdir, StateFile), State{RequestID: "abcdefabcdefabcdef", Action: ActionInstall, Operation: OpInstall, Version: "0.9.1", Phase: StateRestarting})
	m := e.manager(func(o *Options) { o.Capable, o.Reason = false, "unit outdated" })
	m.Startup()
	u := m.Report().Update
	if u == nil || u.State != StateFailed || !strings.Contains(u.Error, "did not handle the request") || !strings.Contains(u.Error, "unit outdated") {
		t.Fatalf("update = %+v", u)
	}
	if got := e.ops()[selfmon.PHPAgentOp{Operation: OpInstall, Result: "failure"}]; got != 1 {
		t.Errorf("operations = %v", e.ops())
	}
}

func TestManagerRemoteConfigDisabled(t *testing.T) {
	e := newMgrEnv(t)
	m := e.manager(func(o *Options) { o.Config.RemoteConfig = false })
	e.setRemote(m, e.release("0.9.1", goodSO))
	m.Evaluate(context.Background())
	if e.restarts != 0 || m.Report().Source != SourceLocal {
		t.Fatalf("remote settings applied despite remote_config: false")
	}
	e.setRemote(m, Remote{Mode: "bogus"})
	if _, err := os.Stat(filepath.Join(e.stateDir, StateSubdir, RemoteFile)); err != nil {
		t.Errorf("valid remote settings not persisted: %v", err)
	}
}
