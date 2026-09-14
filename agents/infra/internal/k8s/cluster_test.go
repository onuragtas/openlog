package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

func TestClusterMetrics(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.set("GET", "/version", `{"gitVersion":"v1.37.0"}`)
	f.set("GET", "/api/v1/nodes", `{"items":[{"metadata":{"name":"node-a","uid":"n1","creationTimestamp":"2026-09-01T00:00:00Z","labels":{"node-role.kubernetes.io/control-plane":"","kubernetes.io/os":"linux"}},
	  "spec":{"unschedulable":true},
	  "status":{"allocatable":{"cpu":"3500m","memory":"8Gi","pods":"110"},"addresses":[{"type":"InternalIP","address":"172.18.0.2"}],
	   "conditions":[{"type":"Ready","status":"True"},{"type":"MemoryPressure","status":"False"},{"type":"DiskPressure","status":"Unknown"},{"type":"Foo","status":"True"}],
	   "nodeInfo":{"kubeletVersion":"v1.37.0","osImage":"Debian","containerRuntimeVersion":"containerd://2.1"}}}]}`)
	cronPod := `{"metadata":{"name":"backup-1-xyz","namespace":"ops","uid":"p3","ownerReferences":[{"kind":"Job","name":"backup-1","uid":"j1","controller":true}]},"spec":{"nodeName":"node-a","containers":[{"name":"b","image":"busybox"}]},"status":{"phase":"Succeeded","containerStatuses":[{"name":"b","containerID":"containerd://c3","state":{"terminated":{"reason":"Completed"}}}]}}`
	evicted := `{"metadata":{"name":"old","namespace":"shop","uid":"p4"},"spec":{"containers":[{"name":"x","image":"x"}]},"status":{"phase":"Failed","reason":"Evicted"}}`
	f.set("GET", "/api/v1/pods", `{"items":[`+podDeploy+`,`+cronPod+`,`+evicted+`]}`)
	f.set("GET", "/apis/apps/v1/deployments", `{"items":[{"metadata":{"name":"web","namespace":"shop","uid":"d1","creationTimestamp":"2026-09-02T00:00:00Z"},"spec":{"replicas":3},"status":{"readyReplicas":1,"availableReplicas":1,"updatedReplicas":3}}]}`)
	f.set("GET", "/apis/apps/v1/replicasets", `{"items":[{"metadata":{"name":"web-7c9f8d6b5","namespace":"shop","uid":"rs-uid","ownerReferences":[{"kind":"Deployment","name":"web","uid":"d1","controller":true}]},"spec":{"replicas":3}},
	  {"metadata":{"name":"lonely","namespace":"shop","uid":"rs2"},"spec":{"replicas":2},"status":{"availableReplicas":2,"readyReplicas":2}}]}`)
	f.set("GET", "/apis/apps/v1/statefulsets", `{"items":[{"metadata":{"name":"db","namespace":"shop","uid":"s1"},"spec":{"replicas":2},"status":{"readyReplicas":2,"availableReplicas":2,"currentReplicas":2,"updatedReplicas":2}}]}`)
	f.set("GET", "/apis/apps/v1/daemonsets", `{"items":[{"metadata":{"name":"agent","namespace":"openlog","uid":"ds1"},"status":{"desiredNumberScheduled":3,"currentNumberScheduled":3,"numberReady":2,"numberAvailable":2,"updatedNumberScheduled":3}}]}`)
	f.set("GET", "/apis/batch/v1/jobs", `{"items":[{"metadata":{"name":"backup-1","namespace":"ops","uid":"j1","ownerReferences":[{"kind":"CronJob","name":"backup","uid":"cj1","controller":true}]},"spec":{"completions":1},"status":{"succeeded":1}},
	  {"metadata":{"name":"migrate","namespace":"ops","uid":"j2"},"spec":{"completions":2,"parallelism":1},"status":{"failed":3,"succeeded":1,"active":1}}]}`)
	f.set("GET", "/apis/batch/v1/cronjobs", `{"items":[{"metadata":{"name":"backup","namespace":"ops","uid":"cj1"},"spec":{"suspend":true},"status":{"active":[{"name":"backup-1"}]}}]}`)
	f.status["GET /apis/autoscaling/v2/horizontalpodautoscalers"] = 403 // RBAC without HPA: skipped
	f.set("GET", "/api/v1/namespaces", `{"items":[{"metadata":{"name":"shop"},"status":{"phase":"Active"}},{"metadata":{"name":"gone"},"status":{"phase":"Terminating"}}]}`)
	f.set("GET", "/api/v1/resourcequotas", `{"items":[{"metadata":{"name":"q","namespace":"shop","uid":"q1"},"status":{"hard":{"limits.cpu":"4","pods":"10"},"used":{"limits.cpu":"1500m","pods":"3"}}}]}`)

	cc := &ClusterCollector{Client: testClient(t, srv)}
	ms, err := cc.Collect(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range f.queries {
		if strings.Contains(q, "/apis/") || strings.Contains(q, "/api/v1/") {
			if !strings.Contains(q, "resourceVersion=0") {
				t.Errorf("list without resourceVersion=0: %s", q)
			}
		}
	}
	if p := onePoint(t, ms, "openlog.k8s.cluster.status", nil); p.attrs["openlog.k8s.cluster.version"] != "v1.37.0" || p.attrs["openlog.k8s.cluster.nodes"] != "1" {
		t.Errorf("cluster %+v", p)
	}
	ns := onePoint(t, ms, "openlog.k8s.node.status", nil)
	if ns.attrs["openlog.k8s.node.ready"] != "true" || ns.attrs["openlog.k8s.node.roles"] != "control-plane" || ns.attrs["openlog.k8s.node.unschedulable"] != "true" ||
		ns.attrs["openlog.k8s.node.internal_ip"] != "172.18.0.2" || ns.attrs["openlog.k8s.node.container_runtime"] != "containerd://2.1" {
		t.Errorf("node status %+v", ns)
	}
	if n := len(findMetric(ms, "k8s.node.condition")); n != 3 {
		t.Errorf("%d node conditions", n)
	}
	if p := onePoint(t, ms, "k8s.node.condition", map[string]string{"condition": "DiskPressure"}); p.value != -1 {
		t.Errorf("unknown condition %+v", p)
	}
	if p := onePoint(t, ms, "k8s.node.allocatable_cpu", nil); p.value != 3.5 {
		t.Errorf("alloc cpu %+v", p)
	}
	ps := onePoint(t, ms, "openlog.k8s.pod.status", map[string]string{"k8s.pod.uid": "uid-web-1"})
	if ps.attrs["openlog.k8s.pod.reason"] != "CrashLoopBackOff" || ps.attrs["openlog.k8s.pod.ready"] != "false" || ps.attrs["openlog.k8s.pod.restarts"] != "7" ||
		ps.attrs["k8s.deployment.name"] != "web" || ps.attrs["openlog.k8s.workload.kind"] != "Deployment" {
		t.Errorf("pod status %+v", ps)
	}
	var cjs []podContainerJSON
	if err := json.Unmarshal([]byte(ps.attrs["openlog.k8s.pod.containers"]), &cjs); err != nil || len(cjs) != 2 || cjs[0].ContainerID != "abcdef0123" || cjs[0].Reason != "CrashLoopBackOff" {
		t.Errorf("containers json %v %+v", err, cjs)
	}
	if p := onePoint(t, ms, "openlog.k8s.pod.status", map[string]string{"k8s.pod.uid": "p3"}); p.attrs["k8s.cronjob.name"] != "backup" || p.attrs["openlog.k8s.pod.reason"] != "" {
		t.Errorf("cron pod %+v", p)
	}
	if p := onePoint(t, ms, "openlog.k8s.pod.status", map[string]string{"k8s.pod.uid": "p4"}); p.attrs["openlog.k8s.pod.reason"] != "Evicted" || p.attrs["openlog.k8s.pod.containers"] != `[{"name":"x","container_id":"","image":"x","ready":false,"restarts":0,"state":"waiting","reason":""}]` {
		t.Errorf("evicted pod %+v", p)
	}
	if p := onePoint(t, ms, "k8s.pod.phase", map[string]string{"k8s.pod.uid": "p4"}); p.value != 4 {
		t.Errorf("phase %+v", p)
	}
	if p := onePoint(t, ms, "k8s.container.restarts", map[string]string{"k8s.container.name": "app"}); p.value != 7 || p.attrs["container.image.name"] != "ghcr.io/acme/web" {
		t.Errorf("restarts %+v", p)
	}
	if p := onePoint(t, ms, "k8s.container.memory_limit", nil); p.value != 256<<20 {
		t.Errorf("mem limit %+v", p)
	}
	if n := len(findMetric(ms, "k8s.container.cpu_limit")); n != 0 {
		t.Errorf("unset cpu limit reported %d times", n)
	}
	wd := onePoint(t, ms, "openlog.k8s.workload.status", map[string]string{"openlog.k8s.workload.kind": "Deployment"})
	if wd.attrs["openlog.k8s.workload.desired"] != "3" || wd.attrs["openlog.k8s.workload.available"] != "1" || wd.attrs["k8s.deployment.name"] != "web" {
		t.Errorf("deployment %+v", wd)
	}
	if p := onePoint(t, ms, "openlog.k8s.workload.unavailable", map[string]string{"openlog.k8s.workload.name": "web"}); p.value != 2 {
		t.Errorf("unavailable %+v", p)
	}
	if p := onePoint(t, ms, "openlog.k8s.workload.unavailable", map[string]string{"openlog.k8s.workload.name": "agent"}); p.value != 1 {
		t.Errorf("ds unavailable %+v", p)
	}
	// ReplicaSet owned by the deployment and the CronJob's job are not workloads; the bare ReplicaSet and Job are.
	kinds := map[string]int{}
	for _, p := range findMetric(ms, "openlog.k8s.workload.status") {
		kinds[p.attrs["openlog.k8s.workload.kind"]+"/"+p.attrs["openlog.k8s.workload.name"]]++
	}
	want := map[string]int{"Deployment/web": 1, "StatefulSet/db": 1, "DaemonSet/agent": 1, "ReplicaSet/lonely": 1, "Job/migrate": 1, "CronJob/backup": 1}
	if len(kinds) != len(want) {
		t.Errorf("workloads %v", kinds)
	}
	for k := range want {
		if kinds[k] != 1 {
			t.Errorf("workload %s missing (%v)", k, kinds)
		}
	}
	if p := onePoint(t, ms, "openlog.k8s.workload.status", map[string]string{"openlog.k8s.workload.kind": "CronJob"}); p.attrs["openlog.k8s.workload.suspended"] != "true" || p.attrs["openlog.k8s.workload.ready"] != "1" {
		t.Errorf("cronjob %+v", p)
	}
	if p := onePoint(t, ms, "k8s.job.failed_pods", map[string]string{"k8s.job.name": "migrate"}); p.value != 3 {
		t.Errorf("job failed %+v", p)
	}
	if n := len(findMetric(ms, "k8s.job.successful_pods")); n != 2 {
		t.Errorf("job metrics for %d jobs", n)
	}
	if p := onePoint(t, ms, "k8s.namespace.phase", map[string]string{"k8s.namespace.name": "gone"}); p.value != 0 {
		t.Errorf("ns phase %+v", p)
	}
	if p := onePoint(t, ms, "k8s.resource_quota.used", map[string]string{"resource": "limits.cpu"}); p.value != 1.5 {
		t.Errorf("quota %+v", p)
	}
	if n := len(findMetric(ms, "k8s.hpa.current_replicas")); n != 0 {
		t.Errorf("hpa without permission: %d", n)
	}

	// Nodes are required.
	f.status["GET /api/v1/nodes"] = 403
	if _, err := cc.Collect(context.Background(), time.Now()); err == nil {
		t.Error("collect without node access succeeded")
	}
}

func TestEventsRecordAndDedupe(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	e := &Events{Now: func() time.Time { return now }}
	var ev Event
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"web.1","namespace":"shop","uid":"e1"},"involvedObject":{"kind":"Pod","namespace":"shop","name":"web-x","uid":"uid-web-1","fieldPath":"spec.containers{app}"},
	 "reason":"BackOff","message":"Back-off restarting failed container","type":"Warning","count":3,"lastTimestamp":"2026-09-14T11:59:00Z","source":{"component":"kubelet","host":"node-a"}}`), &ev); err != nil {
		t.Fatal(err)
	}
	rec, ok := e.record(&ev, now.Add(-5*time.Minute))
	if !ok {
		t.Fatal("event not emitted")
	}
	a := attrMap(rec.Attributes)
	if rec.EventName != "k8s.event" || rec.SeverityText != "WARN" || rec.Body.GetStringValue() != "Back-off restarting failed container" ||
		a["k8s.event.reason"] != "BackOff" || a["k8s.event.count"] != "3" || a["k8s.pod.uid"] != "uid-web-1" || a["k8s.object.kind"] != "Pod" ||
		a["k8s.node.name"] != "node-a" || a["k8s.namespace.name"] != "shop" || a["k8s.event.source"] != "kubelet" {
		t.Errorf("record %v %v", rec, a)
	}
	if time.Unix(0, int64(rec.TimeUnixNano)).UTC() != time.Date(2026, 9, 14, 11, 59, 0, 0, time.UTC) {
		t.Errorf("time %d", rec.TimeUnixNano)
	}
	if _, ok := e.record(&ev, time.Time{}); ok {
		t.Error("same count emitted twice")
	}
	ev.Count = 4
	if _, ok := e.record(&ev, time.Time{}); !ok {
		t.Error("updated count not emitted")
	}
	old := ev
	old.Metadata.UID = "e2"
	ts := now.Add(-time.Hour)
	old.LastTimestamp = &ts
	if _, ok := e.record(&old, now.Add(-5*time.Minute)); ok {
		t.Error("old event at start emitted")
	}
	var series Event
	_ = json.Unmarshal([]byte(`{"metadata":{"uid":"e3"},"type":"Normal","eventTime":"2026-09-14T11:00:00.000000Z","series":{"count":9,"lastObservedTime":"2026-09-14T11:58:30.123456Z"},"involvedObject":{"kind":"Node","name":"node-a"}}`), &series)
	r2, ok := e.record(&series, time.Time{})
	if !ok || attrMap(r2.Attributes)["k8s.event.count"] != "9" || attrMap(r2.Attributes)["k8s.node.name"] != "node-a" || r2.SeverityText != "INFO" {
		t.Errorf("series event %v", r2)
	}
}

func TestEventsRunEmits(t *testing.T) {
	f, srv := newFakeAPI(t)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	f.set("GET", "/api/v1/events", `{"metadata":{"resourceVersion":"10"},"items":[{"metadata":{"uid":"a"},"type":"Normal","reason":"Pulled","lastTimestamp":"`+recent+`","involvedObject":{"kind":"Pod","name":"p"}},
	 {"metadata":{"uid":"b"},"type":"Normal","reason":"Old","lastTimestamp":"2020-01-01T00:00:00Z","involvedObject":{"kind":"Pod","name":"p"}}]}`)
	f.watches["/api/v1/events"] = []string{`{"type":"ADDED","object":{"metadata":{"uid":"c","resourceVersion":"11"},"type":"Warning","reason":"Failed","lastTimestamp":"` + recent + `","involvedObject":{"kind":"Pod","name":"p"}}}`}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 10)
	e := &Events{Client: testClient(t, srv), Emit: func(recs []*logspb.LogRecord) {
		for _, r := range recs {
			got <- attrMap(r.Attributes)["k8s.event.reason"]
		}
	}}
	go e.Run(ctx)
	want := []string{"Pulled", "Failed"}
	for _, w := range want {
		select {
		case r := <-got:
			if r != w {
				t.Fatalf("got %s, want %s", r, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for %s", w)
		}
	}
}

func TestElector(t *testing.T) {
	f, srv := newFakeAPI(t)
	c := testClient(t, srv)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	a := &Elector{Client: c, Namespace: "openlog", Name: "lease", Identity: "pod-a", Now: clock}
	b := &Elector{Client: c, Namespace: "openlog", Name: "lease", Identity: "pod-b", Now: clock}
	ctx := context.Background()
	if ok, err := a.TryAcquireOrRenew(ctx); !ok || err != nil {
		t.Fatalf("a acquire: %v %v", ok, err)
	}
	if ok, err := b.TryAcquireOrRenew(ctx); ok || err != nil {
		t.Fatalf("b acquired a live lease: %v %v", ok, err)
	}
	now = now.Add(5 * time.Second)
	if ok, _ := a.TryAcquireOrRenew(ctx); !ok {
		t.Fatal("a renew failed")
	}
	if *f.lease.Spec.HolderIdentity != "pod-a" || f.lease.Spec.RenewTime.Time != now {
		t.Errorf("lease %+v", f.lease.Spec)
	}
	now = now.Add(LeaseDuration + time.Second) // a stopped renewing
	if ok, _ := b.TryAcquireOrRenew(ctx); !ok {
		t.Fatal("b did not take over an expired lease")
	}
	if *f.lease.Spec.HolderIdentity != "pod-b" || *f.lease.Spec.LeaseTransitions != 1 {
		t.Errorf("takeover %+v", f.lease.Spec)
	}
	if ok, _ := a.TryAcquireOrRenew(ctx); ok {
		t.Error("a still leads")
	}
	b.release()
	if ok, _ := a.TryAcquireOrRenew(ctx); !ok {
		t.Error("a could not acquire a released lease")
	}
}
