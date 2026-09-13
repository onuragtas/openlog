package fleet

import (
	"hash/fnv"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/catalog"
	lib "github.com/onuragtas/openlog/libs/release"
)

// Update states reported by agents (contract §3).
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

// InProgress reports whether an agent is in the middle of applying an update.
func InProgress(state string) bool {
	switch state {
	case StateDownloading, StateVerifying, StateStaged, StateRestarting, StateConfirming:
		return true
	}
	return false
}

// Update actions.
const (
	ActionUpgrade  = "upgrade"
	ActionRollback = "rollback"
)

// Host override actions.
const (
	OverrideHold = "hold"
	OverridePin  = "pin"
)

// Rollout states.
const (
	RolloutActive     = "active"
	RolloutPaused     = "paused"
	RolloutHalted     = "halted"
	RolloutCompleted  = "completed"
	RolloutSuperseded = "superseded"
)

// HostReport is what an agent reported in its last sync.
type HostReport struct {
	HostID          string
	HostName        string
	AgentName       string
	Version         string
	Commit          string
	OS              string
	Arch            string
	InstallMethod   string
	UpdateCapable   bool
	UpdateState     string
	UpdateFrom      string
	UpdateTo        string
	UpdateError     string
	UpdateChangedAt time.Time
	ConfigHash      string
}

// Override is a per-host exception to the policy.
type Override struct {
	HostID    string
	Action    string // hold | pin
	Version   string // pin only
	UpdatedAt time.Time
}

// Counters are a rollout's host counts, refreshed by the controller.
type Counters struct {
	Pending    int `json:"pending"`
	Attempted  int `json:"attempted"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	RolledBack int `json:"rolled_back"`
}

// Rollout is a staged fleet update.
type Rollout struct {
	ID              string
	OrgID           string
	Action          string
	FromVersion     string            // "" = unknown
	ToVersion       string            // "" for target=patch rollouts
	Targets         map[string]string // patch rollouts: "major.minor" → version
	Waves           []int
	CurrentWave     int
	WaveStartedAt   time.Time
	WaveSoakMinutes int
	HaltFailureRate float64
	State           string
	StateReason     string
	Counters        Counters
	AckAttempted    int
	AckFailed       int
	CreatedBy       string // user id; "" = controller
	CreatedByEmail  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	EndedAt         *time.Time
}

// Open reports whether the rollout can still change (active, paused or halted).
func (r *Rollout) Open() bool {
	return r != nil && (r.State == RolloutActive || r.State == RolloutPaused || r.State == RolloutHalted)
}

// WavePercent is the share of hosts eligible in the current wave.
func (r *Rollout) WavePercent() int {
	if r == nil || len(r.Waves) == 0 {
		return 0
	}
	if r.State == RolloutCompleted {
		return 100
	}
	i := min(max(r.CurrentWave, 0), len(r.Waves)-1)
	return r.Waves[i]
}

// TargetFor returns the rollout's target version for a host running cur.
func (r *Rollout) TargetFor(cur lib.Version) (string, bool) {
	if r.ToVersion != "" {
		return r.ToVersion, true
	}
	t, ok := r.Targets[MinorKey(cur)]
	return t, ok
}

// MinorKey is "major.minor" of v.
func MinorKey(v lib.Version) string {
	return strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10)
}

// Bucket is the host's wave bucket for a rollout: fnv32a(host_id + ":" + rollout_id) % 100.
func Bucket(hostID, rolloutID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(hostID + ":" + rolloutID))
	return int(h.Sum32() % 100)
}

// InWave reports whether the host is eligible in the rollout's current wave.
func InWave(hostID string, r *Rollout) bool {
	return Bucket(hostID, r.ID) < r.WavePercent()
}

// DesiredTarget is the release the policy wants for a host running cur (target=patch uses cur's
// minor). It does not compare with cur.
func DesiredTarget(p Policy, snap *catalog.Snapshot, cur lib.Version) (*catalog.Release, bool) {
	switch p.Target {
	case TargetLatest:
		return snap.Latest(p.Channel)
	case TargetPatch:
		return snap.LatestPatch(p.Channel, cur.Major, cur.Minor)
	case TargetPinned:
		if p.PinnedVersion == nil {
			return nil, false
		}
		return snap.Release(*p.PinnedVersion)
	}
	return nil, false
}

// Reason explains a decision.
type Reason string

// Decision reasons. Only ReasonOffer carries an update.
const (
	ReasonOffer             Reason = "offer"
	ReasonNoCatalog         Reason = "no_catalog"
	ReasonModeOff           Reason = "mode_off"
	ReasonNotifyOnly        Reason = "notify_only"
	ReasonInvalidVersion    Reason = "invalid_version"
	ReasonHold              Reason = "hold"
	ReasonNotCapable        Reason = "not_capable"
	ReasonNoRollout         Reason = "no_rollout"
	ReasonRolloutPaused     Reason = "rollout_paused"
	ReasonRolloutHalted     Reason = "rollout_halted"
	ReasonRolloutOutdated   Reason = "rollout_outdated"
	ReasonNotInRollout      Reason = "not_in_rollout"
	ReasonUpToDate          Reason = "up_to_date"
	ReasonNotInWave         Reason = "not_in_wave"
	ReasonTargetUnavailable Reason = "target_unavailable"
	ReasonIncompatible      Reason = "incompatible"
	ReasonAlreadyFailed     Reason = "already_failed"
	ReasonNoArtifact        Reason = "no_artifact"
	ReasonOutsideWindow     Reason = "outside_window"
)

// Input is everything a decision depends on.
type Input struct {
	Now      time.Time
	Host     HostReport
	Policy   Policy
	Override *Override
	Rollout  *Rollout // the organization's current rollout (newest not superseded), or nil
	Catalog  *catalog.Snapshot

	// Used by the controller to find hosts that will eventually receive the update.
	IgnoreWave   bool
	IgnoreWindow bool
	// IgnorePolicyTarget evaluates an upgrade rollout against its own target even when the policy
	// now wants another one (the controller supersedes such rollouts instead of completing them).
	IgnorePolicyTarget bool
}

// Decision is the result of Decide.
type Decision struct {
	Reason    Reason
	Action    string
	Target    string           // final target version
	Release   *catalog.Release // offered release: Target, or an intermediate version (min_upgrade_from)
	Artifact  lib.Artifact
	RolloutID string
	Deadline  time.Time // end of the maintenance window; zero without windows
}

// Offer reports whether the decision carries an update.
func (d Decision) Offer() bool { return d.Reason == ReasonOffer }

// Decide selects the update for one host. It is pure: all state is in the input.
func Decide(in Input) Decision {
	var d Decision
	stop := func(r Reason) Decision { d.Reason = r; return d }

	switch in.Policy.Mode {
	case ModeAuto:
	case ModeNotify:
		return stop(ReasonNotifyOnly)
	default:
		return stop(ReasonModeOff)
	}
	cur, err := lib.ParseVersion(in.Host.Version)
	if err != nil {
		return stop(ReasonInvalidVersion)
	}
	ov := in.Override
	if ov != nil && ov.Action == OverrideHold {
		return stop(ReasonHold)
	}
	if !in.Host.UpdateCapable {
		return stop(ReasonNotCapable)
	}
	if in.Catalog.Len() == 0 {
		return stop(ReasonNoCatalog)
	}

	var (
		target lib.Version
		since  time.Time // failures reported before this belong to an earlier attempt
	)
	if ov != nil && ov.Action == OverridePin {
		pv, err := lib.ParseVersion(ov.Version)
		if err != nil {
			return stop(ReasonTargetUnavailable)
		}
		switch c := lib.Compare(pv, cur); {
		case c == 0:
			return stop(ReasonUpToDate)
		case c > 0:
			d.Action = ActionUpgrade
		default:
			d.Action = ActionRollback
		}
		target, since = pv, ov.UpdatedAt
	} else {
		r := in.Rollout
		if r == nil {
			return stop(ReasonNoRollout)
		}
		switch r.State {
		case RolloutActive, RolloutCompleted:
		case RolloutPaused:
			return stop(ReasonRolloutPaused)
		case RolloutHalted:
			return stop(ReasonRolloutHalted)
		default:
			return stop(ReasonNoRollout)
		}
		d.RolloutID = r.ID
		ts, ok := r.TargetFor(cur)
		if !ok {
			return stop(ReasonNotInRollout)
		}
		tv, err := lib.ParseVersion(ts)
		if err != nil {
			return stop(ReasonTargetUnavailable)
		}
		if r.Action == ActionRollback {
			if lib.Compare(cur, tv) <= 0 {
				return stop(ReasonUpToDate)
			}
			d.Action = ActionRollback
		} else {
			if lib.Compare(cur, tv) >= 0 {
				return stop(ReasonUpToDate)
			}
			// The policy changed since the rollout was created; the controller replaces it.
			if want, ok := DesiredTarget(in.Policy, in.Catalog, cur); !in.IgnorePolicyTarget && (!ok || lib.Compare(want.Version, tv) != 0) {
				return stop(ReasonRolloutOutdated)
			}
			d.Action = ActionUpgrade
		}
		d.Target = tv.String()
		if r.State == RolloutActive && !in.IgnoreWave && !InWave(in.Host.HostID, r) {
			return stop(ReasonNotInWave)
		}
		target, since = tv, r.CreatedAt
	}
	d.Target = target.String()

	rel, ok := in.Catalog.Release(d.Target)
	if !ok {
		return stop(ReasonTargetUnavailable)
	}
	step := rel
	if d.Action == ActionUpgrade {
		if !upgradableFrom(rel, cur) {
			// Upgrade through the newest intermediate release that accepts cur.
			step = nil
			for _, c := range in.Catalog.Releases() {
				if lib.Compare(c.Version, cur) <= 0 || lib.Compare(c.Version, target) >= 0 {
					continue
				}
				if !c.VisibleOn(in.Policy.Channel) || !upgradableFrom(c, cur) {
					continue
				}
				if _, ok := agentArtifact(c, in.Host); !ok {
					continue
				}
				step = c
				break
			}
			if step == nil {
				return stop(ReasonIncompatible)
			}
		}
	} else {
		// The agent checks the target against the rollback floor of its running version.
		running, ok := in.Catalog.Release(cur.String())
		if !ok {
			return stop(ReasonIncompatible)
		}
		if floor := running.Manifest.Compatibility.RollbackFloor; floor != "" {
			if fv, err := lib.ParseVersion(floor); err == nil && lib.Compare(target, fv) < 0 {
				return stop(ReasonIncompatible)
			}
		}
	}

	if in.Host.UpdateState == StateFailed || in.Host.UpdateState == StateRolledBack {
		if to, err := lib.ParseVersion(in.Host.UpdateTo); err == nil &&
			(lib.Compare(to, step.Version) == 0 || lib.Compare(to, target) == 0) &&
			!in.Host.UpdateChangedAt.Before(since) {
			return stop(ReasonAlreadyFailed)
		}
	}

	art, ok := agentArtifact(step, in.Host)
	if !ok {
		return stop(ReasonNoArtifact)
	}
	if !in.IgnoreWindow {
		open, end := in.Policy.InWindow(in.Now)
		if !open {
			return stop(ReasonOutsideWindow)
		}
		d.Deadline = end
	}
	d.Release, d.Artifact = step, art
	return stop(ReasonOffer)
}

func upgradableFrom(r *catalog.Release, cur lib.Version) bool {
	min := r.Manifest.Compatibility.MinUpgradeFrom
	if min == "" {
		return true
	}
	mv, err := lib.ParseVersion(min)
	return err == nil && lib.Compare(cur, mv) >= 0
}

func agentArtifact(r *catalog.Release, h HostReport) (lib.Artifact, bool) {
	return r.Manifest.Artifact(lib.ComponentInfraAgent, h.OS, h.Arch, lib.FormatTarGz)
}
