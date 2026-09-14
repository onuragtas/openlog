// Package phpagent installs, upgrades and removes the openlog PHP agent (openlog.so + openlog-php-install) through
// the infra agent (docs/contracts/php-agent.md §7.3, D-082, D-083).
//
// The unprivileged agent keeps an inventory of the host's PHP runtimes, decides from its configuration (or the
// fleet's settings received through sync) whether the PHP agent should be installed, upgraded or removed, downloads
// and verifies the signed release into state_dir and writes a request. It then exits once: the root pre-start step
// "-apply" of the next start verifies everything again, installs into php_agent.install_root, runs
// openlog-php-install and writes a root-owned status. After an installation the agent checks the host's health and,
// on failure, requests a rollback the same way.
package phpagent

import (
	"fmt"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

const (
	// InstallerRel is the installer inside a version directory.
	InstallerRel = "bin/openlog-php-install"
	// MarkerFile in the install root marks an installation made by the infra agent; roots without it (packages,
	// manual installs) are never changed.
	MarkerFile = ".managed-by-openlog-infra-agent"
	// StatusFile is the root-owned result of the privileged step in the infra agent's install root.
	StatusFile = "php-agent-status.json"

	// StateSubdir below state_dir holds the agent's files (all untrusted for the privileged step).
	StateSubdir   = "php-agent"
	RequestFile   = "request.json"
	StateFile     = "state.json"
	RemoteFile    = "remote.json"
	StagedDir     = "staged"
	ArchiveFile   = "archive.tar.gz"
	ManifestFile  = "manifest.json"
	SignatureFile = "manifest.json.sig"
	VersionFile   = "VERSION"

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

// Operations of openlog.agent.php_agent.operations.
const (
	OpInstall   = "install"
	OpUpgrade   = "upgrade"
	OpRollback  = "rollback"
	OpUninstall = "uninstall"
)

// Who manages the install root (Report.ManagedBy).
const (
	ManagedFleet   = "fleet"
	ManagedPackage = "package"
	ManagedManual  = "manual"
	ManagedNone    = "none"
)

// Configuration sources (Report.Source).
const (
	SourceLocal  = "local"
	SourceRemote = "remote"
)

// TopDir is the release tarball's top-level directory (php-agent.md §7.1).
func TopDir(version, os, arch string) string {
	return fmt.Sprintf("openlog-php-agent_%s_%s_%s", version, os, arch)
}

// Runtime is one PHP binary: the fields of `openlog-php-install status --json` plus Excluded.
type Runtime struct {
	Bin       string `json:"bin"`
	Version   string `json:"version"`
	API       string `json:"api"`
	ZTS       bool   `json:"zts"`
	Debug     bool   `json:"debug"`
	Libc      string `json:"libc"`
	ScanDir   string `json:"scan_dir"`
	Module    string `json:"module"`
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	Loaded    bool   `json:"loaded"`
	// Excluded by php_agent.exclude_bins: never enabled.
	Excluded bool `json:"excluded,omitempty"`
}

// Remote is the php_agent section of a sync response: the fleet's settings for this host.
type Remote struct {
	Mode        string   `json:"mode"`
	Version     string   `json:"version"`
	Reload      string   `json:"reload"`
	ExcludeBins []string `json:"exclude_bins"`
	// TargetVersion is the version the host should run now ("" = keep what is installed: manual mode, not in the
	// current wave, outside a maintenance window, no release).
	TargetVersion string `json:"target_version"`
	// Manifest (base64) and Signature of TargetVersion's release; DownloadURL may point to the backend mirror.
	Manifest    string `json:"manifest,omitempty"`
	Signature   string `json:"signature,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	// Reason is the fleet decision (informational).
	Reason string `json:"reason,omitempty"`
}

// Report is the php_agent section of a sync request.
type Report struct {
	Mode   string `json:"mode"`
	Source string `json:"source"`
	// Capable is false when this start has no privileged pre-start step (legacy unit, container, dev build).
	Capable   bool      `json:"capable"`
	Reason    string    `json:"reason,omitempty"`
	ManagedBy string    `json:"managed_by"`
	Version   string    `json:"version"`
	Runtimes  []Runtime `json:"runtimes"`
	// Update is the last installation, upgrade, rollback or removal (absent when there was none).
	Update *UpdateReport `json:"update,omitempty"`
}

// UpdateReport is php_agent_update of the contract.
type UpdateReport struct {
	Operation string `json:"operation"`
	Version   string `json:"version"`
	State     string `json:"state"`
	Error     string `json:"error"`
	ChangedAt string `json:"changed_at"`
}

// Request is <state_dir>/php-agent/request.json, written by the agent for the privileged step (untrusted).
type Request struct {
	ID          string    `json:"id"`
	Action      string    `json:"action"`
	Version     string    `json:"version"`
	Reload      string    `json:"reload"`
	ExcludeBins []string  `json:"exclude_bins"`
	At          time.Time `json:"at"`
}

// Status is <infra install root>/php-agent-status.json, written by the privileged step (root-owned, 0644).
type Status struct {
	RequestID string    `json:"request_id"`
	At        time.Time `json:"at"`
	Action    string    `json:"action"`
	Operation string    `json:"operation"`
	Result    string    `json:"result"`
	// Target is the requested version; Version is current after the step ("" = not installed); Previous is the
	// version that was current before an installation (the rollback target of a failed health check).
	Target   string   `json:"target"`
	Version  string   `json:"version"`
	Previous string   `json:"previous,omitempty"`
	Error    string   `json:"error,omitempty"`
	Runtimes []string `json:"runtimes,omitempty"`
	Units    []string `json:"units,omitempty"`
}

func validVersion(s string) bool {
	v, err := lib.ParseVersion(s)
	return err == nil && v.String() == s
}
