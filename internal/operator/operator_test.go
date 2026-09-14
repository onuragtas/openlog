package operator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/quota"
)

type fakeGateSource struct {
	suspended map[string]string
	limits    map[string]*HostLimitState
	err       error
	saved     map[string]int
}

func (f *fakeGateSource) SuspendedTenants(context.Context) (map[string]string, map[string]string, error) {
	return f.suspended, nil, f.err
}

func (f *fakeGateSource) LoadHostLimits(context.Context) (map[string]*HostLimitState, error) {
	// A copy: the gate keeps the maps.
	out := map[string]*HostLimitState{}
	for t, l := range f.limits {
		known := map[string]struct{}{}
		for h := range l.Known {
			known[h] = struct{}{}
		}
		out[t] = &HostLimitState{Limit: l.Limit, Known: known}
	}
	return out, f.err
}

func (f *fakeGateSource) SaveSourceCounts(_ context.Context, _ time.Time, _ string, counts map[string]int) error {
	if f.saved == nil {
		f.saved = map[string]int{}
	}
	for t, n := range counts {
		f.saved[t] = n
	}
	return f.err
}

func set(vs ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, v := range vs {
		out[v] = struct{}{}
	}
	return out
}

func TestIngestGateHostLimit(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	src := &fakeGateSource{suspended: map[string]string{"bad": "abuse"}, limits: map[string]*HostLimitState{
		"acme": {Limit: 2, Known: set("h1")},
		"down": {Limit: 1, Known: set("a", "b", "c")}, // downgraded: above the limit
	}}
	g := NewIngestGate(src, GateOptions{Now: func() time.Time { return now }, Instance: "pod-1"})

	// Before the first load everything is admitted (fail open).
	if rej, _ := g.RejectHosts("acme", []string{"x", "y", "z"}); len(rej) != 0 {
		t.Fatalf("not loaded: %v", rej)
	}
	if _, ok := g.Suspended("bad"); ok {
		t.Fatal("not loaded: suspended")
	}
	if err := g.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Suspended("bad"); !ok {
		t.Error("bad is suspended")
	}
	if _, ok := g.Suspended("acme"); ok {
		t.Error("acme is active")
	}
	// h1 known; h2 admitted (1 known + 0 local < 2); h3 rejected.
	rej, limit := g.RejectHosts("acme", []string{"h1", "h2", "h3"})
	if limit != 2 || len(rej) != 1 || !rej["h3"] {
		t.Fatalf("acme: %v limit %d", rej, limit)
	}
	// h2 stays admitted; still no room for h3.
	if rej, _ := g.RejectHosts("acme", []string{"h2", "h3"}); len(rej) != 1 || !rej["h3"] {
		t.Fatalf("acme again: %v", rej)
	}
	// Known hosts above the limit keep reporting; new ones are rejected.
	if rej, _ := g.RejectHosts("down", []string{"a", "b", "c", "d"}); len(rej) != 1 || !rej["d"] {
		t.Fatalf("down: %v", rej)
	}
	// Tenants without a limit are never limited.
	if rej, _ := g.RejectHosts("free-for-all", []string{"1", "2", "3"}); len(rej) != 0 {
		t.Fatalf("unlimited: %v", rej)
	}
	// The local admission survives a refresh until the host is known or the TTL passes.
	now = now.Add(5 * time.Minute)
	if err := g.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rej, _ := g.RejectHosts("acme", []string{"h2", "h3"}); !rej["h3"] || rej["h2"] {
		t.Fatalf("after refresh: %v", rej)
	}
	now = now.Add(20 * time.Minute)
	if err := g.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rej, _ := g.RejectHosts("acme", []string{"h3"}); len(rej) != 0 {
		t.Fatalf("local admission of h2 expired, h3 fits: %v", rej)
	}
	// Stale state (PostgreSQL unreachable for longer than MaxStale) is not enforced.
	src.err = errors.New("down")
	now = now.Add(time.Hour)
	_ = g.Refresh(context.Background())
	if rej, _ := g.RejectHosts("down", []string{"zz"}); len(rej) != 0 {
		t.Fatalf("stale: %v", rej)
	}
}

func TestIngestGateSourceCounts(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	src := &fakeGateSource{}
	g := NewIngestGate(src, GateOptions{Now: func() time.Time { return now }, MaxSourcesPerTenant: 3})
	for _, a := range []string{"10.0.0.1:1234", "10.0.0.1:999", "10.0.0.2:1", "[::1]:5", "10.0.0.9:1"} {
		g.ObserveSource("acme", a)
	}
	if err := g.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.saved["acme"] != 3 {
		t.Errorf("saved %v (distinct, capped at 3)", src.saved)
	}
}

func TestEvaluateAbuse(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	cfg := config.SaaS{AbuseIngestMultiplier: 10, AbuseNewOrgDays: 7, AbuseNewOrgHosts: 50, AbuseSourceIPs: 200}
	plan := quota.Plan{ID: "free", Limits: quota.Limits{IngestGBMonth: 73}} // 0.1 GiB per hour
	base := AbuseInput{OrgID: "o", Plan: plan, CreatedAt: now.Add(-30 * 24 * time.Hour)}

	if f := EvaluateAbuse(base, cfg, now); len(f) != 0 {
		t.Fatalf("quiet org: %v", f)
	}
	in := base
	in.HourIngestBytes = quota.GiB * 101 / 100 // > 10 × 0.1 GiB
	if f := EvaluateAbuse(in, cfg, now); len(f) != 1 || f[0].Kind != FlagIngestSpike {
		t.Fatalf("spike: %v", f)
	}
	in.HourIngestBytes = quota.GiB * 99 / 100
	if f := EvaluateAbuse(in, cfg, now); len(f) != 0 {
		t.Fatalf("below spike threshold: %v", f)
	}
	unlimited := base
	unlimited.Plan = quota.Plan{ID: "enterprise"}
	unlimited.HourIngestBytes = 1 << 50
	if f := EvaluateAbuse(unlimited, cfg, now); len(f) != 0 {
		t.Fatalf("unlimited plans have no spike threshold: %v", f)
	}
	hosts := base
	hosts.ActiveHosts = 51
	if f := EvaluateAbuse(hosts, cfg, now); len(f) != 0 {
		t.Fatalf("old org with many hosts: %v", f)
	}
	hosts.CreatedAt = now.Add(-2 * 24 * time.Hour)
	if f := EvaluateAbuse(hosts, cfg, now); len(f) != 1 || f[0].Kind != FlagNewOrgHosts {
		t.Fatalf("new org hosts: %v", f)
	}
	ips := base
	ips.SourceIPs = 201
	if f := EvaluateAbuse(ips, cfg, now); len(f) != 1 || f[0].Kind != FlagIngestSourceIPs {
		t.Fatalf("source ips: %v", f)
	}
	off := config.SaaS{AbuseNewOrgDays: 7}
	all := hosts
	all.HourIngestBytes, all.SourceIPs = 1<<40, 1000
	if f := EvaluateAbuse(all, off, now); len(f) != 0 {
		t.Fatalf("disabled thresholds: %v", f)
	}
}

func TestDaysLeft(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]int{time.Hour: 1, 24 * time.Hour: 1, 25 * time.Hour: 2, 72 * time.Hour: 3, 7*24*time.Hour - time.Minute: 7} {
		if got := daysLeft(now.Add(d), now); got != want {
			t.Errorf("daysLeft(%s) = %d, want %d", d, got, want)
		}
	}
}
