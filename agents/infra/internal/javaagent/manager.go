package javaagent

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
	"github.com/onuragtas/openlog/agents/infra/internal/update"
	lib "github.com/onuragtas/openlog/libs/release"
)

// Options configures a Manager.
type Options struct {
	// Config is java_agent of config.yaml.
	Config config.JavaAgentConfig
	// StateDir is state_dir; StatusDir the infra agent's install root (StatusFile).
	StateDir, StatusDir string
	// AgentVersion is the running infra agent's version (java_agent.version: agent).
	AgentVersion string
	OS           string
	Trusted      []ed25519.PublicKey
	// Capable is true when requests can be applied (Linux: the privileged pre-start step ran for this start; macOS and
	// Windows: the privileged service process). Reason explains why not.
	Capable bool
	Reason  string
	// InProcess applies requests in this process (macOS and Windows services run as root / LocalSystem) with Sys;
	// otherwise the agent exits so that "-apply" of the next start handles them (Linux).
	InProcess bool
	Sys       *update.Sys
	// OwnManifest returns the signed manifest and signature kept next to the running infra agent binary.
	OwnManifest func() (manifest, signature []byte, err error)
	HTTPClient  *http.Client
	// DownloadHeader returns the request header of a download (license key for the backend mirror).
	DownloadHeader func(url string) http.Header
	// CanRestart reports whether the agent may exit for the privileged step now (no self-update in progress).
	CanRestart func() bool
	// Restart stops the agent so that the service manager starts it again (and runs "-apply").
	Restart func()
	// ListProcs lists JVM candidates (default ListProcs).
	ListProcs func() ([]Proc, error)

	Now            func() time.Time
	Log            *slog.Logger
	InventoryEvery time.Duration // [5m]
	Tick           time.Duration // [30s]
}

// State is <state_dir>/java-agent/state.json.
type State struct {
	RequestID string    `json:"request_id,omitempty"`
	Action    string    `json:"action,omitempty"`
	Operation string    `json:"operation,omitempty"`
	Version   string    `json:"version,omitempty"`
	Phase     string    `json:"phase,omitempty"`
	Error     string    `json:"error,omitempty"`
	ChangedAt time.Time `json:"changed_at,omitzero"`
	// HealthAt is when the applied jar is verified; SwitchedAt when current was switched.
	HealthAt   time.Time `json:"health_at,omitzero"`
	SwitchedAt time.Time `json:"switched_at,omitzero"`
}

// Manager keeps the JVM inventory and drives installations. Its methods are safe for concurrent use.
type Manager struct {
	o   Options
	log *slog.Logger
	dir string

	mu         sync.Mutex
	st         State
	remote     *Remote
	status     *Status
	current    string
	managed    bool
	linkState  string
	jvms       []JVM
	invAt      time.Time
	invFP      uint64
	jars       map[string]jarInfo
	lastSwitch time.Time

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
	if o.ListProcs == nil {
		o.ListProcs = ListProcs
	}
	if o.InventoryEvery <= 0 {
		o.InventoryEvery = 5 * time.Minute
	}
	if o.Tick <= 0 {
		o.Tick = 30 * time.Second
	}
	m := &Manager{o: o, log: o.Log.With("component", "java_agent"), dir: filepath.Join(o.StateDir, StateSubdir),
		jars: map[string]jarInfo{}, jvms: []JVM{}, linkState: LinkMissing,
		wake: make(chan struct{}, 1), kick: make(chan struct{}, 1)}
	if b, err := os.ReadFile(filepath.Join(m.dir, StateFile)); err == nil {
		if err := json.Unmarshal(b, &m.st); err != nil {
			m.log.Warn("Java agent state unreadable; starting from idle", "error", err)
			m.st = State{}
		}
	}
	if b, err := os.ReadFile(filepath.Join(m.dir, RemoteFile)); err == nil {
		var r Remote
		if json.Unmarshal(b, &r) == nil {
			m.remote = &r
		}
	}
	m.loadInstall()
	return m
}

// Kick is signalled when the report changed and should be synced soon.
func (m *Manager) Kick() <-chan struct{} { return m.kick }

// loadInstall re-reads the install state (status file, current, marker, link_path).
func (m *Manager) loadInstall() {
	c := m.o.Config
	st, _ := LoadStatus(m.o.StatusDir)
	cur := InstalledVersion(c.InstallRoot)
	fi, err := os.Lstat(filepath.Join(c.InstallRoot, MarkerFile))
	managed := err == nil && fi.Mode().IsRegular()
	sha := ""
	if st != nil {
		sha = st.LinkSHA256
	}
	link := LinkStateOf(c.LinkPath, c.InstallRoot, sha)
	if link == LinkManaged && st != nil && st.LinkState == LinkPending && st.LinkVersion != cur {
		link = LinkPending
	}
	m.mu.Lock()
	m.status, m.current, m.managed, m.linkState = st, cur, managed, link
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
		if m.st.Action == ActionSwitch {
			m.setLocked(StateApplied, st.Error)
			return
		}
		m.st.SwitchedAt = st.At
		m.st.HealthAt = st.At.Add(m.o.Config.HealthCheckAfter.D())
		m.setLocked(StateConfirming, "")
		m.log.Info("Java agent installed; verification pending", "version", st.Version, "link", st.LinkState, "health_at", m.st.HealthAt)
	case ResultRolledBack:
		msg := st.Error
		if m.st.Error != "" {
			msg = m.st.Error // the verification's reason
		}
		m.setLocked(StateRolledBack, msg)
		m.log.Error("Java agent rolled back", "version", m.st.Version, "now", st.Version, "error", msg)
	case ResultUninstalled:
		m.setLocked(StateUninstalled, "")
		m.log.Info("Java agent removed")
	default:
		m.setLocked(StateFailed, st.Error)
		m.log.Error("Java agent request failed", "action", m.st.Action, "version", m.st.Version, "error", st.Error)
	}
}

// SetRemote applies the java_agent section of a sync response.
func (m *Manager) SetRemote(raw json.RawMessage) {
	var r Remote
	if err := json.Unmarshal(raw, &r); err != nil {
		m.log.Warn("java_agent section of the sync response ignored", "error", err)
		return
	}
	switch r.Mode {
	case config.JavaAgentModeOff, config.JavaAgentModeManual, config.JavaAgentModeAuto:
	default:
		m.log.Warn("java_agent section of the sync response ignored", "error", fmt.Sprintf("invalid mode %q", r.Mode))
		return
	}
	if r.TargetVersion != "" && !validVersion(r.TargetVersion) {
		m.log.Warn("java_agent section of the sync response ignored", "error", fmt.Sprintf("invalid target_version %q", r.TargetVersion))
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
				m.log.Warn("Java agent fleet settings not persisted", "error", err)
			}
		}
	}
	if m.o.Config.RemoteConfig {
		m.log.Info("Java agent fleet settings received", "mode", r.Mode, "target", r.TargetVersion, "reason", r.Reason)
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
	if target == config.JavaAgentVersionAgent {
		target = m.o.AgentVersion
	}
	return effective{mode: c.Mode, target: target, source: SourceLocal}
}

// Report is the java_agent section of the next sync request.
func (m *Manager) Report() Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.effectiveLocked()
	r := Report{Mode: e.mode, Source: e.source, Capable: m.o.Capable, Managed: m.managed, CurrentVersion: m.current,
		TargetVersion: e.target, LinkPath: m.o.Config.LinkPath, LinkState: m.linkState, JVMs: append([]JVM{}, m.jvms...)}
	if !m.o.Capable {
		r.Reason = m.o.Reason
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
	c := m.o.Config
	pending := 0
	for _, j := range m.jvms {
		if j.RestartPending {
			pending++
		}
	}
	switch {
	case m.linkState == LinkUnmanaged:
		return StatusUnmanaged, fmt.Sprintf("%s was not installed by the infra agent and is left alone. To keep the Java agent updated, "+
			"point -javaagent at %s, or move the file away (running JVMs keep their open copy) so that the infra agent creates %s as a link",
			c.LinkPath, filepath.Join(c.InstallRoot, "current", JarName), c.LinkPath)
	case m.st.Phase == StateFailed:
		return StatusError, m.st.Error
	case m.status != nil && m.status.LinkState == LinkError && m.current != "":
		return StatusError, m.status.Error
	case m.st.Phase == StateDownloading || m.st.Phase == StateRestarting:
		return StatusStaged, ""
	case m.current == "":
		return StatusNotFound, ""
	case m.linkState == LinkPending:
		return StatusRestartPending, m.status.Error
	case pending > 0:
		return StatusRestartPending, fmt.Sprintf("%d JVM(s) still run an older Java agent: restart the application(s) to load %s", pending, m.current)
	}
	return StatusInstalled, ""
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

// refresh updates the install state and JVM inventory every InventoryEvery (or after invalidate).
func (m *Manager) refresh() {
	m.mu.Lock()
	due := m.invAt.IsZero() || m.o.Now().Sub(m.invAt) >= m.o.InventoryEvery
	m.mu.Unlock()
	if !due {
		return
	}
	m.loadInstall()
	procs, err := m.o.ListProcs()
	if err != nil {
		m.log.Debug("process list failed", "error", err)
	}
	m.mu.Lock()
	in := &inspector{goos: m.o.OS, root: m.o.Config.InstallRoot, link: m.o.Config.LinkPath, foreignLink: m.linkState == LinkUnmanaged,
		current: m.current, status: m.status, cache: m.jars}
	m.mu.Unlock()
	jvms := in.JVMs(procs)
	h := fnv.New64a()
	b, _ := json.Marshal(jvms)
	h.Write(b)
	m.mu.Lock()
	h.Write([]byte(m.current + "\x00" + m.linkState + fmt.Sprint(m.managed)))
	fp := h.Sum64()
	changed := fp != m.invFP
	m.jvms, m.invAt, m.invFP = jvms, m.o.Now(), fp
	m.mu.Unlock()
	if changed {
		m.signal()
	}
}

// inUseLocked are the versions loaded by managed JVMs (unknown loads count as current and previous).
func (m *Manager) inUseLocked() []string {
	var out []string
	for _, j := range m.jvms {
		if !j.Managed || j.Container {
			continue
		}
		vs := []string{j.LoadedVersion}
		if j.LoadedVersion == "" {
			vs = []string{m.current}
			if m.status != nil {
				vs = append(vs, m.status.Previous)
			}
		}
		for _, v := range vs {
			if v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// Evaluate refreshes the inventory when due and takes the next step (verification, install, upgrade, link retry,
// removal).
func (m *Manager) Evaluate(ctx context.Context) {
	m.refresh()
	m.mu.Lock()
	e := m.effectiveLocked()
	st, cur, managed, link := m.st, m.current, m.managed, m.linkState
	inUse := m.inUseLocked()
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
	switch e.mode {
	case config.JavaAgentModeOff:
		if !managed {
			return
		}
		if len(inUse) > 0 {
			msg := fmt.Sprintf("java_agent.mode is off, but running JVMs use the managed jar (%s): it is removed once they are stopped",
				strings.Join(inUse, ", "))
			if st.Error != msg {
				m.mu.Lock()
				m.st = State{Action: ActionUninstall, Operation: OpUninstall, Version: cur}
				m.setLocked(StateFailed, msg)
				m.mu.Unlock()
			}
			return
		}
		retryLater := st.Action == ActionUninstall && st.Phase == StateFailed && st.RequestID != "" && m.o.Now().Sub(st.ChangedAt) < FailedRetryDelay
		if !retryLater && m.mayRestart() {
			m.request(ctx, ActionUninstall, OpUninstall, cur, nil)
		}
	case config.JavaAgentModeAuto:
		if e.target == "" {
			return
		}
		if e.target == cur {
			m.mu.Lock()
			retry := link == LinkPending && m.o.InProcess && m.o.Now().Sub(m.lastSwitch) >= m.o.Tick
			if retry {
				m.lastSwitch = m.o.Now()
			}
			m.mu.Unlock()
			if retry {
				m.request(ctx, ActionSwitch, OpSwitch, cur, inUse)
			}
			return
		}
		if m.blocked(st, e.target) || !m.mayRestart() {
			return
		}
		m.install(ctx, e, cur, inUse)
	}
}

func (m *Manager) mayRestart() bool {
	return m.o.InProcess || m.o.CanRestart == nil || m.o.CanRestart()
}

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

// install downloads and verifies the target jar into state_dir and hands the request to the privileged step.
func (m *Manager) install(ctx context.Context, e effective, installed string, inUse []string) {
	op := OpInstall
	if installed != "" {
		op = OpUpgrade
	}
	id := newID()
	m.mu.Lock()
	m.st = State{RequestID: id, Action: ActionInstall, Operation: op, Version: e.target}
	m.setLocked(StateDownloading, "")
	m.mu.Unlock()
	m.log.Info("Java agent installation started", "operation", op, "version", e.target, "installed", installed)
	if err := m.stage(ctx, e, installed); err != nil {
		m.mu.Lock()
		m.setLocked(StateFailed, err.Error())
		m.mu.Unlock()
		os.RemoveAll(filepath.Join(m.dir, StagedDir))
		m.log.Error("Java agent installation failed before the privileged step", "version", e.target, "error", err)
		return
	}
	m.handOver(ctx, id, ActionInstall, e.target, inUse)
}

func (m *Manager) stage(ctx context.Context, e effective, installed string) error {
	manifest, sig, dl, err := m.release(e)
	if err != nil {
		return err
	}
	root := m.o.Config.InstallRoot
	v, err := Verify(manifest, sig, m.o.Trusted, e.target, func() (*lib.Manifest, error) {
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
	jar := filepath.Join(partial, JarName)
	if err := update.Download(ctx, m.o.HTTPClient, dl, header, jar, v.Artifact.Size, v.Artifact.SHA256); err != nil {
		return err
	}
	if err := ValidateJar(jar, ver); err != nil {
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
	return nil, nil, "", fmt.Errorf("no signed manifest for Java agent %s (the fleet sends it; locally only the infra agent's own version is available)", e.target)
}

// request writes a rollback, switch or uninstall request and hands it over.
func (m *Manager) request(ctx context.Context, action, op, version string, inUse []string) {
	id := newID()
	m.mu.Lock()
	keepErr := ""
	if action == ActionRollback {
		keepErr = m.st.Error
	}
	m.st = State{RequestID: id, Action: action, Operation: op, Version: version, Error: keepErr}
	m.mu.Unlock()
	m.handOver(ctx, id, action, version, inUse)
}

// handOver writes the request and applies it in this process, or exits so that "-apply" handles it.
func (m *Manager) handOver(ctx context.Context, id, action, version string, inUse []string) {
	m.mu.Lock()
	req := Request{ID: id, Action: action, Version: version, InUse: inUse, At: m.o.Now().UTC()}
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
	if m.o.InProcess {
		st := Apply(ctx, ApplyOptions{Sys: m.o.Sys, StateDir: m.o.StateDir, StatusDir: m.o.StatusDir, Config: m.o.Config,
			Trusted: m.o.Trusted, OS: m.o.OS, Now: m.o.Now, Log: m.o.Log})
		m.mu.Lock()
		m.cleanup()
		if st == nil {
			m.setLocked(StateFailed, "the privileged step did not handle the request (not privileged?)")
		} else {
			m.finishLocked(st)
		}
		m.mu.Unlock()
		m.loadInstall()
		m.invalidate()
		return
	}
	m.log.Info("exiting so that the privileged pre-start step handles the Java agent request", "action", action, "version", version)
	if m.o.Restart != nil {
		m.o.Restart()
	}
}

// verify runs HealthCheckAfter after a switch: current points at the version, its jar is the signed release (sha256
// of the manifest kept next to it) and a Java agent, and a managed link_path resolves to it. A JVM that crashes with
// the new jar is not detected (see java-agent.md §2.5): on failure it requests a rollback to the previous version.
func (m *Manager) verify(ctx context.Context) {
	m.loadInstall()
	m.mu.Lock()
	st, c, cur := m.st, m.o.Config, m.current
	link := m.linkState
	m.mu.Unlock()
	var problems []string
	v := st.Version
	jar := filepath.Join(c.InstallRoot, "versions", v, JarName)
	if cur != v {
		problems = append(problems, fmt.Sprintf("current points at %q, not %s", cur, v))
	}
	if err := ValidateJar(jar, v); err != nil {
		problems = append(problems, err.Error())
	} else if b, err := os.ReadFile(filepath.Join(c.InstallRoot, "versions", v, ManifestFile)); err != nil {
		problems = append(problems, "manifest: "+err.Error())
	} else if mf, err := lib.ParseManifest(b); err != nil {
		problems = append(problems, "manifest: "+err.Error())
	} else if art, ok := mf.Artifact(lib.ComponentJavaAgent, lib.PlatformAny, lib.PlatformAny, lib.FormatJar); !ok {
		problems = append(problems, "manifest has no java-agent jar")
	} else if sum, err := fileSHA256(jar); err != nil || !strings.EqualFold(sum, art.SHA256) {
		problems = append(problems, "installed jar does not match the signed sha256")
	}
	if link == LinkManaged && m.o.OS != "windows" {
		if real, err := filepath.EvalSymlinks(c.LinkPath); err != nil {
			problems = append(problems, "link_path: "+err.Error())
		} else if realJar, err := filepath.EvalSymlinks(jar); err != nil || real != realJar {
			problems = append(problems, fmt.Sprintf("%s does not resolve to %s", c.LinkPath, jar))
		}
	}
	if len(problems) == 0 {
		m.mu.Lock()
		m.setLocked(StateApplied, "")
		m.mu.Unlock()
		m.log.Info("Java agent verified", "version", v)
		m.invalidate()
		return
	}
	reason := "verification failed: " + strings.Join(problems, "; ")
	m.log.Error("Java agent verification failed; rolling back", "version", v, "reason", reason)
	m.mu.Lock()
	m.setLocked(StateFailed, update.Truncate(reason, 1024))
	inUse := m.inUseLocked()
	m.mu.Unlock()
	if m.mayRestart() {
		m.request(ctx, ActionRollback, OpRollback, v, inUse)
	}
}

// setLocked changes the phase and persists the state.
func (m *Manager) setLocked(phase, errMsg string) {
	m.st.Phase, m.st.Error, m.st.ChangedAt = phase, update.Truncate(errMsg, 1024), m.o.Now().UTC()
	if err := os.MkdirAll(m.dir, 0o750); err == nil {
		b, _ := json.MarshalIndent(m.st, "", "  ")
		if err := update.WriteFileAtomic(filepath.Join(m.dir, StateFile), b, 0o640); err != nil {
			m.log.Error("Java agent state not saved", "error", err)
		}
	}
	m.signal()
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
