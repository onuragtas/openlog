// Package update implements agent self-update: the sync client (POST /v1/openlog/agent/sync),
// install method detection, verification of signed release manifests, download and safe
// extraction, staging with an atomic "current" symlink switch, confirmation after the first
// successful export and automatic rollback.
//
// Contract: docs/contracts/releases-updates.md §3.
package update

import (
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
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
	// BinaryName is the executable inside versions/<v>/.
	BinaryName = "openlog-infra-agent"
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
)

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
