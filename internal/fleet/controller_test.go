package fleet_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/fleet/fleettest"
	"github.com/onuragtas/openlog/internal/fleet/testutil"
)

const tenantID = "t1"

var baseSpecs = []testutil.ReleaseSpec{
	{Version: "0.2.0"},
	{Version: "0.3.0", MinUpgradeFrom: "0.2.0", RollbackFloor: "0.2.0"},
	{Version: "0.4.0", MinUpgradeFrom: "0.3.0", RollbackFloor: "0.3.0", OldestSupportedAgent: "0.3.0"},
}

// env is a fleet with a mirror catalog, an in-memory store, fake agents and a fake clock.
type env struct {
	t      *testing.T
	ctx    context.Context
	signer testutil.Signer
	dir    string
	gen    time.Time
	now    time.Time
	store  *fleettest.MemStore
	cat    *catalog.Catalog
	ctl    *fleet.Controller
	mgr    *fleet.Manager
	agents map[string]*fleet.HostReport
	ids    []string
	org    string
	admin  fleet.Actor
}

func newEnv(t *testing.T, hosts int, specs ...testutil.ReleaseSpec) *env {
	t.Helper()
	s, err := testutil.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: context.Background(), signer: s, dir: t.TempDir(), gen: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		now: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC), store: fleettest.NewMemStore(), agents: map[string]*fleet.HostReport{},
		admin: fleet.Actor{UserID: "u1", Email: "admin@example.com"}}
	e.org = e.store.OrgOf(tenantID)
	if len(specs) == 0 {
		specs = baseSpecs
	}
	e.publish(specs...)
	clock := func() time.Time { return e.now }
	e.ctl = fleet.NewController(e.store, e.cat.Snapshot, fleet.ControllerOptions{Now: clock})
	e.mgr = fleet.NewManager(e.store, e.cat, fleet.ManagerOptions{Now: clock})
	for i := range hosts {
		id := fmt.Sprintf("host-%03d", i)
		e.ids = append(e.ids, id)
		e.agents[id] = &fleet.HostReport{HostID: id, HostName: "web-" + id, Version: "0.3.0", OS: "linux", Arch: "amd64",
			InstallMethod: "tarball", UpdateCapable: true, UpdateState: fleet.StateIdle}
	}
	e.syncAll()
	return e
}

// publish (re)writes the mirror and refreshes the catalog.
func (e *env) publish(specs ...testutil.ReleaseSpec) {
	e.t.Helper()
	e.gen = e.gen.Add(time.Hour)
	if err := testutil.WriteMirror(e.dir, e.signer, specs, e.gen); err != nil {
		e.t.Fatal(err)
	}
	if e.cat == nil {
		e.cat = catalog.New(catalog.Options{MirrorDir: e.dir, TrustedKeys: []ed25519.PublicKey{e.signer.PublicKey()}})
	}
	if err := e.cat.Refresh(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// syncAll runs one sync for every agent like the ingest handler and returns the offers.
func (e *env) syncAll() map[string]fleet.Decision {
	e.t.Helper()
	st, err := e.store.LoadOrgState(e.ctx, tenantID)
	if err != nil {
		e.t.Fatal(err)
	}
	offers := map[string]fleet.Decision{}
	var recs []fleet.HostRecord
	for _, id := range e.ids {
		a := e.agents[id]
		in := fleet.Input{Now: e.now, Host: *a, Policy: st.Policy, Rollout: st.Rollout, Catalog: e.cat.Snapshot()}
		if o, ok := st.Overrides[id]; ok {
			in.Override = &o
		}
		d := fleet.Decide(in)
		rec := fleet.HostRecord{TenantID: tenantID, Report: *a, SyncAt: e.now}
		if d.Offer() {
			offers[id] = d
			rec.RolloutID = d.RolloutID
		}
		recs = append(recs, rec)
	}
	if err := e.store.UpsertHosts(e.ctx, recs); err != nil {
		e.t.Fatal(err)
	}
	return offers
}

// apply lets an agent finish an offered update: succeed, fail or roll back.
func (e *env) apply(id string, d fleet.Decision, result string) {
	a := e.agents[id]
	a.UpdateFrom, a.UpdateTo, a.UpdateState, a.UpdateChangedAt = a.Version, d.Release.VersionString(), result, e.now
	switch result {
	case fleet.StateSucceeded:
		a.Version = d.Release.VersionString()
	case fleet.StateFailed:
		a.UpdateError = "self-test failed"
	}
}

func (e *env) tick() {
	e.t.Helper()
	if err := e.ctl.Tick(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) current() *fleet.Rollout {
	e.t.Helper()
	r, err := e.store.CurrentRollout(e.ctx, e.org)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) rollouts() []fleet.Rollout {
	rs, _ := e.store.ListRollouts(e.ctx, e.org, 100)
	return rs
}

func sortedKeys(m map[string]fleet.Decision) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestControllerRolloutLifecycle(t *testing.T) {
	e := newEnv(t, 100)

	// Auto policy + hosts behind the latest release → rollout in the first (10%) wave.
	e.tick()
	r := e.current()
	if r == nil || r.State != fleet.RolloutActive || r.ToVersion != "0.4.0" || r.CurrentWave != 0 || r.FromVersion != "0.3.0" {
		t.Fatalf("rollout = %+v", r)
	}
	if r.Counters.Pending != 100 {
		t.Errorf("pending = %d", r.Counters.Pending)
	}
	e.tick()
	if n := len(e.rollouts()); n != 1 {
		t.Fatalf("rollouts = %d after a second tick", n)
	}

	// Wave 1: only ~10% of hosts are offered the update.
	offers := e.syncAll()
	if len(offers) < 3 || len(offers) > 20 {
		t.Fatalf("first wave offers = %d of 100", len(offers))
	}
	for id, d := range offers {
		if d.Action != fleet.ActionUpgrade || d.Release.VersionString() != "0.4.0" || d.RolloutID != r.ID || fleet.Bucket(id, r.ID) >= 10 {
			t.Fatalf("bad offer for %s: %+v", id, d)
		}
		e.apply(id, d, fleet.StateSucceeded)
	}
	wave1 := len(offers)
	e.syncAll()
	e.tick()
	r = e.current()
	if r.Counters.Succeeded != wave1 || r.Counters.Attempted != wave1 || r.Counters.Pending != 100-wave1 || r.CurrentWave != 0 {
		t.Fatalf("after wave 1: %+v", r)
	}

	// Soak not over: still wave 0. After the soak time: wave 1 (50%).
	e.now = e.now.Add(59 * time.Minute)
	e.tick()
	if e.current().CurrentWave != 0 {
		t.Fatal("advanced before the soak time")
	}
	e.now = e.now.Add(2 * time.Minute)
	e.tick()
	if r = e.current(); r.CurrentWave != 1 || !r.WaveStartedAt.Equal(e.now) {
		t.Fatalf("not advanced: %+v", r)
	}

	// A failure spike in wave 2 halts the rollout.
	offers = e.syncAll()
	failed := 0
	for _, id := range sortedKeys(offers) {
		if failed < 4 {
			e.apply(id, offers[id], fleet.StateFailed)
			failed++
			continue
		}
		e.apply(id, offers[id], fleet.StateSucceeded)
	}
	e.syncAll()
	e.tick()
	r = e.current()
	if r.State != fleet.RolloutHalted || !strings.Contains(r.StateReason, "failure rate") {
		t.Fatalf("not halted: %+v", r)
	}
	if r.Counters.Failed != 4 {
		t.Errorf("failed = %d", r.Counters.Failed)
	}
	// Halted: hosts that did not update get nothing; updated hosts stay.
	if offers := e.syncAll(); len(offers) != 0 {
		t.Fatalf("halted rollout still offered %d updates", len(offers))
	}

	// Admin resumes: known failures are acknowledged, the rollout does not halt again.
	if _, err := e.mgr.Resume(e.ctx, e.org, r.ID, e.admin); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if r = e.current(); r.State != fleet.RolloutActive || r.AckFailed != 4 {
		t.Fatalf("after resume: %+v", r)
	}
	e.now = e.now.Add(61 * time.Minute)
	e.tick()
	if r = e.current(); r.CurrentWave != 2 {
		t.Fatalf("wave = %d", r.CurrentWave)
	}
	offers = e.syncAll()
	for id, d := range offers {
		e.apply(id, d, fleet.StateSucceeded)
	}
	e.syncAll()
	e.tick()
	r = e.current()
	if r.State != fleet.RolloutCompleted || r.EndedAt == nil || r.Counters.Pending != 0 || r.Counters.Succeeded != 96 || r.Counters.Failed != 4 {
		t.Fatalf("not completed: %+v", r)
	}
	// Failed hosts are not retried within the rollout; no new rollout for the same target.
	if offers := e.syncAll(); len(offers) != 0 {
		t.Errorf("completed rollout offered %d updates to failed hosts", len(offers))
	}
	e.tick()
	if n := len(e.rollouts()); n != 1 {
		t.Errorf("rollouts = %d", n)
	}
	want := []string{"fleet.rollout.create", "fleet.rollout.advance", "fleet.rollout.halt", "fleet.rollout.resume", "fleet.rollout.advance", "fleet.rollout.complete"}
	if got := e.store.AuditActions(); !slices.Equal(got, want) {
		t.Errorf("audit = %v, want %v", got, want)
	}
}

func TestControllerMinimumAttemptsBeforeHalt(t *testing.T) {
	e := newEnv(t, 40)
	p := fleet.DefaultPolicy()
	p.Waves = []int{50, 100}
	if _, err := e.mgr.PutPolicy(e.ctx, e.org, p, e.admin); err != nil {
		t.Fatal(err)
	}
	e.tick()
	offers := e.syncAll()
	ids := sortedKeys(offers)
	if len(ids) < 4 {
		t.Fatalf("offers = %d", len(ids))
	}
	for _, id := range ids[:2] {
		e.apply(id, offers[id], fleet.StateFailed)
	}
	e.syncAll()
	e.tick()
	if r := e.current(); r.State != fleet.RolloutActive || r.Counters.Failed != 2 {
		t.Fatalf("halted with fewer than 3 attempts: %+v", r)
	}
	// With fewer than 3 attempts the wave advances on soak alone.
	e.now = e.now.Add(61 * time.Minute)
	e.tick()
	if r := e.current(); r.CurrentWave != 1 {
		t.Fatalf("wave = %d", r.CurrentWave)
	}
	e.apply(ids[2], offers[ids[2]], fleet.StateRolledBack)
	e.syncAll()
	e.tick()
	if r := e.current(); r.State != fleet.RolloutHalted || r.Counters.RolledBack != 1 {
		t.Fatalf("not halted at 3 failed attempts: %+v", r)
	}
}

func TestControllerNewReleaseSupersedes(t *testing.T) {
	e := newEnv(t, 20)
	e.tick()
	first := e.current()
	e.publish(append(baseSpecs, testutil.ReleaseSpec{Version: "0.4.1", MinUpgradeFrom: "0.3.0", RollbackFloor: "0.3.0"})...)
	e.tick()
	rs := e.rollouts()
	if len(rs) != 2 || rs[0].ToVersion != "0.4.1" || rs[0].State != fleet.RolloutActive || rs[1].ID != first.ID || rs[1].State != fleet.RolloutSuperseded {
		t.Fatalf("rollouts = %+v", rs)
	}
	if !strings.Contains(rs[1].StateReason, "0.4.1") {
		t.Errorf("reason = %q", rs[1].StateReason)
	}
}

func TestControllerPolicyChangeSupersedesHaltedRollout(t *testing.T) {
	e := newEnv(t, 20)
	e.tick()
	r := e.current()
	r.State = fleet.RolloutHalted
	if err := e.store.UpdateRollout(e.ctx, r, fleet.RolloutActive); err != nil {
		t.Fatal(err)
	}
	p := fleet.DefaultPolicy()
	p.Target, p.PinnedVersion = fleet.TargetPinned, ptr("0.3.0")
	for _, id := range e.ids[:5] {
		e.agents[id].Version = "0.2.0"
	}
	e.syncAll()
	if _, err := e.mgr.PutPolicy(e.ctx, e.org, p, e.admin); err != nil {
		t.Fatal(err)
	}
	e.tick()
	cur := e.current()
	if cur.ToVersion != "0.3.0" || cur.Counters.Pending != 5 || cur.State != fleet.RolloutActive {
		t.Fatalf("current = %+v", cur)
	}
}

func ptr[T any](v T) *T { return &v }

func TestControllerNotifyModeCreatesNothing(t *testing.T) {
	e := newEnv(t, 10)
	p := fleet.DefaultPolicy()
	p.Mode = fleet.ModeNotify
	if _, err := e.mgr.PutPolicy(e.ctx, e.org, p, e.admin); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if len(e.rollouts()) != 0 {
		t.Fatal("notify mode created a rollout")
	}
	sum, err := e.mgr.Summary(e.ctx, e.org)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.UpdateAvailable || sum.Outdated != 10 || sum.Target == nil || sum.Target.Version != "0.4.0" || sum.Latest["stable"].Version != "0.4.0" ||
		len(sum.Versions) != 1 || sum.Versions[0].Version != "0.3.0" || sum.Versions[0].Latest || !sum.Versions[0].Supported || sum.OldestSupported != "0.3.0" {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestControllerPauseStopsWaves(t *testing.T) {
	e := newEnv(t, 10)
	e.tick()
	r := e.current()
	if _, err := e.mgr.Pause(e.ctx, e.org, r.ID, e.admin); err != nil {
		t.Fatal(err)
	}
	var pe *fleet.PreconditionError
	if _, err := e.mgr.Pause(e.ctx, e.org, r.ID, e.admin); !errors.As(err, &pe) {
		t.Fatalf("second pause err = %v", err)
	}
	e.now = e.now.Add(3 * time.Hour)
	e.tick()
	if r = e.current(); r.State != fleet.RolloutPaused || r.CurrentWave != 0 {
		t.Fatalf("paused rollout changed: %+v", r)
	}
	if offers := e.syncAll(); len(offers) != 0 {
		t.Fatal("paused rollout offered updates")
	}
	if _, err := e.mgr.Resume(e.ctx, e.org, r.ID, e.admin); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if r = e.current(); r.State != fleet.RolloutActive || r.CurrentWave != 0 {
		t.Fatalf("resume restarts the soak of the current wave: %+v", r)
	}
}

func TestRollback(t *testing.T) {
	e := newEnv(t, 30)
	p := fleet.DefaultPolicy()
	p.Waves, p.WaveSoakMinutes = []int{100}, 0
	if _, err := e.mgr.PutPolicy(e.ctx, e.org, p, e.admin); err != nil {
		t.Fatal(err)
	}
	e.tick()
	for id, d := range e.syncAll() {
		e.apply(id, d, fleet.StateSucceeded)
	}
	e.syncAll()
	e.tick()
	if r := e.current(); r.State != fleet.RolloutCompleted {
		t.Fatalf("upgrade not completed: %+v", r)
	}

	var ve *fleet.ValidationError
	var pe *fleet.PreconditionError
	if _, err := e.mgr.Rollback(e.ctx, e.org, "0.9.0", e.admin); !errors.As(err, &ve) {
		t.Errorf("unknown version err = %v", err)
	}
	if _, err := e.mgr.Rollback(e.ctx, e.org, "0.2.0", e.admin); !errors.As(err, &pe) {
		t.Errorf("below rollback floor err = %v", err)
	}
	if _, err := e.mgr.Rollback(e.ctx, e.org, "0.4.0", e.admin); !errors.As(err, &ve) {
		t.Errorf("not lower err = %v", err)
	}
	rb, err := e.mgr.Rollback(e.ctx, e.org, "0.3.0", e.admin)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Action != fleet.ActionRollback || rb.FromVersion != "0.4.0" || rb.ToVersion != "0.3.0" || rb.State != fleet.RolloutActive {
		t.Fatalf("rollback = %+v", rb)
	}
	e.tick()
	if cur := e.current(); cur.ID != rb.ID {
		t.Fatalf("controller replaced the rollback with %+v", cur)
	}
	offers := e.syncAll()
	if len(offers) != 30 {
		t.Fatalf("rollback offers = %d", len(offers))
	}
	for id, d := range offers {
		if d.Action != fleet.ActionRollback || d.Release.VersionString() != "0.3.0" {
			t.Fatalf("offer = %+v", d)
		}
		e.apply(id, d, fleet.StateSucceeded)
	}
	e.syncAll()
	e.tick()
	e.tick()
	rs := e.rollouts()
	if len(rs) != 2 || rs[0].State != fleet.RolloutCompleted || rs[0].Counters.Succeeded != 30 {
		t.Fatalf("rollouts after rollback = %+v", rs)
	}
	if offers := e.syncAll(); len(offers) != 0 {
		t.Error("hosts were upgraded again after the rollback")
	}
	// A newer release starts a new upgrade.
	e.publish(append(baseSpecs, testutil.ReleaseSpec{Version: "0.4.1", MinUpgradeFrom: "0.3.0", RollbackFloor: "0.3.0"})...)
	e.tick()
	if cur := e.current(); cur.Action != fleet.ActionUpgrade || cur.ToVersion != "0.4.1" {
		t.Fatalf("current = %+v", cur)
	}
}

func TestPatchTargetRollout(t *testing.T) {
	e := newEnv(t, 10, append(baseSpecs, testutil.ReleaseSpec{Version: "0.3.2", MinUpgradeFrom: "0.2.0"}, testutil.ReleaseSpec{Version: "0.2.1"})...)
	for _, id := range e.ids[:4] {
		e.agents[id].Version = "0.2.0"
	}
	e.syncAll()
	p := fleet.DefaultPolicy()
	p.Target, p.Waves = fleet.TargetPatch, []int{100}
	if _, err := e.mgr.PutPolicy(e.ctx, e.org, p, e.admin); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r := e.current()
	if r.ToVersion != "" || r.Targets["0.3"] != "0.3.2" || r.Targets["0.2"] != "0.2.1" || r.Counters.Pending != 10 {
		t.Fatalf("patch rollout = %+v", r)
	}
	offers := e.syncAll()
	for id, d := range offers {
		want := "0.3.2"
		if e.agents[id].Version == "0.2.0" {
			want = "0.2.1"
		}
		if d.Release.VersionString() != want {
			t.Errorf("%s offered %s, want %s", id, d.Release.VersionString(), want)
		}
	}
}

func TestHostOverrides(t *testing.T) {
	e := newEnv(t, 5)
	if _, err := e.mgr.PutOverride(e.ctx, e.org, "host-000", fleet.OverrideHold, "", e.admin); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.PutOverride(e.ctx, e.org, "host-001", fleet.OverridePin, "0.2.0", e.admin); err != nil {
		t.Fatal(err)
	}
	var ve *fleet.ValidationError
	if _, err := e.mgr.PutOverride(e.ctx, e.org, "host-002", fleet.OverridePin, "7.7.7", e.admin); !errors.As(err, &ve) {
		t.Errorf("unknown pin version err = %v", err)
	}
	if _, err := e.mgr.PutOverride(e.ctx, e.org, "nope", fleet.OverrideHold, "", e.admin); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("unknown host err = %v", err)
	}
	e.tick()
	if r := e.current(); r.Counters.Pending != 3 {
		t.Fatalf("held and pinned hosts counted as pending: %+v", r.Counters)
	}
	p := fleet.DefaultPolicy()
	p.Waves = []int{100}
	_, _ = e.mgr.PutPolicy(e.ctx, e.org, p, e.admin)
	e.tick()
	offers := e.syncAll()
	if _, ok := offers["host-000"]; ok {
		t.Error("held host offered an update")
	}
	if d, ok := offers["host-001"]; !ok || d.Action != fleet.ActionRollback || d.Release.VersionString() != "0.2.0" {
		t.Errorf("pinned host offer = %+v", d)
	}
	hosts, _, err := e.mgr.Hosts(e.ctx, e.org, fleet.HostFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if hosts[0].Override == nil || hosts[0].Status != fleet.ReasonHold || !hosts[0].Outdated {
		t.Errorf("host view = %+v", hosts[0])
	}
	if err := e.mgr.DeleteOverride(e.ctx, e.org, "host-000", e.admin); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.store.AuditActions(), "fleet.host_override.delete") {
		t.Error("override delete not audited")
	}
}
