// Package ebpfprofiler keeps the whole-host CPU profiler current through the infra agent
// (docs/contracts/ebpf-profiler.md, D-149). It follows the Java agent installation (internal/javaagent, D-123):
// the agent decides from its configuration or the fleet's settings, downloads and verifies the signed release
// into state_dir and writes a request; the privileged step ("-apply" of the next start) verifies everything
// again, installs root-owned into install_root/versions/<v>/ and switches current to it.
//
// It never touches an installation it does not own. install.sh installs the .deb or .rpm by default, and a root
// without this agent's marker file belongs to the package manager or to a person; the agent reports it and
// leaves it alone.
package ebpfprofiler

import (
	"fmt"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

const (
	// BinaryRel is the profiler inside a version directory (the release tarball puts it at the top).
	BinaryRel = "openlog-ebpf-profiler"
	// UnitName is the systemd unit the privileged step enables.
	UnitName = "openlog-ebpf-profiler.service"
	// UnitRel is the unit file inside a version directory.
	UnitRel = "packaging/systemd/openlog-ebpf-profiler.service"
	// MarkerFile in the install root marks an installation made by the infra agent; roots without it (packages,
	// manual installs) are never changed.
	MarkerFile = ".managed-by-openlog-infra-agent"
	// StatusFile is the root-owned result of the privileged step in the infra agent's install root.
	StatusFile = "ebpf-profiler-status.json"

	// StateSubdir below state_dir holds the agent's files (all untrusted for the privileged step).
	StateSubdir   = "ebpf-profiler"
	RequestFile   = "request.json"
	StateFile     = "state.json"
	RemoteFile    = "remote.json"
	StagedDir     = "staged"
	ArchiveFile   = "archive.tar.gz"
	ManifestFile  = "manifest.json"
	SignatureFile = "manifest.json.sig"

	maxManifestBytes  = 1 << 20
	maxSignatureBytes = 64 << 10
	maxRequestBytes   = 64 << 10
	maxStatusBytes    = 1 << 20

	// FailedRetryDelay: a failed installation of a version is retried after this; a rolled back one never.
	FailedRetryDelay = time.Hour
)

// Request actions (Request.Action).
const (
	ActionInstall   = "install"
	ActionRollback  = "rollback"
	ActionUninstall = "uninstall"
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

// States reported in sync (State.Phase).
const (
	StateDownloading = "downloading"
	StateRestarting  = "restarting"
	StateConfirming  = "confirming"
	StateApplied     = "applied"
	StateFailed      = "failed"
	StateRolledBack  = "rolled_back"
	StateUninstalled = "uninstalled"
)

// Operations (State.Operation).
const (
	OpInstall   = "install"
	OpUpgrade   = "upgrade"
	OpRollback  = "rollback"
	OpUninstall = "uninstall"
)

// Who owns the install root (ManagedBy).
const (
	ManagedNone    = "none"
	ManagedFleet   = "fleet"
	ManagedPackage = "package"
	ManagedManual  = "manual"
)

// Where the effective settings came from.
const (
	SourceLocal  = "local"
	SourceRemote = "remote"
)

// Request is what the agent leaves for the privileged step.
type Request struct {
	ID      string    `json:"id"`
	Action  string    `json:"action"`
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// Status is the root-owned result of the privileged step.
type Status struct {
	RequestID string    `json:"request_id"`
	At        time.Time `json:"at"`
	Action    string    `json:"action"`
	Operation string    `json:"operation"`
	Result    string    `json:"result"`
	// Target is the requested version; Version is current after the step ("" = not installed); Previous is the
	// version that was current before an installation (the rollback target of a failed health check).
	Target   string `json:"target"`
	Version  string `json:"version"`
	Previous string `json:"previous,omitempty"`
	Error    string `json:"error,omitempty"`
	Unit     string `json:"unit,omitempty"`
}

// State is what the agent reports about itself.
type State struct {
	RequestID string    `json:"request_id,omitempty"`
	Phase     string    `json:"phase"`
	Action    string    `json:"action,omitempty"`
	Operation string    `json:"operation,omitempty"`
	Version   string    `json:"version,omitempty"`
	Error     string    `json:"error,omitempty"`
	ChangedAt time.Time `json:"changed_at"`
	HealthAt  time.Time `json:"health_at,omitempty"`
}

// TopDir is the release tarball's top-level directory (the Makefile stages it under this name).
func TopDir(version, osName, arch string) string {
	return fmt.Sprintf("openlog-ebpf-profiler_%s_%s_%s", version, osName, arch)
}

func validVersion(s string) bool {
	v, err := lib.ParseVersion(s)
	return err == nil && v.String() == s
}
