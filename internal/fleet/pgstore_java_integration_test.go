//go:build integration

package fleet_test

import (
	"context"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet"
)

// TestPGStoreJavaAgent covers 0086_java_agent_fleet: the policy's java_agent section, per-host overrides and the
// java_agent report stored with the host.
func TestPGStoreJavaAgent(t *testing.T) {
	ctx := context.Background()
	st := fleet.NewPGStore(pgPool)
	org, tenant, user := newOrg(t)

	sp, err := st.GetPolicy(ctx, org)
	if err != nil || sp.JavaAgent.Mode != fleet.JavaModeManual || sp.JavaAgent.Version != fleet.JavaVersionAgent {
		t.Fatalf("default java_agent = %+v, %v", sp.JavaAgent, err)
	}
	// A policy stored without the section (an older api, '{}') reads as the defaults.
	p := fleet.DefaultPolicy()
	if err := st.PutPolicy(ctx, org, p, user, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := pgPool.Exec(ctx, `UPDATE agent_update_policies SET java_agent = '{}' WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if sp, err = st.GetPolicy(ctx, org); err != nil || sp.JavaAgent.Mode != fleet.JavaModeManual {
		t.Fatalf("'{}' java_agent = %+v, %v", sp.JavaAgent, err)
	}
	changed := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	p.JavaAgent = fleet.JavaAgentPolicy{Mode: fleet.JavaModeAuto, Version: "0.9.1", ChangedAt: &changed}
	if err := st.PutPolicy(ctx, org, p, user, time.Now()); err != nil {
		t.Fatal(err)
	}
	sp, err = st.GetPolicy(ctx, org)
	if err != nil || sp.JavaAgent.Mode != fleet.JavaModeAuto || sp.JavaAgent.Version != "0.9.1" || sp.JavaAgent.ChangedAt == nil ||
		!sp.JavaAgent.ChangedAt.Equal(changed) || sp.PHPAgent.Mode != fleet.PHPModeManual {
		t.Fatalf("stored java_agent = %+v (php %+v), %v", sp.JavaAgent, sp.PHPAgent, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := st.PutJavaOverride(ctx, org, fleet.JavaOverride{HostID: "h1", Mode: fleet.JavaModeOff, UpdatedAt: now}, user); err != nil {
		t.Fatal(err)
	}
	if err := st.PutJavaOverride(ctx, org, fleet.JavaOverride{HostID: "h1", Mode: fleet.JavaModeAuto, UpdatedAt: now}, user); err != nil {
		t.Fatal(err)
	}
	ovs, err := st.ListJavaOverrides(ctx, org)
	if err != nil || len(ovs) != 1 || ovs["h1"].Mode != fleet.JavaModeAuto || !ovs["h1"].UpdatedAt.Equal(now) {
		t.Fatalf("java overrides = %+v, %v", ovs, err)
	}
	if err := st.PutJavaOverride(ctx, org, fleet.JavaOverride{HostID: "h2", Mode: "sometimes", UpdatedAt: now}, user); err == nil {
		t.Error("invalid mode stored")
	}
	state, err := st.LoadOrgState(ctx, tenant)
	if err != nil || state.JavaOverrides["h1"].Mode != fleet.JavaModeAuto || state.Policy.JavaAgent.Mode != fleet.JavaModeAuto {
		t.Fatalf("org state = %+v, %v", state.JavaOverrides, err)
	}
	if ok, err := st.DeleteJavaOverride(ctx, org, "h1"); err != nil || !ok {
		t.Fatalf("delete = %v, %v", ok, err)
	}
	if ok, _ := st.DeleteJavaOverride(ctx, org, "h1"); ok {
		t.Error("second delete reported a row")
	}

	rep := fleet.HostReport{HostID: "h1", HostName: "app-1", Version: "0.9.1", OS: "linux", Arch: "amd64", UpdateState: fleet.StateIdle,
		JavaAgent: &fleet.JavaAgentReport{Mode: "auto", Source: "remote", Capable: true, Managed: true, CurrentVersion: "0.9.1",
			Status: "restart_pending", LinkPath: "/opt/openlog/openlog-javaagent.jar", LinkState: "managed",
			JVMs:   []fleet.JavaJVM{{PID: 4242, Name: "java", AgentPath: "/opt/openlog/openlog-javaagent.jar", LoadedVersion: "0.9.0", Managed: true, RestartPending: true}},
			Update: &fleet.JavaAgentUpdate{Operation: "upgrade", Version: "0.9.1", State: "applied"}}}
	if err := st.UpsertHosts(ctx, []fleet.HostRecord{{TenantID: tenant, Report: rep, SyncAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	h, err := st.GetHost(ctx, org, "h1")
	if err != nil || h.JavaAgent == nil || h.JavaAgent.Status != "restart_pending" || len(h.JavaAgent.JVMs) != 1 ||
		!h.JavaAgent.JVMs[0].RestartPending || h.JavaAgent.Update == nil || h.PHPAgent != nil {
		t.Fatalf("stored host java_agent = %+v, %v", h.JavaAgent, err)
	}
	// An agent that stops reporting the section clears it.
	rep.JavaAgent = nil
	if err := st.UpsertHosts(ctx, []fleet.HostRecord{{TenantID: tenant, Report: rep, SyncAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if h, err = st.GetHost(ctx, org, "h1"); err != nil || h.JavaAgent != nil {
		t.Fatalf("cleared java_agent = %+v, %v", h.JavaAgent, err)
	}
}
