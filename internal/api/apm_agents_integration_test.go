//go:build apmga

// ClickHouse integration test of GET /api/v1/apm/agents (schema 0090_apm_agent_versions, D-124): spans with openlog
// and plain OpenTelemetry resources go through the materialized view; the endpoint groups versions per service and
// environment, counts instances, recognises Go instrumentation modules and compares with a release catalog. Same
// environment as apmga_integration_test.go (gaServer).
package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/fleet/testutil"
)

func TestAPMGAAgentVersions(t *testing.T) {
	s, conn, tenantID := gaServer(t)
	now := time.Now().UTC()
	var values []string
	n := 0
	span := func(ts time.Time, svc, env, host, scope string, res map[string]string) {
		n++
		var kv []string
		for k, v := range res {
			kv = append(kv, fmt.Sprintf("'%s','%s'", k, v))
		}
		values = append(values, fmt.Sprintf("('%s', fromUnixTimestamp64Nano(%d), 1000, '%032x', '%016x', '', 'GET /', 'server', 'ok', '%s', '', '%s', '%s', map(%s), '%s')",
			tenantID, ts.UnixNano(), n, n, svc, env, host, strings.Join(kv, ","), scope))
	}
	node := func(ver, inst string) map[string]string {
		return map[string]string{"telemetry.distro.name": "openlog", "telemetry.distro.version": ver, "telemetry.sdk.name": "opentelemetry",
			"telemetry.sdk.language": "nodejs", "telemetry.sdk.version": "1.30.0", "service.instance.id": inst}
	}
	// web/prod: three instances on 0.1.9, one on 0.1.31; web/staging: 0.1.31.
	for i, inst := range []string{"a", "b", "c", "a"} {
		span(now.Add(-time.Duration(i+1)*time.Minute), "web", "prod", "h1", "", node("0.1.9", inst))
	}
	span(now.Add(-2*time.Minute), "web", "prod", "h2", "", node("0.1.31", "d"))
	span(now.Add(-2*time.Minute), "web", "staging", "h3", "", node("0.1.31", "e"))
	// orders: Go agent with gin and grpc, instances by host_id.
	goRes := map[string]string{"telemetry.distro.name": "openlog", "telemetry.distro.version": "0.1.30", "telemetry.sdk.language": "go", "telemetry.sdk.name": "opentelemetry"}
	span(now.Add(-time.Minute), "orders", "prod", "h4", "go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin", goRes)
	span(now.Add(-time.Minute), "orders", "prod", "h5", "go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc", goRes)
	// legacy: plain OpenTelemetry Python; old: outside the range.
	span(now.Add(-time.Minute), "legacy", "prod", "h6", "", map[string]string{"telemetry.sdk.name": "opentelemetry", "telemetry.sdk.language": "python", "telemetry.sdk.version": "1.27.0"})
	span(now.Add(-3*time.Hour), "old", "prod", "h7", "", node("0.1.1", "x"))
	gaExec(t, conn, `INSERT INTO openlog.spans_local (tenant_id, timestamp, duration_ns, trace_id, span_id, parent_span_id, name, kind, status_code,
		service_name, service_namespace, deployment_environment, host_id, resource_attributes, scope_name) VALUES %s`, strings.Join(values, ","))

	type resp struct {
		Release struct {
			Catalog         string  `json:"catalog"`
			Latest          *string `json:"latest"`
			OldestSupported *string `json:"oldest_supported"`
		} `json:"release"`
		Services []struct {
			ServiceName string `json:"service_name"`
			Environment string `json:"environment"`
			Status      string `json:"status"`
			Agents      []struct {
				Kind     string   `json:"kind"`
				Status   string   `json:"status"`
				Modules  []string `json:"instrumentation_modules"`
				Versions []struct {
					Version   string `json:"version"`
					Instances uint64 `json:"instances"`
					Status    string `json:"status"`
				} `json:"versions"`
				Upgrade *struct {
					Command string `json:"command"`
				} `json:"upgrade"`
			} `json:"agents"`
		} `json:"services"`
	}
	path := fmt.Sprintf("/api/v1/apm/agents?from=%d&to=%d", now.Add(-time.Hour).UnixMilli(), now.Add(time.Minute).UnixMilli())

	var out resp
	gaGet(t, s, path, &out)
	if out.Release.Catalog != agentCatalogDisabled || out.Release.Latest != nil {
		t.Errorf("release without catalog = %+v", out.Release)
	}
	var names []string
	for _, sv := range out.Services {
		names = append(names, sv.ServiceName+"/"+sv.Environment+"="+sv.Status)
	}
	if got := strings.Join(names, " "); got != "legacy/prod=third_party orders/prod=unknown web/prod=unknown web/staging=unknown" {
		t.Fatalf("services = %s", got)
	}

	signer, err := testutil.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	snap, err := testutil.Snapshot(signer, testutil.ReleaseSpec{Version: "0.1.31", OldestSupportedAgent: "0.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetAgentReleases(func() *catalog.Snapshot { return snap })
	out = resp{}
	gaGet(t, s, path+"&environment=prod", &out)
	if out.Release.Catalog != agentCatalogOK || *out.Release.Latest != "0.1.31" || *out.Release.OldestSupported != "0.1.20" {
		t.Errorf("release = %+v", out.Release)
	}
	if len(out.Services) != 3 {
		t.Fatalf("prod services = %+v", out.Services)
	}
	legacy, orders, web := out.Services[0], out.Services[1], out.Services[2]
	if legacy.Agents[0].Kind != agentKindThirdParty || legacy.Agents[0].Versions[0].Version != "1.27.0" || legacy.Agents[0].Upgrade != nil {
		t.Errorf("legacy = %+v", legacy)
	}
	if o := orders.Agents[0]; orders.Status != agentStatusOutdated || o.Kind != agentKindGo || strings.Join(o.Modules, ",") != "gin,grpc" ||
		o.Versions[0].Instances != 2 || o.Upgrade == nil || !strings.Contains(o.Upgrade.Command, "/instrumentation/grpc@v0.1.31") {
		t.Errorf("orders = %+v", orders)
	}
	w := web.Agents[0]
	if web.Status != agentStatusUnsupported || w.Kind != agentKindNode || len(w.Versions) != 2 ||
		w.Versions[0].Version != "0.1.31" || w.Versions[0].Instances != 1 || w.Versions[0].Status != agentStatusOK ||
		w.Versions[1].Version != "0.1.9" || w.Versions[1].Instances != 3 || w.Versions[1].Status != agentStatusUnsupported || w.Upgrade == nil {
		t.Errorf("web = %+v", web)
	}

	// One service, without upgrade instructions.
	out = resp{}
	gaGet(t, s, path+"&service=web&environment=staging&upgrade=false", &out)
	if len(out.Services) != 1 || out.Services[0].Status != agentStatusOK || out.Services[0].Agents[0].Upgrade != nil {
		t.Errorf("web staging = %+v", out.Services)
	}
}
