package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
)

func TestParseQuantity(t *testing.T) {
	cases := map[string]float64{
		"100m": 0.1, "2": 2, "1.5": 1.5, "512Mi": 512 << 20, "2Gi": 2 << 30, "1k": 1000, "1e3": 1000, "3M": 3e6,
		"250u": 250e-6, "10Ki": 10240, "0": 0,
	}
	for in, want := range cases {
		got, ok := ParseQuantity(in)
		if !ok || math.Abs(got-want) > 1e-9*math.Max(1, want) {
			t.Errorf("ParseQuantity(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "Mi", "1.2.3"} {
		if _, ok := ParseQuantity(bad); ok {
			t.Errorf("ParseQuantity(%q) ok", bad)
		}
	}
}

func TestParsePodLogPath(t *testing.T) {
	lp, ok := ParsePodLogPath("/var/log/pods/kube-system_coredns-7db6d8ff4d-abcde_3f1c2b7a-1111-2222-3333-444455556666/coredns/0.log")
	if !ok || lp.Namespace != "kube-system" || lp.Pod != "coredns-7db6d8ff4d-abcde" || lp.PodUID != "3f1c2b7a-1111-2222-3333-444455556666" || lp.Container != "coredns" {
		t.Fatalf("got %+v %v", lp, ok)
	}
	if _, ok := ParsePodLogPath("/var/log/pods/ns_pod_uid/app/1.log.20240101-000000"); !ok {
		t.Error("rotated log not parsed")
	}
	for _, bad := range []string{"/var/log/containers/x.log", "/var/log/pods/nsonly/app/0.log", "/var/lib/docker/containers/abc/abc-json.log", "/var/log/pods/a_b_c/app/0.txt"} {
		if _, ok := ParsePodLogPath(bad); ok {
			t.Errorf("%s parsed", bad)
		}
	}
}

func podJSON(t *testing.T, s string) Pod {
	t.Helper()
	var p Pod
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

const podDeploy = `{"metadata":{"name":"web-7c9f8d6b5-x2x4k","namespace":"shop","uid":"uid-web-1","labels":{"app":"web","pod-template-hash":"7c9f8d6b5","secret":"no"},
 "ownerReferences":[{"kind":"ReplicaSet","name":"web-7c9f8d6b5","uid":"rs-uid","controller":true}]},
 "spec":{"nodeName":"node-a","containers":[{"name":"app","image":"ghcr.io/acme/web:1.2","resources":{"requests":{"cpu":"250m","memory":"128Mi"},"limits":{"memory":"256Mi"}}},{"name":"sidecar","image":"envoy"}]},
 "status":{"phase":"Running","podIP":"10.0.0.5","qosClass":"Burstable","startTime":"2026-09-14T10:00:00Z",
  "conditions":[{"type":"Ready","status":"False"}],
  "containerStatuses":[{"name":"app","containerID":"containerd://ABCDEF0123","image":"ghcr.io/acme/web:1.2","ready":false,"restartCount":7,"state":{"waiting":{"reason":"CrashLoopBackOff"}}},
   {"name":"sidecar","containerID":"containerd://beef","ready":true,"restartCount":0,"state":{"running":{"startedAt":"2026-09-14T10:00:01Z"}}}]}}`

func TestResolveOwnerHeuristics(t *testing.T) {
	var r *Resolver
	o := r.ResolveOwner(ptr(podJSON(t, podDeploy)))
	if o.ReplicaSet != "web-7c9f8d6b5" || o.Deployment != "web" || o.Kind != "Deployment" || o.Name != "web" {
		t.Errorf("deployment owner: %+v", o)
	}
	cj := podJSON(t, `{"metadata":{"name":"backup-29012345-abcde","namespace":"ops","uid":"u2","ownerReferences":[{"kind":"Job","name":"backup-29012345","uid":"j","controller":true}]}}`)
	if o := r.ResolveOwner(&cj); o.Job != "backup-29012345" || o.CronJob != "backup" || o.Kind != "CronJob" {
		t.Errorf("cronjob owner: %+v", o)
	}
	bare := podJSON(t, `{"metadata":{"name":"debug","namespace":"ops","uid":"u3"}}`)
	if o := r.ResolveOwner(&bare); o.Kind != "Pod" || o.Name != "debug" {
		t.Errorf("bare pod: %+v", o)
	}
	static := podJSON(t, `{"metadata":{"name":"etcd-cp","namespace":"kube-system","uid":"u5","ownerReferences":[{"kind":"Node","name":"cp","controller":true}]}}`)
	if o := r.ResolveOwner(&static); o.Kind != "Pod" || o.Name != "etcd-cp" {
		t.Errorf("static pod: %+v", o)
	}
	ds := podJSON(t, `{"metadata":{"name":"agent-x","uid":"u4","ownerReferences":[{"kind":"DaemonSet","name":"agent","controller":true}]}}`)
	if o := r.ResolveOwner(&ds); o.DaemonSet != "agent" || o.Kind != "DaemonSet" {
		t.Errorf("daemonset: %+v", o)
	}
	// Exact resolution: a ReplicaSet owned by nothing stays the workload.
	exact := &Resolver{ReplicaSetOwner: func(ns, name string) (OwnerReference, bool) {
		return OwnerReference{Kind: "ReplicaSet", Name: name}, true
	}}
	if o := exact.ResolveOwner(ptr(podJSON(t, podDeploy))); o.Deployment != "" || o.Kind != "ReplicaSet" {
		t.Errorf("exact bare replicaset: %+v", o)
	}
}

func ptr[T any](v T) *T { return &v }

func TestPodCacheListWatchAndEnrich(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.set("GET", "/api/v1/pods", `{"metadata":{"resourceVersion":"100"},"items":[`+podDeploy+`]}`)
	newPod := strings.ReplaceAll(strings.ReplaceAll(podDeploy, "uid-web-1", "uid-web-2"), "ABCDEF0123", "cafe")
	f.watches["/api/v1/pods"] = []string{
		`{"type":"BOOKMARK","object":{"metadata":{"resourceVersion":"101"}}}`,
		`{"type":"ADDED","object":` + newPod + `}`,
		`{"type":"DELETED","object":` + podDeploy + `}`,
	}
	c := testClient(t, srv)
	cache := NewPodCache(c, "node-a", time.Minute, DefaultLabelAllowlist, nil)
	rv, err := cache.List(context.Background())
	if err != nil || rv != "100" {
		t.Fatalf("list: %v %v", rv, err)
	}
	if !strings.Contains(f.queries[0], "fieldSelector=spec.nodeName%3Dnode-a") {
		t.Errorf("query %s", f.queries[0])
	}
	if f.auth[0] != "Bearer tok-1" {
		t.Errorf("auth %q", f.auth[0])
	}
	pod, name := cache.PodByContainerID("abcdef0123")
	if pod == nil || name != "app" || pod.Owner.Deployment != "web" {
		t.Fatalf("by container id: %+v %q", pod, name)
	}

	// Enrich: a CRI container with labels, one found only by container id, one unrelated Docker container.
	cs := []containers.Container{
		{ID: "abcdef0123", Labels: map[string]string{"io.kubernetes.pod.uid": "uid-web-1", "io.kubernetes.container.name": "app"}},
		{ID: "beef", Labels: map[string]string{}},
		{ID: "d0cc", Labels: map[string]string{"com.docker.compose.project": "x"}},
		{ID: "0ff", Labels: map[string]string{}, LogPath: "/var/log/pods/other_job-1_uid-9/main/0.log"},
	}
	(&Enricher{Cache: cache, NodeName: "node-a", ClusterName: "prod"}).Enrich(cs)
	a := attrMap(cs[0].Extra)
	for k, want := range map[string]string{"k8s.pod.uid": "uid-web-1", "k8s.deployment.name": "web", "k8s.replicaset.name": "web-7c9f8d6b5",
		"openlog.k8s.workload.kind": "Deployment", "k8s.pod.label.app": "web", "k8s.cluster.name": "prod", "k8s.node.name": "node-a", "k8s.container.name": "app"} {
		if a[k] != want {
			t.Errorf("enrich %s = %q, want %q (%v)", k, a[k], want, a)
		}
	}
	if _, ok := a["k8s.pod.label.secret"]; ok {
		t.Error("label outside the allowlist sent")
	}
	if b := attrMap(cs[1].Extra); b["k8s.container.name"] != "sidecar" || b["k8s.pod.name"] != "web-7c9f8d6b5-x2x4k" {
		t.Errorf("by id: %v", b)
	}
	if cs[2].Extra != nil {
		t.Errorf("docker container enriched: %v", cs[2].Extra)
	}
	if d := attrMap(cs[3].Extra); d["k8s.pod.uid"] != "uid-9" || d["k8s.namespace.name"] != "other" || d["k8s.container.name"] != "main" {
		t.Errorf("log path fallback: %v", d)
	}
	// Container attributes: runtime labels win, Extra adds the rest without duplicates.
	attrs := attrMap(containers.Attributes(cs[0].ID, "containerd", &containers.Container{ID: cs[0].ID, Runtime: "containerd", Labels: cs[0].Labels, Extra: cs[0].Extra}))
	if attrs["k8s.deployment.name"] != "web" || attrs["container.id"] != "abcdef0123" {
		t.Errorf("attributes: %v", attrs)
	}

	rv2, err := c.Watch(context.Background(), "/api/v1/pods", cache.query(), rv, cache.apply)
	if err != nil {
		t.Fatal(err)
	}
	if rv2 == "" || cache.Pod("uid-web-1") != nil || cache.Pod("uid-web-2") == nil {
		t.Errorf("after watch rv=%s web-1=%v web-2=%v", rv2, cache.Pod("uid-web-1"), cache.Pod("uid-web-2"))
	}
	if p, _ := cache.PodByContainerID("abcdef0123"); p != nil {
		t.Error("deleted pod still indexed by container id")
	}
}

func TestWatchGone(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.watches["/api/v1/events"] = []string{`{"type":"ERROR","object":{"kind":"Status","code":410,"reason":"Expired"}}`}
	_, err := testClient(t, srv).Watch(context.Background(), "/api/v1/events", nil, "5", func(WatchEvent) error { return nil })
	if !errors.Is(err, ErrGone) {
		t.Fatalf("err = %v", err)
	}
	f.status["GET /api/v1/nodes"] = 403
	err = testClient(t, srv).Get(context.Background(), "/api/v1/nodes", nil, &NodeList{})
	if !IsStatus(err, 403) {
		t.Fatalf("403: %v", err)
	}
}

const summaryJSON = `{
 "node":{"nodeName":"node-a","cpu":{"usageNanoCores":1500000000,"usageCoreNanoSeconds":90000000000000},
  "memory":{"usageBytes":4000,"workingSetBytes":3000,"rssBytes":2000,"availableBytes":1000},
  "network":{"name":"eth0","rxBytes":10,"txBytes":20,"interfaces":[{"name":"eth0","rxBytes":10,"txBytes":20},{"name":"eth1","rxBytes":1,"txBytes":2}]},
  "fs":{"availableBytes":5,"capacityBytes":10,"usedBytes":5}},
 "pods":[{"podRef":{"name":"web-7c9f8d6b5-x2x4k","namespace":"shop","uid":"uid-web-1"},
  "cpu":{"usageNanoCores":250000000,"usageCoreNanoSeconds":1000000000},"memory":{"workingSetBytes":64000000},
  "network":{"name":"eth0","rxBytes":100,"txBytes":200,"rxErrors":1,"txErrors":0},
  "ephemeral-storage":{"usedBytes":4096},
  "containers":[{"name":"app","cpu":{"usageNanoCores":200000000},"memory":{"workingSetBytes":60000000},"rootfs":{"usedBytes":100},"logs":{"usedBytes":50}}]},
  {"podRef":{"name":"orphan","namespace":"x","uid":"uid-unknown"},"cpu":{"usageNanoCores":1}}]}`

func TestKubeletMetrics(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.set("GET", "/api/v1/pods", `{"metadata":{"resourceVersion":"1"},"items":[`+podDeploy+`]}`)
	f.set("GET", "/stats/summary", summaryJSON)
	c := testClient(t, srv)
	cache := NewPodCache(c, "node-a", time.Minute, DefaultLabelAllowlist, nil)
	if _, err := cache.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	k := &Kubelet{Client: c, NodeName: "node-a", Pods: cache}
	ms, err := k.Collect(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p := onePoint(t, ms, "k8s.node.cpu.usage", nil); p.value != 1.5 || p.attrs["k8s.node.name"] != "node-a" {
		t.Errorf("node cpu %+v", p)
	}
	if p := onePoint(t, ms, "k8s.node.network.io", map[string]string{"network.io.direction": "receive"}); p.value != 11 {
		t.Errorf("node rx %+v", p)
	}
	p := onePoint(t, ms, "k8s.pod.cpu.usage", map[string]string{"k8s.pod.uid": "uid-web-1"})
	if p.value != 0.25 || p.attrs["k8s.deployment.name"] != "web" || p.attrs["k8s.pod.label.app"] != "web" {
		t.Errorf("pod cpu %+v", p)
	}
	if p := onePoint(t, ms, "k8s.pod.network.errors", map[string]string{"network.io.direction": "receive"}); p.value != 1 {
		t.Errorf("pod rx errors %+v", p)
	}
	if p := onePoint(t, ms, "k8s.pod.filesystem.usage", nil); p.value != 4096 {
		t.Errorf("pod fs %+v", p)
	}
	cp := onePoint(t, ms, "k8s.container.memory.working_set", nil)
	if cp.value != 60e6 || cp.attrs["container.id"] != "abcdef0123" || cp.attrs["k8s.container.name"] != "app" {
		t.Errorf("container mem %+v", cp)
	}
	if p := onePoint(t, ms, "k8s.container.filesystem.usage", nil); p.value != 150 {
		t.Errorf("container fs %+v", p)
	}
	if p := onePoint(t, ms, "k8s.pod.cpu.usage", map[string]string{"k8s.pod.uid": "uid-unknown"}); p.attrs["k8s.pod.name"] != "orphan" || p.attrs["k8s.node.name"] != "node-a" {
		t.Errorf("uncached pod %+v", p)
	}
	for _, m := range ms {
		if m.Name == "k8s.pod.cpu.time" && m.GetSum() == nil {
			t.Error("cpu.time must be a sum")
		}
	}
}
