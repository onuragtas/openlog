package fleet_test

import (
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/fleet/testutil"
)

func TestDecideJava(t *testing.T) {
	s, _ := testutil.NewSigner()
	released := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	snap, err := testutil.Snapshot(s,
		testutil.ReleaseSpec{Version: "0.9.1", JavaAgent: true, ReleasedAt: released},
		testutil.ReleaseSpec{Version: "0.9.2", ReleasedAt: released})
	if err != nil {
		t.Fatal(err)
	}
	now := released.Add(30 * 24 * time.Hour)
	host := func(cur string, capable bool) fleet.HostReport {
		return fleet.HostReport{HostID: "h1", Version: "0.9.1", OS: "linux", Arch: "amd64",
			JavaAgent: &fleet.JavaAgentReport{Capable: capable, CurrentVersion: cur}}
	}
	pol := fleet.DefaultPolicy()
	decide := func(h fleet.HostReport, mode, version string, ov *fleet.JavaOverride) fleet.JavaDecision {
		p := pol
		p.JavaAgent = fleet.JavaAgentPolicy{Mode: mode, Version: version}
		return fleet.DecideJava(fleet.JavaInput{Now: now, Host: h, Policy: p, Override: ov, Catalog: snap})
	}
	for _, tc := range []struct {
		name string
		d    fleet.JavaDecision
		want fleet.JavaReason
	}{
		{"default manual", fleet.DecideJava(fleet.JavaInput{Now: now, Host: host("", true), Policy: fleet.Policy{}, Catalog: snap}), fleet.ReasonJavaManual},
		{"off", decide(host("", true), "off", "agent", nil), fleet.ReasonJavaModeOff},
		{"override auto", decide(host("", true), "manual", "agent", &fleet.JavaOverride{Mode: "auto"}), fleet.ReasonJavaOffer},
		{"offer", decide(host("", true), "auto", "agent", nil), fleet.ReasonJavaOffer},
		{"up to date", decide(host("0.9.1", true), "auto", "agent", nil), fleet.ReasonJavaUpToDate},
		{"not capable", decide(host("", false), "auto", "agent", nil), fleet.ReasonJavaNotCapable},
		{"not reported", decide(fleet.HostReport{HostID: "h1", Version: "0.9.1"}, "auto", "agent", nil), fleet.ReasonJavaNotReported},
		{"no artifact", decide(host("", true), "auto", "0.9.2", nil), fleet.ReasonJavaNoArtifact},
		{"unavailable", decide(host("", true), "auto", "1.0.0", nil), fleet.ReasonJavaTargetUnavailable},
		{"invalid agent version", decide(fleet.HostReport{HostID: "h1", Version: "dev", JavaAgent: &fleet.JavaAgentReport{Capable: true}}, "auto", "agent", nil), fleet.ReasonJavaInvalidVersion},
	} {
		if tc.d.Reason != tc.want {
			t.Errorf("%s: reason %s, want %s", tc.name, tc.d.Reason, tc.want)
		}
	}
	rolled := host("", true)
	rolled.JavaAgent.Update = &fleet.JavaAgentUpdate{Version: "0.9.1", State: "rolled_back"}
	if d := decide(rolled, "auto", "agent", nil); d.Reason != fleet.ReasonJavaAlreadyFailed {
		t.Errorf("rolled back: %s", d.Reason)
	}
	if _, err := (fleet.JavaAgentPolicy{Mode: "sometimes"}).Normalize(); err == nil {
		t.Error("invalid mode accepted")
	}
	if p, err := (fleet.JavaAgentPolicy{Mode: "auto", Version: " v1.2.3 "}).Normalize(); err != nil || p.Version != "1.2.3" {
		t.Errorf("normalize = %+v %v", p, err)
	}
}

func TestParseJavaAgentReport(t *testing.T) {
	if fleet.ParseJavaAgentReport(nil) != nil || fleet.ParseJavaAgentReport([]byte("null")) != nil || fleet.ParseJavaAgentReport([]byte("{")) != nil {
		t.Fatal("absent or invalid report parsed")
	}
	jvms := strings.Repeat(`{"pid":1,"name":"java","command":"`+strings.Repeat("x", 2000)+`"},`, 70)
	r := fleet.ParseJavaAgentReport([]byte(`{"status":"installed","jvms":[` + strings.TrimSuffix(jvms, ",") + `]}`))
	if r == nil || len(r.JVMs) != 64 || len(r.JVMs[0].Command) != 512 {
		t.Fatalf("bounds: %d jvms", len(r.JVMs))
	}
}
