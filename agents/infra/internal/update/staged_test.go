package update

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stagedManager is a manager in staged mode whose start saw the given apply status.
func (f *fixture) stagedManager(version string, apply *ApplyStatus) (*Manager, *atomic.Int32) {
	restarts := &atomic.Int32{}
	m := NewManager(Options{
		StateDir: f.stateDir, Version: version,
		Install: Install{Method: MethodTarball, Capable: true, Mode: ModeStaged, InstallRoot: f.root, VersionDir: version, Apply: apply},
		Trusted: []ed25519.PublicKey{f.key.pub}, OS: "linux", Arch: "amd64",
		Now: func() time.Time { return f.now }, SelfTestTimeout: 10 * time.Second,
	})
	m.SetRestart(func() { restarts.Add(1) })
	return m, restarts
}

// TestStagedRoundTrip runs the unprivileged manager and the privileged apply against the same install.
func TestStagedRoundTrip(t *testing.T) {
	f := newFixture(t)
	sys := newFakeSys(t)
	installVersion(t, f.root, "0.9.0", "exit 0", manifestFor(t, "0.9.0", "https://x", []byte("a"), map[string]string{"rollback_floor": "0.8.0"}, nil))
	SwitchCurrent(f.root, "0.9.0")
	apply := func(inv string) *ApplyStatus {
		cur, _ := CurrentDir(f.root)
		return Apply(context.Background(), ApplyOptions{
			Sys: sys.Sys, Install: Install{Method: MethodTarball, InstallRoot: f.root, VersionDir: cur},
			StateDir: f.stateDir, Version: cur, Trusted: []ed25519.PublicKey{f.key.pub}, UpdatesEnabled: true,
			InvocationID: inv, OS: "linux", Arch: "amd64", Now: func() time.Time { return f.now },
			SelfTest: func(ctx context.Context, bin string, _, _ int) error { return RunSelfTest(ctx, bin, "", 5*time.Second) },
		})
	}

	a := apply("i1")
	m, restarts := f.stagedManager("0.9.0", a)
	if m.Startup() || m.AgentInfo().UpdateMode != ModeStaged {
		t.Fatal("startup")
	}
	m.Handle(context.Background(), f.release(ActionUpgrade, "0.9.1", `[ "$1" = "-self-test" ] || exit 1`, nil))
	st := m.State()
	if restarts.Load() != 1 || st.Status != StateRestarting || st.Staged != "0.9.1" || st.Candidate != "0.9.1" {
		t.Fatalf("staging: %+v", st)
	}
	if f.current() != "0.9.0" {
		t.Fatal("the agent switched current itself")
	}
	for _, file := range []string{ArchiveFile, ManifestFile, SignatureFile} {
		if _, err := os.Stat(filepath.Join(f.stateDir, UpdatesDir, "0.9.1", file)); err != nil {
			t.Errorf("staged %s: %v", file, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.stateDir, UpdatesDir, "0.9.1", "extract")); err == nil {
		t.Error("self-test extraction left in the staging directory")
	}

	f.now = f.now.Add(5 * time.Second)
	a = apply("i2")
	if a.Result != ApplySwitched || f.current() != "0.9.1" {
		t.Fatalf("apply %+v", a)
	}
	m2, _ := f.stagedManager("0.9.1", a)
	if m2.Startup() {
		t.Fatal("exit")
	}
	if st := m2.State(); st.Status != StateConfirming || st.Staged != "" || st.Attempts != 1 {
		t.Fatalf("candidate start: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(f.stateDir, UpdatesDir)); err == nil {
		t.Error("staging directory not cleaned")
	}
	f.now = f.now.Add(time.Second)
	m2.Confirm()
	if st := m2.State(); st.Status != StateSucceeded || st.ConfirmedVersion != "0.9.1" {
		t.Fatalf("confirm %+v", st)
	}
	installVersion(t, f.root, "0.7.0", "exit 0", nil)
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(f.root, "versions", "0.7.0")); err != nil {
		t.Error("the agent pruned root-owned versions")
	}

	// Next start: apply settles the candidate and prunes.
	f.now = f.now.Add(time.Minute)
	if a := apply("i3"); a.Candidate != nil {
		t.Fatalf("not settled: %+v", a)
	}
	if _, err := os.Stat(filepath.Join(f.root, "versions", "0.7.0")); err == nil {
		t.Error("not pruned by apply")
	}
}

func TestStagedStartupResults(t *testing.T) {
	stage := func(f *fixture) {
		SaveState(filepath.Join(f.stateDir, StateFile), State{
			Previous: "0.9.0", Candidate: "0.9.1", Staged: "0.9.1", StagedAt: f.now, Status: StateRestarting,
			FromVersion: "0.9.0", ToVersion: "0.9.1", Action: ActionUpgrade,
		})
		os.MkdirAll(filepath.Join(f.stateDir, UpdatesDir, "0.9.1"), 0o750)
	}
	cases := []struct {
		name    string
		running string
		apply   *ApplyStatus
		status  string
		errText string
	}{
		{name: "rejected", running: "0.9.0", apply: &ApplyStatus{Result: ApplyRejected, ToVersion: "0.9.1", Error: "manifest signature invalid"},
			status: StateFailed, errText: "signature"},
		{name: "not processed", running: "0.9.0", apply: &ApplyStatus{}, status: StateFailed, errText: "was not installed"},
		{name: "rolled back", running: "0.9.0", apply: &ApplyStatus{Result: ApplyRolledBack, FromVersion: "0.9.1", ToVersion: "0.9.0", Error: "did not confirm"},
			status: StateRolledBack, errText: "did not confirm"},
		{name: "rollback failed", running: "0.9.1", apply: &ApplyStatus{Result: ApplyRollbackFailed, FromVersion: "0.9.1", Error: "rollback impossible"},
			status: StateFailed, errText: "rollback impossible"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			stage(f)
			if c.name == "rolled back" || c.name == "rollback failed" {
				st, _ := LoadState(filepath.Join(f.stateDir, StateFile))
				st.Staged = ""
				SaveState(filepath.Join(f.stateDir, StateFile), st)
			}
			m, _ := f.stagedManager(c.running, c.apply)
			if m.Startup() {
				t.Fatal("exit")
			}
			st := m.State()
			if st.Status != c.status || !strings.Contains(st.Error, c.errText) || st.Staged != "" || st.Candidate != "" {
				t.Fatalf("state %+v", st)
			}
			if _, err := os.Stat(filepath.Join(f.stateDir, UpdatesDir)); err == nil {
				t.Error("staging directory kept")
			}
			if m.Report().State != c.status {
				t.Error("not reported")
			}
		})
	}
	t.Run("legacy unit with a staged update", func(t *testing.T) {
		f := newFixture(t)
		stage(f)
		m, _ := f.manager("0.9.0", true)
		m.Startup()
		if st := m.State(); st.Status != StateFailed || !strings.Contains(st.Error, "unit outdated") || st.Staged != "" {
			t.Fatalf("state %+v", st)
		}
	})
}

func TestStagedWatchdogRequestsRollback(t *testing.T) {
	f := newFixture(t)
	f.stageCandidate("0.9.1", "0.9.2")
	m := NewManager(Options{
		StateDir: f.stateDir, Version: "0.9.2", ConfirmWindow: 100 * time.Millisecond, Now: func() time.Time { return f.now },
		Install: Install{Method: MethodTarball, Capable: true, Mode: ModeStaged, InstallRoot: f.root, VersionDir: "0.9.2",
			Apply: &ApplyStatus{Candidate: &RootCandidate{Version: "0.9.2", Attempts: 2}}},
	})
	restarted := make(chan struct{})
	m.SetRestart(func() { close(restarted) })
	m.Startup()
	if m.State().Attempts != 2 {
		t.Errorf("attempts %d, want root's count", m.State().Attempts)
	}
	go m.Run(context.Background())
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not fire")
	}
	st, _ := LoadState(filepath.Join(f.stateDir, StateFile))
	if st.RollbackRequest != "0.9.2" || !strings.Contains(st.RollbackReason, "did not confirm") || f.current() != "0.9.2" {
		t.Fatalf("state %+v current %s", st, f.current())
	}
}

func TestDetectModes(t *testing.T) {
	root := t.TempDir()
	installVersion(t, root, "0.9.0", "exit 0", nil)
	SwitchCurrent(root, "0.9.0")
	detect := func(inv string, privileged bool, env map[string]string) Install {
		sys := t.TempDir()
		os.WriteFile(filepath.Join(sys, ".dockerenv"), nil, 0o644)
		return Detect(Env{
			Root: sys, Getenv: func(k string) string { return env[k] }, Executable: filepath.Join(root, "current", BinaryName),
			InstallRoot: root, UpdatesEnabled: true, HaveTrustedKeys: true, InvocationID: inv, Privileged: privileged,
		})
	}
	noContainer := map[string]string{ContainerEnv: "0"}
	if in := detect("inv-1", false, noContainer); in.Mode != ModeLegacy || !in.Capable || in.Notice != NoticeUnitOutdated || in.Apply != nil {
		t.Errorf("no apply status: %+v", in)
	}
	saveStatus(filepath.Join(root, ApplyStatusFile), ApplyStatus{InvocationID: "inv-1"})
	saveStatus(filepath.Join(root, ReconcileStatusFile), ReconcileStatus{Version: "0.9.0", Docker: DockerMember})
	if in := detect("inv-1", false, noContainer); in.Mode != ModeStaged || !in.Capable || in.Notice != "" || in.Apply == nil || in.Reconcile == nil {
		t.Errorf("apply ran for this start: %+v", in)
	}
	if in := detect("inv-2", false, noContainer); in.Mode != ModeLegacy || in.Notice == "" {
		t.Errorf("apply ran for another start: %+v", in)
	}
	if in := detect("", false, noContainer); in.Mode != ModeLegacy {
		t.Errorf("outside systemd: %+v", in)
	}
	if in := detect("", true, nil); in.Method != MethodTarball || in.VersionDir != "0.9.0" {
		t.Errorf("privileged detection treats a container with the layout as a container: %+v", in)
	}
	if in := detect("", true, map[string]string{ContainerEnv: "1"}); in.Method != MethodContainer {
		t.Errorf("explicit container: %+v", in)
	}
}
