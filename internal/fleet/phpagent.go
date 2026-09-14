package fleet

// PHP agent installation through the infra agent (docs/contracts/php-agent.md §7.3, releases-updates.md §3–§4
// "PHP agent", D-083): the policy's php_agent section, per-host overrides, the agent's PHP runtime inventory and the
// pure decision served in the sync response.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/catalog"
	lib "github.com/onuragtas/openlog/libs/release"
)

// PHP agent modes, versions and reload settings.
const (
	PHPModeOff    = "off"
	PHPModeManual = "manual"
	PHPModeAuto   = "auto"

	PHPVersionAgent = "agent"

	PHPReloadNone     = "none"
	PHPReloadGraceful = "graceful"
)

// PHPAgentPolicy is the php_agent section of the update policy.
type PHPAgentPolicy struct {
	// Mode: off (fleet installations are removed), manual (inventory only; default), auto (install where a
	// supported PHP runtime is found).
	Mode string `json:"mode"`
	// Version: agent (the host's infra agent version; default) or a SemVer version.
	Version     string   `json:"version"`
	Reload      string   `json:"reload"`
	ExcludeBins []string `json:"exclude_bins"`
	// ChangedAt is when mode, version, reload or exclude_bins last changed (set by the server). PHP agent waves start
	// then, or when the target release was published if that is later.
	ChangedAt *time.Time `json:"changed_at"`
}

// DefaultPHPAgentPolicy is the section of organizations that never changed it.
func DefaultPHPAgentPolicy() PHPAgentPolicy {
	return PHPAgentPolicy{Mode: PHPModeManual, Version: PHPVersionAgent, Reload: PHPReloadNone, ExcludeBins: []string{}}
}

// Normalize validates the section and returns it in canonical form.
func (p PHPAgentPolicy) Normalize() (PHPAgentPolicy, error) {
	switch p.Mode {
	case PHPModeOff, PHPModeManual, PHPModeAuto:
	default:
		return p, invalidf("php_agent.mode must be off, manual or auto")
	}
	p.Version = strings.TrimSpace(p.Version)
	if p.Version == "" {
		p.Version = PHPVersionAgent
	}
	if p.Version != PHPVersionAgent {
		v, err := lib.ParseVersion(p.Version)
		if err != nil {
			return p, invalidf("php_agent.version must be agent or a SemVer version: %v", err)
		}
		p.Version = v.String()
	}
	switch p.Reload {
	case "":
		p.Reload = PHPReloadNone
	case PHPReloadNone, PHPReloadGraceful:
	default:
		return p, invalidf("php_agent.reload must be none or graceful")
	}
	if len(p.ExcludeBins) > 50 {
		return p, invalidf("php_agent.exclude_bins: at most 50 globs")
	}
	bins := make([]string, 0, len(p.ExcludeBins))
	for _, g := range p.ExcludeBins {
		g = strings.TrimSpace(g)
		if g == "" || len(g) > 512 {
			return p, invalidf("php_agent.exclude_bins: globs must be 1 to 512 bytes")
		}
		if _, err := filepath.Match(g, ""); err != nil {
			return p, invalidf("php_agent.exclude_bins: invalid glob %q", g)
		}
		bins = append(bins, g)
	}
	p.ExcludeBins = bins
	return p, nil
}

func (p PHPAgentPolicy) sameSettings(o PHPAgentPolicy) bool {
	if p.Mode != o.Mode || p.Version != o.Version || p.Reload != o.Reload || len(p.ExcludeBins) != len(o.ExcludeBins) {
		return false
	}
	for i := range p.ExcludeBins {
		if p.ExcludeBins[i] != o.ExcludeBins[i] {
			return false
		}
	}
	return true
}

// PHPOverride sets the PHP agent mode of one host.
type PHPOverride struct {
	HostID    string
	Mode      string
	UpdatedAt time.Time
}

// PHPRuntime is one PHP binary of a host (openlog-php-install status --json).
type PHPRuntime struct {
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
	Excluded  bool   `json:"excluded"`
}

// PHPAgentUpdate is the host's last PHP agent operation.
type PHPAgentUpdate struct {
	Operation string `json:"operation"`
	Version   string `json:"version"`
	State     string `json:"state"`
	Error     string `json:"error"`
	ChangedAt string `json:"changed_at"`
}

// PHPAgentReport is the php_agent section of a sync request.
type PHPAgentReport struct {
	Mode      string          `json:"mode"`
	Source    string          `json:"source"`
	Capable   bool            `json:"capable"`
	Reason    string          `json:"reason"`
	ManagedBy string          `json:"managed_by"`
	Version   string          `json:"version"`
	Runtimes  []PHPRuntime    `json:"runtimes"`
	Update    *PHPAgentUpdate `json:"update"`
}

// PHP agent update states reported by agents.
const (
	PHPStateApplied    = "applied"
	PHPStateFailed     = "failed"
	PHPStateRolledBack = "rolled_back"
)

const maxPHPRuntimes = 64

// ParsePHPAgentReport decodes and bounds an agent's php_agent section (nil when absent or invalid).
func ParsePHPAgentReport(raw []byte) *PHPAgentReport {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var r PHPAgentReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	r.Mode, r.Source, r.Reason = clip(r.Mode, 16), clip(r.Source, 16), clip(r.Reason, 512)
	r.ManagedBy, r.Version = clip(r.ManagedBy, 16), clip(r.Version, 128)
	if len(r.Runtimes) > maxPHPRuntimes {
		r.Runtimes = r.Runtimes[:maxPHPRuntimes]
	}
	if r.Runtimes == nil {
		r.Runtimes = []PHPRuntime{}
	}
	for i := range r.Runtimes {
		rt := &r.Runtimes[i]
		rt.Bin, rt.Version, rt.API, rt.Libc = clip(rt.Bin, 512), clip(rt.Version, 64), clip(rt.API, 16), clip(rt.Libc, 16)
		rt.ScanDir, rt.Module = clip(rt.ScanDir, 512), clip(rt.Module, 64)
	}
	if u := r.Update; u != nil {
		u.Operation, u.Version, u.State = clip(u.Operation, 16), clip(u.Version, 128), clip(u.State, 32)
		u.Error, u.ChangedAt = clip(u.Error, 2048), clip(u.ChangedAt, 64)
	}
	return &r
}

// PHPReason explains a PHP agent decision.
type PHPReason string

// PHP agent decision reasons. ReasonPHPOffer and ReasonPHPUpToDate carry the target version.
const (
	ReasonPHPOffer             PHPReason = "offer"
	ReasonPHPUpToDate          PHPReason = "up_to_date"
	ReasonPHPModeOff           PHPReason = "mode_off"
	ReasonPHPManual            PHPReason = "manual"
	ReasonPHPNotReported       PHPReason = "not_reported"
	ReasonPHPNotCapable        PHPReason = "not_capable"
	ReasonPHPManagedElsewhere  PHPReason = "managed_elsewhere"
	ReasonPHPNoRuntime         PHPReason = "no_php"
	ReasonPHPInvalidVersion    PHPReason = "invalid_version"
	ReasonPHPNoCatalog         PHPReason = "no_catalog"
	ReasonPHPTargetUnavailable PHPReason = "target_unavailable"
	ReasonPHPNoArtifact        PHPReason = "no_artifact"
	ReasonPHPAlreadyFailed     PHPReason = "already_failed"
	ReasonPHPNotInWave         PHPReason = "not_in_wave"
	ReasonPHPOutsideWindow     PHPReason = "outside_window"
)

// PHPInput is everything a PHP agent decision depends on.
type PHPInput struct {
	Now      time.Time
	Host     HostReport
	Policy   Policy
	Override *PHPOverride
	Catalog  *catalog.Snapshot
}

// PHPDecision is the result of DecidePHP.
type PHPDecision struct {
	Reason   PHPReason
	Mode     string // effective mode (override or policy)
	Target   string // target version ("" when it cannot be determined)
	Release  *catalog.Release
	Artifact lib.Artifact
}

// Offer reports whether the host is told to install Target now.
func (d PHPDecision) Offer() bool { return d.Reason == ReasonPHPOffer }

// DecidePHP selects the PHP agent version a host should run. It is pure: all state is in the input. Waves: a host is
// eligible when fnv32a(host_id + ":php-agent:" + target) % 100 is below the policy wave reached after
// wave_soak_minutes per wave since the start (the later of php_agent.changed_at and the release time); a per-host
// override skips waves. Maintenance windows apply.
func DecidePHP(in PHPInput) PHPDecision {
	p := in.Policy.PHPAgent
	d := PHPDecision{Mode: p.Mode}
	stop := func(r PHPReason) PHPDecision { d.Reason = r; return d }
	if in.Override != nil {
		d.Mode = in.Override.Mode
	}
	switch d.Mode {
	case PHPModeOff:
		return stop(ReasonPHPModeOff)
	case PHPModeAuto:
	default:
		return stop(ReasonPHPManual)
	}
	target := p.Version
	if target == PHPVersionAgent || target == "" {
		v, err := lib.ParseVersion(in.Host.Version)
		if err != nil || v.String() != strings.TrimPrefix(in.Host.Version, "v") {
			return stop(ReasonPHPInvalidVersion)
		}
		target = v.String()
	}
	d.Target = target
	rep := in.Host.PHPAgent
	switch {
	case rep == nil:
		return stop(ReasonPHPNotReported)
	case rep.ManagedBy == "package" || rep.ManagedBy == "manual":
		return stop(ReasonPHPManagedElsewhere)
	case !rep.Capable:
		return stop(ReasonPHPNotCapable)
	case rep.Version == target:
		return stop(ReasonPHPUpToDate)
	}
	supported := false
	for _, rt := range rep.Runtimes {
		supported = supported || (rt.Supported && !rt.Excluded)
	}
	if !supported {
		return stop(ReasonPHPNoRuntime)
	}
	if in.Catalog.Len() == 0 {
		return stop(ReasonPHPNoCatalog)
	}
	rel, ok := in.Catalog.Release(target)
	if !ok {
		return stop(ReasonPHPTargetUnavailable)
	}
	art, ok := rel.Manifest.Artifact(lib.ComponentPHPAgent, in.Host.OS, in.Host.Arch, lib.FormatTarGz)
	if !ok {
		return stop(ReasonPHPNoArtifact)
	}
	if u := rep.Update; u != nil && u.Version == target && u.State == PHPStateRolledBack {
		return stop(ReasonPHPAlreadyFailed) // the agent never retries a rolled back version by itself
	}
	if in.Override == nil {
		start := rel.Manifest.ReleasedAt
		if p.ChangedAt != nil && p.ChangedAt.After(start) {
			start = *p.ChangedAt
		}
		if Bucket(in.Host.HostID, "php-agent:"+target) >= PHPWavePercent(in.Policy, start, in.Now) {
			return stop(ReasonPHPNotInWave)
		}
	}
	if open, _ := in.Policy.InWindow(in.Now); !open {
		return stop(ReasonPHPOutsideWindow)
	}
	d.Release, d.Artifact = rel, art
	return stop(ReasonPHPOffer)
}

// PHPWavePercent is the share of hosts eligible for a PHP agent version whose waves started at start: the policy's
// waves advance every wave_soak_minutes (0 = all at once).
func PHPWavePercent(p Policy, start, now time.Time) int {
	if len(p.Waves) == 0 || p.WaveSoakMinutes <= 0 {
		return 100
	}
	i := 0
	if elapsed := now.Sub(start); elapsed > 0 {
		i = int(elapsed / (time.Duration(p.WaveSoakMinutes) * time.Minute))
	}
	return p.Waves[min(i, len(p.Waves)-1)]
}

// PHPAgentJSON is the php_agent section of a sync response.
type PHPAgentJSON struct {
	Mode        string   `json:"mode"`
	Version     string   `json:"version"`
	Reload      string   `json:"reload"`
	ExcludeBins []string `json:"exclude_bins"`
	// TargetVersion: the version to run ("" = keep what is installed). Manifest, Signature and DownloadURL are set
	// when the host should install it now.
	TargetVersion string `json:"target_version"`
	Manifest      string `json:"manifest,omitempty"`
	Signature     string `json:"signature,omitempty"`
	DownloadURL   string `json:"download_url,omitempty"`
	Reason        string `json:"reason"`
}
