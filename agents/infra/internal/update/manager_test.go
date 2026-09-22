package update

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
)

// releaseServer serves archives by path and counts requests.
type releaseServer struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	hits  atomic.Int32
}

func newReleaseServer(t *testing.T) *releaseServer {
	rs := &releaseServer{files: map[string][]byte{}}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.hits.Add(1)
		rs.mu.Lock()
		b, ok := rs.files[r.URL.Path]
		rs.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *releaseServer) put(path string, b []byte) string {
	rs.mu.Lock()
	rs.files[path] = b
	rs.mu.Unlock()
	return rs.URL + path
}

type fixture struct {
	t        *testing.T
	key      testKey
	root     string
	stateDir string
	srv      *releaseServer
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, key: newKey(t), root: t.TempDir(), stateDir: t.TempDir(), srv: newReleaseServer(t), now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	return f
}

// release publishes version with a script binary and returns its instruction.
func (f *fixture) release(action, version, script string, compat map[string]string) *Instruction {
	archive := agentTarball(f.t, version, script)
	name := TopDir(version, "linux", "amd64") + ".tar.gz"
	url := f.srv.put("/"+name, archive)
	m := manifestFor(f.t, version, f.srv.URL, archive, compat, nil)
	return instruction(action, version, m, sign(m, f.key), url)
}

func (f *fixture) manager(version string, capable bool) (*Manager, *atomic.Int32) {
	restarts := &atomic.Int32{}
	m := NewManager(Options{
		StateDir: f.stateDir, Version: version,
		Install: Install{Method: MethodTarball, Capable: capable, InstallRoot: f.root, VersionDir: version},
		Trusted: []ed25519.PublicKey{f.key.pub}, OS: "linux", Arch: "amd64",
		Now: func() time.Time { return f.now }, SelfTestTimeout: 10 * time.Second,
	})
	m.SetRestart(func() { restarts.Add(1) })
	return m, restarts
}

func (f *fixture) current() string {
	d, err := CurrentDir(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *fixture) versions() []string {
	entries, _ := os.ReadDir(filepath.Join(f.root, "versions"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestUpgradeConfirmAndPrune(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	f := newFixture(t)
	installVersion(t, f.root, "0.8.0", "exit 0", nil)
	installVersion(t, f.root, "0.9.0", "exit 0", manifestFor(t, "0.9.0", "https://x", []byte("a"), nil, nil))
	if err := SwitchCurrent(f.root, "0.9.0"); err != nil {
		t.Fatal(err)
	}

	m, restarts := f.manager("0.9.0", true)
	stats := selfmon.New(f.now)
	m.SetStats(stats)
	if m.Startup() {
		t.Fatal("startup exit on idle state")
	}
	ins := f.release(ActionUpgrade, "0.9.1", `[ "$1" = "-self-test" ] || exit 1`, map[string]string{"rollback_floor": "0.9.0"})
	m.Handle(context.Background(), ins)

	st := m.State()
	if restarts.Load() != 1 || !m.Restarting() {
		t.Fatalf("restart not requested: %+v", st)
	}
	if st.Status != StateRestarting || st.Candidate != "0.9.1" || st.Previous != "0.9.0" || st.Attempts != 0 || st.Confirmed || !st.StagedAt.Equal(f.now) {
		t.Fatalf("state %+v", st)
	}
	if f.current() != "0.9.1" {
		t.Fatalf("current %s", f.current())
	}
	for _, file := range []string{BinaryName, ManifestFile, SignatureFile, "LICENSE", "packaging/config.example.yaml"} {
		if _, err := os.Stat(filepath.Join(f.root, "versions", "0.9.1", file)); err != nil {
			t.Errorf("staged %s: %v", file, err)
		}
	}
	if slices.ContainsFunc(f.versions(), func(s string) bool { return strings.HasPrefix(s, ".") }) {
		t.Errorf("temporary files left: %v", f.versions())
	}
	if onDisk, _ := LoadState(filepath.Join(f.stateDir, StateFile)); onDisk.Candidate != "0.9.1" {
		t.Fatalf("state not persisted: %+v", onDisk)
	}

	// The service manager starts the candidate.
	f.now = f.now.Add(10 * time.Second)
	m2, _ := f.manager("0.9.1", true)
	if m2.Startup() {
		t.Fatal("candidate exited on first start")
	}
	if st := m2.State(); st.Attempts != 1 || st.Status != StateConfirming {
		t.Fatalf("after start: %+v", st)
	}
	stats2 := selfmon.New(f.now)
	m2.SetStats(stats2)
	m2.Confirm()
	m2.Confirm() // idempotent
	st = m2.State()
	if !st.Confirmed || st.Status != StateSucceeded || st.FromVersion != "0.9.0" || st.ToVersion != "0.9.1" {
		t.Fatalf("confirmed: %+v", st)
	}
	if r := m2.Report(); r.State != StateSucceeded || r.ChangedAt == "" {
		t.Errorf("report %+v", r)
	}
	if got := stats2.Snapshot(); got.UpdateAttempts["succeeded"] != 1 || got.UpdateState != StateSucceeded {
		t.Errorf("metrics %+v", got)
	}
	select {
	case <-m2.Kick():
	default:
		t.Error("confirmation did not kick a sync")
	}
	waitFor(t, func() bool { return slices.Equal(f.versions(), []string{"0.9.0", "0.9.1"}) }, "prune to current+previous, got %v", f.versions)

	// A restart after confirmation does not count attempts and does not count the result twice.
	m3, _ := f.manager("0.9.1", true)
	stats3 := selfmon.New(f.now)
	m3.SetStats(stats3)
	if m3.Startup() || m3.State().Attempts != 1 || stats3.Snapshot().UpdateAttempts["succeeded"] != 0 {
		t.Errorf("restart after confirm: %+v", m3.State())
	}
}

func waitFor(t *testing.T, cond func() bool, format string, arg func() []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf(format, arg())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stageCandidate writes the state a switch into candidate leaves behind.
func (f *fixture) stageCandidate(previous, candidate string) {
	installVersion(f.t, f.root, previous, "exit 0", nil)
	installVersion(f.t, f.root, candidate, "exit 0", nil)
	if err := SwitchCurrent(f.root, candidate); err != nil {
		f.t.Fatal(err)
	}
	st := State{Previous: previous, Candidate: candidate, StagedAt: f.now, Status: StateRestarting,
		FromVersion: previous, ToVersion: candidate, Action: ActionUpgrade, RolloutID: "r1", ChangedAt: f.now}
	if err := SaveState(filepath.Join(f.stateDir, StateFile), st); err != nil {
		f.t.Fatal(err)
	}
}

func TestRollbackAfterThreeFailedStarts(t *testing.T) {
	f := newFixture(t)
	f.stageCandidate("0.9.1", "0.9.2")
	for attempt := 1; attempt <= MaxStartAttempts; attempt++ {
		f.now = f.now.Add(5 * time.Second)
		m, _ := f.manager("0.9.2", true)
		if m.Startup() {
			t.Fatalf("rolled back on attempt %d", attempt)
		}
		if got := m.State().Attempts; got != attempt {
			t.Fatalf("attempts %d, want %d", got, attempt)
		}
		// process crashes: no confirmation
	}
	f.now = f.now.Add(5 * time.Second)
	m, _ := f.manager("0.9.2", true)
	if !m.Startup() {
		t.Fatal("fourth start did not roll back")
	}
	if f.current() != "0.9.1" {
		t.Fatalf("current %s", f.current())
	}
	st := m.State()
	if st.Status != StateRolledBack || st.Candidate != "" || !strings.Contains(st.Error, "start attempt 4") {
		t.Fatalf("state %+v", st)
	}

	// Previous version starts, reports rolled_back and counts it once.
	prev, _ := f.manager("0.9.1", true)
	if prev.Startup() {
		t.Fatal("previous version exited")
	}
	stats := selfmon.New(f.now)
	prev.SetStats(stats)
	if r := prev.Report(); r.State != StateRolledBack || r.FromVersion != "0.9.1" || r.ToVersion != "0.9.2" {
		t.Errorf("report %+v", r)
	}
	if stats.Snapshot().UpdateAttempts["rolled_back"] != 1 {
		t.Error("rolled_back not counted")
	}
	prev.Confirm() // exports on the previous version do not change anything
	if prev.State().Status != StateRolledBack {
		t.Error("confirm changed a rolled back state")
	}

	// The same rollout is not retried.
	f.srv.put("/unused", nil)
	ins := f.release(ActionUpgrade, "0.9.2", "exit 0", nil)
	ins.RolloutID = "r1"
	hits := f.srv.hits.Load()
	prev.Handle(context.Background(), ins)
	if f.srv.hits.Load() != hits || prev.State().Status != StateRolledBack {
		t.Error("rolled back update was retried")
	}
}

func TestRollbackAfterConfirmWindow(t *testing.T) {
	f := newFixture(t)
	f.stageCandidate("0.9.1", "0.9.2")
	f.now = f.now.Add(ConfirmWindow + time.Second)
	m, _ := f.manager("0.9.2", true)
	if !m.Startup() {
		t.Fatal("no rollback after the confirm window")
	}
	if f.current() != "0.9.1" || m.State().Status != StateRolledBack {
		t.Fatalf("current %s state %+v", f.current(), m.State())
	}
}

func TestWatchdogRollsBackRunningCandidate(t *testing.T) {
	f := newFixture(t)
	f.stageCandidate("0.9.1", "0.9.2")
	m := NewManager(Options{
		StateDir: f.stateDir, Version: "0.9.2", ConfirmWindow: 100 * time.Millisecond, Now: func() time.Time { return f.now },
		Install: Install{Method: MethodTarball, Capable: true, InstallRoot: f.root, VersionDir: "0.9.2"},
	})
	restarted := make(chan struct{})
	m.SetRestart(func() { close(restarted) })
	reported := atomic.Bool{}
	m.SetReporter(func(context.Context) { reported.Store(true) })
	if m.Startup() {
		t.Fatal("exit on first start")
	}
	go m.Run(context.Background())
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not fire")
	}
	if f.current() != "0.9.1" || m.State().Status != StateRolledBack || !reported.Load() {
		t.Fatalf("current %s state %+v reported %v", f.current(), m.State(), reported.Load())
	}
}

func TestWatchdogStopsAfterConfirm(t *testing.T) {
	f := newFixture(t)
	f.stageCandidate("0.9.1", "0.9.2")
	m := NewManager(Options{
		StateDir: f.stateDir, Version: "0.9.2", ConfirmWindow: 200 * time.Millisecond, Now: func() time.Time { return f.now },
		Install: Install{Method: MethodTarball, Capable: true, InstallRoot: f.root, VersionDir: "0.9.2"},
	})
	m.SetRestart(func() { t.Error("restart after confirmation") })
	m.Startup()
	m.Confirm()
	done := make(chan struct{})
	go func() { m.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchdog still running")
	}
	if f.current() != "0.9.2" {
		t.Error("confirmed candidate rolled back")
	}
}

func TestRollbackImpossibleWithoutPrevious(t *testing.T) {
	f := newFixture(t)
	f.stageCandidate("0.9.1", "0.9.2")
	os.RemoveAll(filepath.Join(f.root, "versions", "0.9.1"))
	f.now = f.now.Add(ConfirmWindow + time.Second)
	m, _ := f.manager("0.9.2", true)
	if m.Startup() {
		t.Fatal("exited although the previous version is gone")
	}
	if st := m.State(); st.Status != StateFailed || !strings.Contains(st.Error, "rollback impossible") || f.current() != "0.9.2" {
		t.Fatalf("state %+v current %s", st, f.current())
	}
}

func TestHandleRejections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	f := newFixture(t)
	installVersion(t, f.root, "0.9.1", "exit 0", manifestFor(t, "0.9.1", "https://x", []byte("a"), map[string]string{"rollback_floor": "0.9.0"}, nil))
	if err := SwitchCurrent(f.root, "0.9.1"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	check := func(t *testing.T, m *Manager, restarts *atomic.Int32, want string) {
		t.Helper()
		st := m.State()
		if st.Status != StateFailed || !strings.Contains(st.Error, want) {
			t.Fatalf("state %+v, want failed with %q", st, want)
		}
		if restarts.Load() != 0 || f.current() != "0.9.1" {
			t.Fatalf("switched: restarts %d current %s", restarts.Load(), f.current())
		}
		if slices.ContainsFunc(f.versions(), func(s string) bool { return s != "0.9.1" }) {
			t.Fatalf("leftovers: %v", f.versions())
		}
		select {
		case <-m.Kick():
		default:
			t.Error("failure did not kick a sync")
		}
	}

	t.Run("not update capable", func(t *testing.T) {
		m, r := f.manager("0.9.1", false)
		hits := f.srv.hits.Load()
		m.Handle(ctx, f.release(ActionUpgrade, "1.0.0", "exit 0", nil))
		check(t, m, r, "not update capable")
		if f.srv.hits.Load() != hits {
			t.Error("downloaded although not update capable")
		}
	})
	t.Run("tampered manifest", func(t *testing.T) {
		m, r := f.manager("0.9.1", true)
		ins := f.release(ActionUpgrade, "1.0.1", "exit 0", nil)
		ins.Manifest = instruction("", "", []byte(strings.Replace(string(mustB64(t, ins.Manifest)), `"stable"`, `"beta"`, 1)), "", "").Manifest
		m.Handle(ctx, ins)
		check(t, m, r, "signature")
	})
	t.Run("sha mismatch", func(t *testing.T) {
		m, r := f.manager("0.9.1", true)
		ins := f.release(ActionUpgrade, "1.0.2", "exit 0", nil)
		orig := f.srv.files["/"+TopDir("1.0.2", "linux", "amd64")+".tar.gz"]
		corrupt := append([]byte{}, orig...)
		corrupt[len(corrupt)/2] ^= 0xff
		f.srv.put("/"+TopDir("1.0.2", "linux", "amd64")+".tar.gz", corrupt)
		m.Handle(ctx, ins)
		check(t, m, r, "sha256 mismatch")
	})
	t.Run("downgrade without rollback action", func(t *testing.T) {
		m, r := f.manager("0.9.1", true)
		m.Handle(ctx, f.release(ActionUpgrade, "0.9.0", "exit 0", nil))
		check(t, m, r, "not newer")
	})
	t.Run("rollback below floor", func(t *testing.T) {
		m, r := f.manager("0.9.1", true)
		m.Handle(ctx, f.release(ActionRollback, "0.8.0", "exit 0", nil))
		check(t, m, r, "rollback_floor")
	})
	t.Run("self-test fails", func(t *testing.T) {
		m, r := f.manager("0.9.1", true)
		m.Handle(ctx, f.release(ActionUpgrade, "1.0.3", "echo cannot parse config >&2; exit 2", nil))
		check(t, m, r, "cannot parse config")
	})
	t.Run("cancelled attempt is not a failure", func(t *testing.T) {
		// A cancelled context says the agent is stopping, not that the candidate is broken. Reporting it as
		// failed made one host halt a whole rollout: the fleet counts failures against halt_failure_rate,
		// and "self-test of …/extract/openlog-infra-agent failed: context canceled" was enough at 1 of 3.
		m, r := f.manager("0.9.1", true)
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		m.Handle(cctx, f.release(ActionUpgrade, "1.0.9", "exit 0", nil))
		if st := m.State(); st.Status == StateFailed {
			t.Fatalf("a cancelled attempt was reported as failed: %+v", st)
		}
		if r.Load() != 0 || f.current() != "0.9.1" {
			t.Fatalf("switched on a cancelled attempt: restarts %d current %s", r.Load(), f.current())
		}
	})

	t.Run("failed instruction not retried within delay", func(t *testing.T) {
		m, r := f.manager("0.9.1", true)
		ins := f.release(ActionUpgrade, "1.0.4", "exit 5", nil)
		m.Handle(ctx, ins)
		check(t, m, r, "self-test")
		hits := f.srv.hits.Load()
		changed := m.State().ChangedAt
		m.Handle(ctx, ins)
		if f.srv.hits.Load() != hits || !m.State().ChangedAt.Equal(changed) {
			t.Error("retried immediately")
		}
		f.now = f.now.Add(FailedRetryDelay + time.Minute)
		m.Handle(ctx, ins)
		if f.srv.hits.Load() == hits {
			t.Error("not retried after the delay")
		}
	})
	t.Run("not_before and deadline", func(t *testing.T) {
		m, r := f.manager("0.9.1", true)
		before := m.State()
		ins := f.release(ActionUpgrade, "1.0.5", "exit 0", nil)
		ins.NotBefore = f.now.Add(time.Hour).Format(time.RFC3339)
		m.Handle(ctx, ins)
		ins.NotBefore = ""
		ins.Deadline = f.now.Add(-time.Minute).Format(time.RFC3339)
		m.Handle(ctx, ins)
		if r.Load() != 0 || m.State().ChangedAt != before.ChangedAt {
			t.Errorf("acted on an instruction outside its window: %+v", m.State())
		}
	})
}

func TestRollbackAction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	f := newFixture(t)
	installVersion(t, f.root, "0.9.1", "exit 0", manifestFor(t, "0.9.1", "https://x", []byte("a"), map[string]string{"rollback_floor": "0.9.0"}, nil))
	if err := SwitchCurrent(f.root, "0.9.1"); err != nil {
		t.Fatal(err)
	}
	m, restarts := f.manager("0.9.1", true)
	m.Handle(context.Background(), f.release(ActionRollback, "0.9.0", "exit 0", nil))
	if restarts.Load() != 1 || f.current() != "0.9.0" {
		t.Fatalf("rollback not applied: %+v", m.State())
	}
	if st := m.State(); st.Candidate != "0.9.0" || st.Previous != "0.9.1" || st.Action != ActionRollback {
		t.Fatalf("state %+v", st)
	}
	m2, _ := f.manager("0.9.0", true)
	m2.Startup()
	m2.Confirm()
	if st := m2.State(); st.Status != StateSucceeded {
		t.Fatalf("state %+v", st)
	}
}

func TestStartedOtherVersionThanCandidate(t *testing.T) {
	f := newFixture(t)
	f.stageCandidate("0.9.1", "0.9.2")
	m, _ := f.manager("0.9.3", true)
	if m.Startup() {
		t.Fatal("exit")
	}
	if st := m.State(); st.Status != StateFailed || st.Candidate != "" {
		t.Fatalf("state %+v", st)
	}
}

func TestCorruptStateFile(t *testing.T) {
	f := newFixture(t)
	os.WriteFile(filepath.Join(f.stateDir, StateFile), []byte("{"), 0o600)
	m, _ := f.manager("0.9.1", true)
	if m.Startup() || m.State().Status != StateIdle {
		t.Fatalf("state %+v", m.State())
	}
}

func TestDownloadHeaderOnlyForEndpointHost(t *testing.T) {
	m := NewManager(Options{StateDir: t.TempDir(), Version: "1.0.0", Endpoint: "https://ingest.example:4318", LicenseKey: "secret"})
	if m.downloadHeader("https://ingest.example:4318/v1/openlog/releases/1.0.0/a.tar.gz").Get("openlog-license-key") != "secret" {
		t.Error("mirror download not authenticated")
	}
	if m.downloadHeader("https://github.com/onuragtas/openlog/releases/download/v1.0.0/a.tar.gz").Get("openlog-license-key") != "" {
		t.Error("license key sent to a third party")
	}
}
