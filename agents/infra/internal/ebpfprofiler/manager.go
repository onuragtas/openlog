package ebpfprofiler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
	lib "github.com/onuragtas/openlog/libs/release"
)

// Options configures a Manager.
type Options struct {
	// Config is ebpf_profiler of config.yaml.
	Config config.EBPFProfilerConfig
	// StateDir is state_dir; StatusDir the infra agent's install root (StatusFile).
	StateDir, StatusDir string
	// AgentVersion is the running infra agent's version (ebpf_profiler.version: agent).
	AgentVersion string
	OS           string
	Trusted      []ed25519.PublicKey
	// Capable is false when this start cannot run the privileged step, or the platform has no profiler at all
	// (it is built for Linux only). Reason explains why not.
	Capable bool
	Reason  string
	// OwnManifest returns the signed manifest and signature kept next to the running infra agent binary. Unlike
	// the Java agent's jar, the profiler's tarballs are listed in it, so a host can install without the fleet.
	OwnManifest func() (manifest, signature []byte, err error)
	HTTPClient  *http.Client
	// DownloadHeader returns the request header of a download (license key for the backend mirror).
	DownloadHeader func(url string) http.Header
	// CanRestart reports whether the agent may exit for the privileged step now (no self-update in progress).
	CanRestart func() bool
	// Restart stops the agent so that the service manager starts it again (and runs "-apply").
	Restart func()
	// UnitActive reports whether the profiler service is running (default: systemctl is-active).
	UnitActive func(ctx context.Context) bool

	Now          func() time.Time
	Log          *slog.Logger
	RefreshEvery time.Duration // [1m]
	Tick         time.Duration // [30s]
}

// Manager decides what the host should run and drives installations. Its methods are safe for concurrent use.
type Manager struct {
	o   Options
	log *slog.Logger
	dir string

	mu      sync.Mutex
	st      State
	remote  *Remote
	status  *Status
	current string
	managed string
	active  bool
	seenAt  time.Time
	fp      uint64

	wake chan struct{}
	kick chan struct{}
}

// NewManager loads the persisted state and remote settings.
func NewManager(o Options) *Manager {
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: update.DownloadTimeout}
	}
	if o.UnitActive == nil {
		o.UnitActive = unitActive
	}
	if o.RefreshEvery <= 0 {
		o.RefreshEvery = time.Minute
	}
	if o.Tick <= 0 {
		o.Tick = 30 * time.Second
	}
	m := &Manager{o: o, log: o.Log.With("component", "ebpf_profiler"), dir: filepath.Join(o.StateDir, StateSubdir),
		managed: ManagedNone, wake: make(chan struct{}, 1), kick: make(chan struct{}, 1)}
	if b, err := os.ReadFile(filepath.Join(m.dir, StateFile)); err == nil {
		if err := json.Unmarshal(b, &m.st); err != nil {
			m.log.Warn("profiler state unreadable; starting from idle", "error", err)
			m.st = State{}
		}
	}
	if b, err := os.ReadFile(filepath.Join(m.dir, RemoteFile)); err == nil {
		var r Remote
		if json.Unmarshal(b, &r) == nil {
			m.remote = &r
		}
	}
	m.loadInstall(context.Background())
	return m
}

// unitActive asks systemd; any error means "not running" (also on hosts without systemctl).
func unitActive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", UnitName).Run() == nil
}

// Kick is signalled when the report changed and should be synced soon.
func (m *Manager) Kick() <-chan struct{} { return m.kick }

// loadInstall re-reads the install state (status file, current, who owns the root, whether it runs).
func (m *Manager) loadInstall(ctx context.Context) {
	root := m.o.Config.InstallRoot
	st, _ := LoadStatus(m.o.StatusDir)
	cur := InstalledVersion(root)
	// Without a Sys this only sees whether the marker is there, not whether it is root-owned; the privileged
	// step checks that again before it changes anything.
	managed := ManagedBy(nil, root)
	active := false
	if cur != "" && m.o.OS == "linux" {
		active = m.o.UnitActive(ctx)
	}
	m.mu.Lock()
	m.status, m.current, m.managed, m.active = st, cur, managed, active
	m.mu.Unlock()
}

// Startup takes over the result of this start's privileged step. Call it before Run.
func (m *Manager) Startup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.st.Phase != StateRestarting {
		return
	}
	defer m.cleanup()
	st, err := LoadStatus(m.o.StatusDir)
	if err != nil || st.RequestID != m.st.RequestID {
		msg := "the privileged pre-start step did not handle the request (unit without ExecStartPre=+… -apply?)"
		if !m.o.Capable && m.o.Reason != "" {
			msg += ": " + m.o.Reason
		}
		m.setLocked(StateFailed, msg)
		return
	}
	m.finishLocked(st)
}

func (m *Manager) cleanup() {
	os.Remove(filepath.Join(m.dir, RequestFile))
	os.RemoveAll(filepath.Join(m.dir, StagedDir))
}

// finishLocked maps the privileged step's result to the state.
func (m *Manager) finishLocked(st *Status) {
	m.status = st
	switch st.Result {
	case ResultApplied:
		m.st.HealthAt = st.At.Add(m.o.Config.HealthCheckAfter.D())
		m.setLocked(StateConfirming, "")
		m.log.Info("profiler installed; verification pending", "version", st.Version, "health_at", m.st.HealthAt)
	case ResultRolledBack:
		msg := st.Error
		if m.st.Error != "" {
			msg = m.st.Error // the verification's reason
		}
		m.setLocked(StateRolledBack, msg)
		m.log.Error("profiler rolled back", "version", m.st.Version, "now", st.Version, "error", msg)
	case ResultUninstalled:
		m.setLocked(StateUninstalled, "")
		m.log.Info("profiler removed")
	default:
		m.setLocked(StateFailed, st.Error)
		m.log.Error("profiler request failed", "action", m.st.Action, "version", m.st.Version, "error", st.Error)
	}
}

// SetRemote applies the ebpf_profiler section of a sync response.
func (m *Manager) SetRemote(raw json.RawMessage) {
	var r Remote
	if err := json.Unmarshal(raw, &r); err != nil {
		m.log.Warn("ebpf_profiler section of the sync response ignored", "error", err)
		return
	}
	switch r.Mode {
	case config.EBPFProfilerModeOff, config.EBPFProfilerModeManual, config.EBPFProfilerModeAuto:
	default:
		m.log.Warn("ebpf_profiler section of the sync response ignored", "error", fmt.Sprintf("invalid mode %q", r.Mode))
		return
	}
	if r.TargetVersion != "" && !validVersion(r.TargetVersion) {
		m.log.Warn("ebpf_profiler section of the sync response ignored", "error", fmt.Sprintf("invalid target_version %q", r.TargetVersion))
		return
	}
	m.mu.Lock()
	changed := m.remote == nil || *m.remote != r
	m.remote = &r
	m.mu.Unlock()
	if !changed {
		return
	}
	if b, err := json.Marshal(r); err == nil {
		if err := os.MkdirAll(m.dir, 0o750); err == nil {
			if err := update.WriteFileAtomic(filepath.Join(m.dir, RemoteFile), b, 0o640); err != nil {
				m.log.Warn("profiler fleet settings not persisted", "error", err)
			}
		}
	}
	if m.o.Config.RemoteConfig {
		m.log.Info("profiler fleet settings received", "mode", r.Mode, "target", r.TargetVersion, "reason", r.Reason)
	}
	m.invalidate()
}

type effective struct {
	mode, target, source string
	remote               *Remote
}

func (m *Manager) effectiveLocked() effective {
	c := m.o.Config
	if m.remote != nil && c.RemoteConfig {
		return effective{mode: m.remote.Mode, target: m.remote.TargetVersion, source: SourceRemote, remote: m.remote}
	}
	target := c.Version
	if target == config.EBPFProfilerVersionAgent {
		target = m.o.AgentVersion
	}
	return effective{mode: c.Mode, target: target, source: SourceLocal}
}

// Report is the ebpf_profiler section of the next sync request.
func (m *Manager) Report() Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.effectiveLocked()
	r := Report{Mode: e.mode, Source: e.source, Capable: m.o.Capable, Managed: m.managed, CurrentVersion: m.current,
		TargetVersion: e.target, Active: m.active}
	if !m.o.Capable {
		r.Reason = m.o.Reason
	}
	if m.current != "" {
		r.Unit = UnitName
	}
	r.Status, r.Detail = m.statusLocked()
	if m.st.Phase != "" {
		r.Update = &UpdateReport{Operation: m.st.Operation, Version: m.st.Version, State: m.st.Phase, Error: m.st.Error}
		if !m.st.ChangedAt.IsZero() {
			r.Update.ChangedAt = m.st.ChangedAt.UTC().Format(time.RFC3339)
		}
	}
	return r
}

func (m *Manager) statusLocked() (string, string) {
	switch {
	case m.managed == ManagedPackage || m.managed == ManagedManual:
		return StatusUnmanaged, fmt.Sprintf("%s was not installed by the infra agent (install.sh installs the package by default) "+
			"and is left alone; the package manager keeps it updated", m.o.Config.InstallRoot)
	case m.st.Phase == StateFailed:
		return StatusError, m.st.Error
	case m.st.Phase == StateDownloading || m.st.Phase == StateRestarting:
		return StatusStaged, ""
	case m.current == "":
		return StatusNotFound, ""
	case !m.active && m.o.OS == "linux":
		return StatusInactive, fmt.Sprintf("%s is installed but %s is not running", m.current, UnitName)
	}
	return StatusInstalled, ""
}

func (m *Manager) invalidate() {
	m.mu.Lock()
	m.seenAt = time.Time{}
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run keeps the install state current and performs installations until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(m.o.Tick)
	defer t.Stop()
	for {
		m.Evaluate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.wake:
		}
	}
}

// refresh re-reads the install state every RefreshEvery (or after invalidate).
func (m *Manager) refresh(ctx context.Context) {
	m.mu.Lock()
	due := m.seenAt.IsZero() || m.o.Now().Sub(m.seenAt) >= m.o.RefreshEvery
	m.mu.Unlock()
	if !due {
		return
	}
	m.loadInstall(ctx)
	m.mu.Lock()
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%t\x00%s", m.current, m.managed, m.active, m.st.Phase)
	fp := h.Sum64()
	changed := fp != m.fp
	m.seenAt, m.fp = m.o.Now(), fp
	m.mu.Unlock()
	if changed {
		m.signal()
	}
}

// Evaluate re-reads the state when due and takes the next step (verification, install, upgrade, removal).
func (m *Manager) Evaluate(ctx context.Context) {
	m.refresh(ctx)
	m.mu.Lock()
	e := m.effectiveLocked()
	st, cur, managed := m.st, m.current, m.managed
	m.mu.Unlock()

	switch st.Phase {
	case StateDownloading, StateRestarting:
		return
	case StateConfirming:
		if !m.o.Now().Before(st.HealthAt) {
			m.verify(ctx)
		}
		return
	}
	if !m.o.Capable {
		return
	}
	// An installation this agent did not make belongs to the package manager or to a person.
	if managed == ManagedPackage || managed == ManagedManual {
		return
	}
	switch e.mode {
	case config.EBPFProfilerModeOff:
		if managed != ManagedFleet {
			return
		}
		retryLater := st.Action == ActionUninstall && st.Phase == StateFailed && st.RequestID != "" &&
			m.o.Now().Sub(st.ChangedAt) < FailedRetryDelay
		if !retryLater && m.mayRestart() {
			m.request(ctx, ActionUninstall, OpUninstall, cur)
		}
	case config.EBPFProfilerModeAuto:
		if e.target == "" || e.target == cur {
			return
		}
		if !m.releaseAvailable(e) || m.blocked(st, e.target) || !m.mayRestart() {
			return
		}
		m.install(ctx, e, cur)
	}
}

func (m *Manager) mayRestart() bool { return m.o.CanRestart == nil || m.o.CanRestart() }

// blocked: a rolled back version is never retried automatically, a failed one not within FailedRetryDelay.
func (m *Manager) blocked(st State, target string) bool {
	if st.Version != target || (st.Action != ActionInstall && st.Action != ActionRollback) {
		return false
	}
	switch st.Phase {
	case StateRolledBack:
		return true
	case StateFailed:
		return m.o.Now().Sub(st.ChangedAt) < FailedRetryDelay
	}
	return false
}

func (m *Manager) signal() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// install downloads and verifies the target release into state_dir and hands the request to the privileged step.
func (m *Manager) install(ctx context.Context, e effective, installed string) {
	op := OpInstall
	if installed != "" {
		op = OpUpgrade
	}
	id := newID()
	m.mu.Lock()
	m.st = State{RequestID: id, Action: ActionInstall, Operation: op, Version: e.target}
	m.setLocked(StateDownloading, "")
	m.mu.Unlock()
	m.log.Info("profiler installation started", "operation", op, "version", e.target, "installed", installed)
	if err := m.stage(ctx, e, installed); err != nil {
		m.mu.Lock()
		m.setLocked(StateFailed, err.Error())
		m.mu.Unlock()
		os.RemoveAll(filepath.Join(m.dir, StagedDir))
		m.log.Error("profiler installation failed before the privileged step", "version", e.target, "error", err)
		return
	}
	m.handOver(ctx, id, ActionInstall, e.target)
}

func (m *Manager) stage(ctx context.Context, e effective, installed string) error {
	manifest, sig, dl, err := m.release(e)
	if err != nil {
		return err
	}
	root := m.o.Config.InstallRoot
	v, err := Verify(manifest, sig, m.o.Trusted, e.target, m.o.OS, runtime.GOARCH, func() (*lib.Manifest, error) {
		if installed == "" {
			return nil, nil
		}
		b, err := os.ReadFile(filepath.Join(root, "versions", installed, ManifestFile))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return lib.ParseManifest(b)
	})
	if err != nil {
		return err
	}
	if dl == "" {
		dl = v.Artifact.URL
	}
	if u, err := url.Parse(dl); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("invalid download URL %q", dl)
	}
	ver := v.Version.String()
	staged := filepath.Join(m.dir, StagedDir)
	if err := os.RemoveAll(staged); err != nil {
		return err
	}
	partial := filepath.Join(staged, "."+ver+".partial")
	if err := os.MkdirAll(partial, 0o750); err != nil {
		return err
	}
	header := http.Header{}
	if m.o.DownloadHeader != nil {
		header = m.o.DownloadHeader(dl)
	}
	archive := filepath.Join(partial, ArchiveFile)
	if err := update.Download(ctx, m.o.HTTPClient, dl, header, archive, v.Artifact.Size, v.Artifact.SHA256); err != nil {
		return err
	}
	if err := update.WriteFileAtomic(filepath.Join(partial, ManifestFile), manifest, 0o640); err != nil {
		return err
	}
	if err := update.WriteFileAtomic(filepath.Join(partial, SignatureFile), sig, 0o640); err != nil {
		return err
	}
	return os.Rename(partial, filepath.Join(staged, ver))
}

// release returns the signed manifest of the target: from the fleet, or the infra agent's own for its version.
func (m *Manager) release(e effective) (manifest, sig []byte, downloadURL string, err error) {
	if r := e.remote; r != nil && r.TargetVersion == e.target && r.Manifest != "" {
		manifest, err := base64.StdEncoding.DecodeString(r.Manifest)
		if err != nil {
			return nil, nil, "", errors.New("manifest from the backend is not valid base64")
		}
		return manifest, []byte(r.Signature), r.DownloadURL, nil
	}
	if e.target == m.o.AgentVersion && m.o.OwnManifest != nil {
		manifest, sig, err := m.o.OwnManifest()
		if err != nil {
			return nil, nil, "", fmt.Errorf("manifest of infra agent %s: %w", e.target, err)
		}
		return manifest, sig, "", nil
	}
	return nil, nil, "", fmt.Errorf("no signed manifest for profiler %s (the fleet sends it; locally only the infra agent's own version is available)", e.target)
}

// releaseAvailable reports whether release can supply a manifest listing the profiler for this platform.
func (m *Manager) releaseAvailable(e effective) bool {
	if r := e.remote; r != nil && r.TargetVersion == e.target && r.Manifest != "" {
		return true
	}
	if e.target != m.o.AgentVersion || m.o.OwnManifest == nil {
		return false
	}
	b, _, err := m.o.OwnManifest()
	if err != nil {
		return false
	}
	mf, err := lib.ParseManifest(b)
	if err != nil {
		return false
	}
	_, ok := mf.Artifact(lib.ComponentEBPFProfiler, m.o.OS, runtime.GOARCH, lib.FormatTarGz)
	return ok
}

// request writes a rollback or uninstall request and hands it over.
func (m *Manager) request(ctx context.Context, action, op, version string) {
	id := newID()
	m.mu.Lock()
	keepErr := ""
	if action == ActionRollback {
		keepErr = m.st.Error
	}
	m.st = State{RequestID: id, Action: action, Operation: op, Version: version, Error: keepErr}
	m.mu.Unlock()
	m.handOver(ctx, id, action, version)
}

// handOver writes the request and exits so that "-apply" of the next start handles it.
func (m *Manager) handOver(_ context.Context, id, action, version string) {
	m.mu.Lock()
	req := Request{ID: id, Action: action, Version: version, At: m.o.Now().UTC()}
	err := os.MkdirAll(m.dir, 0o750)
	if err == nil {
		b, _ := json.Marshal(req)
		err = update.WriteFileAtomic(filepath.Join(m.dir, RequestFile), b, 0o640)
	}
	if err != nil {
		m.setLocked(StateFailed, "request not written: "+err.Error())
		m.mu.Unlock()
		return
	}
	m.setLocked(StateRestarting, m.st.Error)
	m.mu.Unlock()
	m.log.Info("exiting so that the privileged pre-start step handles the profiler request", "action", action, "version", version)
	if m.o.Restart != nil {
		m.o.Restart()
	}
}

// verify runs HealthCheckAfter after an installation: current points at the version, its binary is there and the
// service is running. The signed sha256 covers the tarball, not the extracted file, so it is not re-checked here;
// the privileged step verified it before extracting. On failure it requests a rollback to the previous version.
func (m *Manager) verify(ctx context.Context) {
	m.loadInstall(ctx)
	m.mu.Lock()
	st, c, cur, active := m.st, m.o.Config, m.current, m.active
	m.mu.Unlock()
	var problems []string
	v := st.Version
	dir := filepath.Join(c.InstallRoot, "versions", v)
	if cur != v {
		problems = append(problems, fmt.Sprintf("current points at %q, not %s", cur, v))
	}
	if fi, err := os.Lstat(filepath.Join(dir, BinaryRel)); err != nil || !fi.Mode().IsRegular() {
		problems = append(problems, BinaryRel+" is missing")
	}
	if b, err := os.ReadFile(filepath.Join(dir, ManifestFile)); err != nil {
		problems = append(problems, "manifest: "+err.Error())
	} else if mf, err := lib.ParseManifest(b); err != nil {
		problems = append(problems, "manifest: "+err.Error())
	} else if mf.Version != v {
		problems = append(problems, fmt.Sprintf("the installed manifest is for %s, not %s", mf.Version, v))
	}
	if !active && m.o.OS == "linux" {
		problems = append(problems, UnitName+" is not running")
	}
	if len(problems) == 0 {
		m.mu.Lock()
		m.setLocked(StateApplied, "")
		m.mu.Unlock()
		m.log.Info("profiler verified", "version", v)
		m.invalidate()
		return
	}
	reason := "verification failed: " + strings.Join(problems, "; ")
	m.log.Error("profiler verification failed; rolling back", "version", v, "reason", reason)
	m.mu.Lock()
	m.setLocked(StateFailed, update.Truncate(reason, 1024))
	prev := ""
	if m.status != nil {
		prev = m.status.Previous
	}
	m.mu.Unlock()
	if prev != "" && m.mayRestart() {
		m.request(ctx, ActionRollback, OpRollback, prev)
	}
}

// setLocked changes the phase and persists the state.
func (m *Manager) setLocked(phase, errMsg string) {
	m.st.Phase, m.st.Error, m.st.ChangedAt = phase, update.Truncate(errMsg, 1024), m.o.Now().UTC()
	if err := os.MkdirAll(m.dir, 0o750); err == nil {
		b, _ := json.MarshalIndent(m.st, "", "  ")
		if err := update.WriteFileAtomic(filepath.Join(m.dir, StateFile), b, 0o640); err != nil {
			m.log.Error("profiler state not saved", "error", err)
		}
	}
	m.signal()
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
