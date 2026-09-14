package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet"
)

func TestFleetPHPAgentAPI(t *testing.T) {
	e := newFleetEnv(t)
	p := decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/policy", nil))
	php, ok := p["php_agent"].(map[string]any)
	if !ok || php["mode"] != "manual" || php["version"] != "agent" || php["reload"] != "none" || php["changed_at"] != nil {
		t.Fatalf("default php_agent = %v", p["php_agent"])
	}

	base := map[string]any{"mode": "auto", "channel": "stable", "target": "latest", "waves": []int{10, 100}, "wave_soak_minutes": 5,
		"halt_failure_rate": 0.1, "maintenance_windows": []any{}}
	bad := map[string]any{}
	for k, v := range base {
		bad[k] = v
	}
	bad["php_agent"] = map[string]any{"mode": "sometimes"}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/policy", bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid php_agent: %d %s", rec.Code, rec.Body)
	}
	good := map[string]any{}
	for k, v := range base {
		good[k] = v
	}
	good["php_agent"] = map[string]any{"mode": "auto", "version": "agent", "reload": "graceful", "exclude_bins": []string{"/usr/bin/php7*"}}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/policy", good); rec.Code != http.StatusOK {
		t.Fatalf("put policy: %d %s", rec.Code, rec.Body)
	}
	p = decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/policy", nil))
	php = p["php_agent"].(map[string]any)
	if php["mode"] != "auto" || php["reload"] != "graceful" || php["changed_at"] == nil {
		t.Fatalf("stored php_agent = %v", php)
	}
	changedAt := php["changed_at"]
	// A PUT without php_agent (older clients) keeps the section and its wave start.
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/policy", base); rec.Code != http.StatusOK {
		t.Fatalf("put policy without php_agent: %d %s", rec.Code, rec.Body)
	}
	php = decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/policy", nil))["php_agent"].(map[string]any)
	if php["mode"] != "auto" || php["changed_at"] != changedAt {
		t.Fatalf("php_agent after a PUT without it = %v", php)
	}

	if err := e.fleet.UpsertHosts(context.Background(), []fleet.HostRecord{{TenantID: "tenant-a", SyncAt: time.Now(), Report: fleet.HostReport{
		HostID: "h1", HostName: "web-1", Version: "0.3.0", OS: "linux", Arch: "amd64", InstallMethod: "tarball", UpdateCapable: true, UpdateState: "idle",
		PHPAgent: &fleet.PHPAgentReport{Mode: "auto", Capable: true, ManagedBy: "fleet", Version: "0.3.0",
			Runtimes: []fleet.PHPRuntime{{Bin: "/usr/sbin/php-fpm8.2", Version: "8.2.29", Supported: true, Enabled: true, Loaded: true}},
			Update:   &fleet.PHPAgentUpdate{Operation: "install", Version: "0.3.0", State: "applied"}}}}}); err != nil {
		t.Fatal(err)
	}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/hosts/h1/php-agent", map[string]string{"mode": "maybe"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid override: %d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPut, "/api/v1/fleet/hosts/nope/php-agent", map[string]string{"mode": "off"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown host: %d %s", rec.Code, rec.Body)
	}
	rec := e.owner.do(http.MethodPut, "/api/v1/fleet/hosts/h1/php-agent", map[string]string{"mode": "off"})
	if rec.Code != http.StatusOK || decode[map[string]any](t, rec)["mode"] != "off" {
		t.Fatalf("set override: %d %s", rec.Code, rec.Body)
	}

	hosts := decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/hosts", nil))["hosts"].([]any)
	h := hosts[0].(map[string]any)["php_agent"].(map[string]any)
	if h["reported"] != true || h["mode"] != "off" || h["status"] != "mode_off" || h["version"] != "0.3.0" || h["managed_by"] != "fleet" ||
		len(h["runtimes"].([]any)) != 1 || h["override"].(map[string]any)["mode"] != "off" || h["update"].(map[string]any)["state"] != "applied" {
		t.Fatalf("host php_agent = %v", h)
	}

	if rec := e.owner.do(http.MethodDelete, "/api/v1/fleet/hosts/h1/php-agent", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete override: %d", rec.Code)
	}
	h = decode[map[string]any](t, e.owner.do(http.MethodGet, "/api/v1/fleet/hosts", nil))["hosts"].([]any)[0].(map[string]any)["php_agent"].(map[string]any)
	if h["override"] != nil || h["mode"] != "auto" {
		t.Fatalf("after delete = %v", h)
	}

	want := []string{"fleet.policy.update", "fleet.policy.update", "fleet.php_agent_override.set", "fleet.php_agent_override.delete"}
	got := e.fleet.AuditActions()
	if len(got) != len(want) {
		t.Fatalf("audit = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("audit = %v, want %v", got, want)
		}
	}
}
