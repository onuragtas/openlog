package fleet

// Java agent jar management through the infra agent (docs/contracts/java-agent.md §2, releases-updates.md §3
// "Java agent", D-123): the policy's java_agent section, per-host mode overrides, the agent's JVM inventory and the
// pure decision served in the sync response. It mirrors the PHP agent (phpagent.go).

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/catalog"
	lib "github.com/onuragtas/openlog/libs/release"
)

// Java agent modes and versions (the same values as the PHP agent's).
const (
	JavaModeOff    = "off"
	JavaModeManual = "manual"
	JavaModeAuto   = "auto"

	JavaVersionAgent = "agent"
)

// JavaAgentPolicy is the java_agent section of the update policy.
type JavaAgentPolicy struct {
	// Mode: off (fleet installations are removed), manual (JVM inventory only; default), auto (install and upgrade).
	Mode string `json:"mode"`
	// Version: agent (the host's infra agent version; default) or a SemVer version.
	Version string `json:"version"`
	// ChangedAt is when mode or version last changed (set by the server); Java agent waves start then, or when the
	// target release was published if that is later.
	ChangedAt *time.Time `json:"changed_at"`
}

// DefaultJavaAgentPolicy is the section of organizations that never changed it.
func DefaultJavaAgentPolicy() JavaAgentPolicy {
	return JavaAgentPolicy{Mode: JavaModeManual, Version: JavaVersionAgent}
}

// Normalize validates the section and returns it in canonical form.
func (p JavaAgentPolicy) Normalize() (JavaAgentPolicy, error) {
	switch p.Mode {
	case JavaModeOff, JavaModeManual, JavaModeAuto:
	default:
		return p, invalidf("java_agent.mode must be off, manual or auto")
	}
	p.Version = strings.TrimSpace(p.Version)
	if p.Version == "" {
		p.Version = JavaVersionAgent
	}
	if p.Version != JavaVersionAgent {
		v, err := lib.ParseVersion(p.Version)
		if err != nil {
			return p, invalidf("java_agent.version must be agent or a SemVer version: %v", err)
		}
		p.Version = v.String()
	}
	return p, nil
}

func (p JavaAgentPolicy) sameSettings(o JavaAgentPolicy) bool {
	return p.Mode == o.Mode && p.Version == o.Version
}

// JavaOverride sets the Java agent mode of one host.
type JavaOverride struct {
	HostID    string
	Mode      string
	UpdatedAt time.Time
}

// JavaJVM is one JVM of a host that loads an openlog Java agent.
type JavaJVM struct {
	PID            int    `json:"pid"`
	Name           string `json:"name"`
	Command        string `json:"command"`
	AgentPath      string `json:"agent_path"`
	LoadedVersion  string `json:"loaded_version"`
	Managed        bool   `json:"managed"`
	RestartPending bool   `json:"restart_pending"`
	StartedAt      string `json:"started_at"`
	Container      bool   `json:"container"`
}

// JavaAgentUpdate is the host's last Java agent operation.
type JavaAgentUpdate struct {
	Operation string `json:"operation"`
	Version   string `json:"version"`
	State     string `json:"state"`
	Error     string `json:"error"`
	ChangedAt string `json:"changed_at"`
}

// JavaAgentReport is the java_agent section of a sync request.
type JavaAgentReport struct {
	Mode           string           `json:"mode"`
	Source         string           `json:"source"`
	Capable        bool             `json:"capable"`
	Reason         string           `json:"reason"`
	Managed        bool             `json:"managed"`
	CurrentVersion string           `json:"current_version"`
	TargetVersion  string           `json:"target_version"`
	Status         string           `json:"status"`
	Detail         string           `json:"detail"`
	LinkPath       string           `json:"link_path"`
	LinkState      string           `json:"link_state"`
	JVMs           []JavaJVM        `json:"jvms"`
	Update         *JavaAgentUpdate `json:"update"`
}

// Java agent update states reported by agents.
const (
	JavaStateRolledBack = "rolled_back"
)

const maxJavaJVMs = 64

// ParseJavaAgentReport decodes and bounds an agent's java_agent section (nil when absent or invalid).
func ParseJavaAgentReport(raw []byte) *JavaAgentReport {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var r JavaAgentReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	r.Mode, r.Source, r.Reason = clip(r.Mode, 16), clip(r.Source, 16), clip(r.Reason, 512)
	r.CurrentVersion, r.TargetVersion, r.Status = clip(r.CurrentVersion, 128), clip(r.TargetVersion, 128), clip(r.Status, 32)
	r.Detail, r.LinkPath, r.LinkState = clip(r.Detail, 1024), clip(r.LinkPath, 512), clip(r.LinkState, 16)
	if len(r.JVMs) > maxJavaJVMs {
		r.JVMs = r.JVMs[:maxJavaJVMs]
	}
	if r.JVMs == nil {
		r.JVMs = []JavaJVM{}
	}
	for i := range r.JVMs {
		j := &r.JVMs[i]
		j.Name, j.Command, j.AgentPath = clip(j.Name, 128), clip(j.Command, 512), clip(j.AgentPath, 512)
		j.LoadedVersion, j.StartedAt = clip(j.LoadedVersion, 128), clip(j.StartedAt, 64)
	}
	if u := r.Update; u != nil {
		u.Operation, u.Version, u.State = clip(u.Operation, 16), clip(u.Version, 128), clip(u.State, 32)
		u.Error, u.ChangedAt = clip(u.Error, 2048), clip(u.ChangedAt, 64)
	}
	return &r
}

// JavaReason explains a Java agent decision.
type JavaReason string

// Java agent decision reasons. ReasonJavaOffer and ReasonJavaUpToDate carry the target version.
const (
	ReasonJavaOffer             JavaReason = "offer"
	ReasonJavaUpToDate          JavaReason = "up_to_date"
	ReasonJavaModeOff           JavaReason = "mode_off"
	ReasonJavaManual            JavaReason = "manual"
	ReasonJavaNotReported       JavaReason = "not_reported"
	ReasonJavaNotCapable        JavaReason = "not_capable"
	ReasonJavaInvalidVersion    JavaReason = "invalid_version"
	ReasonJavaNoCatalog         JavaReason = "no_catalog"
	ReasonJavaTargetUnavailable JavaReason = "target_unavailable"
	ReasonJavaNoArtifact        JavaReason = "no_artifact"
	ReasonJavaAlreadyFailed     JavaReason = "already_failed"
	ReasonJavaNotInWave         JavaReason = "not_in_wave"
	ReasonJavaOutsideWindow     JavaReason = "outside_window"
)

// JavaInput is everything a Java agent decision depends on.
type JavaInput struct {
	Now      time.Time
	Host     HostReport
	Policy   Policy
	Override *JavaOverride
	Catalog  *catalog.Snapshot
}

// JavaDecision is the result of DecideJava.
type JavaDecision struct {
	Reason   JavaReason
	Mode     string
	Target   string
	Release  *catalog.Release
	Artifact lib.Artifact
}

// Offer reports whether the host is told to install Target now.
func (d JavaDecision) Offer() bool { return d.Reason == ReasonJavaOffer }

// DecideJava selects the Java agent version a host should run. It is pure. Waves: a host is eligible when
// fnv32a(host_id + ":java-agent:" + target) % 100 is below the policy wave reached after wave_soak_minutes per wave
// since the later of java_agent.changed_at and the release time; a per-host override skips waves. Maintenance windows
// apply. A host whose link_path is a user's own file still gets the managed installation (the report says unmanaged).
func DecideJava(in JavaInput) JavaDecision {
	p := in.Policy.JavaAgent
	if p.Mode == "" {
		p = DefaultJavaAgentPolicy()
	}
	d := JavaDecision{Mode: p.Mode}
	stop := func(r JavaReason) JavaDecision { d.Reason = r; return d }
	if in.Override != nil {
		d.Mode = in.Override.Mode
	}
	switch d.Mode {
	case JavaModeOff:
		return stop(ReasonJavaModeOff)
	case JavaModeAuto:
	default:
		return stop(ReasonJavaManual)
	}
	target := p.Version
	if target == JavaVersionAgent || target == "" {
		v, err := lib.ParseVersion(in.Host.Version)
		if err != nil || v.String() != strings.TrimPrefix(in.Host.Version, "v") {
			return stop(ReasonJavaInvalidVersion)
		}
		target = v.String()
	}
	d.Target = target
	rep := in.Host.JavaAgent
	switch {
	case rep == nil:
		return stop(ReasonJavaNotReported)
	case !rep.Capable:
		return stop(ReasonJavaNotCapable)
	case rep.CurrentVersion == target:
		return stop(ReasonJavaUpToDate)
	}
	if in.Catalog.Len() == 0 {
		return stop(ReasonJavaNoCatalog)
	}
	rel, ok := in.Catalog.Release(target)
	if !ok {
		return stop(ReasonJavaTargetUnavailable)
	}
	art, ok := rel.Manifest.Artifact(lib.ComponentJavaAgent, lib.PlatformAny, lib.PlatformAny, lib.FormatJar)
	if !ok {
		return stop(ReasonJavaNoArtifact)
	}
	if u := rep.Update; u != nil && u.Version == target && u.State == JavaStateRolledBack {
		return stop(ReasonJavaAlreadyFailed)
	}
	if in.Override == nil {
		start := rel.Manifest.ReleasedAt
		if p.ChangedAt != nil && p.ChangedAt.After(start) {
			start = *p.ChangedAt
		}
		if Bucket(in.Host.HostID, "java-agent:"+target) >= PHPWavePercent(in.Policy, start, in.Now) {
			return stop(ReasonJavaNotInWave)
		}
	}
	if open, _ := in.Policy.InWindow(in.Now); !open {
		return stop(ReasonJavaOutsideWindow)
	}
	d.Release, d.Artifact = rel, art
	return stop(ReasonJavaOffer)
}

// JavaAgentJSON is the java_agent section of a sync response.
type JavaAgentJSON struct {
	Mode    string `json:"mode"`
	Version string `json:"version"`
	// TargetVersion: the version to run ("" = keep what is installed). Manifest, Signature and DownloadURL are set
	// when the host should install it now.
	TargetVersion string `json:"target_version"`
	Manifest      string `json:"manifest,omitempty"`
	Signature     string `json:"signature,omitempty"`
	DownloadURL   string `json:"download_url,omitempty"`
	Reason        string `json:"reason"`
}
