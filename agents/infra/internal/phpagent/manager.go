package phpagent

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
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
	lib "github.com/onuragtas/openlog/libs/release"
)

// Options configures a Manager.
type Options struct {
	// Config is php_agent of config.yaml.
	Config config.PHPAgentConfig
	// StateDir is state_dir; StatusDir the infra agent's install root (StatusFile).
	StateDir, StatusDir string
	// AgentVersion is the running infra agent's version (php_agent.version: agent).
	AgentVersion string
	OS, Arch     string
	Trusted      []ed25519.PublicKey
	// Capable is true when the privileged pre-start step ran for this start (update mode staged); Reason explains
	// why not.
	Capable bool
	Reason  string
	// OwnManifest returns the signed manifest and signature kept next to the running infra agent binary.
	OwnManifest func() (manifest, signature []byte, err error)
	HTTPClient  *http.Client
	// DownloadHeader returns the request header of a download (license key for the backend mirror).
	DownloadHeader func(url string) http.Header
	// ExtraBins returns further PHP binaries (discovered php-fpm executables).
	ExtraBins func() []string
	// APMActive reports whether PHP spans arrived within the last 10 minutes (apm_hint.status=active).
	APMActive func() bool
	// CanRestart reports whether the agent may exit for the privileged step now (no self-update in progress).
	CanRestart func() bool
	// Restart stops the agent so that the service manager starts it again (and runs "-apply").
	Restart func()

	Run            Runner
	Now            func() time.Time
	Log            *slog.Logger
	InventoryEvery time.Duration // [5m]
	Tick           time.Duration // [30s]
}

// State is <state_dir>/php-agent/state.json.
type State struct {
	RequestID string    `json:"request_id,omitempty"`
	Action    string    `json:"action,omitempty"`
	Operation string    `json:"operation,omitempty"`
	Version   string    `json:"version,omitempty"`
	Phase     string    `json:"phase,omitempty"`
	Error     string    `json:"error,omitempty"`
	ChangedAt time.Time `json:"changed_at,omitzero"`
	// HealthAt is when the health check of an applied installation runs.
	HealthAt time.Time `json:"health_at,omitzero"`
	// APMBefore: PHP services sent data when the installation was requested.
	APMBefore bool     `json:"apm_before,omitempty"`
	Units     []string `json:"units,omitempty"`
	// Counted: the operation's result was added to openlog.agent.php_agent.operations.
	Counted bool `json:"counted,omitempty"`
}

// Manager keeps the PHP runtime inventory and drives installations. Its methods are safe for concurrent use.
type Manager struct {
	o   Options
	log *slog.Logger
	dir string

	mu        sync.Mutex
	st        State
	remote    *Remote
	stats     *selfmon.Stats
	inv       []Runtime
	invAt     time.Time
	invFP     uint64
	installed string
	managedBy string

	wake chan struct{}
	kick chan struct{}
}

// NewManager loads the persisted state and remote settings.
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
	if o.Run == nil {
		o.Run = ExecRunner
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: update.DownloadTimeout}
	}
	if o.InventoryEvery <= 0 {
		o.InventoryEvery = 5 * time.Minute
	}
	if o.Tick <= 0 {
		o.Tick = 30 * time.Second
	}
	m := &Manager{o: o, log: o.Log.With("component", "php_agent"), dir: filepath.Join(o.StateDir, StateSubdir),
		wake: make(chan struct{}, 1), kick: make(chan struct{}, 1)}
	if b, err := os.ReadFile(filepath.Join(m.dir, StateFile)); err == nil {
		if err := json.Unmarshal(b, &m.st); err != nil {
			m.log.Warn("PHP agent state unreadable; starting from idle", "error", err)
			m.st = State{}
		}
	}
	if b, err := os.ReadFile(filepath.Join(m.dir, RemoteFile)); err == nil {
		var r Remote
		if json.Unmarshal(b, &r) == nil {
			m.remote = &r
		}
	}
	m.installed = InstalledVersion(o.Config.InstallRoot)
	return m
}

// Kick is signalled when the report changed and should be synced soon.
func (m *Manager) Kick() <-chan struct{} { return m.kick }

// SetStats attaches self-telemetry.
func (m *Manager) SetStats(s *selfmon.Stats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stats = s
	m.countLocked()
}

// Startup takes over the result of this start's privileged step. Call it before Run.
func (m *Manager) Startup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.st.Phase != StateRestarting {
		return
	}
	defer func() {
		os.Remove(filepath.Join(m.dir, RequestFile))
		os.RemoveAll(filepath.Join(m.dir, StagedDir))
	}()
	st, err := LoadStatus(m.o.StatusDir)
	if err != nil || st.RequestID != m.st.RequestID {
		msg := "the privileged pre-start step did not handle the request (unit without ExecStartPre=+… -apply?)"
		if !m.o.Capable && m.o.Reason != "" {
			msg += ": " + m.o.Reason
		}
		m.setLocked(StateFailed, msg)
		return
	}
	m.installed = InstalledVersion(m.o.Config.InstallRoot)
	switch st.Result {
	case ResultApplied:
		m.st.Units = st.Units
		m.st.HealthAt = st.At.Add(m.o.Config.HealthCheckAfter.D())
		m.setLocked(StateConfirming, "")
		m.log.Info("PHP agent installed; health check pending", "version", st.Version, "runtimes", st.Runtimes, "health_at", m.st.HealthAt)
	case ResultRolledBack:
		msg := st.Error
		if m.st.Action == ActionRollback && m.st.Error != "" {
			msg = m.st.Error // the health check's reason
		}
		m.setLocked(StateRolledBack, msg)
		m.log.Error("PHP agent rolled back", "version", m.st.Version, "now", st.Version, "error", msg)
	case ResultUninstalled:
		m.setLocked(StateUninstalled, "")
		m.log.Info("PHP agent removed")
	default:
		m.setLocked(StateFailed, st.Error)
		m.log.Error("PHP agent request failed", "action", m.st.Action, "version", m.st.Version, "error", st.Error)
	}
}

// SetRemote applies the php_agent section of a sync response.
func (m *Manager) SetRemote(raw json.RawMessage) {
	var r Remote
	if err := json.Unmarshal(raw, &r); err != nil {
		m.log.Warn("php_agent section of the sync response ignored", "error", err)
		return
	}
	if err := r.validate(); err != nil {
		m.log.Warn("php_agent section of the sync response ignored", "error", err)
		return
	}
	m.mu.Lock()
	changed := m.remote == nil || !remoteEqual(*m.remote, r)
	m.remote = &r
	m.mu.Unlock()
	if !changed {
		return
	}
	if b, err := json.Marshal(r); err == nil {
		if err := os.MkdirAll(m.dir, 0o750); err == nil {
			if err := update.WriteFileAtomic(filepath.Join(m.dir, RemoteFile), b, 0o640); err != nil {
				m.log.Warn("PHP agent fleet settings not persisted", "error", err)
			}
		}
	}
	if m.o.Config.RemoteConfig {
		m.log.Info("PHP agent fleet settings received", "mode", r.Mode, "target", r.TargetVersion, "reload", r.Reload, "reason", r.Reason)
	}
	m.invalidate()
}

func remoteEqual(a, b Remote) bool {
	return a.Mode == b.Mode && a.Version == b.Version && a.Reload == b.Reload && slices.Equal(a.ExcludeBins, b.ExcludeBins) &&
		a.TargetVersion == b.TargetVersion && a.Manifest == b.Manifest && a.Signature == b.Signature && a.DownloadURL == b.DownloadURL
}

func (r Remote) validate() error {
	switch r.Mode {
	case config.PHPAgentModeOff, config.PHPAgentModeManual, config.PHPAgentModeAuto:
	default:
		return fmt.Errorf("invalid mode %q", r.Mode)
	}
	switch r.Reload {
	case "", config.PHPAgentReloadNone, config.PHPAgentReloadGraceful:
	default:
		return fmt.Errorf("invalid reload %q", r.Reload)
	}
	if r.TargetVersion != "" && !validVersion(r.TargetVersion) {
		return fmt.Errorf("invalid target_version %q", r.TargetVersion)
	}
	return config.ValidatePHPExcludeBins(r.ExcludeBins)
}

// effective are the settings in force: the fleet's when it sent some and remote_config allows it.
type effective struct {
	mode, reload, target, source string
	exclude                      []string
	remote                       *Remote
}

func (m *Manager) effectiveLocked() effective {
	c := m.o.Config
	if m.remote != nil && c.RemoteConfig {
		r := m.remote
		reload := r.Reload
		if reload == "" {
			reload = config.PHPAgentReloadNone
		}
		return effective{mode: r.Mode, reload: reload, target: r.TargetVersion, exclude: r.ExcludeBins, source: SourceRemote, remote: r}
	}
	target := c.Version
	if target == config.PHPAgentVersionAgent {
		target = m.o.AgentVersion
	}
	return effective{mode: c.Mode, reload: c.Reload, target: target, exclude: c.ExcludeBins, source: SourceLocal}
}

// Report is the php_agent section of the next sync request.
func (m *Manager) Report() Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.effectiveLocked()
	r := Report{Mode: e.mode, Source: e.source, Capable: m.o.Capable, ManagedBy: m.managedBy, Version: m.installed,
		Runtimes: append([]Runtime{}, m.inv...)}
	if !m.o.Capable {
		r.Reason = m.o.Reason
	}
	if r.ManagedBy == "" {
		r.ManagedBy = ManagedNone
	}
	if m.st.Phase != "" {
		r.Update = &UpdateReport{Operation: m.st.Operation, Version: m.st.Version, State: m.st.Phase, Error: m.st.Error}
		if !m.st.ChangedAt.IsZero() {
			r.Update.ChangedAt = m.st.ChangedAt.UTC().Format(time.RFC3339)
		}
	}
	return r
}

func (m *Manager) invalidate() {
	m.mu.Lock()
	m.invAt = time.Time{}
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run keeps the inventory current and performs installations until ctx is done.
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

// Evaluate refreshes the inventory when due and takes the next step (health check, install, upgrade, removal).
func (m *Manager) Evaluate(ctx context.Context) {
	m.refresh(ctx)
	m.mu.Lock()
	e := m.effectiveLocked()
	st := m.st
	installed, managedBy := m.installed, m.managedBy
	inv := append([]Runtime{}, m.inv...)
	m.mu.Unlock()

	switch st.Phase {
	case StateDownloading, StateRestarting:
		return
	case StateConfirming:
		if !m.o.Now().Before(st.HealthAt) {
			m.healthCheck(ctx)
		}
		return
	}
	if !m.o.Capable || managedBy == ManagedPackage || managedBy == ManagedManual {
		return
	}
	// A failed health check whose rollback had to wait for a self-update to finish.
	if st.Phase == StateFailed && st.Action == ActionInstall && !st.HealthAt.IsZero() && installed == st.Version && managedBy == ManagedFleet {
		if m.mayRestart() {
			m.request(ActionRollback, OpRollback, st.Version, e)
		}
		return
	}
	switch e.mode {
	case config.PHPAgentModeOff:
		retryLater := st.Action == ActionUninstall && st.Phase == StateFailed && m.o.Now().Sub(st.ChangedAt) < FailedRetryDelay
		if managedBy == ManagedFleet && !retryLater && m.mayRestart() {
			m.request(ActionUninstall, OpUninstall, installed, e)
		}
	case config.PHPAgentModeAuto:
		if e.target == "" || e.target == installed || m.blocked(st, e.target) || !anySupported(inv) || !m.mayRestart() {
			return
		}
		m.install(ctx, e, installed, inv)
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

func anySupported(rts []Runtime) bool {
	for _, r := range rts {
		if r.Supported && !r.Excluded {
			return true
		}
	}
	return false
}

// refresh updates the inventory every InventoryEvery (or after invalidate) and kicks a sync when it changed.
func (m *Manager) refresh(ctx context.Context) {
	m.mu.Lock()
	due := m.invAt.IsZero() || m.o.Now().Sub(m.invAt) >= m.o.InventoryEvery
	e := m.effectiveLocked()
	m.mu.Unlock()
	if !due {
		return
	}
	root := m.o.Config.InstallRoot
	var rts []Runtime
	inst := filepath.Join(root, "current", InstallerRel)
	if fi, err := os.Stat(inst); err == nil && fi.Mode().IsRegular() {
		var err error
		if rts, err = InstallerStatus(ctx, m.o.Run, inst); err != nil {
			m.log.Debug("installer status failed; inspecting PHP binaries directly", "error", err)
			rts = nil
		}
	}
	if rts == nil {
		var extra []string
		if m.o.ExtraBins != nil {
			extra = m.o.ExtraBins()
		}
		rts = DetectRuntimes(ctx, m.o.Run, Candidates(extra))
	}
	MarkExcluded(rts, e.exclude)
	managedBy := ManagedBy(ctx, m.o.Run, root)
	installed := InstalledVersion(root)
	fp := fingerprint(rts, managedBy, installed)
	m.mu.Lock()
	changed := fp != m.invFP
	m.inv, m.invAt, m.invFP, m.managedBy, m.installed = rts, m.o.Now(), fp, managedBy, installed
	m.mu.Unlock()
	if changed {
		m.signal()
	}
}

func fingerprint(rts []Runtime, managedBy, installed string) uint64 {
	h := fnv.New64a()
	b, _ := json.Marshal(rts)
	h.Write(b)
	h.Write([]byte(managedBy + "\x00" + installed))
	return h.Sum64()
}

func (m *Manager) signal() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// install downloads and verifies the target release into state_dir, writes the request and restarts.
func (m *Manager) install(ctx context.Context, e effective, installed string, inv []Runtime) {
	op := OpInstall
	if installed != "" {
		op = OpUpgrade
	}
	id := newID()
	m.mu.Lock()
	m.st = State{RequestID: id, Action: ActionInstall, Operation: op, Version: e.target}
	m.setLocked(StateDownloading, "")
	m.mu.Unlock()
	m.log.Info("PHP agent installation started", "operation", op, "version", e.target, "installed", installed)

	if err := m.stage(ctx, e, inv); err != nil {
		m.mu.Lock()
		m.setLocked(StateFailed, err.Error())
		m.mu.Unlock()
		os.RemoveAll(filepath.Join(m.dir, StagedDir))
		m.log.Error("PHP agent installation failed before the privileged step", "version", e.target, "error", err)
		return
	}
	m.handOver(id, ActionInstall, e)
}

func (m *Manager) stage(ctx context.Context, e effective, inv []Runtime) error {
	manifest, sig, dl, err := m.release(e)
	if err != nil {
		return err
	}
	root := m.o.Config.InstallRoot
	v, err := Verify(manifest, sig, m.o.Trusted, e.target, m.o.OS, m.o.Arch, func() (*lib.Manifest, error) {
		b, err := os.ReadFile(filepath.Join(root, "current", ManifestFile))
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
	archive := filepath.Join(partial, ArchiveFile)
	header := http.Header{}
	if m.o.DownloadHeader != nil {
		header = m.o.DownloadHeader(dl)
	}
	if err := update.Download(ctx, m.o.HTTPClient, dl, header, archive, v.Artifact.Size, v.Artifact.SHA256); err != nil {
		return err
	}
	// The layout and a module for this host are checked before restarting; "-apply" checks everything again.
	tree := filepath.Join(partial, "extract")
	if err := update.ExtractTarGz(archive, tree, TopDir(ver, m.o.OS, m.o.Arch), update.MaxExtractBytes); err != nil {
		return err
	}
	defer os.RemoveAll(tree)
	if fi, err := os.Lstat(filepath.Join(tree, InstallerRel)); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("release %s has no %s", ver, InstallerRel)
	}
	var missing []string
	found := false
	for _, r := range inv {
		if !r.Supported || r.Excluded {
			continue
		}
		if fi, err := os.Stat(filepath.Join(tree, "modules", r.Module, "openlog.so")); err == nil && fi.Mode().IsRegular() {
			found = true
		} else {
			missing = append(missing, r.Bin+" ("+r.Module+")")
		}
	}
	if !found {
		return fmt.Errorf("release %s has no openlog.so for the PHP runtimes of this host: %s", ver, strings.Join(missing, ", "))
	}
	if err := os.RemoveAll(tree); err != nil {
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
	return nil, nil, "", fmt.Errorf("no signed manifest for PHP agent %s (the fleet sends it; locally only the infra agent's own version is available)", e.target)
}

// request writes a rollback or uninstall request and restarts.
func (m *Manager) request(action, op, version string, e effective) {
	id := newID()
	m.mu.Lock()
	keepErr := ""
	if action == ActionRollback {
		keepErr = m.st.Error
	}
	m.st = State{RequestID: id, Action: action, Operation: op, Version: version, Error: keepErr}
	m.mu.Unlock()
	m.handOver(id, action, e)
}

// handOver writes the request for "-apply" and restarts the agent.
func (m *Manager) handOver(id, action string, e effective) {
	m.mu.Lock()
	req := Request{ID: id, Action: action, Version: m.st.Version, Reload: e.reload, ExcludeBins: e.exclude, At: m.o.Now().UTC()}
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
	if m.o.APMActive != nil {
		m.st.APMBefore = m.o.APMActive()
	}
	m.setLocked(StateRestarting, m.st.Error)
	m.mu.Unlock()
	m.log.Info("exiting so that the privileged pre-start step handles the PHP agent request", "action", action, "version", req.Version)
	if m.o.Restart != nil {
		m.o.Restart()
	}
}

// healthCheck runs HealthCheckAfter after an installation: every enabled runtime loads openlog.so, reloaded units are
// active and PHP services that sent data before still do. On failure it requests a rollback.
func (m *Manager) healthCheck(ctx context.Context) {
	m.mu.Lock()
	st, e := m.st, m.effectiveLocked()
	m.mu.Unlock()
	var problems []string
	inst := filepath.Join(m.o.Config.InstallRoot, "current", InstallerRel)
	rts, err := InstallerStatus(ctx, m.o.Run, inst)
	if err != nil {
		problems = append(problems, err.Error())
	}
	MarkExcluded(rts, e.exclude)
	loaded := 0
	for _, r := range rts {
		switch {
		case r.Excluded:
		case r.Enabled && !r.Loaded:
			problems = append(problems, "openlog.so not loaded by "+r.Bin)
		case r.Enabled && r.Loaded:
			loaded++
		}
	}
	if err == nil && loaded == 0 {
		problems = append(problems, "openlog.so is loaded by no PHP runtime")
	}
	for _, u := range st.Units {
		out, _ := m.o.Run(ctx, "systemctl", "is-active", u)
		if s := strings.TrimSpace(string(out)); s != "active" {
			problems = append(problems, fmt.Sprintf("unit %s is %s after the reload", u, s))
		}
	}
	if st.APMBefore && m.o.APMActive != nil && !m.o.APMActive() {
		problems = append(problems, "PHP services sent data before the installation but not since")
	}
	if len(problems) == 0 {
		m.mu.Lock()
		m.setLocked(StateApplied, "")
		m.mu.Unlock()
		m.log.Info("PHP agent health check passed", "version", st.Version, "runtimes", loaded)
		m.invalidate()
		return
	}
	reason := "health check failed: " + strings.Join(problems, "; ")
	m.log.Error("PHP agent health check failed; rolling back", "version", st.Version, "reason", reason)
	m.mu.Lock()
	m.st.Error = update.Truncate(reason, 1024)
	m.st.Counted = false
	m.setLocked(StateFailed, m.st.Error)
	m.mu.Unlock()
	if !m.mayRestart() {
		return // the rollback is requested once a self-update finished (phase failed keeps the reason)
	}
	m.request(ActionRollback, OpRollback, st.Version, e)
}

// setLocked changes the phase, persists the state and counts terminal results.
func (m *Manager) setLocked(phase, errMsg string) {
	if m.st.Phase != phase {
		m.st.Counted = false
	}
	m.st.Phase, m.st.Error, m.st.ChangedAt = phase, update.Truncate(errMsg, 1024), m.o.Now().UTC()
	m.countLocked()
	if err := os.MkdirAll(m.dir, 0o750); err == nil {
		b, _ := json.MarshalIndent(m.st, "", "  ")
		if err := update.WriteFileAtomic(filepath.Join(m.dir, StateFile), b, 0o640); err != nil {
			m.log.Error("PHP agent state not saved", "error", err)
		}
	}
	m.signal()
}

// countLocked records openlog.agent.php_agent.operations once per terminal result.
func (m *Manager) countLocked() {
	if m.stats == nil || m.st.Counted {
		return
	}
	result := ""
	switch m.st.Phase {
	case StateApplied, StateUninstalled:
		result = "success"
	case StateFailed:
		result = "failure"
	case StateRolledBack:
		result = "failure" // the installation failed
		if m.st.Action == ActionRollback {
			result = "success"
		}
	default:
		return
	}
	m.stats.AddPHPAgentOperation(m.st.Operation, result)
	m.st.Counted = true
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
