package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/version"
	lib "github.com/onuragtas/openlog/libs/release"
)

// PreconditionError means the operation is not allowed in the current state (409).
type PreconditionError struct{ Msg string }

func (e *PreconditionError) Error() string { return e.Msg }

func preconditionf(format string, args ...any) error {
	return &PreconditionError{Msg: fmt.Sprintf(format, args...)}
}

// Actor is the user performing a management operation (for the audit log).
type Actor struct {
	UserID string
	Email  string
	IP     string
}

// ManagerOptions configure Manager.
type ManagerOptions struct {
	StaleAfter time.Duration // [24h]
	Log        *slog.Logger
	Now        func() time.Time
}

// Manager implements the fleet management API operations.
type Manager struct {
	store Store
	cat   *catalog.Catalog // may be nil
	o     ManagerOptions
}

// NewManager creates a manager. cat may be nil (no catalog configured).
func NewManager(store Store, cat *catalog.Catalog, o ManagerOptions) *Manager {
	if o.StaleAfter <= 0 {
		o.StaleAfter = 24 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Manager{store: store, cat: cat, o: o}
}

func (m *Manager) snapshot() *catalog.Snapshot {
	if m.cat == nil {
		return nil
	}
	return m.cat.Snapshot()
}

// CatalogStatus reports the release catalog status.
func (m *Manager) CatalogStatus() catalog.Status {
	if m.cat == nil {
		return catalog.Status{State: catalog.StateDisabled, Error: "release catalog not configured", Warnings: []string{}}
	}
	return m.cat.Status()
}

func (m *Manager) audit(ctx context.Context, orgID string, a Actor, action, targetType, targetID string, details map[string]any) {
	e := AuditEntry{OrgID: orgID, ActorUserID: a.UserID, ActorEmail: a.Email, Action: action, TargetType: targetType,
		TargetID: targetID, Details: details, IP: a.IP, At: m.o.Now()}
	if err := m.store.AddAudit(context.WithoutCancel(ctx), e); err != nil {
		m.o.Log.Error("cannot write audit log", "action", action, "org_id", orgID, "err", err)
	}
}

// Policy returns the organization's policy (defaults when none is stored).
func (m *Manager) Policy(ctx context.Context, orgID string) (StoredPolicy, error) {
	return m.store.GetPolicy(ctx, orgID)
}

// PutPolicy validates and stores a policy.
func (m *Manager) PutPolicy(ctx context.Context, orgID string, p Policy, a Actor) (StoredPolicy, error) {
	np, err := p.Normalize()
	if err != nil {
		return StoredPolicy{}, err
	}
	if np.Target == TargetPinned {
		if snap := m.snapshot(); snap.Len() > 0 {
			if _, ok := snap.Release(*np.PinnedVersion); !ok {
				return StoredPolicy{}, invalidf("pinned_version %s is not a verified release", *np.PinnedVersion)
			}
		}
	}
	old, err := m.store.GetPolicy(ctx, orgID)
	if err != nil {
		return StoredPolicy{}, err
	}
	if err := m.store.PutPolicy(ctx, orgID, np, a.UserID, m.o.Now()); err != nil {
		return StoredPolicy{}, err
	}
	m.audit(ctx, orgID, a, "fleet.policy.update", "policy", orgID, map[string]any{"from": old.Policy, "to": np})
	return m.store.GetPolicy(ctx, orgID)
}

// ReleaseInfo describes a release for the UI.
type ReleaseInfo struct {
	Version    string    `json:"version"`
	Channel    string    `json:"channel"`
	ReleasedAt time.Time `json:"released_at"`
	NotesURL   string    `json:"notes_url"`
}

func releaseInfo(r *catalog.Release) *ReleaseInfo {
	if r == nil {
		return nil
	}
	return &ReleaseInfo{Version: r.VersionString(), Channel: r.Manifest.Channel, ReleasedAt: r.Manifest.ReleasedAt, NotesURL: r.Manifest.NotesURL}
}

// VersionCount is the number of hosts on one version.
type VersionCount struct {
	Version   string `json:"version"`
	Hosts     int    `json:"hosts"`
	Latest    bool   `json:"latest"`
	Outdated  bool   `json:"outdated"`
	Supported bool   `json:"supported"`
}

// ReasonCount counts hosts per reason.
type ReasonCount struct {
	Reason string `json:"reason"`
	Hosts  int    `json:"hosts"`
}

// Summary is the fleet overview.
type Summary struct {
	TotalHosts       int                     `json:"total_hosts"`
	ActiveHosts      int                     `json:"active_hosts"`
	UpdateCapable    int                     `json:"update_capable"`
	NotUpdateCapable []ReasonCount           `json:"not_update_capable"`
	Outdated         int                     `json:"outdated"`
	Unsupported      int                     `json:"unsupported"`
	InProgress       int                     `json:"in_progress"`
	Failed           int                     `json:"failed"`
	Held             int                     `json:"held"`
	Pinned           int                     `json:"pinned"`
	Versions         []VersionCount          `json:"versions"`
	Latest           map[string]*ReleaseInfo `json:"latest"`
	Target           *ReleaseInfo            `json:"target"`
	OldestSupported  string                  `json:"oldest_supported_version"`
	PolicyMode       Mode                    `json:"policy_mode"`
	UpdateAvailable  bool                    `json:"update_available"`
	StaleAfterSecs   int                     `json:"stale_after_seconds"`
	Catalog          catalog.Status          `json:"catalog"`
	CurrentRollout   *Rollout                `json:"-"`
}

// oldestSupported returns the oldest supported agent per the manifest of this backend's version,
// or of the newest stable release when this version is not in the catalog.
func oldestSupported(snap *catalog.Snapshot) (lib.Version, bool) {
	rel, ok := snap.Release(version.Version)
	if !ok {
		rel, ok = snap.Latest(lib.ChannelStable)
	}
	if !ok || rel.Manifest.Compatibility.OldestSupportedAgent == "" {
		return lib.Version{}, false
	}
	v, err := lib.ParseVersion(rel.Manifest.Compatibility.OldestSupportedAgent)
	return v, err == nil
}

// Summary builds the fleet overview from aggregated host rows.
func (m *Manager) Summary(ctx context.Context, orgID string) (Summary, error) {
	now := m.o.Now()
	sp, err := m.store.GetPolicy(ctx, orgID)
	if err != nil {
		return Summary{}, err
	}
	groups, err := m.store.HostGroups(ctx, orgID, now.Add(-m.o.StaleAfter))
	if err != nil {
		return Summary{}, err
	}
	ovs, err := m.store.ListOverrides(ctx, orgID)
	if err != nil {
		return Summary{}, err
	}
	cur, err := m.store.CurrentRollout(ctx, orgID)
	if err != nil {
		return Summary{}, err
	}
	snap := m.snapshot()
	s := Summary{PolicyMode: sp.Mode, Latest: map[string]*ReleaseInfo{}, Versions: []VersionCount{}, NotUpdateCapable: []ReasonCount{},
		StaleAfterSecs: int(m.o.StaleAfter / time.Second), Catalog: m.CatalogStatus(), CurrentRollout: cur}
	for _, ch := range []string{lib.ChannelStable, lib.ChannelBeta} {
		r, _ := snap.Latest(ch)
		s.Latest[ch] = releaseInfo(r)
	}
	if sp.Target != TargetPatch {
		if r, ok := DesiredTarget(sp.Policy, snap, lib.Version{}); ok {
			s.Target = releaseInfo(r)
		}
	}
	floor, hasFloor := oldestSupported(snap)
	if hasFloor {
		s.OldestSupported = floor.String()
	}
	for _, o := range ovs {
		switch o.Action {
		case OverrideHold:
			s.Held++
		case OverridePin:
			s.Pinned++
		}
	}
	byVersion := map[string]*VersionCount{}
	notCapable := map[string]int{}
	latest := ""
	if s.Latest[sp.Channel] != nil {
		latest = s.Latest[sp.Channel].Version
	}
	for _, g := range groups {
		s.TotalHosts += g.Hosts
		n := g.RecentlySeen
		if n == 0 {
			continue
		}
		s.ActiveHosts += n
		v, verr := lib.ParseVersion(g.Version)
		key := g.Version
		if verr == nil {
			key = v.String()
		}
		vc := byVersion[key]
		if vc == nil {
			vc = &VersionCount{Version: key, Supported: true}
			if verr == nil {
				vc.Latest = key == latest
				if hasFloor && lib.Compare(v, floor) < 0 {
					vc.Supported = false
				}
				if want, ok := DesiredTarget(sp.Policy, snap, v); ok && lib.Compare(v, want.Version) < 0 {
					vc.Outdated = true
				}
			} else {
				vc.Supported = false
			}
			byVersion[key] = vc
		}
		vc.Hosts += n
		if g.UpdateCapable {
			s.UpdateCapable += n
		} else {
			reason := g.InstallMethod
			if reason == "" {
				reason = "unknown"
			}
			notCapable[reason] += n
		}
		if vc.Outdated {
			s.Outdated += n
		}
		if !vc.Supported {
			s.Unsupported += n
		}
		switch {
		case InProgress(g.UpdateState):
			s.InProgress += n
		case g.UpdateState == StateFailed || g.UpdateState == StateRolledBack:
			s.Failed += n
		}
	}
	for _, vc := range byVersion {
		s.Versions = append(s.Versions, *vc)
	}
	sort.Slice(s.Versions, func(i, j int) bool {
		a, errA := lib.ParseVersion(s.Versions[i].Version)
		b, errB := lib.ParseVersion(s.Versions[j].Version)
		if errA != nil || errB != nil {
			return errA == nil
		}
		return lib.Compare(a, b) > 0
	})
	for reason, n := range notCapable {
		s.NotUpdateCapable = append(s.NotUpdateCapable, ReasonCount{Reason: reason, Hosts: n})
	}
	sort.Slice(s.NotUpdateCapable, func(i, j int) bool { return s.NotUpdateCapable[i].Reason < s.NotUpdateCapable[j].Reason })
	s.UpdateAvailable = s.Outdated > 0
	return s, nil
}

// HostView is a host with its override and the current update decision.
type HostView struct {
	Host
	Override  *Override
	Outdated  bool
	Supported bool
	Status    Reason // decision for the host now (offer, not_in_wave, hold, …)
	Target    string // target version of the decision, if any
}

// Hosts lists hosts with filters and pagination.
func (m *Manager) Hosts(ctx context.Context, orgID string, f HostFilter) ([]HostView, string, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = min(max(f.Limit, 100), 1000)
	}
	if f.Version != "" {
		if v, err := lib.ParseVersion(f.Version); err == nil && !strings.Contains(f.Version, "+") {
			f.Version = v.String()
		}
	}
	hosts, next, err := m.store.ListHosts(ctx, orgID, f)
	if err != nil {
		return nil, "", err
	}
	sp, err := m.store.GetPolicy(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	ovs, err := m.store.ListOverrides(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	cur, err := m.store.CurrentRollout(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	snap := m.snapshot()
	floor, hasFloor := oldestSupported(snap)
	now := m.o.Now()
	out := make([]HostView, 0, len(hosts))
	for _, h := range hosts {
		hv := HostView{Host: h, Supported: true}
		in := Input{Now: now, Host: h.HostReport, Policy: sp.Policy, Rollout: cur, Catalog: snap}
		if o, ok := ovs[h.HostID]; ok {
			hv.Override = &o
			in.Override = &o
		}
		if v, err := lib.ParseVersion(h.Version); err == nil {
			if hasFloor && lib.Compare(v, floor) < 0 {
				hv.Supported = false
			}
			if want, ok := DesiredTarget(sp.Policy, snap, v); ok && lib.Compare(v, want.Version) < 0 {
				hv.Outdated = true
			}
		} else {
			hv.Supported = false
		}
		d := Decide(in)
		hv.Status, hv.Target = d.Reason, d.Target
		out = append(out, hv)
	}
	return out, next, nil
}

// Host returns a stored agent host (ErrNotFound when it never synced).
func (m *Manager) Host(ctx context.Context, orgID, hostID string) (Host, error) {
	return m.store.GetHost(ctx, orgID, hostID)
}

// PutOverride sets hold or pin for a known host.
func (m *Manager) PutOverride(ctx context.Context, orgID, hostID, action, ver string, a Actor) (Override, error) {
	o := Override{HostID: hostID, Action: action, UpdatedAt: m.o.Now()}
	switch action {
	case OverrideHold:
	case OverridePin:
		v, err := lib.ParseVersion(strings.TrimSpace(ver))
		if err != nil {
			return Override{}, invalidf("version: %v", err)
		}
		o.Version = v.String()
		if snap := m.snapshot(); snap.Len() > 0 {
			if _, ok := snap.Release(o.Version); !ok {
				return Override{}, invalidf("version %s is not a verified release", o.Version)
			}
		}
	default:
		return Override{}, invalidf("action must be hold or pin")
	}
	if _, err := m.store.GetHost(ctx, orgID, hostID); err != nil {
		return Override{}, err
	}
	if err := m.store.PutOverride(ctx, orgID, o, a.UserID); err != nil {
		return Override{}, err
	}
	m.audit(ctx, orgID, a, "fleet.host_override.set", "agent_host", hostID, map[string]any{"action": o.Action, "version": o.Version})
	return o, nil
}

// DeleteOverride removes a host override (idempotent).
func (m *Manager) DeleteOverride(ctx context.Context, orgID, hostID string, a Actor) error {
	deleted, err := m.store.DeleteOverride(ctx, orgID, hostID)
	if err != nil {
		return err
	}
	if deleted {
		m.audit(ctx, orgID, a, "fleet.host_override.delete", "agent_host", hostID, nil)
	}
	return nil
}

// Rollouts lists rollouts, newest first.
func (m *Manager) Rollouts(ctx context.Context, orgID string, limit int) ([]Rollout, error) {
	return m.store.ListRollouts(ctx, orgID, min(max(limit, 1), 100))
}

// Pause stops handing out a rollout's update.
func (m *Manager) Pause(ctx context.Context, orgID, id string, a Actor) (Rollout, error) {
	r, err := m.store.GetRollout(ctx, orgID, id)
	if err != nil {
		return Rollout{}, err
	}
	if r.State != RolloutActive {
		return Rollout{}, preconditionf("only an active rollout can be paused (rollout is %s)", r.State)
	}
	prev := r.State
	r.State, r.StateReason, r.UpdatedAt = RolloutPaused, "paused by "+a.Email, m.o.Now()
	if err := m.store.UpdateRollout(ctx, &r, prev); err != nil {
		return Rollout{}, conflictAsPrecondition(err)
	}
	m.audit(ctx, orgID, a, "fleet.rollout.pause", "rollout", id, map[string]any{"wave": r.CurrentWave})
	return r, nil
}

// Resume continues a paused or halted rollout. The soak time of the current wave restarts, and
// failures seen so far no longer count towards the halt threshold.
func (m *Manager) Resume(ctx context.Context, orgID, id string, a Actor) (Rollout, error) {
	r, err := m.store.GetRollout(ctx, orgID, id)
	if err != nil {
		return Rollout{}, err
	}
	if r.State != RolloutPaused && r.State != RolloutHalted {
		return Rollout{}, preconditionf("only a paused or halted rollout can be resumed (rollout is %s)", r.State)
	}
	prev := r.State
	now := m.o.Now()
	if prev == RolloutHalted {
		r.AckAttempted = r.Counters.Attempted
		r.AckFailed = r.Counters.Failed + r.Counters.RolledBack
	}
	r.State, r.StateReason, r.WaveStartedAt, r.UpdatedAt = RolloutActive, "", now, now
	if err := m.store.UpdateRollout(ctx, &r, prev); err != nil {
		return Rollout{}, conflictAsPrecondition(err)
	}
	m.audit(ctx, orgID, a, "fleet.rollout.resume", "rollout", id, map[string]any{"from_state": prev, "wave": r.CurrentWave})
	return r, nil
}

// DeployNow skips the remaining soak times of an active rollout: it moves straight to the last wave
// (100 %), so every remaining host is offered the update at its next sync. The failure-rate halt
// and the policy's maintenance windows still apply.
func (m *Manager) DeployNow(ctx context.Context, orgID, id string, a Actor) (Rollout, error) {
	r, err := m.store.GetRollout(ctx, orgID, id)
	if err != nil {
		return Rollout{}, err
	}
	if r.State != RolloutActive {
		return Rollout{}, preconditionf("only an active rollout can be deployed to all agents (rollout is %s)", r.State)
	}
	last := len(r.Waves) - 1
	if last < 0 || r.CurrentWave >= last {
		return Rollout{}, preconditionf("the rollout is already in its last wave")
	}
	fromWave := r.CurrentWave
	now := m.o.Now()
	r.CurrentWave, r.WaveStartedAt, r.UpdatedAt = last, now, now
	if err := m.store.UpdateRollout(ctx, &r, RolloutActive); err != nil {
		return Rollout{}, conflictAsPrecondition(err)
	}
	m.audit(ctx, orgID, a, "fleet.rollout.deploy_now", "rollout", id, map[string]any{"from_wave": fromWave, "wave": last, "percent": r.Waves[last]})
	return r, nil
}

func conflictAsPrecondition(err error) error {
	if errors.Is(err, ErrConflict) {
		return preconditionf("the rollout changed concurrently; reload and try again")
	}
	return err
}

// Rollback creates a rollback rollout to toVersion with the policy's waves.
func (m *Manager) Rollback(ctx context.Context, orgID, toVersion string, a Actor) (Rollout, error) {
	snap := m.snapshot()
	if snap.Len() == 0 {
		return Rollout{}, preconditionf("the release catalog is unavailable")
	}
	tv, err := lib.ParseVersion(strings.TrimSpace(toVersion))
	if err != nil {
		return Rollout{}, invalidf("to_version: %v", err)
	}
	if _, ok := snap.Release(tv.String()); !ok {
		return Rollout{}, invalidf("to_version %s is not a verified release", tv.String())
	}
	sp, err := m.store.GetPolicy(ctx, orgID)
	if err != nil {
		return Rollout{}, err
	}
	cur, err := m.store.CurrentRollout(ctx, orgID)
	if err != nil {
		return Rollout{}, err
	}
	now := m.o.Now()
	from := ""
	if cur != nil && cur.Action == ActionUpgrade && cur.ToVersion != "" {
		from = cur.ToVersion
	} else {
		hosts, err := m.store.AllHosts(ctx, orgID, now.Add(-m.o.StaleAfter))
		if err != nil {
			return Rollout{}, err
		}
		var best lib.Version
		for _, h := range hosts {
			if v, err := lib.ParseVersion(h.Version); err == nil && (from == "" || lib.Compare(v, best) > 0) {
				best, from = v, v.String()
			}
		}
	}
	if from == "" {
		return Rollout{}, preconditionf("no agent runs a version newer than %s", tv.String())
	}
	fv, _ := lib.ParseVersion(from)
	if lib.Compare(tv, fv) >= 0 {
		return Rollout{}, invalidf("to_version must be lower than %s", from)
	}
	if fr, ok := snap.Release(from); ok && fr.Manifest.Compatibility.RollbackFloor != "" {
		if floor, err := lib.ParseVersion(fr.Manifest.Compatibility.RollbackFloor); err == nil && lib.Compare(tv, floor) < 0 {
			return Rollout{}, preconditionf("%s cannot be rolled back below %s (rollback_floor)", from, floor.String())
		}
	}
	r := &Rollout{OrgID: orgID, Action: ActionRollback, FromVersion: from, ToVersion: tv.String(),
		Waves: append([]int(nil), sp.Waves...), WaveStartedAt: now, WaveSoakMinutes: sp.WaveSoakMinutes,
		HaltFailureRate: sp.HaltFailureRate, State: RolloutActive, CreatedBy: a.UserID, CreatedByEmail: a.Email, CreatedAt: now}
	if err := m.store.CreateRollout(ctx, r, "superseded by rollback to "+tv.String()); err != nil {
		if errors.Is(err, ErrConflict) {
			return Rollout{}, preconditionf("another rollout was created concurrently; reload and try again")
		}
		return Rollout{}, err
	}
	details := map[string]any{"from_version": from, "to_version": r.ToVersion}
	if cur != nil {
		details["superseded_rollout_id"] = cur.ID
	}
	m.audit(ctx, orgID, a, "fleet.rollback", "rollout", r.ID, details)
	return *r, nil
}
