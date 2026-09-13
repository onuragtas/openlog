package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
	lib "github.com/onuragtas/openlog/libs/release"
)

// Options configures a Manager.
type Options struct {
	StateDir string
	// ConfigPath is passed to "-self-test -config"; empty when the agent runs without a file.
	ConfigPath string
	// Version and Commit identify the running binary.
	Version, Commit string
	Install         Install
	Trusted         []ed25519.PublicKey
	// Endpoint and LicenseKey are used to authenticate downloads from the backend mirror.
	Endpoint, LicenseKey string
	Log                  *slog.Logger

	// Overridable for tests.
	OS, Arch        string
	Now             func() time.Time
	HTTPClient      *http.Client
	SelfTestTimeout time.Duration
	ConfirmWindow   time.Duration
}

// Manager owns the update state machine. Its methods are safe for concurrent use.
type Manager struct {
	o         Options
	statePath string
	log       *slog.Logger

	mu    sync.Mutex
	st    State
	stats *selfmon.Stats

	kick       chan struct{}
	settled    atomic.Bool // no unconfirmed candidate: Confirm is a no-op
	restarting atomic.Bool
	restart    func()
	report     func(context.Context)
}

// NewManager loads the state file. A corrupt state file is logged and replaced by an idle state.
func NewManager(o Options) *Manager {
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: DownloadTimeout}
	}
	if o.SelfTestTimeout == 0 {
		o.SelfTestTimeout = SelfTestTimeout
	}
	if o.ConfirmWindow == 0 {
		o.ConfirmWindow = ConfirmWindow
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	m := &Manager{
		o: o, statePath: filepath.Join(o.StateDir, StateFile),
		log:  o.Log.With("component", "update"),
		kick: make(chan struct{}, 1),
	}
	st, err := LoadState(m.statePath)
	if err != nil {
		m.log.Error("update state unreadable; starting from idle", "error", err)
	}
	m.st = st
	m.settled.Store(!m.pendingCandidate())
	return m
}

// pendingCandidate reports whether the running version is an unconfirmed candidate. Caller holds mu
// or has exclusive access.
func (m *Manager) pendingCandidate() bool {
	return m.st.Candidate != "" && m.st.Candidate == m.o.Version && !m.st.Confirmed
}

// Kick is signalled when the state changed and should be reported soon.
func (m *Manager) Kick() <-chan struct{} { return m.kick }

// SetStats attaches self-telemetry and records the current state and any uncounted result.
func (m *Manager) SetStats(s *selfmon.Stats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stats = s
	s.SetUpdateState(m.st.Status)
	m.countLocked()
}

// SetRestart registers the function that stops the agent after a switch (main exits 0 and
// systemd starts the new version).
func (m *Manager) SetRestart(fn func()) { m.restart = fn }

// SetReporter registers a best-effort immediate sync used right before exiting for a restart.
func (m *Manager) SetReporter(fn func(context.Context)) { m.report = fn }

// Restarting reports whether the agent stopped to restart into a new version.
func (m *Manager) Restarting() bool { return m.restarting.Load() }

// State returns a copy of the current state.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

// Report returns the update section of the next sync request.
func (m *Manager) Report() Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := Report{State: m.st.Status, FromVersion: m.st.FromVersion, ToVersion: m.st.ToVersion, Error: m.st.Error}
	if !m.st.ChangedAt.IsZero() {
		r.ChangedAt = m.st.ChangedAt.UTC().Format(time.RFC3339)
	}
	return r
}

// AgentInfo returns the agent section of the next sync request.
func (m *Manager) AgentInfo() AgentInfo {
	return AgentInfo{
		Name: AgentName, Version: m.o.Version, Commit: m.o.Commit, OS: m.o.OS, Arch: m.o.Arch,
		InstallMethod: m.o.Install.Method, UpdateCapable: m.o.Install.Capable,
		UpdateMode: m.o.Install.Mode, UpdateNotice: m.o.Install.Notice,
	}
}

// Startup runs first thing in main.
//
// Legacy mode: when the running binary is an unconfirmed candidate it counts the start attempt;
// after MaxStartAttempts starts or ConfirmWindow since staging it switches current back to the
// previous version and returns true: the process must exit so the service manager starts the
// previous version.
//
// Staged mode: attempts and rollbacks are handled by the privileged "-apply" before this start; the
// agent takes over its result (switched, rejected, rolled back) into the reported state.
func (m *Manager) Startup() (exit bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.o.Install.Mode == ModeStaged {
		m.startupStagedLocked()
		return false
	}
	if m.st.Staged != "" {
		m.st.Staged, m.st.Candidate = "", ""
		m.setLocked(StateFailed, "staged update "+m.st.ToVersion+" not installed: "+NoticeUnitOutdated)
		m.settled.Store(true)
		os.RemoveAll(filepath.Join(m.o.StateDir, UpdatesDir))
	}
	now := m.o.Now()
	switch {
	case m.pendingCandidate():
		m.st.Attempts++
		age := now.Sub(m.st.StagedAt)
		if m.st.Attempts > MaxStartAttempts || age > m.o.ConfirmWindow {
			reason := fmt.Sprintf("version %s did not confirm: start attempt %d, %s since staged (limits: %d attempts, %s)",
				m.o.Version, m.st.Attempts, age.Round(time.Second), MaxStartAttempts, m.o.ConfirmWindow)
			return m.rollbackLocked(reason)
		}
		m.setLocked(StateConfirming, "")
		m.log.Info("running an unconfirmed update; waiting for the first successful export",
			"version", m.o.Version, "previous", m.st.Previous, "attempt", m.st.Attempts)
	case m.st.Candidate != "" && m.st.Candidate != m.o.Version && !m.st.Confirmed &&
		(m.st.Status == StateRestarting || m.st.Status == StateConfirming || m.st.Status == StateStaged):
		// The service manager started something else than the staged candidate (e.g. a package
		// manager switched current meanwhile).
		m.st.Candidate = ""
		m.setLocked(StateFailed, fmt.Sprintf("started version %s instead of candidate %s", m.o.Version, m.st.ToVersion))
	}
	return false
}

// startupStagedLocked applies the result of this start's "-apply" to the state.
func (m *Manager) startupStagedLocked() {
	a := m.o.Install.Apply
	if a == nil {
		a = &ApplyStatus{}
	}
	defer func() {
		m.settled.Store(!m.pendingCandidate())
		if m.st.Staged == "" {
			os.RemoveAll(filepath.Join(m.o.StateDir, UpdatesDir))
		}
	}()
	cand := m.st.Candidate
	switch {
	case a.Result == ApplyRolledBack && cand != "" && a.FromVersion == cand:
		m.st.Candidate, m.st.Staged, m.st.RollbackRequest, m.st.RollbackReason = "", "", "", ""
		m.setLocked(StateRolledBack, a.Error)
		m.log.Error("the update was rolled back before this start", "candidate", cand, "running", m.o.Version, "reason", a.Error)
	case a.Result == ApplyRollbackFailed && cand != "" && a.FromVersion == cand:
		m.st.Candidate, m.st.RollbackRequest, m.st.RollbackReason = "", "", ""
		m.st.Confirmed = true
		m.setLocked(StateFailed, a.Error)
		m.log.Error("the update failed and could not be rolled back; staying on this version", "error", a.Error)
	case a.Result == ApplyRejected && m.st.Staged != "" && a.ToVersion == m.st.Staged:
		m.st.Candidate, m.st.Staged = "", ""
		m.setLocked(StateFailed, a.Error)
		m.log.Error("staged update rejected by the privileged pre-start step", "target", a.ToVersion, "error", a.Error)
	case a.Result == ApplySwitched && a.ToVersion == m.o.Version && m.st.Staged == m.o.Version:
		m.st.Staged = ""
		m.st.Attempts = 1
		m.setLocked(StateConfirming, "")
		m.log.Info("running an unconfirmed update; waiting for the first successful export", "version", m.o.Version, "previous", m.st.Previous)
	case m.st.Staged != "":
		msg := "staged update " + m.st.Staged + " was not installed by the privileged pre-start step"
		if a.Error != "" {
			msg += ": " + a.Error
		}
		m.st.Staged, m.st.Candidate = "", ""
		m.setLocked(StateFailed, msg)
	case m.pendingCandidate():
		if c := a.Candidate; c != nil && c.Version == m.o.Version {
			m.st.Attempts = c.Attempts
		}
		m.setLocked(StateConfirming, "")
	case cand != "" && cand != m.o.Version && !m.st.Confirmed &&
		(m.st.Status == StateRestarting || m.st.Status == StateConfirming || m.st.Status == StateStaged):
		m.st.Candidate = ""
		m.setLocked(StateFailed, fmt.Sprintf("started version %s instead of candidate %s", m.o.Version, m.st.ToVersion))
	}
}

// rollbackLocked switches current back to the previous version. It returns true when the switch
// happened and the process must exit.
func (m *Manager) rollbackLocked(reason string) bool {
	prev := m.st.Previous
	m.log.Error("update failed; rolling back", "candidate", m.st.Candidate, "previous", prev, "reason", reason)
	var err error
	switch {
	case prev == "":
		err = errors.New("no previous version recorded")
	case m.o.Install.Method != MethodTarball && m.o.Install.Method != MethodDeb && m.o.Install.Method != MethodRPM:
		err = fmt.Errorf("install method %s has no version layout", m.o.Install.Method)
	default:
		err = SwitchCurrent(m.o.Install.InstallRoot, prev)
	}
	m.st.Candidate = ""
	if err != nil {
		m.log.Error("rollback impossible; staying on this version", "error", err)
		m.st.Confirmed = true
		m.setLocked(StateFailed, reason+"; rollback impossible: "+err.Error())
		m.settled.Store(true)
		return false
	}
	m.setLocked(StateRolledBack, reason)
	m.settled.Store(true)
	return true
}

// setLocked changes the reported state and persists it.
func (m *Manager) setLocked(status, errMsg string) {
	m.st.Status = status
	m.st.Error = errMsg
	m.st.ChangedAt = m.o.Now().UTC()
	if terminal(status) {
		m.st.Counted = false
	}
	if err := SaveState(m.statePath, m.st); err != nil {
		m.log.Error("update state not saved", "error", err)
	}
	if m.stats != nil {
		m.stats.SetUpdateState(status)
		m.countLocked()
	}
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) countLocked() {
	if m.stats == nil || m.st.Counted || !terminal(m.st.Status) {
		return
	}
	m.stats.AddUpdateAttempt(m.st.Status)
	m.st.Counted = true
	if err := SaveState(m.statePath, m.st); err != nil {
		m.log.Error("update state not saved", "error", err)
	}
}

// Confirm is called after every successful export. On an unconfirmed candidate it marks the
// update succeeded and prunes old versions. It is cheap once settled.
func (m *Manager) Confirm() {
	if m.settled.Load() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.pendingCandidate() {
		m.settled.Store(true)
		return
	}
	m.st.Confirmed = true
	m.st.ConfirmedVersion, m.st.ConfirmedAt = m.o.Version, m.o.Now().UTC()
	m.setLocked(StateSucceeded, "")
	m.settled.Store(true)
	m.log.Info("update confirmed after a successful export", "version", m.o.Version, "previous", m.st.Previous)
	if m.o.Install.Mode == ModeStaged {
		return // "-apply" prunes root-owned versions at the next start
	}
	keep := []string{m.o.Install.VersionDir, m.st.Previous}
	if m.o.Install.PackageVersion != "" {
		keep = append(keep, packageUpstreamVersion(m.o.Install.PackageVersion)) // owned by dpkg/rpm
	}
	root := m.o.Install.InstallRoot
	go func() {
		removed, err := Prune(root, keep...)
		if err != nil {
			m.log.Warn("pruning old versions failed", "error", err)
		}
		if len(removed) > 0 {
			m.log.Info("pruned old versions", "removed", removed, "kept", keep)
		}
	}()
}

// Run enforces ConfirmWindow for a running candidate: without a confirmation it rolls back and
// stops the agent. It returns when ctx is done or the candidate is settled.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	if !m.pendingCandidate() {
		m.mu.Unlock()
		return
	}
	wait := m.st.StagedAt.Add(m.o.ConfirmWindow).Sub(m.o.Now())
	m.mu.Unlock()
	t := time.NewTimer(max(wait, 0))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return
	case <-t.C:
	}
	m.mu.Lock()
	if !m.pendingCandidate() {
		m.mu.Unlock()
		return
	}
	reason := fmt.Sprintf("version %s did not confirm within %s (no successful export)", m.o.Version, m.o.ConfirmWindow)
	exit := true
	if m.o.Install.Mode == ModeStaged {
		// Only root can switch current: ask the next "-apply" to roll back.
		m.log.Error("update did not confirm; restarting so the privileged pre-start step rolls back", "reason", reason)
		m.st.RollbackRequest, m.st.RollbackReason = m.o.Version, reason
		m.setLocked(StateRestarting, reason)
	} else {
		exit = m.rollbackLocked(reason)
	}
	m.mu.Unlock()
	if exit {
		m.stopForRestart(ctx)
	}
}

func (m *Manager) stopForRestart(ctx context.Context) {
	m.restarting.Store(true)
	if m.report != nil {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		m.report(rctx)
		cancel()
	}
	if m.restart != nil {
		m.restart()
	}
}

// Handle processes an update instruction from a sync response. It runs in the sync goroutine and
// may take minutes (download, self-test); it never blocks collection or export.
func (m *Manager) Handle(ctx context.Context, ins *Instruction) {
	if ins == nil || m.restarting.Load() {
		return
	}
	m.mu.Lock()
	skip := m.skipLocked(ins)
	m.mu.Unlock()
	if skip != "" {
		m.log.Debug("ignoring update instruction", "reason", skip, "action", ins.Action, "target", ins.TargetVersion, "rollout", ins.RolloutID)
		return
	}
	now := m.o.Now()
	notBefore, deadline := ins.times()
	if !notBefore.IsZero() && now.Before(notBefore) {
		m.log.Debug("update not yet due", "target", ins.TargetVersion, "not_before", notBefore)
		return
	}
	if !deadline.IsZero() && now.After(deadline) {
		m.log.Info("ignoring update instruction past its deadline", "target", ins.TargetVersion, "deadline", deadline)
		return
	}

	m.mu.Lock()
	m.st.Action, m.st.RolloutID = ins.Action, ins.RolloutID
	m.st.FromVersion, m.st.ToVersion = m.o.Version, ins.TargetVersion
	m.setLocked(StateVerifying, "")
	m.mu.Unlock()
	m.log.Info("update instruction received", "action", ins.Action, "from", m.o.Version, "target", ins.TargetVersion, "rollout", ins.RolloutID)

	if err := m.stage(ctx, ins, deadline); err != nil {
		if ctx.Err() != nil && m.restarting.Load() {
			return
		}
		m.mu.Lock()
		m.setLocked(StateFailed, err.Error())
		m.mu.Unlock()
		m.log.Error("update rejected", "action", ins.Action, "target", ins.TargetVersion, "error", err)
		return
	}
	m.log.Info("update staged; exiting to restart into the new version", "target", ins.TargetVersion)
	m.stopForRestart(ctx)
}

// skipLocked returns a reason not to act on ins, or "".
func (m *Manager) skipLocked(ins *Instruction) string {
	if m.pendingCandidate() {
		return "current version is not confirmed yet"
	}
	if ins.Action == ActionUpgrade && ins.TargetVersion == m.o.Version {
		return "already running the target version"
	}
	same := m.st.ToVersion == ins.TargetVersion && m.st.Action == ins.Action && m.st.RolloutID == ins.RolloutID
	if !same {
		return ""
	}
	switch m.st.Status {
	case StateRolledBack:
		return "this update was rolled back"
	case StateFailed:
		if m.o.Now().Sub(m.st.ChangedAt) < FailedRetryDelay {
			return "this update failed recently"
		}
	case StateSucceeded:
		if m.o.Version == ins.TargetVersion {
			return "already applied"
		}
	}
	return ""
}

// stage verifies, downloads, extracts and self-tests the target, then switches current.
func (m *Manager) stage(ctx context.Context, ins *Instruction, deadline time.Time) error {
	root := m.o.Install.InstallRoot
	v, err := VerifyInstruction(VerifyInput{
		Instruction: ins, Trusted: m.o.Trusted, Running: m.o.Version, OS: m.o.OS, Arch: m.o.Arch,
		RunningManifest: func() (*lib.Manifest, error) {
			if m.o.Install.VersionDir == "" {
				return nil, errors.New("running binary is not in the version layout")
			}
			b, err := os.ReadFile(filepath.Join(root, "versions", m.o.Install.VersionDir, ManifestFile))
			if err != nil {
				return nil, err
			}
			return lib.ParseManifest(b)
		},
	})
	if err != nil {
		return err
	}
	// Rule 8, before anything touches the network or the install root.
	if !m.o.Install.Capable {
		m.log.Warn("update requested but this install cannot update itself", "install_method", m.o.Install.Method, "reason", m.o.Install.Reason)
		return ruleErr(8, ErrNotCapable)
	}
	if m.o.Install.Mode == ModeStaged {
		return m.stageForApply(ctx, v, deadline)
	}

	ver := v.Version.String()
	versions := filepath.Join(root, "versions")
	partial := filepath.Join(versions, "."+ver+".partial")
	extract := filepath.Join(versions, "."+ver+".extract")
	defer os.Remove(partial)
	os.RemoveAll(extract)

	m.mu.Lock()
	m.setLocked(StateDownloading, "")
	m.mu.Unlock()
	if err := Download(ctx, m.o.HTTPClient, v.DownloadURL, m.downloadHeader(v.DownloadURL), partial, v.Artifact.Size, v.Artifact.SHA256); err != nil {
		return err
	}

	m.mu.Lock()
	m.setLocked(StateVerifying, "")
	m.mu.Unlock()
	if err := ExtractTarGz(partial, extract, TopDir(ver, m.o.OS, m.o.Arch), MaxExtractBytes); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(extract)
		}
	}()
	bin := filepath.Join(extract, BinaryName)
	if fi, err := os.Lstat(bin); err != nil || !fi.Mode().IsRegular() {
		return ruleErr(6, "archive has no regular file %s", BinaryName)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(extract, ManifestFile), v.ManifestBytes, 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(extract, SignatureFile), v.Signature, 0o644); err != nil {
		return err
	}
	if err := RunSelfTest(ctx, bin, m.o.ConfigPath, m.o.SelfTestTimeout); err != nil {
		return err
	}
	if !deadline.IsZero() && m.o.Now().After(deadline) {
		return fmt.Errorf("deadline %s passed before the switch", deadline.UTC().Format(time.RFC3339))
	}

	final := filepath.Join(versions, ver)
	if ver == m.o.Install.VersionDir {
		return fmt.Errorf("target directory %s is the running version", final)
	}
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	if err := os.Rename(extract, final); err != nil {
		return err
	}
	ok = true
	syncDir(versions)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.Previous = m.o.Install.VersionDir
	m.st.Candidate = ver
	m.st.Attempts = 0
	m.st.StagedAt = m.o.Now().UTC()
	m.st.Confirmed = false
	m.setLocked(StateStaged, "")
	if err := SwitchCurrent(root, ver); err != nil {
		m.st.Candidate = ""
		return err
	}
	m.setLocked(StateRestarting, "")
	return nil
}

// stageForApply is staging in staged mode: download, extract and self-test inside the sandbox, then
// keep the verified archive, manifest and signature in <state_dir>/updates/<v>/ and restart. The
// privileged "-apply" of the next start verifies everything again before installing.
func (m *Manager) stageForApply(ctx context.Context, v *Verified, deadline time.Time) error {
	ver := v.Version.String()
	updates := filepath.Join(m.o.StateDir, UpdatesDir)
	if err := os.RemoveAll(updates); err != nil {
		return err
	}
	partial := filepath.Join(updates, "."+ver+".partial")
	if err := os.MkdirAll(partial, 0o750); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(updates)
		}
	}()

	m.mu.Lock()
	m.setLocked(StateDownloading, "")
	m.mu.Unlock()
	archive := filepath.Join(partial, ArchiveFile)
	if err := Download(ctx, m.o.HTTPClient, v.DownloadURL, m.downloadHeader(v.DownloadURL), archive, v.Artifact.Size, v.Artifact.SHA256); err != nil {
		return err
	}

	m.mu.Lock()
	m.setLocked(StateVerifying, "")
	m.mu.Unlock()
	extract := filepath.Join(partial, "extract")
	if err := ExtractTarGz(archive, extract, TopDir(ver, m.o.OS, m.o.Arch), MaxExtractBytes); err != nil {
		return err
	}
	bin := filepath.Join(extract, BinaryName)
	if fi, err := os.Lstat(bin); err != nil || !fi.Mode().IsRegular() {
		return ruleErr(6, "archive has no regular file %s", BinaryName)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	// Fails early, before a restart, for candidates that cannot run on this host.
	if err := RunSelfTest(ctx, bin, m.o.ConfigPath, m.o.SelfTestTimeout); err != nil {
		return err
	}
	if err := os.RemoveAll(extract); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(partial, ManifestFile), v.ManifestBytes, 0o640); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(partial, SignatureFile), v.Signature, 0o640); err != nil {
		return err
	}
	if !deadline.IsZero() && m.o.Now().After(deadline) {
		return fmt.Errorf("deadline %s passed before the restart", deadline.UTC().Format(time.RFC3339))
	}
	if err := os.Rename(partial, filepath.Join(updates, ver)); err != nil {
		return err
	}
	ok = true
	syncDir(updates)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.Previous = m.o.Install.VersionDir
	m.st.Candidate, m.st.Staged = ver, ver
	m.st.Attempts = 0
	m.st.StagedAt = m.o.Now().UTC()
	m.st.Confirmed = false
	m.st.RollbackRequest, m.st.RollbackReason = "", ""
	m.setLocked(StateStaged, "")
	m.setLocked(StateRestarting, "")
	return nil
}

// downloadHeader authenticates downloads served by the backend mirror (same host as the endpoint).
func (m *Manager) downloadHeader(download string) http.Header {
	h := http.Header{}
	h.Set("User-Agent", AgentName+"/"+m.o.Version)
	du, err1 := url.Parse(download)
	eu, err2 := url.Parse(m.o.Endpoint)
	if err1 == nil && err2 == nil && m.o.LicenseKey != "" && du.Scheme == eu.Scheme && du.Host == eu.Host {
		h.Set(exporter.LicenseHeader, m.o.LicenseKey)
	}
	return h
}
