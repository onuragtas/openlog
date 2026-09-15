package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet"
)

func TestFleetJavaAgentAPI(t *testing.T) {
	e := newFleetEnv(t)
	p := decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/policy", nil))
	java, ok := p["java_agent"].(map[string]any)
	if !ok || java["mode"] != "manual" || java["version"] != "agent" || java["changed_at"] != nil {
		t.Fatalf("default java_agent = %v", p["java_agent"])
	}

	base := map[string]any{"mode": "auto", "channel": "stable", "target": "latest", "waves": []int{10, 100}, "wave_soak_minutes": 5,
		"halt_failure_rate": 0.1, "maintenance_windows": []any{}}
	with := func(section map[string]any) map[string]any {
		out := map[string]any{"java_agent": section}
		for k, v := range base {
			out[k] = v
		}
		return out
	}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/policy", with(map[string]any{"mode": "always"})); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid java_agent: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/policy", with(map[string]any{"mode": "auto", "version": "agent"})); rec.Code != http.StatusOK {
		t.Fatalf("put policy: %d %s", rec.Code, rec.Body)
	}
	java = decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/policy", nil))["java_agent"].(map[string]any)
	if java["mode"] != "auto" || java["changed_at"] == nil {
		t.Fatalf("stored java_agent = %v", java)
	}
	changedAt := java["changed_at"]
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/policy", base); rec.Code != http.StatusOK {
		t.Fatalf("put policy without java_agent: %d %s", rec.Code, rec.Body)
	}
	java = decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/policy", nil))["java_agent"].(map[string]any)
	if java["mode"] != "auto" || java["changed_at"] != changedAt {
		t.Fatalf("java_agent after a PUT without it = %v", java)
	}

	if err := e.fleet.UpsertHosts(context.Background(), []fleet.HostRecord{{TenantID: "tenant-a", SyncAt: time.Now(), Report: fleet.HostReport{
		HostID: "h1", HostName: "app-1", Version: "0.3.0", OS: "linux", Arch: "amd64", InstallMethod: "tarball", UpdateCapable: true, UpdateState: "idle",
		JavaAgent: &fleet.JavaAgentReport{Mode: "auto", Source: "remote", Capable: true, Managed: true, CurrentVersion: "0.3.0",
			Status: "restart_pending", LinkPath: "/opt/openlog/openlog-javaagent.jar", LinkState: "managed",
			JVMs:   []fleet.JavaJVM{{PID: 42, Name: "java", AgentPath: "/opt/openlog/openlog-javaagent.jar", LoadedVersion: "0.2.0", Managed: true, RestartPending: true}},
			Update: &fleet.JavaAgentUpdate{Operation: "upgrade", Version: "0.3.0", State: "applied"}}}}}); err != nil {
		t.Fatal(err)
	}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/hosts/h1/java-agent", map[string]string{"mode": "maybe"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid override: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/hosts/nope/java-agent", map[string]string{"mode": "off"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown host: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/hosts/h1/java-agent", map[string]string{"mode": "manual"}); rec.Code != http.StatusOK {
		t.Fatalf("set override: %d %s", rec.Code, rec.Body)
	}
	h := decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/hosts", nil))["hosts"].([]any)[0].(map[string]any)["java_agent"].(map[string]any)
	jvms := h["jvms"].([]any)
	if h["reported"] != true || h["mode"] != "manual" || h["status"] != "manual" || h["version"] != "0.3.0" || h["state"] != "restart_pending" ||
		len(jvms) != 1 || jvms[0].(map[string]any)["restart_pending"] != true || h["override"].(map[string]any)["mode"] != "manual" {
		t.Fatalf("host java_agent = %v", h)
	}
	if rec := e.owner.do(http.MethodDelete, "/api/v1/fleet/hosts/h1/java-agent", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete override: %d", rec.Code)
	}
	h = decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/hosts", nil))["hosts"].([]any)[0].(map[string]any)["java_agent"].(map[string]any)
	if h["override"] != nil || h["mode"] != "auto" {
		t.Fatalf("after delete = %v", h)
	}
	got := e.fleet.AuditActions()
	if len(got) < 2 || got[len(got)-2] != "fleet.java_agent_override.set" || got[len(got)-1] != "fleet.java_agent_override.delete" {
		t.Fatalf("audit = %v", got)
	}
}
