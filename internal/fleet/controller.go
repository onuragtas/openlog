package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/fleet/catalog"
	lib "github.com/onuragtas/openlog/libs/release"
)

// MinAttemptsForHalt is the number of attempts before the failure rate is evaluated (contract §4).
const MinAttemptsForHalt = 3

// ControllerOptions configure the rollout controller. Zero values take the defaults in brackets.
type ControllerOptions struct {
	Interval   time.Duration // [30s]
	StaleAfter time.Duration // hosts that did not sync for this long are ignored [24h]
	Registerer prometheus.Registerer
	Log        *slog.Logger
	Now        func() time.Time
}

// Controller creates, advances, halts and completes rollouts. Only the leader runs it.
type Controller struct {
	store    Store
	snapshot func() *catalog.Snapshot
	o        ControllerOptions

	transitions *prometheus.CounterVec
	updates     *prometheus.CounterVec
	hostsGauge  *prometheus.GaugeVec
}

// NewController creates a controller. snapshot returns the current release catalog (may be nil).
func NewController(store Store, snapshot func() *catalog.Snapshot, o ControllerOptions) *Controller {
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 24 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	c := &Controller{store: store, snapshot: snapshot, o: o,
		transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_fleet_rollout_transitions_total", Help: "Rollout transitions made by the controller (create, advance, halt, complete).",
		}, []string{"transition"}),
		updates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_agent_updates_total", Help: "Agent update results observed by the rollout controller (succeeded, failed, rolled_back).",
		}, []string{"result"}),
		hostsGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "openlog_fleet_hosts", Help: "Agents that synced within the stale window by version (leader only).",
		}, []string{"version"}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(c.transitions, c.updates, c.hostsGauge)
	}
	return c
}

// Run reconciles every Interval until ctx is done. It must run on the leader only (a task of
// postgres.Leader, whose context ends when leadership is lost).
func (c *Controller) Run(ctx context.Context) {
	defer c.hostsGauge.Reset()
	t := time.NewTicker(c.o.Interval)
	defer t.Stop()
	for {
		tctx, cancel := context.WithTimeout(ctx, max(c.o.Interval, time.Minute))
		if err := c.Tick(tctx); err != nil && ctx.Err() == nil {
			c.o.Log.Warn("fleet rollout controller pass failed", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick reconciles every organization with agents or open rollouts once.
func (c *Controller) Tick(ctx context.Context) error {
	orgs, err := c.store.FleetOrgs(ctx)
	if err != nil {
		return err
	}
	versions := map[string]float64{}
	var errs []error
	for _, org := range orgs {
		if err := c.ReconcileOrg(ctx, org, versions); err != nil {
			errs = append(errs, fmt.Errorf("org %s: %w", org, err))
		}
	}
	c.hostsGauge.Reset()
	for v, n := range versions {
		c.hostsGauge.WithLabelValues(v).Set(n)
	}
	return errors.Join(errs...)
}

// progress classifies hosts for rollout r.
func progress(r *Rollout, pol Policy, hosts []Host, ovs map[string]Override, snap *catalog.Snapshot, now time.Time) Counters {
	var ctr Counters
	// Evaluate as if the rollout were active in auto mode, ignoring waves and windows: pending hosts
	// are those that will receive the update eventually.
	eval := *r
	eval.State = RolloutActive
	pol.Mode = ModeAuto
	for i := range hosts {
		h := &hosts[i]
		mine := h.RolloutID == r.ID
		recent := !h.UpdateChangedAt.Before(r.CreatedAt)
		cur, verr := lib.ParseVersion(h.Version)
		if mine && verr == nil {
			if t, ok := r.TargetFor(cur); ok {
				if tv, err := lib.ParseVersion(t); err == nil {
					c := lib.Compare(cur, tv)
					if (r.Action == ActionUpgrade && c >= 0) || (r.Action == ActionRollback && c <= 0) {
						ctr.Succeeded++
						ctr.Attempted++
						continue
					}
				}
			}
		}
		if mine && recent {
			switch {
			case h.UpdateState == StateFailed:
				ctr.Failed++
				ctr.Attempted++
				continue
			case h.UpdateState == StateRolledBack:
				ctr.RolledBack++
				ctr.Attempted++
				continue
			case InProgress(h.UpdateState):
				ctr.Attempted++
				ctr.Pending++
				continue
			case h.UpdateState == StateSucceeded:
				// An intermediate version succeeded; the host still needs the target.
				ctr.Attempted++
			}
		}
		in := Input{Now: now, Host: h.HostReport, Policy: pol, Rollout: &eval, Catalog: snap, IgnoreWave: true, IgnoreWindow: true, IgnorePolicyTarget: true}
		if o, ok := ovs[h.HostID]; ok {
			in.Override = &o
			if o.Action == OverridePin {
				continue // pinned hosts are not part of fleet rollouts
			}
		}
		if Decide(in).Offer() {
			ctr.Pending++
		}
	}
	return ctr
}

// desired is the fleet-level target of a policy.
type desired struct {
	toVersion string
	targets   map[string]string
}

func desiredTargets(pol Policy, snap *catalog.Snapshot, hosts []Host) (desired, bool) {
	switch pol.Target {
	case TargetPatch:
		d := desired{targets: map[string]string{}}
		for _, h := range hosts {
			v, err := lib.ParseVersion(h.Version)
			if err != nil {
				continue
			}
			if r, ok := snap.LatestPatch(pol.Channel, v.Major, v.Minor); ok {
				d.targets[MinorKey(v)] = r.VersionString()
			}
		}
		return d, len(d.targets) > 0
	default:
		r, ok := DesiredTarget(pol, snap, lib.Version{})
		if !ok {
			return desired{}, false
		}
		return desired{toVersion: r.VersionString()}, true
	}
}

func (d desired) matches(r *Rollout) bool {
	if d.toVersion != "" || r.ToVersion != "" {
		return d.toVersion == r.ToVersion
	}
	return maps.Equal(d.targets, r.Targets)
}

func (d desired) contains(version string) bool {
	if version == "" {
		return false
	}
	if d.toVersion == version {
		return true
	}
	for _, v := range d.targets {
		if v == version {
			return true
		}
	}
	return false
}

func (d desired) String() string {
	if d.toVersion != "" {
		return d.toVersion
	}
	keys := make([]string, 0, len(d.targets))
	for k := range d.targets {
		keys = append(keys, d.targets[k])
	}
	sort.Strings(keys)
	return fmt.Sprint(keys)
}

// ReconcileOrg advances the organization's current rollout and creates a new one when the policy
// is auto and hosts need a different target. versions (may be nil) collects hosts per version.
func (c *Controller) ReconcileOrg(ctx context.Context, orgID string, versions map[string]float64) error {
	now := c.o.Now()
	sp, err := c.store.GetPolicy(ctx, orgID)
	if err != nil {
		return err
	}
	pol := sp.Policy
	hosts, err := c.store.AllHosts(ctx, orgID, now.Add(-c.o.StaleAfter))
	if err != nil {
		return err
	}
	if versions != nil {
		for _, h := range hosts {
			versions[h.Version]++
		}
	}
	snap := c.snapshot()
	if snap.Len() == 0 {
		return nil
	}
	ovs, err := c.store.ListOverrides(ctx, orgID)
	if err != nil {
		return err
	}
	cur, err := c.store.CurrentRollout(ctx, orgID)
	if err != nil {
		return err
	}

	want, haveWant := desiredTargets(pol, snap, hosts)
	// An open upgrade rollout to a target the policy no longer wants is replaced, not advanced.
	outdated := pol.Mode == ModeAuto && haveWant && cur.Open() && cur.Action == ActionUpgrade && !want.matches(cur)
	if cur.Open() && !outdated {
		if cur, err = c.advance(ctx, cur, pol, hosts, ovs, snap, now); err != nil {
			return err
		}
	}
	if pol.Mode != ModeAuto || !haveWant {
		return nil
	}
	if cur != nil {
		if cur.Action == ActionUpgrade && want.matches(cur) {
			return nil
		}
		// Never undo an admin rollback automatically: only a release other than the one rolled
		// back from starts a new upgrade.
		if cur.Action == ActionRollback && (cur.FromVersion == "" || want.contains(cur.FromVersion)) {
			return nil
		}
	}
	next := &Rollout{
		OrgID: orgID, Action: ActionUpgrade, ToVersion: want.toVersion, Targets: want.targets,
		Waves: append([]int(nil), pol.Waves...), WaveStartedAt: now, WaveSoakMinutes: pol.WaveSoakMinutes,
		HaltFailureRate: pol.HaltFailureRate, State: RolloutActive, CreatedAt: now,
	}
	next.ID = "candidate" // not a uuid: no host is attributed to it yet
	ctr := progress(next, pol, hosts, ovs, snap, now)
	if ctr.Pending == 0 {
		if outdated {
			// Nothing needs the new target: end the outdated rollout.
			old := *cur
			old.State, old.StateReason, old.EndedAt, old.UpdatedAt = RolloutSuperseded, "the policy no longer targets "+cur.ToVersion, &now, now
			if err := c.store.UpdateRollout(ctx, &old, cur.State); err != nil {
				return ignoreConflict(err)
			}
			c.transitions.WithLabelValues("supersede").Inc()
			c.audit(ctx, orgID, "fleet.rollout.supersede", cur.ID, map[string]any{"reason": old.StateReason}, now)
		}
		return nil
	}
	next.ID = ""
	next.Counters = ctr
	next.FromVersion = commonVersion(next, pol, hosts, ovs, snap, now)
	reason := "superseded by rollout to " + want.String()
	if err := c.store.CreateRollout(ctx, next, reason); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil
		}
		return err
	}
	c.transitions.WithLabelValues("create").Inc()
	c.o.Log.Info("fleet rollout created", "org_id", orgID, "rollout_id", next.ID, "to", want.String(), "pending_hosts", ctr.Pending)
	details := map[string]any{"to_version": want.toVersion, "targets": want.targets, "waves": next.Waves, "pending_hosts": ctr.Pending}
	if cur != nil {
		details["superseded_rollout_id"] = cur.ID
	}
	c.audit(ctx, orgID, "fleet.rollout.create", next.ID, details, now)
	// Counters were computed for the candidate before it had an id; store them.
	return ignoreConflict(c.store.UpdateRollout(ctx, next, RolloutActive))
}

// commonVersion is the most common running version among hosts that need the rollout.
func commonVersion(r *Rollout, pol Policy, hosts []Host, ovs map[string]Override, snap *catalog.Snapshot, now time.Time) string {
	counts := map[string]int{}
	for _, h := range hosts {
		one := []Host{h}
		if progress(r, pol, one, ovs, snap, now).Pending > 0 {
			if v, err := lib.ParseVersion(h.Version); err == nil {
				counts[v.String()]++
			}
		}
	}
	best, n := "", 0
	for v, c := range counts {
		if c > n || (c == n && v < best) {
			best, n = v, c
		}
	}
	return best
}

func ignoreConflict(err error) error {
	if errors.Is(err, ErrConflict) {
		return nil
	}
	return err
}

// advance refreshes counters of an open rollout and applies halt/advance/complete.
func (c *Controller) advance(ctx context.Context, cur *Rollout, pol Policy, hosts []Host, ovs map[string]Override, snap *catalog.Snapshot, now time.Time) (*Rollout, error) {
	next := *cur
	next.Counters = progress(cur, pol, hosts, ovs, snap, now)
	for result, delta := range map[string]int{
		StateSucceeded:  next.Counters.Succeeded - cur.Counters.Succeeded,
		StateFailed:     next.Counters.Failed - cur.Counters.Failed,
		StateRolledBack: next.Counters.RolledBack - cur.Counters.RolledBack,
	} {
		if delta > 0 {
			c.updates.WithLabelValues(result).Add(float64(delta))
		}
	}
	transition, details := "", map[string]any{}
	if cur.State == RolloutActive && pol.Mode == ModeAuto {
		attempted := next.Counters.Attempted - cur.AckAttempted
		bad := next.Counters.Failed + next.Counters.RolledBack - cur.AckFailed
		soakDone := now.Sub(cur.WaveStartedAt) >= time.Duration(cur.WaveSoakMinutes)*time.Minute
		switch {
		case attempted >= MinAttemptsForHalt && bad > 0 && float64(bad) >= cur.HaltFailureRate*float64(attempted):
			transition = "halt"
			next.State = RolloutHalted
			next.StateReason = fmt.Sprintf("failure rate %d/%d reached the halt threshold %.0f%%", bad, attempted, cur.HaltFailureRate*100)
			details = map[string]any{"failed": bad, "attempted": attempted, "halt_failure_rate": cur.HaltFailureRate}
		case next.Counters.Pending == 0:
			transition = "complete"
			next.State = RolloutCompleted
			next.StateReason = ""
			next.EndedAt = &now
			details = map[string]any{"succeeded": next.Counters.Succeeded, "failed": next.Counters.Failed, "rolled_back": next.Counters.RolledBack}
		case soakDone && cur.CurrentWave < len(cur.Waves)-1:
			transition = "advance"
			next.CurrentWave++
			next.WaveStartedAt = now
			details = map[string]any{"wave": next.CurrentWave, "percent": next.Waves[next.CurrentWave], "attempted": attempted, "failed": bad}
		}
	}
	if transition == "" && next.Counters == cur.Counters {
		return cur, nil
	}
	next.UpdatedAt = now
	if err := c.store.UpdateRollout(ctx, &next, cur.State); err != nil {
		if errors.Is(err, ErrConflict) {
			return c.store.CurrentRollout(ctx, cur.OrgID) // an admin changed it meanwhile
		}
		return nil, err
	}
	if transition != "" {
		c.transitions.WithLabelValues(transition).Inc()
		c.o.Log.Info("fleet rollout "+transition, "org_id", cur.OrgID, "rollout_id", cur.ID, "details", details)
		c.audit(ctx, cur.OrgID, "fleet.rollout."+transition, cur.ID, details, now)
	}
	return &next, nil
}

func (c *Controller) audit(ctx context.Context, orgID, action, rolloutID string, details map[string]any, at time.Time) {
	e := AuditEntry{OrgID: orgID, ActorEmail: "openlog-controller", Action: action, TargetType: "rollout", TargetID: rolloutID, Details: details, At: at}
	if err := c.store.AddAudit(context.WithoutCancel(ctx), e); err != nil {
		c.o.Log.Error("cannot write audit log", "action", action, "org_id", orgID, "err", err)
	}
}
