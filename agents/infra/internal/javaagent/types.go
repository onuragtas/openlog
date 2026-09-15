// Package javaagent keeps openlog-javaagent.jar current through the infra agent (docs/contracts/java-agent.md §2,
// D-123). It follows the PHP agent installation (internal/phpagent, D-082): the agent decides from its configuration
// or the fleet's settings, downloads and verifies the signed release into state_dir and writes a request; the
// privileged step (Linux: "-apply" of the next start; macOS and Windows: the privileged service process itself)
// verifies everything again, installs the jar root-owned into install_root/versions/<v>/ and switches the stable
// link_path to it without ever changing a jar in place. Running JVMs keep the file they opened and load the new
// version at their next start; the agent reports them as restart_pending and never signals a process.
package javaagent

import (
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

const (
	// JarName is the jar inside a version directory and the default name of the stable link.
	JarName = "openlog-javaagent.jar"
	// MarkerFile in the install root marks an installation made by the infra agent; roots without it are never changed.
	MarkerFile = ".managed-by-openlog-infra-agent"
	// StatusFile is the root-owned result of the privileged step in the infra agent's install root.
	StatusFile = "java-agent-status.json"
	// VersionAttribute is the jar manifest attribute with the openlog version (agents/java/README.md).
	VersionAttribute = "Openlog-Javaagent-Version"

	// StateSubdir below state_dir holds the agent's files (all untrusted for the privileged step).
	StateSubdir   = "java-agent"
	RequestFile   = "request.json"
	StateFile     = "state.json"
	RemoteFile    = "remote.json"
	StagedDir     = "staged"
	ManifestFile  = "manifest.json"
	SignatureFile = "manifest.json.sig"

	maxManifestBytes  = 1 << 20
	maxSignatureBytes = 64 << 10
	maxRequestBytes   = 64 << 10
	maxStatusBytes    = 1 << 20
	// MaxJarBytes bounds the jar (the release jar is about 25 MiB).
	MaxJarBytes = 256 << 20

	// FailedRetryDelay: a failed installation of a version is retried after this; a rolled back one never.
	FailedRetryDelay = time.Hour
	// maxHistory is the number of recorded switches (loaded version detection by start time).
	maxHistory = 4
	// MaxJVMs bounds the reported JVMs.
	MaxJVMs = 64
)

// Request actions (Request.Action).
const (
	ActionInstall   = "install"
	ActionRollback  = "rollback"
	ActionUninstall = "uninstall"
	// ActionSwitch retries switching link_path to the current version (Windows: the old copy was locked by a JVM).
	ActionSwitch = "switch"
)

// Results of the privileged step (Status.Result).
const (
	ResultApplied     = "applied"
	ResultFailed      = "failed"
	ResultRolledBack  = "rolled_back"
	ResultUninstalled = "uninstalled"
	// ResultRejected: verification or ownership checks failed; nothing was changed.
	ResultRejected = "rejected"
)

// Update states reported in sync (UpdateReport.State).
const (
	StateDownloading = "downloading"
	StateRestarting  = "restarting"
	StateConfirming  = "confirming"
	StateApplied     = "applied"
	StateFailed      = "failed"
	StateRolledBack  = "rolled_back"
	StateUninstalled = "uninstalled"
)

// Operations (UpdateReport.Operation).
const (
	OpInstall   = "install"
	OpUpgrade   = "upgrade"
	OpRollback  = "rollback"
	OpUninstall = "uninstall"
	OpSwitch    = "switch"
)

// Report.Status values.
const (
	StatusInstalled      = "installed"
	StatusStaged         = "staged"
	StatusRestartPending = "restart_pending"
	StatusUnmanaged      = "unmanaged"
	StatusError          = "error"
	StatusNotFound       = "not_found"
)

// Link states of link_path (Report.LinkState, Status.LinkState).
const (
	LinkMissing   = "missing"
	LinkManaged   = "managed"
	LinkUnmanaged = "unmanaged"
	// LinkPending: the managed copy still holds an older version because a JVM locks it (Windows).
	LinkPending = "pending"
	LinkError   = "error"
)

// Configuration sources (Report.Source).
const (
	SourceLocal  = "local"
	SourceRemote = "remote"
)

// Remote is the java_agent section of a sync response: the fleet's settings for this host.
type Remote struct {
	Mode    string `json:"mode"`
	Version string `json:"version"`
	// TargetVersion is the version the host should run ("" = keep what is installed).
	TargetVersion string `json:"target_version"`
	Manifest      string `json:"manifest,omitempty"`
	Signature     string `json:"signature,omitempty"`
	DownloadURL   string `json:"download_url,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// JVM is one running JVM that loads an openlog Java agent (-javaagent: on its command line or in
// JAVA_TOOL_OPTIONS / JDK_JAVA_OPTIONS / _JAVA_OPTIONS).
type JVM struct {
	PID     int    `json:"pid"`
	Name    string `json:"name"`
	Command string `json:"command"`
	// AgentPath is the -javaagent: path as the JVM received it.
	AgentPath string `json:"agent_path"`
	// LoadedVersion is the version the JVM loaded ("" = unknown).
	LoadedVersion string `json:"loaded_version"`
	// Managed: AgentPath is link_path or inside install_root.
	Managed bool `json:"managed"`
	// RestartPending: managed, and it runs another version than the current one.
	RestartPending bool   `json:"restart_pending"`
	StartedAt      string `json:"started_at,omitempty"`
	// Container: the process runs in a container (its paths are not the host's; never managed).
	Container bool `json:"container,omitempty"`
}

// Report is the java_agent section of a sync request.
type Report struct {
	Mode   string `json:"mode"`
	Source string `json:"source"`
	// Capable is false when this start cannot run the privileged step (legacy unit, container, dev build, no keys).
	Capable bool   `json:"capable"`
	Reason  string `json:"reason,omitempty"`
	// Managed: the install root carries the infra agent's marker.
	Managed        bool   `json:"managed"`
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
	Status         string `json:"status"`
	// Detail explains Status (guidance for unmanaged, the error, the applications to restart).
	Detail    string        `json:"detail,omitempty"`
	LinkPath  string        `json:"link_path"`
	LinkState string        `json:"link_state"`
	JVMs      []JVM         `json:"jvms"`
	Update    *UpdateReport `json:"update,omitempty"`
}

// UpdateReport is the last installation, upgrade, rollback, link switch or removal.
type UpdateReport struct {
	Operation string `json:"operation"`
	Version   string `json:"version"`
	State     string `json:"state"`
	Error     string `json:"error"`
	ChangedAt string `json:"changed_at"`
}

// Request is <state_dir>/java-agent/request.json, written by the agent for the privileged step (untrusted).
type Request struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Version string `json:"version"`
	// InUse are versions running JVMs still use: pruning keeps them, uninstall is refused.
	InUse []string  `json:"in_use"`
	At    time.Time `json:"at"`
}

// Switch records when current or link_path started pointing at a version.
type Switch struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// Status is <infra install root>/java-agent-status.json, written by the privileged step (root-owned, 0644).
type Status struct {
	RequestID string    `json:"request_id"`
	At        time.Time `json:"at"`
	Action    string    `json:"action"`
	Operation string    `json:"operation"`
	Result    string    `json:"result"`
	// Target is the requested version; Version is current after the step ("" = not installed); Previous is the
	// version that was current before (the rollback target).
	Target   string `json:"target"`
	Version  string `json:"version"`
	Previous string `json:"previous,omitempty"`
	Error    string `json:"error,omitempty"`
	// LinkState of link_path after the step; LinkVersion is the version it points at (or holds, Windows copy);
	// LinkSHA256 is the digest of a Windows copy (identifies a managed copy).
	LinkState   string `json:"link_state,omitempty"`
	LinkVersion string `json:"link_version,omitempty"`
	LinkSHA256  string `json:"link_sha256,omitempty"`
	// Switches (current) and LinkSwitches (link_path), newest first.
	Switches     []Switch `json:"switches,omitempty"`
	LinkSwitches []Switch `json:"link_switches,omitempty"`
}

func validVersion(s string) bool {
	v, err := lib.ParseVersion(s)
	return err == nil && v.String() == s
}

func pushSwitch(h []Switch, v string, at time.Time) []Switch {
	out := append([]Switch{{Version: v, At: at}}, h...)
	if len(out) > maxHistory {
		out = out[:maxHistory]
	}
	return out
}
