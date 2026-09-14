package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

func TestK8sWorkloadHealth(t *testing.T) {
	cases := []struct {
		kind                        string
		desired, available, updated int64
		reporting                   bool
		want                        string
	}{
		{"Deployment", 3, 3, 3, true, "healthy"},
		{"Deployment", 0, 0, 0, true, "healthy"},
		{"Deployment", 3, 0, 0, true, "unavailable"},
		{"StatefulSet", 3, 2, 3, true, "degraded"},
		{"DaemonSet", 5, 6, 5, true, "healthy"},
		{"Deployment", 3, 3, 3, false, "unknown"},
		{"Job", 1, 0, 1, true, "degraded"},
		{"Job", 1, 1, 1, true, "healthy"},
		{"Job", 1, 0, 0, true, "healthy"},
		{"CronJob", 0, 0, 0, true, "healthy"},
	}
	for _, c := range cases {
		if got := workloadHealth(c.kind, c.desired, c.available, c.updated, c.reporting); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
}

func TestK8sHelpers(t *testing.T) {
	now := time.Unix(1_757_757_600, 0)
	vals := []k8sLatest{{v: 1, t: now}, {v: 2, t: now.Add(-time.Minute)}, {v: 100, t: now.Add(-10 * time.Minute)}}
	if got := sumCurrent(vals); got == nil || *got != 3 {
		t.Errorf("sumCurrent %v", got)
	}
	if sumCurrent(nil) != nil {
		t.Error("sumCurrent(nil) not nil")
	}
	if !current(now.Unix()-60, now) || current(now.Unix()-300, now) {
		t.Error("current")
	}
	cs := parsePodContainers(`[{"name":"app","container_id":"AB","image":"nginx:1","ready":true,"restarts":3,"state":"running"},
		{"name":"side","ready":"false","restarts":"2","reason":"CrashLoopBackOff","state":"waiting"}]`)
	if len(cs) != 2 || cs[0].ContainerID != "ab" || !cs[0].Ready || cs[0].Restarts != 3 || cs[1].Ready || cs[1].Restarts != 2 || cs[1].Reason != "CrashLoopBackOff" {
		t.Errorf("containers %+v %+v", cs[0], cs[1])
	}
	if len(parsePodContainers("not json")) != 0 || len(parsePodContainers("")) != 0 {
		t.Error("invalid containers JSON")
	}
	if !matchTerms("ORD shop", "orders-1", "shop") || matchTerms("x", "orders") || !matchTerms("", "a") {
		t.Error("matchTerms")
	}
	if got := splitList("control-plane, master,"); !reflect.DeepEqual(got, []string{"control-plane", "master"}) {
		t.Errorf("splitList %v", got)
	}
	var c k8sPodPhaseCounts
	c.add("Running", 2)
	c.add("Weird", 1)
	if c.Running != 2 || c.Unknown != 1 {
		t.Errorf("phase counts %+v", c)
	}
	byKind := countByKind([]*k8sWorkloadJSON{{Kind: "Deployment", Health: "healthy"}, {Kind: "Deployment", Health: "degraded"}, {Kind: "Job", Health: "unknown"}})
	if len(byKind) != 2 || byKind[0].Kind != "Deployment" || byKind[0].Total != 2 || byKind[0].Degraded != 1 || byKind[1].Unknown != 1 {
		t.Errorf("by kind %+v %+v", byKind[0], byKind[1])
	}
}

var k8sPaths = []string{
	"/api/v1/kubernetes/clusters",
	"/api/v1/kubernetes/clusters/cu1",
	"/api/v1/kubernetes/nodes?cluster_uid=cu1&q=worker",
	"/api/v1/kubernetes/workloads?cluster_uid=cu1&namespace=shop&kind=Deployment&health=degraded&q=ord",
	"/api/v1/kubernetes/workloads/cu1/shop/Deployment/orders",
	"/api/v1/kubernetes/workloads/cu1/shop/Deployment/orders/timeseries?step=30s",
	"/api/v1/kubernetes/pods?cluster_uid=cu1&namespace=shop&node=n1&workload_kind=Deployment&workload_name=orders&phase=Running&q=x",
	"/api/v1/kubernetes/pods/pu1",
	"/api/v1/kubernetes/pods/pu1/timeseries",
	"/api/v1/kubernetes/pods/pu1/events",
	"/api/v1/kubernetes/events?cluster_uid=cu1&namespace=shop&type=Warning&object_kind=Pod&object_name=orders-1&object_uid=pu1&reason=BackOff&limit=5000",
	"/api/v1/apm/services/orders/kubernetes?environment=prod",
	"/api/v1/logs?k8s_pod_uid=pu1",
}

// TestKubernetesEndpointsTenantScoped issues every Kubernetes request and checks that every table reference is
// followed by the bound tenant predicate (like TestEveryEndpointIsTenantScoped).
func TestKubernetesEndpointsTenantScoped(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	for _, p := range k8sPaths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("openlog-license-key", "key-a")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Errorf("%s: status %d %s", p, rec.Code, rec.Body)
		}
	}
	if len(conn.sql) < len(k8sPaths) {
		t.Fatalf("only %d statements executed", len(conn.sql))
	}
	tables := map[string]bool{}
	for _, sql := range conn.sql {
		refs := tableRef.FindAllStringSubmatchIndex(sql, -1)
		if len(refs) == 0 {
			t.Errorf("statement without table: %s", sql)
		}
		for _, r := range refs {
			tables[sql[r[2]:r[3]]] = true
			if !strings.HasPrefix(sql[r[1]:], " WHERE (tenant_id = {tenant_id:String})") {
				t.Errorf("unscoped table reference: %s", sql)
			}
		}
	}
	// Metric reads only run for entities that exist (empty results here); they are covered by the entity-less
	// helpers' callers in the ClickHouse check.
	for _, want := range []string{"k8s_clusters", "k8s_nodes", "k8s_workloads", "k8s_pods", "logs", "spans", "apm_service_containers"} {
		if !tables[want] {
			t.Errorf("no statement read %s", want)
		}
	}
}

func TestKubernetesParameterErrors(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	do := func(path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer key-a")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	long := strings.Repeat("x", 300)
	for path, want := range map[string]int{
		"/api/v1/kubernetes/clusters":                                           200,
		"/api/v1/kubernetes/clusters/cu1":                                       404,
		"/api/v1/kubernetes/nodes?q=" + long:                                    400,
		"/api/v1/kubernetes/workloads?kind=Service":                             400,
		"/api/v1/kubernetes/workloads?health=bad":                               400,
		"/api/v1/kubernetes/workloads/cu1/shop/Service/orders":                  400,
		"/api/v1/kubernetes/workloads/cu1/shop/Deployment/orders":               404,
		"/api/v1/kubernetes/workloads/cu1/shop/Deployment/orders/timeseries":    404,
		"/api/v1/kubernetes/workloads/cu1/shop/Deployment/x/timeseries?step=1s": 400,
		"/api/v1/kubernetes/pods?phase=Sleeping":                                400,
		"/api/v1/kubernetes/pods?workload_kind=Service":                         400,
		"/api/v1/kubernetes/pods/pu1":                                           404,
		"/api/v1/kubernetes/pods/pu1/timeseries":                                404,
		"/api/v1/kubernetes/pods/pu1/events":                                    200,
		"/api/v1/kubernetes/events?type=Error":                                  400,
		"/api/v1/kubernetes/events?limit=0":                                     400,
		"/api/v1/kubernetes/events":                                             200,
		"/api/v1/apm/services/orders/kubernetes":                                200,
	} {
		if got, body := do(path); got != want {
			t.Errorf("%s: %d, want %d (%s)", path, got, want, body)
		}
	}
	// Empty lists are arrays, not null.
	if code, body := do("/api/v1/kubernetes/pods"); code != 200 || !strings.Contains(body, `"pods":[]`) {
		t.Errorf("pods: %d %s", code, body)
	}
}

// TestK8sMetricQueriesBuild runs the metric readers that only execute for existing entities, so that fragments the
// query layer rejects (e.g. literal openlog.* attribute keys) fail here instead of in production.
func TestK8sMetricQueriesBuild(t *testing.T) {
	s, conn := newTestServer(t)
	res, _ := s.db.Scope("tenant-a")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	now := time.Now()
	from := now.Add(-time.Hour)
	wls := []*k8sWorkloadJSON{{ClusterUID: "cu1", ClusterName: "prod", Namespace: "shop", Kind: "Deployment", Name: "orders", lastSeen: now}}
	checks := map[string]error{
		"addWorkloadPods":  s.addWorkloadPods(r, res, wls, from, now),
		"addWorkloadStats": s.addWorkloadStats(r, res, wls, from, now),
		"addPodStats":      s.addPodStats(r, res, []*k8sPodJSON{{PodUID: "pu1"}}, from, now),
		"addNodeStats":     s.addNodeStats(r, res, []*k8sNodeJSON{{ClusterUID: "cu1", ClusterName: "prod", NodeName: "n1"}}, from, now),
	}
	_, err := s.restartSeries(r, res, from, now, time.Minute, func(q *query.Select) { bindWorkloadKeys(q) })
	checks["restartSeries"] = err
	_, err = s.clusterSummary(r, res, []*k8sClusterJSON{{ClusterUID: "cu1", lastSeen: now}}, from, now)
	checks["clusterSummary"] = err
	for name, err := range checks {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, sql := range conn.sql {
		for _, r := range tableRef.FindAllStringIndex(sql, -1) {
			if !strings.HasPrefix(sql[r[1]:], " WHERE (tenant_id = {tenant_id:String})") {
				t.Errorf("unscoped table reference: %s", sql)
			}
		}
	}
}
