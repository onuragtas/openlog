// Package update implements agent self-update: the sync client (POST /v1/openlog/agent/sync),
// install method detection, verification of signed release manifests, download and safe
// extraction, staging with an atomic "current" symlink switch, confirmation after the first
// successful export and automatic rollback.
//
// Contract: docs/contracts/releases-updates.md §3.
package update

import (
	"encoding/json"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/phpaccess"
)

// Update states reported in sync requests.
const (
	StateIdle        = "idle"
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateStaged      = "staged"
	StateRestarting  = "restarting"
	StateConfirming  = "confirming"
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
	StateRolledBack  = "rolled_back"
)

// Instruction actions.
const (
	ActionUpgrade  = "upgrade"
	ActionRollback = "rollback"
)

// Install methods.
const (
	MethodTarball   = "tarball"
	MethodDeb       = "deb"
	MethodRPM       = "rpm"
	MethodContainer = "container"
	MethodDev       = "dev"
)

const (
	// AgentName is agent.name in sync requests and the deb/rpm package name.
	AgentName = "openlog-infra-agent"
	// BinaryName is the executable inside versions/<v>/ (".exe" on Windows).
	BinaryName = "openlog-infra-agent" + exeSuffix
	// ManifestFile and SignatureFile are kept next to each installed binary.
	ManifestFile  = "manifest.json"
	SignatureFile = "manifest.json.sig"
	// StateFile is the update state file in state_dir.
	StateFile = "update-state.json"

	// MaxStartAttempts is the number of starts a candidate gets before rollback.
	MaxStartAttempts = 3
	// ConfirmWindow is how long a candidate may take to confirm before rollback.
	ConfirmWindow = 5 * time.Minute
	// SelfTestTimeout bounds "<new binary> -self-test".
	SelfTestTimeout = 30 * time.Second
	// FailedRetryDelay is how long the same failed instruction (action, version, rollout) is not retried.
	FailedRetryDelay = time.Hour
	// MaxArchiveBytes caps the downloaded archive size.
	MaxArchiveBytes = 256 << 20
	// MaxExtractBytes caps the total size of extracted files.
	MaxExtractBytes = 512 << 20
	// DownloadTimeout bounds one archive download.
	DownloadTimeout = 10 * time.Minute

	// UnitName is the systemd service installed by the packages and install.sh.
	UnitName = "openlog-infra-agent.service"
	// AgentUser is the account the service runs as (User= in the unit).
	AgentUser = "openlog-agent"
	// UpdatesDir below state_dir holds releases staged by the agent for "-apply" (staged mode).
	UpdatesDir = "updates"
	// ArchiveFile is the staged release archive in <state_dir>/updates/<v>/.
	ArchiveFile = "archive.tar.gz"
)

// Update modes (how an update reaches the install root).
const (
	// ModeStaged: the agent stages a verified release in state_dir; the unit's privileged
	// pre-start step ("ExecStartPre=+… -apply") re-verifies, installs and reconciles it.
	ModeStaged = "staged"
	// ModeLegacy: an old unit without the pre-start step; the agent replaces the binary itself
	// (only possible while versions/ is writable by the agent user).
	ModeLegacy = "legacy"
)

// NoticeUnitOutdated is reported when the privileged pre-start step did not run for this start.
const NoticeUnitOutdated = "unit outdated: the privileged pre-start step (ExecStartPre=+… -apply) did not run, so updates cannot change " +
	"the systemd unit, groups or ownership; run the package upgrade (apt/dnf) or install.sh once to enable full updates"

// SyncRequest is the body of POST /v1/openlog/agent/sync.
type SyncRequest struct {
	HostID     string    `json:"host_id"`
	HostName   string    `json:"host_name"`
	Agent      AgentInfo `json:"agent"`
	Update     Report    `json:"update"`
	ConfigHash string    `json:"config_hash"`
	// IntegrationsConfigRevision is the applied remote integration config
	// revision ("" none, "disabled" when integrations.remote_config is false).
	IntegrationsConfigRevision string `json:"integrations_config_revision"`
	// Reconcile is the last result of the privileged reconcile step (absent when it never ran).
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
	// PHPAgent is the PHP runtime inventory and PHP agent installation state (phpagent.Report, php-agent.md §7.3).
	PHPAgent any `json:"php_agent,omitempty"`
	// JavaAgent is the JVM inventory and Java agent installation state (javaagent.Report, java-agent.md §2).
	JavaAgent any `json:"java_agent,omitempty"`
	// EBPFProfiler is the whole-host CPU profiler's installation state (ebpfprofiler.Report, ebpf-profiler.md).
	EBPFProfiler any `json:"ebpf_profiler,omitempty"`
	// PHPAccess is which PHP-FPM pools may send to php.sock (absent without PHP-FPM pools, php-agent.md §1).
	PHPAccess *phpaccess.Report `json:"php_access,omitempty"`
}

// AgentInfo describes the running agent.
type AgentInfo struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	InstallMethod string `json:"install_method"`
	UpdateCapable bool   `json:"update_capable"`
	// UpdateMode is ModeStaged, ModeLegacy or "" (not update capable).
	UpdateMode string `json:"update_mode,omitempty"`
	// UpdateNotice is an operator action, e.g. NoticeUnitOutdated.
	UpdateNotice string `json:"update_notice,omitempty"`
}

// ReconcileReport summarizes ReconcileStatus in sync requests.
type ReconcileReport struct {
	Version     string `json:"version"`
	At          string `json:"at"`
	UnitChanged bool   `json:"unit_changed"`
	Docker      string `json:"docker"`
	// PHPAccess is the openlog-php step: added, member, opt_out or error ("" = not run by this release).
	PHPAccess string `json:"php_access,omitempty"`
	Error     string `json:"error"`
}

// Report is the update state reported to the backend.
type Report struct {
	State       string `json:"state"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Error       string `json:"error"`
	ChangedAt   string `json:"changed_at"`
}

// SyncResponse is the sync response body. Unknown fields are ignored.
type SyncResponse struct {
	PollIntervalSeconds int          `json:"poll_interval_seconds"`
	ServerVersion       string       `json:"server_version"`
	Update              *Instruction `json:"update"`
	// IntegrationsConfig is present only when the host's remote integration
	// config differs from the reported revision (null/absent: keep the current one).
	IntegrationsConfig *config.RemoteIntegrations `json:"integrations_config"`
	// PHPAgent is the fleet's PHP agent settings for this host (phpagent.Remote; null/absent: keep the last ones).
	PHPAgent json.RawMessage `json:"php_agent,omitempty"`
	// JavaAgent is the fleet's Java agent settings for this host (javaagent.Remote; null/absent: keep the last ones).
	JavaAgent json.RawMessage `json:"java_agent,omitempty"`
	// EBPFProfiler is the fleet's profiler settings for this host (ebpfprofiler.Remote; null/absent: keep the last ones).
	EBPFProfiler json.RawMessage `json:"ebpf_profiler,omitempty"`
}

// Instruction is an update ordered by the backend. The backend is untrusted: everything that
// matters is taken from the signed manifest.
type Instruction struct {
	Action        string `json:"action"`
	TargetVersion string `json:"target_version"`
	Manifest      string `json:"manifest"`  // base64 of manifest.json bytes
	Signature     string `json:"signature"` // contents of manifest.json.sig
	DownloadURL   string `json:"download_url"`
	RolloutID     string `json:"rollout_id"`
	NotBefore     string `json:"not_before"`
	Deadline      string `json:"deadline"`
}

// times parses not_before and deadline; unparsable or empty values are zero.
func (in *Instruction) times() (notBefore, deadline time.Time) {
	notBefore, _ = time.Parse(time.RFC3339, in.NotBefore)
	deadline, _ = time.Parse(time.RFC3339, in.Deadline)
	return notBefore, deadline
}
