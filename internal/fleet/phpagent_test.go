package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/testutil"
)

func phpCatalogFor(t *testing.T) (*PHPInput, func(string) *PHPAgentReport) {
	t.Helper()
	s, _ := testutil.NewSigner()
	released := testNow.Add(-48 * time.Hour)
	snap, err := testutil.Snapshot(s,
		testutil.ReleaseSpec{Version: "0.9.0", ReleasedAt: released},
		testutil.ReleaseSpec{Version: "0.9.1", PHPAgent: true, ReleasedAt: released, Platforms: []string{"linux/amd64"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	pol := DefaultPolicy()
	pol.PHPAgent.Mode = PHPModeAuto
	in := &PHPInput{Now: testNow, Host: host("0.9.1"), Policy: pol, Catalog: snap}
	report := func(installed string) *PHPAgentReport {
		return &PHPAgentReport{Mode: PHPModeAuto, Capable: true, ManagedBy: "none", Version: installed,
			Runtimes: []PHPRuntime{{Bin: "/usr/sbin/php-fpm8.2", API: "20220829", Supported: true}}}
	}
	return in, report
}

func TestDecidePHP(t *testing.T) {
	base, report := phpCatalogFor(t)
	cases := []struct {
		name   string
		mutate func(in *PHPInput)
		want   PHPReason
		target string
	}{
		{"manual by default", func(in *PHPInput) { in.Policy.PHPAgent = DefaultPHPAgentPolicy() }, ReasonPHPManual, ""},
		{"off", func(in *PHPInput) { in.Policy.PHPAgent.Mode = PHPModeOff }, ReasonPHPModeOff, ""},
		{"override enables one host", func(in *PHPInput) {
			in.Policy.PHPAgent = DefaultPHPAgentPolicy()
			in.Override = &PHPOverride{HostID: "host-1", Mode: PHPModeAuto}
		}, ReasonPHPOffer, "0.9.1"},
		{"override disables one host", func(in *PHPInput) { in.Override = &PHPOverride{Mode: PHPModeOff} }, ReasonPHPModeOff, ""},
		{"agent without report", func(in *PHPInput) { in.Host.PHPAgent = nil }, ReasonPHPNotReported, "0.9.1"},
		{"offer: version follows the agent", func(in *PHPInput) {}, ReasonPHPOffer, "0.9.1"},
		{"up to date", func(in *PHPInput) { in.Host.PHPAgent = report("0.9.1") }, ReasonPHPUpToDate, "0.9.1"},
		{"package installation", func(in *PHPInput) { in.Host.PHPAgent.ManagedBy = "package" }, ReasonPHPManagedElsewhere, "0.9.1"},
		{"not capable", func(in *PHPInput) { in.Host.PHPAgent.Capable = false }, ReasonPHPNotCapable, "0.9.1"},
		{"no supported runtime", func(in *PHPInput) { in.Host.PHPAgent.Runtimes[0].Excluded = true }, ReasonPHPNoRuntime, "0.9.1"},
		{"agent version without php artifact", func(in *PHPInput) { in.Host.Version = "0.9.0" }, ReasonPHPNoArtifact, "0.9.0"},
		{"pinned version not in catalog", func(in *PHPInput) { in.Policy.PHPAgent.Version = "0.8.0" }, ReasonPHPTargetUnavailable, "0.8.0"},
		{"invalid agent version", func(in *PHPInput) { in.Host.Version = "0.0.0-dev+abc" }, ReasonPHPInvalidVersion, ""},
		{"other platform", func(in *PHPInput) { in.Host.Arch = "arm64" }, ReasonPHPNoArtifact, "0.9.1"},
		{"rolled back before", func(in *PHPInput) {
			in.Host.PHPAgent.Update = &PHPAgentUpdate{Version: "0.9.1", State: PHPStateRolledBack}
		}, ReasonPHPAlreadyFailed, "0.9.1"},
		{"outside window", func(in *PHPInput) {
			in.Policy.MaintenanceWindows = []Window{{Start: "10:00", End: "11:00"}}
		}, ReasonPHPOutsideWindow, "0.9.1"},
		{"no catalog", func(in *PHPInput) { in.Catalog = nil }, ReasonPHPNoCatalog, "0.9.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := *base
			in.Host.PHPAgent = report("")
			c.mutate(&in)
			d := DecidePHP(in)
			if d.Reason != c.want || d.Target != c.target {
				t.Fatalf("decision = %s target %q, want %s %q", d.Reason, d.Target, c.want, c.target)
			}
			if d.Offer() != (d.Release != nil) || (d.Offer() && d.Artifact.Component != "php-agent") {
				t.Errorf("offer without release/artifact: %+v", d)
			}
		})
	}
}

func TestDecidePHPWaves(t *testing.T) {
	base, report := phpCatalogFor(t)
	changed := testNow.Add(-30 * time.Minute) // after the release: waves start here
	in := *base
	in.Policy.PHPAgent.ChangedAt = &changed
	in.Policy.WaveSoakMinutes = 60
	target := "php-agent:0.9.1"
	first := hostInBucket(t, target, 10, true)
	later := hostInBucket(t, target, 50, false)
	for _, c := range []struct {
		host string
		now  time.Time
		want PHPReason
	}{
		{first, testNow, ReasonPHPOffer},
		{later, testNow, ReasonPHPNotInWave},
		{later, testNow.Add(2 * time.Hour), ReasonPHPOffer}, // third wave (100 %) after two soak periods
	} {
		in.Now = c.now
		in.Host.HostID = c.host
		in.Host.PHPAgent = report("")
		if d := DecidePHP(in); d.Reason != c.want {
			t.Errorf("%s at %s: %s, want %s", c.host, c.now, d.Reason, c.want)
		}
	}
	in.Now, in.Host.HostID, in.Override = testNow, later, &PHPOverride{Mode: PHPModeAuto}
	if d := DecidePHP(in); d.Reason != ReasonPHPOffer {
		t.Errorf("override must skip waves: %s", d.Reason)
	}
	if got := PHPWavePercent(Policy{Waves: []int{10, 100}, WaveSoakMinutes: 0}, testNow, testNow); got != 100 {
		t.Errorf("soak 0: %d", got)
	}
}

func TestPHPAgentPolicyNormalize(t *testing.T) {
	p, err := PHPAgentPolicy{Mode: PHPModeAuto, Version: " v0.9.1 ", ExcludeBins: []string{" /usr/bin/php7* "}}.Normalize()
	if err != nil || p.Version != "0.9.1" || p.Reload != PHPReloadNone || p.ExcludeBins[0] != "/usr/bin/php7*" {
		t.Fatalf("normalize = %+v, %v", p, err)
	}
	for _, bad := range []PHPAgentPolicy{
		{Mode: "on"}, {Mode: PHPModeAuto, Version: "latest"}, {Mode: PHPModeAuto, Reload: "restart"},
		{Mode: PHPModeAuto, ExcludeBins: []string{"["}}, {Mode: PHPModeAuto, ExcludeBins: make([]string, 51)},
	} {
		if _, err := bad.Normalize(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	pol := DefaultPolicy()
	pol.PHPAgent = PHPAgentPolicy{}
	if np, err := pol.Normalize(); err != nil || np.PHPAgent.Mode != "" {
		t.Errorf("omitted php_agent must be kept for the manager: %+v %v", np.PHPAgent, err)
	}
}

func TestParsePHPAgentReport(t *testing.T) {
	runtimes := make([]map[string]any, 70)
	for i := range runtimes {
		runtimes[i] = map[string]any{"bin": strings.Repeat("b", 600), "supported": true}
	}
	raw, _ := json.Marshal(map[string]any{"mode": "auto", "capable": true, "managed_by": "fleet", "version": "0.9.1",
		"runtimes": runtimes, "update": map[string]any{"state": "applied", "error": strings.Repeat("e", 5000)}})
	r := ParsePHPAgentReport(raw)
	if r == nil || len(r.Runtimes) != maxPHPRuntimes || len(r.Runtimes[0].Bin) != 512 || len(r.Update.Error) != 2048 || !r.Capable {
		t.Fatalf("report = %+v", r)
	}
	for _, s := range []string{"", "null", "[1]", "{"} {
		if ParsePHPAgentReport([]byte(s)) != nil {
			t.Errorf("%q parsed", s)
		}
	}
	if r := ParsePHPAgentReport([]byte(`{"mode":"manual"}`)); r == nil || r.Runtimes == nil {
		t.Errorf("runtimes must default to []: %+v", r)
	}
}
