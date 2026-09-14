package fleet

import (
	"fmt"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/fleet/testutil"
	lib "github.com/onuragtas/openlog/libs/release"
)

func TestAgentArtifactFormat(t *testing.T) {
	art := func(goos, arch, format string) lib.Artifact {
		return lib.Artifact{Component: lib.ComponentInfraAgent, OS: goos, Arch: arch, Format: format,
			Name: "openlog-infra-agent_0.4.0_" + goos + "_" + arch + "." + format}
	}
	r := &catalog.Release{Manifest: &lib.Manifest{Version: "0.4.0", Artifacts: []lib.Artifact{
		art("linux", "amd64", lib.FormatTarGz),
		art("darwin", "arm64", lib.FormatTarGz),
		art("windows", "amd64", lib.FormatZip),
		art("windows", "amd64", lib.FormatMSI),
		art("windows", "arm64", lib.FormatTarGz), // not what Windows agents install
	}}}
	for _, tc := range []struct {
		os, arch, want string
	}{
		{"linux", "amd64", "openlog-infra-agent_0.4.0_linux_amd64.tar.gz"},
		{"darwin", "arm64", "openlog-infra-agent_0.4.0_darwin_arm64.tar.gz"},
		{"windows", "amd64", "openlog-infra-agent_0.4.0_windows_amd64.zip"},
		{"windows", "arm64", ""},
	} {
		h := host("0.3.0")
		h.OS, h.Arch = tc.os, tc.arch
		a, ok := agentArtifact(r, h)
		if got := map[bool]string{true: a.Name, false: ""}[ok]; got != tc.want {
			t.Errorf("%s/%s: artifact %q, want %q", tc.os, tc.arch, got, tc.want)
		}
	}
}

var testNow = time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC) // Monday

func testCatalog(t testing.TB) *catalog.Snapshot {
	t.Helper()
	s, err := testutil.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	snap, err := testutil.Snapshot(s,
		testutil.ReleaseSpec{Version: "0.2.0"},
		testutil.ReleaseSpec{Version: "0.3.0", MinUpgradeFrom: "0.2.0", RollbackFloor: "0.2.0"},
		testutil.ReleaseSpec{Version: "0.3.1", MinUpgradeFrom: "0.2.0", RollbackFloor: "0.2.0", Platforms: []string{"linux/amd64"}},
		testutil.ReleaseSpec{Version: "0.4.0", MinUpgradeFrom: "0.3.0", RollbackFloor: "0.3.0", OldestSupportedAgent: "0.2.0"},
		testutil.ReleaseSpec{Version: "0.5.0-beta.1", MinUpgradeFrom: "0.3.0", RollbackFloor: "0.3.0"},
	)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func mustRelease(t testing.TB, snap *catalog.Snapshot, v string) *catalog.Release {
	t.Helper()
	r, ok := snap.Release(v)
	if !ok {
		t.Fatalf("release %s missing", v)
	}
	return r
}

func host(version string) HostReport {
	return HostReport{HostID: "host-1", HostName: "web-1", Version: version, OS: "linux", Arch: "amd64", UpdateCapable: true, UpdateState: StateIdle}
}

func rollout(to string) *Rollout {
	return &Rollout{ID: "r1", Action: ActionUpgrade, ToVersion: to, Waves: []int{10, 50, 100}, CurrentWave: 2,
		State: RolloutActive, CreatedAt: testNow.Add(-time.Hour), WaveStartedAt: testNow.Add(-time.Hour)}
}

// hostInBucket finds a host id whose bucket for rollout id is below (in=true) or at/above limit.
func hostInBucket(t *testing.T, rolloutID string, limit int, in bool) string {
	t.Helper()
	for i := range 10000 {
		id := fmt.Sprintf("host-%d", i)
		if (Bucket(id, rolloutID) < limit) == in {
			return id
		}
	}
	t.Fatal("no host id found")
	return ""
}

func TestDecide(t *testing.T) {
	snap := testCatalog(t)
	pol := DefaultPolicy()
	inWave := hostInBucket(t, "r1", 10, true)
	outOfWave := hostInBucket(t, "r1", 10, false)

	type tc struct {
		name       string
		in         Input
		reason     Reason
		action     string
		target     string
		release    string // offered release version
		artifact   string
		rolloutID  string
		deadline   time.Time
		noCatalog  bool
		customSnap bool
	}
	base := func() Input {
		return Input{Now: testNow, Host: host("0.3.0"), Policy: pol, Rollout: rollout("0.4.0"), Catalog: snap}
	}
	with := func(mod func(in *Input)) Input { in := base(); mod(&in); return in }

	cases := []tc{
		{name: "upgrade offered", in: base(), reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0",
			artifact: "openlog-infra-agent_0.4.0_linux_amd64.tar.gz", rolloutID: "r1"},
		{name: "no catalog", in: with(func(in *Input) { in.Catalog = nil }), reason: ReasonNoCatalog},
		{name: "empty catalog", in: with(func(in *Input) { in.Catalog = catalog.NewSnapshot(nil, testNow, testNow) }), reason: ReasonNoCatalog},
		{name: "mode off", in: with(func(in *Input) { in.Policy.Mode = ModeOff }), reason: ReasonModeOff},
		{name: "mode notify", in: with(func(in *Input) { in.Policy.Mode = ModeNotify }), reason: ReasonNotifyOnly},
		{name: "mode notify ignores pins", in: with(func(in *Input) {
			in.Policy.Mode = ModeNotify
			in.Override = &Override{Action: OverridePin, Version: "0.3.1"}
		}), reason: ReasonNotifyOnly},
		{name: "invalid agent version", in: with(func(in *Input) { in.Host.Version = "latest" }), reason: ReasonInvalidVersion},
		{name: "hold", in: with(func(in *Input) { in.Override = &Override{Action: OverrideHold} }), reason: ReasonHold},
		{name: "not update capable", in: with(func(in *Input) { in.Host.UpdateCapable = false }), reason: ReasonNotCapable},
		{name: "hold wins over not capable", in: with(func(in *Input) {
			in.Host.UpdateCapable = false
			in.Override = &Override{Action: OverrideHold}
		}), reason: ReasonHold},
		{name: "no rollout", in: with(func(in *Input) { in.Rollout = nil }), reason: ReasonNoRollout},
		{name: "superseded rollout", in: with(func(in *Input) { in.Rollout.State = RolloutSuperseded }), reason: ReasonNoRollout},
		{name: "paused", in: with(func(in *Input) { in.Rollout.State = RolloutPaused }), reason: ReasonRolloutPaused},
		{name: "halted", in: with(func(in *Input) { in.Rollout.State = RolloutHalted }), reason: ReasonRolloutHalted},
		{name: "up to date", in: with(func(in *Input) { in.Host.Version = "0.4.0" }), reason: ReasonUpToDate, rolloutID: "r1"},
		{name: "newer than target", in: with(func(in *Input) { in.Host.Version = "0.5.0-beta.1" }), reason: ReasonUpToDate, rolloutID: "r1"},
		{name: "dev build metadata ignored", in: with(func(in *Input) { in.Host.Version = "0.4.0+abc123" }), reason: ReasonUpToDate, rolloutID: "r1"},

		// Waves.
		{name: "first wave: host in bucket", in: with(func(in *Input) { in.Rollout.CurrentWave = 0; in.Host.HostID = inWave }),
			reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0", rolloutID: "r1"},
		{name: "first wave: host outside bucket", in: with(func(in *Input) { in.Rollout.CurrentWave = 0; in.Host.HostID = outOfWave }),
			reason: ReasonNotInWave, action: ActionUpgrade, target: "0.4.0", rolloutID: "r1"},
		{name: "ignore wave", in: with(func(in *Input) { in.Rollout.CurrentWave = 0; in.Host.HostID = outOfWave; in.IgnoreWave = true }),
			reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0", rolloutID: "r1"},
		{name: "completed rollout serves every host", in: with(func(in *Input) {
			in.Rollout.CurrentWave = 0
			in.Rollout.State = RolloutCompleted
			in.Host.HostID = outOfWave
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0", rolloutID: "r1"},

		// Compatibility.
		{name: "intermediate release for min_upgrade_from", in: with(func(in *Input) { in.Host.Version = "0.2.0" }),
			reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.3.1", artifact: "openlog-infra-agent_0.3.1_linux_amd64.tar.gz", rolloutID: "r1"},
		{name: "intermediate release needs the platform", in: with(func(in *Input) { in.Host.Version = "0.2.0"; in.Host.Arch = "arm64" }),
			reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.3.0", artifact: "openlog-infra-agent_0.3.0_linux_arm64.tar.gz", rolloutID: "r1"},
		{name: "two hops: oldest step first", in: with(func(in *Input) { in.Host.Version = "0.1.0" }),
			reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.2.0", rolloutID: "r1"},
		{name: "too old for any path", in: with(func(in *Input) {
			in.Host.Version = "0.1.0"
			in.Catalog = catalog.NewSnapshot([]*catalog.Release{mustRelease(t, snap, "0.3.0"), mustRelease(t, snap, "0.4.0")}, testNow, testNow)
		}), reason: ReasonIncompatible, action: ActionUpgrade, target: "0.4.0", rolloutID: "r1"},
		{name: "no artifact for platform", in: with(func(in *Input) { in.Host.OS = "windows" }), reason: ReasonNoArtifact, action: ActionUpgrade, target: "0.4.0", rolloutID: "r1"},

		// Policy targets.
		{name: "policy changed since rollout (pinned older)", in: with(func(in *Input) {
			in.Policy.Target, in.Policy.PinnedVersion = TargetPinned, sp("0.3.1")
		}), reason: ReasonRolloutOutdated, rolloutID: "r1"},
		{name: "controller counts an outdated rollout against its own target", in: with(func(in *Input) {
			in.Policy.Target, in.Policy.PinnedVersion = TargetPinned, sp("0.3.1")
			in.IgnorePolicyTarget = true
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0", rolloutID: "r1"},
		{name: "hold is reported without a catalog", in: with(func(in *Input) {
			in.Catalog = nil
			in.Override = &Override{Action: OverrideHold}
		}), reason: ReasonHold},
		{name: "pinned target", in: with(func(in *Input) {
			in.Policy.Target, in.Policy.PinnedVersion = TargetPinned, sp("0.3.1")
			in.Rollout.ToVersion = "0.3.1"
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.3.1", release: "0.3.1", rolloutID: "r1"},
		{name: "pinned target missing from catalog", in: with(func(in *Input) {
			in.Policy.Target, in.Policy.PinnedVersion = TargetPinned, sp("0.9.0")
			in.Rollout.ToVersion = "0.9.0"
		}), reason: ReasonRolloutOutdated, rolloutID: "r1"},
		{name: "patch target", in: with(func(in *Input) {
			in.Policy.Target = TargetPatch
			in.Rollout.ToVersion, in.Rollout.Targets = "", map[string]string{"0.3": "0.3.1"}
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.3.1", release: "0.3.1", rolloutID: "r1"},
		{name: "patch target: minor not in rollout", in: with(func(in *Input) {
			in.Policy.Target = TargetPatch
			in.Host.Version = "0.2.0"
			in.Rollout.ToVersion, in.Rollout.Targets = "", map[string]string{"0.3": "0.3.1"}
		}), reason: ReasonNotInRollout, rolloutID: "r1"},
		{name: "beta channel", in: with(func(in *Input) {
			in.Policy.Channel = "beta"
			in.Host.Version = "0.4.0"
			in.Rollout.ToVersion = "0.5.0-beta.1"
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.5.0-beta.1", release: "0.5.0-beta.1", rolloutID: "r1"},
		{name: "beta rollout on stable policy", in: with(func(in *Input) {
			in.Host.Version = "0.4.0"
			in.Rollout.ToVersion = "0.5.0-beta.1"
		}), reason: ReasonRolloutOutdated, rolloutID: "r1"},

		// Rollback rollouts.
		{name: "rollback offered", in: with(func(in *Input) {
			in.Host.Version = "0.4.0"
			in.Rollout.Action, in.Rollout.ToVersion = ActionRollback, "0.3.0"
		}), reason: ReasonOffer, action: ActionRollback, target: "0.3.0", release: "0.3.0", rolloutID: "r1"},
		{name: "rollback: already at or below target", in: with(func(in *Input) {
			in.Rollout.Action, in.Rollout.ToVersion = ActionRollback, "0.3.0"
		}), reason: ReasonUpToDate, rolloutID: "r1"},
		{name: "rollback below floor", in: with(func(in *Input) {
			in.Host.Version = "0.4.0"
			in.Rollout.Action, in.Rollout.ToVersion = ActionRollback, "0.2.0"
		}), reason: ReasonIncompatible, action: ActionRollback, target: "0.2.0", rolloutID: "r1"},
		{name: "rollback: running version unknown", in: with(func(in *Input) {
			in.Host.Version = "0.4.7"
			in.Rollout.Action, in.Rollout.ToVersion = ActionRollback, "0.3.0"
		}), reason: ReasonIncompatible, action: ActionRollback, target: "0.3.0", rolloutID: "r1"},
		{name: "rollback target missing", in: with(func(in *Input) {
			in.Host.Version = "0.4.0"
			in.Rollout.Action, in.Rollout.ToVersion = ActionRollback, "0.3.5"
		}), reason: ReasonTargetUnavailable, action: ActionRollback, target: "0.3.5", rolloutID: "r1"},
		{name: "rollback ignores policy target", in: with(func(in *Input) {
			in.Host.Version = "0.4.0"
			in.Policy.Target, in.Policy.PinnedVersion = TargetPinned, sp("0.4.0")
			in.Rollout.Action, in.Rollout.ToVersion = ActionRollback, "0.3.1"
		}), reason: ReasonOffer, action: ActionRollback, target: "0.3.1", release: "0.3.1", rolloutID: "r1"},

		// Failed attempts are not repeated within the same rollout.
		{name: "failed in this rollout", in: with(func(in *Input) {
			in.Host.UpdateState, in.Host.UpdateTo, in.Host.UpdateChangedAt = StateFailed, "0.4.0", testNow.Add(-time.Minute)
		}), reason: ReasonAlreadyFailed, action: ActionUpgrade, target: "0.4.0", rolloutID: "r1"},
		{name: "rolled back in this rollout", in: with(func(in *Input) {
			in.Host.UpdateState, in.Host.UpdateTo, in.Host.UpdateChangedAt = StateRolledBack, "0.4.0", testNow.Add(-time.Minute)
		}), reason: ReasonAlreadyFailed, action: ActionUpgrade, target: "0.4.0", rolloutID: "r1"},
		{name: "failed intermediate step", in: with(func(in *Input) {
			in.Host.Version = "0.2.0"
			in.Host.UpdateState, in.Host.UpdateTo, in.Host.UpdateChangedAt = StateFailed, "0.3.1", testNow.Add(-time.Minute)
		}), reason: ReasonAlreadyFailed, action: ActionUpgrade, target: "0.4.0", rolloutID: "r1"},
		{name: "failure before this rollout is retried", in: with(func(in *Input) {
			in.Host.UpdateState, in.Host.UpdateTo, in.Host.UpdateChangedAt = StateFailed, "0.4.0", testNow.Add(-2*time.Hour)
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0", rolloutID: "r1"},
		{name: "failure for another version", in: with(func(in *Input) {
			in.Host.UpdateState, in.Host.UpdateTo, in.Host.UpdateChangedAt = StateFailed, "0.3.1", testNow.Add(-time.Minute)
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0", rolloutID: "r1"},
		{name: "in progress keeps the offer", in: with(func(in *Input) {
			in.Host.UpdateState, in.Host.UpdateTo, in.Host.UpdateChangedAt = StateDownloading, "0.4.0", testNow.Add(-time.Minute)
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0", rolloutID: "r1"},

		// Maintenance windows (testNow is Monday 03:00 UTC).
		{name: "inside window", in: with(func(in *Input) {
			in.Policy.MaintenanceWindows = []Window{{Days: []string{"mon"}, Start: "02:00", End: "05:00"}}
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0", rolloutID: "r1",
			deadline: time.Date(2026, 9, 14, 5, 0, 0, 0, time.UTC)},
		{name: "outside window", in: with(func(in *Input) {
			in.Policy.MaintenanceWindows = []Window{{Days: []string{"tue"}, Start: "02:00", End: "05:00"}}
		}), reason: ReasonOutsideWindow, action: ActionUpgrade, target: "0.4.0", rolloutID: "r1"},
		{name: "ignore window", in: with(func(in *Input) {
			in.Policy.MaintenanceWindows = []Window{{Days: []string{"tue"}, Start: "02:00", End: "05:00"}}
			in.IgnoreWindow = true
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0", rolloutID: "r1"},

		// Host pins.
		{name: "pin upgrade without rollout", in: with(func(in *Input) {
			in.Rollout = nil
			in.Override = &Override{Action: OverridePin, Version: "0.3.1"}
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.3.1", release: "0.3.1"},
		{name: "pin ignores waves and rollout state", in: with(func(in *Input) {
			in.Rollout.State, in.Rollout.CurrentWave = RolloutHalted, 0
			in.Host.HostID = outOfWave
			in.Override = &Override{Action: OverridePin, Version: "0.4.0"}
		}), reason: ReasonOffer, action: ActionUpgrade, target: "0.4.0", release: "0.4.0"},
		{name: "pin lower version rolls back", in: with(func(in *Input) {
			in.Host.Version = "0.4.0"
			in.Override = &Override{Action: OverridePin, Version: "0.3.0"}
		}), reason: ReasonOffer, action: ActionRollback, target: "0.3.0", release: "0.3.0"},
		{name: "pin equal", in: with(func(in *Input) { in.Override = &Override{Action: OverridePin, Version: "0.3.0"} }), reason: ReasonUpToDate},
		{name: "pin below rollback floor", in: with(func(in *Input) {
			in.Host.Version = "0.4.0"
			in.Override = &Override{Action: OverridePin, Version: "0.2.0"}
		}), reason: ReasonIncompatible, action: ActionRollback, target: "0.2.0"},
		{name: "pin failed after pin was set", in: with(func(in *Input) {
			in.Override = &Override{Action: OverridePin, Version: "0.3.1", UpdatedAt: testNow.Add(-time.Hour)}
			in.Host.UpdateState, in.Host.UpdateTo, in.Host.UpdateChangedAt = StateFailed, "0.3.1", testNow.Add(-time.Minute)
		}), reason: ReasonAlreadyFailed, action: ActionUpgrade, target: "0.3.1"},
		{name: "pin respects windows", in: with(func(in *Input) {
			in.Override = &Override{Action: OverridePin, Version: "0.3.1"}
			in.Policy.MaintenanceWindows = []Window{{Days: []string{"tue"}, Start: "02:00", End: "05:00"}}
		}), reason: ReasonOutsideWindow, action: ActionUpgrade, target: "0.3.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(c.in)
			if d.Reason != c.reason {
				t.Fatalf("reason = %s, want %s (decision %+v)", d.Reason, c.reason, d)
			}
			if d.Action != c.action || d.Target != c.target || d.RolloutID != c.rolloutID {
				t.Errorf("action/target/rollout = %q/%q/%q, want %q/%q/%q", d.Action, d.Target, d.RolloutID, c.action, c.target, c.rolloutID)
			}
			if d.Offer() != (c.reason == ReasonOffer) || (d.Release != nil) != d.Offer() {
				t.Errorf("offer/release mismatch: %+v", d)
			}
			if c.release != "" && d.Release.VersionString() != c.release {
				t.Errorf("release = %s, want %s", d.Release.VersionString(), c.release)
			}
			if c.artifact != "" && d.Artifact.Name != c.artifact {
				t.Errorf("artifact = %s, want %s", d.Artifact.Name, c.artifact)
			}
			if !d.Deadline.Equal(c.deadline) {
				t.Errorf("deadline = %v, want %v", d.Deadline, c.deadline)
			}
		})
	}
}

func TestBucketDistribution(t *testing.T) {
	if Bucket("h", "r") != Bucket("h", "r") {
		t.Fatal("bucket not deterministic")
	}
	const n = 20000
	var below10, below50 int
	for i := range n {
		b := Bucket(fmt.Sprintf("host-%d", i), "0b5f6c1e-9a44-4d1f-8f7e-3b7a0b9e2c11")
		if b < 0 || b > 99 {
			t.Fatalf("bucket %d out of range", b)
		}
		if b < 10 {
			below10++
		}
		if b < 50 {
			below50++
		}
	}
	if f := float64(below10) / n; f < 0.08 || f > 0.12 {
		t.Errorf("10%% wave share = %.3f", f)
	}
	if f := float64(below50) / n; f < 0.47 || f > 0.53 {
		t.Errorf("50%% wave share = %.3f", f)
	}
	// Buckets differ per rollout, so the same hosts are not always first.
	same := 0
	for i := range 1000 {
		id := fmt.Sprintf("host-%d", i)
		if (Bucket(id, "r1") < 10) && (Bucket(id, "r2") < 10) {
			same++
		}
	}
	if same > 30 {
		t.Errorf("%d hosts in the first wave of both rollouts", same)
	}
}

func TestRolloutWavePercent(t *testing.T) {
	r := &Rollout{Waves: []int{1, 10, 100}, CurrentWave: 1, State: RolloutActive}
	if r.WavePercent() != 10 {
		t.Error(r.WavePercent())
	}
	r.CurrentWave = 7
	if r.WavePercent() != 100 {
		t.Error("out-of-range wave not clamped")
	}
	r.CurrentWave, r.State = 0, RolloutCompleted
	if r.WavePercent() != 100 {
		t.Error("completed rollout is not 100%")
	}
}
