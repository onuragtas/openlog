package ebpfprofiler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
)

// The unprivileged side cannot verify ownership; it reports what the marker says and lets the privileged step
// decide. Reporting every installation as the package manager's would stop the agent from ever updating its own.
func TestManagedByWithoutSys(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	if got := ManagedBy(nil, e.root); got != ManagedNone {
		t.Fatalf("no root = %q", got)
	}
	e.fakeInstall("1.0.0", e.goos, false)
	if got := ManagedBy(nil, e.root); got != ManagedPackage {
		t.Fatalf("root without marker = %q", got)
	}
	e.fakeInstall("1.0.0", e.goos, true)
	if got := ManagedBy(nil, e.root); got != ManagedFleet {
		t.Fatalf("root with marker = %q", got)
	}
	file := filepath.Join(e.base, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ManagedBy(nil, file); got != ManagedManual {
		t.Fatalf("a file = %q", got)
	}
}

func TestManagerReport(t *testing.T) {
	skipUnsupported(t)
	t.Run("nothing installed", func(t *testing.T) {
		e := newEnv(t)
		r := e.manager("linux", nil).Report()
		if r.Status != StatusNotFound || r.CurrentVersion != "" || r.TargetVersion != "1.0.0" || r.Source != SourceLocal {
			t.Fatalf("= %+v", r)
		}
		if r.Managed != ManagedNone || !r.Capable {
			t.Fatalf("= %+v", r)
		}
	})
	t.Run("installed and running", func(t *testing.T) {
		e := newEnv(t)
		e.fakeInstall("1.0.0", "linux", true)
		e.unitActive = true
		r := e.manager("linux", nil).Report()
		if r.Status != StatusInstalled || r.CurrentVersion != "1.0.0" || !r.Active || r.Unit != UnitName {
			t.Fatalf("= %+v", r)
		}
	})
	t.Run("installed but not running", func(t *testing.T) {
		e := newEnv(t)
		e.fakeInstall("1.0.0", "linux", true)
		r := e.manager("linux", nil).Report()
		if r.Status != StatusInactive || !strings.Contains(r.Detail, UnitName) {
			t.Fatalf("= %+v", r)
		}
	})
	t.Run("installed by the package manager", func(t *testing.T) {
		e := newEnv(t)
		e.fakeInstall("1.0.0", "linux", false)
		e.unitActive = true
		r := e.manager("linux", nil).Report()
		if r.Status != StatusUnmanaged || r.Managed != ManagedPackage || !strings.Contains(r.Detail, "left alone") {
			t.Fatalf("= %+v", r)
		}
	})
	t.Run("not capable", func(t *testing.T) {
		e := newEnv(t)
		r := e.manager("darwin", func(o *Options) { o.Capable, o.Reason = false, "the profiler is built for linux only" }).Report()
		if r.Capable || r.Reason == "" {
			t.Fatalf("= %+v", r)
		}
	})
}

func TestManagerSetRemote(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	m := e.manager("linux", nil)

	m.SetRemote(json.RawMessage(`{"mode":"frobnicate"}`))
	if r := m.Report(); r.Source != SourceLocal || r.Mode != config.EBPFProfilerModeAuto {
		t.Fatalf("an invalid mode was accepted: %+v", r)
	}
	m.SetRemote(json.RawMessage(`{"mode":"auto","target_version":"not-a-version"}`))
	if r := m.Report(); r.Source != SourceLocal {
		t.Fatalf("an invalid target version was accepted: %+v", r)
	}

	m.SetRemote(json.RawMessage(`{"mode":"manual","target_version":"2.0.0"}`))
	r := m.Report()
	if r.Source != SourceRemote || r.Mode != config.EBPFProfilerModeManual || r.TargetVersion != "2.0.0" {
		t.Fatalf("fleet settings not applied: %+v", r)
	}
	if b, err := os.ReadFile(filepath.Join(e.state, StateSubdir, RemoteFile)); err != nil || !strings.Contains(string(b), "2.0.0") {
		t.Fatalf("fleet settings not persisted: %s %v", b, err)
	}
	// A manager that starts again keeps them.
	if r := e.manager("linux", nil).Report(); r.Source != SourceRemote || r.TargetVersion != "2.0.0" {
		t.Fatalf("fleet settings not reloaded: %+v", r)
	}
	// remote_config: false means the host's own configuration wins.
	e.cfg.RemoteConfig = false
	if r := e.manager("linux", nil).Report(); r.Source != SourceLocal || r.TargetVersion != "1.0.0" {
		t.Fatalf("remote_config false ignored: %+v", r)
	}
}

// release serves the profiler tarball of version and returns the signed manifest for it.
func (e *env) release(t *testing.T, version, osName string) (manifest, sig []byte, srv *httptest.Server) {
	t.Helper()
	archive := testArchive(t, version, osName, e.goarch, archiveOpts{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, TopDir(version, osName, e.goarch)+".tar.gz") {
			http.NotFound(w, r)
			return
		}
		w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	manifest = testArchiveManifestAt(t, version, osName, e.goarch, "", archive, srv.URL)
	return manifest, signLine(manifest, e.key), srv
}

func TestManagerInstallsAndHandsOver(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	manifest, sig, _ := e.release(t, "1.0.0", "linux")
	m := e.manager("linux", func(o *Options) {
		o.OwnManifest = func() ([]byte, []byte, error) { return manifest, sig, nil }
	})
	m.Evaluate(context.Background())

	req, ok := e.readRequest()
	if !ok || req.Action != ActionInstall || req.Version != "1.0.0" || req.ID == "" {
		t.Fatalf("request = %+v (ok=%t)", req, ok)
	}
	if e.restarts != 1 {
		t.Fatalf("hand-overs = %d", e.restarts)
	}
	if st := e.readState(); st.Phase != StateRestarting || st.Operation != OpInstall || st.RequestID != req.ID {
		t.Fatalf("state = %+v", st)
	}
	staged := filepath.Join(e.state, StateSubdir, StagedDir, "1.0.0")
	for _, name := range []string{ArchiveFile, ManifestFile, SignatureFile} {
		if _, err := os.Stat(filepath.Join(staged, name)); err != nil {
			t.Fatalf("%s not staged: %v", name, err)
		}
	}
	// What was staged is exactly what the privileged step will accept.
	if b, _ := os.ReadFile(filepath.Join(staged, ManifestFile)); string(b) != string(manifest) {
		t.Fatal("the staged manifest is not the signed one")
	}
	if r := m.Report(); r.Status != StatusStaged {
		t.Fatalf("report = %+v", r)
	}

	// While a request is pending nothing else is started.
	e.restarts = 0
	m.Evaluate(context.Background())
	if e.restarts != 0 {
		t.Fatal("a second request was made while one was pending")
	}
}

func TestManagerDoesNotActWhenItShouldNot(t *testing.T) {
	skipUnsupported(t)
	cases := []struct {
		name  string
		setup func(e *env, o *Options)
	}{
		{"an installation of the package manager", func(e *env, o *Options) { e.fakeInstall("0.9.0", "linux", false) }},
		{"not capable", func(e *env, o *Options) { o.Capable = false }},
		{"mode manual", func(e *env, o *Options) { e.cfg.Mode = config.EBPFProfilerModeManual; o.Config = e.cfg }},
		{"mode off and nothing installed", func(e *env, o *Options) { e.cfg.Mode = config.EBPFProfilerModeOff; o.Config = e.cfg }},
		{"a self-update is running", func(e *env, o *Options) { o.CanRestart = func() bool { return false } }},
		{"no release for this platform", func(e *env, o *Options) { o.OwnManifest = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			manifest, sig, _ := e.release(t, "1.0.0", "linux")
			m := e.manager("linux", func(o *Options) {
				o.OwnManifest = func() ([]byte, []byte, error) { return manifest, sig, nil }
				c.setup(e, o)
			})
			m.Evaluate(context.Background())
			if _, ok := e.readRequest(); ok {
				t.Fatal("a request was written")
			}
			if e.restarts != 0 {
				t.Fatalf("hand-overs = %d", e.restarts)
			}
		})
	}
}

func TestManagerOffRemovesItsOwnInstallation(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	e.fakeInstall("1.0.0", "linux", true)
	e.cfg.Mode = config.EBPFProfilerModeOff
	m := e.manager("linux", func(o *Options) { o.Config = e.cfg })
	m.Evaluate(context.Background())
	req, ok := e.readRequest()
	if !ok || req.Action != ActionUninstall {
		t.Fatalf("request = %+v (ok=%t)", req, ok)
	}
	if st := e.readState(); st.Operation != OpUninstall || st.Phase != StateRestarting {
		t.Fatalf("state = %+v", st)
	}
}

func TestManagerDoesNotRetryWhatFailed(t *testing.T) {
	skipUnsupported(t)
	for _, c := range []struct {
		name, phase string
		after       time.Duration
		want        bool
	}{
		{"rolled back is never retried", StateRolledBack, 365 * 24 * time.Hour, false},
		{"failed is not retried at once", StateFailed, time.Minute, false},
		{"failed is retried later", StateFailed, FailedRetryDelay + time.Minute, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			manifest, sig, _ := e.release(t, "1.0.0", "linux")
			st := State{RequestID: "0000000000000001", Action: ActionInstall, Operation: OpInstall,
				Version: "1.0.0", Phase: c.phase, ChangedAt: e.now}
			b, _ := json.Marshal(st)
			if err := os.MkdirAll(filepath.Join(e.state, StateSubdir), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(e.state, StateSubdir, StateFile), b, 0o640); err != nil {
				t.Fatal(err)
			}
			e.now = e.now.Add(c.after)
			m := e.manager("linux", func(o *Options) {
				o.OwnManifest = func() ([]byte, []byte, error) { return manifest, sig, nil }
			})
			m.Evaluate(context.Background())
			if _, ok := e.readRequest(); ok != c.want {
				t.Fatalf("request written = %t, want %t", ok, c.want)
			}
		})
	}
}

func TestManagerStartupTakesOverTheResult(t *testing.T) {
	skipUnsupported(t)
	t.Run("the privileged step ran", func(t *testing.T) {
		e := newEnv(t)
		e.fakeInstall("1.0.0", "linux", true)
		writeState(t, e, State{RequestID: "00000000000000aa", Action: ActionInstall, Operation: OpInstall,
			Version: "1.0.0", Phase: StateRestarting})
		writeStatus(t, e, Status{RequestID: "00000000000000aa", Action: ActionInstall, Result: ResultApplied,
			Target: "1.0.0", Version: "1.0.0", At: e.now})
		m := e.manager("linux", nil)
		m.Startup()
		st := e.readState()
		if st.Phase != StateConfirming || st.HealthAt.IsZero() {
			t.Fatalf("state = %+v", st)
		}
		// The request and the staged release are cleaned up.
		if _, ok := e.readRequest(); ok {
			t.Fatal("the handled request was kept")
		}
	})
	t.Run("the privileged step did not run", func(t *testing.T) {
		e := newEnv(t)
		writeState(t, e, State{RequestID: "00000000000000bb", Action: ActionInstall, Version: "1.0.0", Phase: StateRestarting})
		m := e.manager("linux", nil)
		m.Startup()
		if st := e.readState(); st.Phase != StateFailed || !strings.Contains(st.Error, "did not handle the request") {
			t.Fatalf("state = %+v", st)
		}
	})
}

func TestManagerVerifyRollsBackWhenItDoesNotRun(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	e.fakeInstall("1.1.0", "linux", true)
	writeStatus(t, e, Status{RequestID: "00000000000000cc", Result: ResultApplied, Target: "1.1.0",
		Version: "1.1.0", Previous: "1.0.0", At: e.now})
	writeState(t, e, State{RequestID: "00000000000000cc", Action: ActionInstall, Operation: OpUpgrade,
		Version: "1.1.0", Phase: StateConfirming, HealthAt: e.now})
	e.now = e.now.Add(time.Hour)

	m := e.manager("linux", nil) // unitActive stays false: the new version does not run
	m.Evaluate(context.Background())
	if st := e.readState(); st.Action != ActionRollback || st.Version != "1.0.0" {
		t.Fatalf("state = %+v", st)
	}
	req, ok := e.readRequest()
	if !ok || req.Action != ActionRollback || req.Version != "1.0.0" {
		t.Fatalf("request = %+v (ok=%t)", req, ok)
	}
}

// The service can be running and still be the wrong version: something else switched current, or the
// privileged step did not get that far. Verification has to notice that, not just that a process is up.
func TestManagerVerifyNoticesTheWrongVersionIsCurrent(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	e.fakeInstall("1.1.0", "linux", true) // the version we think we installed is there and intact
	e.fakeInstall("1.0.0", "linux", true) // ... but current points at the old one
	e.unitActive = true                   // and the service is happily running it
	writeStatus(t, e, Status{RequestID: "00000000000000ee", Result: ResultApplied, Target: "1.1.0",
		Version: "1.1.0", Previous: "1.0.0", At: e.now})
	writeState(t, e, State{RequestID: "00000000000000ee", Action: ActionInstall, Operation: OpUpgrade,
		Version: "1.1.0", Phase: StateConfirming, HealthAt: e.now})
	e.now = e.now.Add(time.Hour)

	e.manager("linux", nil).Evaluate(context.Background())
	st := e.readState()
	if !strings.Contains(st.Error, "current points at") {
		t.Fatalf("the version mismatch went unnoticed: %+v", st)
	}
	req, ok := e.readRequest()
	if !ok || req.Action != ActionRollback || req.Version != "1.0.0" {
		t.Fatalf("request = %+v (ok=%t)", req, ok)
	}
}

func TestManagerVerifyAcceptsAHealthyInstallation(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	e.fakeInstall("1.0.0", "linux", true)
	e.unitActive = true
	writeStatus(t, e, Status{RequestID: "00000000000000dd", Result: ResultApplied, Target: "1.0.0", Version: "1.0.0", At: e.now})
	writeState(t, e, State{RequestID: "00000000000000dd", Action: ActionInstall, Operation: OpInstall,
		Version: "1.0.0", Phase: StateConfirming, HealthAt: e.now})
	e.now = e.now.Add(time.Hour)

	m := e.manager("linux", nil)
	m.Evaluate(context.Background())
	if st := e.readState(); st.Phase != StateApplied || st.Error != "" {
		t.Fatalf("state = %+v", st)
	}
	if _, ok := e.readRequest(); ok {
		t.Fatal("a healthy installation asked for something")
	}
	if r := m.Report(); r.Status != StatusInstalled {
		t.Fatalf("report = %+v", r)
	}
}

func writeState(t *testing.T, e *env, st State) {
	t.Helper()
	if st.ChangedAt.IsZero() {
		st.ChangedAt = e.now
	}
	b, _ := json.Marshal(st)
	if err := os.MkdirAll(filepath.Join(e.state, StateSubdir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.state, StateSubdir, StateFile), b, 0o640); err != nil {
		t.Fatal(err)
	}
}

func writeStatus(t *testing.T, e *env, st Status) {
	t.Helper()
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(e.infra, StatusFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
