package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Cluster collection limits (§7.4).
const (
	DefaultMaxPods       = 10000
	maxWorkloads         = 5000
	maxPodContainersJSON = 4 << 10
)

// ClusterCollector builds kube-state style metrics from lists of the API server (cluster mode).
type ClusterCollector struct {
	Client  *Client
	MaxPods int
	Log     *slog.Logger
	Timeout time.Duration

	warned map[string]bool
}

// ClusterState is one listing of every object kind the collector reads.
type ClusterState struct {
	Version      string
	Nodes        []Node
	Pods         []Pod
	Deployments  []Deployment
	StatefulSets []StatefulSet
	DaemonSets   []DaemonSet
	ReplicaSets  []ReplicaSet
	Jobs         []Job
	CronJobs     []CronJob
	HPAs         []HPA
	Namespaces   []Namespace
	Quotas       []ResourceQuota
}

// listAll GETs a collection from the watch cache (resourceVersion=0: no etcd read, no pagination).
func listAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var l List[T]
	if err := c.Get(ctx, path, url.Values{"resourceVersion": {"0"}}, &l); err != nil {
		return nil, fmt.Errorf("list %s: %w", path, err)
	}
	return l.Items, nil
}

// Fetch lists every kind. Kinds the ServiceAccount may not list (403) or the cluster does not serve (404) are skipped;
// nodes and pods are required.
func (cc *ClusterCollector) Fetch(ctx context.Context) (*ClusterState, error) {
	timeout := cc.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	st := &ClusterState{}
	var ver struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := cc.Client.Get(ctx, "/version", nil, &ver); err == nil {
		st.Version = ver.GitVersion
	}
	var err error
	if st.Nodes, err = listAll[Node](ctx, cc.Client, "/api/v1/nodes"); err != nil {
		return nil, err
	}
	if st.Pods, err = listAll[Pod](ctx, cc.Client, "/api/v1/pods"); err != nil {
		return nil, err
	}
	optional := func(err error) error {
		if err == nil || IsStatus(err, 403) || IsStatus(err, 404) {
			if err != nil {
				cc.warnOnce(err.Error(), err)
			}
			return nil
		}
		return err
	}
	steps := []func() error{
		func() (e error) {
			st.Deployments, e = listAll[Deployment](ctx, cc.Client, "/apis/apps/v1/deployments")
			return
		},
		func() (e error) {
			st.StatefulSets, e = listAll[StatefulSet](ctx, cc.Client, "/apis/apps/v1/statefulsets")
			return
		},
		func() (e error) {
			st.DaemonSets, e = listAll[DaemonSet](ctx, cc.Client, "/apis/apps/v1/daemonsets")
			return
		},
		func() (e error) {
			st.ReplicaSets, e = listAll[ReplicaSet](ctx, cc.Client, "/apis/apps/v1/replicasets")
			return
		},
		func() (e error) { st.Jobs, e = listAll[Job](ctx, cc.Client, "/apis/batch/v1/jobs"); return },
		func() (e error) { st.CronJobs, e = listAll[CronJob](ctx, cc.Client, "/apis/batch/v1/cronjobs"); return },
		func() (e error) {
			st.HPAs, e = listAll[HPA](ctx, cc.Client, "/apis/autoscaling/v2/horizontalpodautoscalers")
			return
		},
		func() (e error) { st.Namespaces, e = listAll[Namespace](ctx, cc.Client, "/api/v1/namespaces"); return },
		func() (e error) {
			st.Quotas, e = listAll[ResourceQuota](ctx, cc.Client, "/api/v1/resourcequotas")
			return
		},
	}
	for _, step := range steps {
		if err := optional(step()); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func (cc *ClusterCollector) warnOnce(key string, err error) {
	if cc.warned == nil {
		cc.warned = map[string]bool{}
	}
	if !cc.warned[key] && cc.Log != nil {
		cc.warned[key] = true
		cc.Log.Warn("kubernetes object kind skipped", "error", err)
	}
}

// Collect fetches the cluster state and converts it.
func (cc *ClusterCollector) Collect(ctx context.Context, now time.Time) ([]*metricspb.Metric, error) {
	st, err := cc.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	return cc.Metrics(st, now), nil
}

// resolver returns an exact owner resolver for the listed ReplicaSets and Jobs.
func (st *ClusterState) resolver() *Resolver {
	rs := map[string]OwnerReference{}
	for _, r := range st.ReplicaSets {
		if ref := controllerOf(r.Metadata); ref != nil {
			rs[r.Metadata.Namespace+"/"+r.Metadata.Name] = *ref
		} else {
			rs[r.Metadata.Namespace+"/"+r.Metadata.Name] = OwnerReference{Kind: "ReplicaSet", Name: r.Metadata.Name, UID: r.Metadata.UID}
		}
	}
	jobs := map[string]OwnerReference{}
	for _, j := range st.Jobs {
		if ref := controllerOf(j.Metadata); ref != nil {
			jobs[j.Metadata.Namespace+"/"+j.Metadata.Name] = *ref
		}
	}
	listedRS := len(st.ReplicaSets) > 0
	listedJobs := len(st.Jobs) > 0
	r := &Resolver{}
	if listedRS {
		r.ReplicaSetOwner = func(ns, name string) (OwnerReference, bool) { ref, ok := rs[ns+"/"+name]; return ref, ok }
	}
	if listedJobs {
		r.JobOwner = func(ns, name string) (OwnerReference, bool) { ref, ok := jobs[ns+"/"+name]; return ref, ok }
	}
	return r
}

func boolStr(b bool) string { return strconv.FormatBool(b) }

func conditionValue(status string) int64 {
	switch status {
	case "True":
		return 1
	case "False":
		return 0
	}
	return -1
}

var podPhases = map[string]int64{"Pending": 1, "Running": 2, "Succeeded": 3, "Failed": 4, "Unknown": 5}

// podReason is the first of: pod status.reason, a waiting reason, a terminated reason of a not-running container.
func podReason(p *Pod) string {
	if p.Status.Reason != "" {
		return p.Status.Reason
	}
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "ContainerCreating" && w.Reason != "PodInitializing" {
			return w.Reason
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.Reason != "" && t.Reason != "Completed" {
			return t.Reason
		}
	}
	return ""
}

func podReady(p *Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

func containerState(s ContainerState) (string, string) {
	switch {
	case s.Running != nil:
		return "running", ""
	case s.Waiting != nil:
		return "waiting", s.Waiting.Reason
	case s.Terminated != nil:
		return "terminated", s.Terminated.Reason
	}
	return "unknown", ""
}

type podContainerJSON struct {
	Name        string `json:"name"`
	ContainerID string `json:"container_id"`
	Image       string `json:"image"`
	Ready       bool   `json:"ready"`
	Restarts    int    `json:"restarts"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
}

func workloadAttrs(ns, kind, name, uid string) []*commonpb.KeyValue {
	attrs := []*commonpb.KeyValue{otlputil.Str(AttrNamespace, ns), otlputil.Str(AttrWorkloadKind, kind), otlputil.Str(AttrWorkloadName, name),
		otlputil.Str(AttrWorkloadUID, uid)}
	key := map[string]string{"Deployment": "k8s.deployment", "StatefulSet": "k8s.statefulset", "DaemonSet": "k8s.daemonset",
		"Job": "k8s.job", "CronJob": "k8s.cronjob", "ReplicaSet": "k8s.replicaset"}[kind]
	if key != "" {
		attrs = append(attrs, otlputil.Str(key+".name", name), otlputil.Str(key+".uid", uid))
	}
	return attrs
}

func itoa(v int) string { return strconv.Itoa(v) }

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// Metrics converts a cluster state into the metrics of §7.4.
func (cc *ClusterCollector) Metrics(st *ClusterState, now time.Time) []*metricspb.Metric {
	b := newBuilder(now)
	b.gaugeInt("openlog.k8s.cluster.status", "1", 1, []*commonpb.KeyValue{
		otlputil.Str("openlog.k8s.cluster.version", st.Version),
		otlputil.Str("openlog.k8s.cluster.nodes", itoa(len(st.Nodes))),
		otlputil.Str("openlog.k8s.cluster.namespaces", itoa(len(st.Namespaces))),
	})

	for i := range st.Nodes {
		n := &st.Nodes[i]
		base := []*commonpb.KeyValue{otlputil.Str(AttrNodeName, n.Metadata.Name)}
		ready := "unknown"
		for _, c := range n.Status.Conditions {
			switch c.Type {
			case "Ready", "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable":
				b.gaugeInt("k8s.node.condition", "1", conditionValue(c.Status), withKV(base, otlputil.Str("condition", c.Type)))
			}
			if c.Type == "Ready" {
				ready = map[string]string{"True": "true", "False": "false"}[c.Status]
				if ready == "" {
					ready = "unknown"
				}
			}
		}
		var roles []string
		for k := range n.Metadata.Labels {
			if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && r != "" {
				roles = append(roles, r)
			}
		}
		sort.Strings(roles)
		ip := ""
		for _, a := range n.Status.Addresses {
			if a.Type == "InternalIP" {
				ip = a.Address
				break
			}
		}
		b.gaugeInt("openlog.k8s.node.status", "1", 1, withKV(base,
			otlputil.Str(AttrNodeUID, n.Metadata.UID),
			otlputil.Str("openlog.k8s.node.ready", ready),
			otlputil.Str("openlog.k8s.node.unschedulable", boolStr(n.Spec.Unschedulable)),
			otlputil.Str("openlog.k8s.node.roles", strings.Join(roles, ",")),
			otlputil.Str("openlog.k8s.node.kubelet_version", n.Status.NodeInfo.KubeletVersion),
			otlputil.Str("openlog.k8s.node.os_image", n.Status.NodeInfo.OSImage),
			otlputil.Str("openlog.k8s.node.container_runtime", n.Status.NodeInfo.ContainerRuntimeVersion),
			otlputil.Str("openlog.k8s.node.internal_ip", ip),
			otlputil.Str("openlog.k8s.node.created_at", formatTime(n.Metadata.CreationTimestamp)),
		))
		for _, r := range []struct{ res, name, unit string }{
			{"cpu", "k8s.node.allocatable_cpu", "{cpu}"}, {"memory", "k8s.node.allocatable_memory", "By"},
			{"pods", "k8s.node.allocatable_pods", "{pod}"}, {"ephemeral-storage", "k8s.node.allocatable_ephemeral_storage", "By"},
		} {
			if v, ok := ParseQuantity(n.Status.Allocatable[r.res]); ok {
				b.gauge(r.name, r.unit, v, base)
			}
		}
	}

	resolver := st.resolver()
	pods := st.Pods
	maxPods := cc.MaxPods
	if maxPods <= 0 {
		maxPods = DefaultMaxPods
	}
	if len(pods) > maxPods {
		cc.warnOnce("pods-truncated", fmt.Errorf("%d pods, reporting %d (kubernetes.cluster.max_pods)", len(pods), maxPods))
		pods = pods[:maxPods]
	}
	for i := range pods {
		p := &pods[i]
		owner := resolver.ResolveOwner(p)
		attrs := []*commonpb.KeyValue{otlputil.Str(AttrNamespace, p.Metadata.Namespace), otlputil.Str(AttrPodName, p.Metadata.Name),
			otlputil.Str(AttrPodUID, p.Metadata.UID), otlputil.Str(AttrNodeName, p.Spec.NodeName)}
		attrs = append(attrs, owner.Attributes()...)
		if phase, ok := podPhases[p.Status.Phase]; ok {
			b.gaugeInt("k8s.pod.phase", "1", phase, attrs)
		} else {
			b.gaugeInt("k8s.pod.phase", "1", 5, attrs)
		}
		statuses := map[string]*ContainerStatus{}
		for j := range p.Status.ContainerStatuses {
			statuses[p.Status.ContainerStatuses[j].Name] = &p.Status.ContainerStatuses[j]
		}
		restarts := 0
		var cjs []podContainerJSON
		for _, c := range p.Spec.Containers {
			cs := statuses[c.Name]
			image := c.Image
			cid := ""
			cj := podContainerJSON{Name: c.Name, Image: image, State: "waiting"}
			if cs != nil {
				cid = trimRuntimePrefix(cs.ContainerID)
				restarts += cs.RestartCount
				cj.ContainerID, cj.Ready, cj.Restarts = cid, cs.Ready, cs.RestartCount
				cj.State, cj.Reason = containerState(cs.State)
			}
			cjs = append(cjs, cj)
			name, tags := containers.ImageName(image)
			cattrs := withKV(attrs, otlputil.Str(AttrContainerName, c.Name), otlputil.Str(AttrContainerID, cid),
				otlputil.Str(containers.AttrImageName, name))
			if len(tags) > 0 {
				cattrs = append(cattrs, otlputil.StrSlice(containers.AttrImageTags, tags))
			}
			if cs != nil {
				b.gaugeInt("k8s.container.restarts", "{restart}", int64(cs.RestartCount), cattrs)
				ready := int64(0)
				if cs.Ready {
					ready = 1
				}
				b.gaugeInt("k8s.container.ready", "1", ready, cattrs)
			}
			for _, r := range []struct {
				list      ResourceList
				res, name string
				unit      string
			}{
				{c.Resources.Requests, "cpu", "k8s.container.cpu_request", "{cpu}"}, {c.Resources.Limits, "cpu", "k8s.container.cpu_limit", "{cpu}"},
				{c.Resources.Requests, "memory", "k8s.container.memory_request", "By"}, {c.Resources.Limits, "memory", "k8s.container.memory_limit", "By"},
			} {
				if v, ok := ParseQuantity(r.list[r.res]); ok {
					b.gauge(r.name, r.unit, v, cattrs)
				}
			}
		}
		cjson, _ := json.Marshal(cjs)
		for len(cjson) > maxPodContainersJSON && len(cjs) > 1 {
			cjs = cjs[:len(cjs)-1]
			cjson, _ = json.Marshal(cjs)
		}
		if cjs == nil {
			cjson = []byte("[]")
		}
		started := ""
		if p.Status.StartTime != nil {
			started = formatTime(p.Status.StartTime)
		}
		b.gaugeInt("openlog.k8s.pod.status", "1", 1, withKV(attrs,
			otlputil.Str("openlog.k8s.pod.phase", p.Status.Phase),
			otlputil.Str("openlog.k8s.pod.ready", boolStr(podReady(p))),
			otlputil.Str("openlog.k8s.pod.reason", podReason(p)),
			otlputil.Str("openlog.k8s.pod.restarts", itoa(restarts)),
			otlputil.Str("openlog.k8s.pod.ip", p.Status.PodIP),
			otlputil.Str("openlog.k8s.pod.qos_class", p.Status.QOSClass),
			otlputil.Str("openlog.k8s.pod.created_at", formatTime(p.Metadata.CreationTimestamp)),
			otlputil.Str("openlog.k8s.pod.started_at", started),
			otlputil.Str("openlog.k8s.pod.containers", string(cjson)),
		))
	}

	workloads := 0
	workload := func(ns, kind, name, uid string, created *time.Time, desired, ready, available, updated, unavailable int, extra ...*commonpb.KeyValue) []*commonpb.KeyValue {
		attrs := workloadAttrs(ns, kind, name, uid)
		workloads++
		if workloads > maxWorkloads {
			if workloads == maxWorkloads+1 {
				cc.warnOnce("workloads-truncated", errors.New("more than 5000 workloads; not reporting more"))
			}
			return nil
		}
		b.gaugeInt("openlog.k8s.workload.status", "1", 1, withKV(attrs, append([]*commonpb.KeyValue{
			otlputil.Str("openlog.k8s.workload.desired", itoa(desired)),
			otlputil.Str("openlog.k8s.workload.ready", itoa(ready)),
			otlputil.Str("openlog.k8s.workload.available", itoa(available)),
			otlputil.Str("openlog.k8s.workload.updated", itoa(updated)),
			otlputil.Str("openlog.k8s.workload.created_at", formatTime(created)),
		}, extra...)...))
		b.gaugeInt("openlog.k8s.workload.unavailable", "{pod}", int64(max(unavailable, 0)), attrs)
		return attrs
	}
	for _, d := range st.Deployments {
		desired := intOr(d.Spec.Replicas, 1)
		if a := workload(d.Metadata.Namespace, "Deployment", d.Metadata.Name, d.Metadata.UID, d.Metadata.CreationTimestamp,
			desired, d.Status.ReadyReplicas, d.Status.AvailableReplicas, d.Status.UpdatedReplicas, desired-d.Status.AvailableReplicas); a != nil {
			b.gaugeInt("k8s.deployment.desired", "{pod}", int64(desired), a)
			b.gaugeInt("k8s.deployment.available", "{pod}", int64(d.Status.AvailableReplicas), a)
		}
	}
	for _, s := range st.StatefulSets {
		desired := intOr(s.Spec.Replicas, 1)
		if a := workload(s.Metadata.Namespace, "StatefulSet", s.Metadata.Name, s.Metadata.UID, s.Metadata.CreationTimestamp,
			desired, s.Status.ReadyReplicas, s.Status.AvailableReplicas, s.Status.UpdatedReplicas, desired-s.Status.AvailableReplicas); a != nil {
			b.gaugeInt("k8s.statefulset.desired_pods", "{pod}", int64(desired), a)
			b.gaugeInt("k8s.statefulset.current_pods", "{pod}", int64(s.Status.CurrentReplicas), a)
			b.gaugeInt("k8s.statefulset.ready_pods", "{pod}", int64(s.Status.ReadyReplicas), a)
			b.gaugeInt("k8s.statefulset.updated_pods", "{pod}", int64(s.Status.UpdatedReplicas), a)
		}
	}
	for _, d := range st.DaemonSets {
		s := d.Status
		if a := workload(d.Metadata.Namespace, "DaemonSet", d.Metadata.Name, d.Metadata.UID, d.Metadata.CreationTimestamp,
			s.DesiredNumberScheduled, s.NumberReady, s.NumberAvailable, s.UpdatedNumberScheduled, s.DesiredNumberScheduled-s.NumberAvailable); a != nil {
			b.gaugeInt("k8s.daemonset.desired_scheduled_nodes", "{node}", int64(s.DesiredNumberScheduled), a)
			b.gaugeInt("k8s.daemonset.current_scheduled_nodes", "{node}", int64(s.CurrentNumberScheduled), a)
			b.gaugeInt("k8s.daemonset.ready_nodes", "{node}", int64(s.NumberReady), a)
			b.gaugeInt("k8s.daemonset.misscheduled_nodes", "{node}", int64(s.NumberMisscheduled), a)
		}
	}
	for _, r := range st.ReplicaSets {
		if controllerOf(r.Metadata) != nil {
			continue // owned by a Deployment (or another controller)
		}
		desired := intOr(r.Spec.Replicas, 1)
		workload(r.Metadata.Namespace, "ReplicaSet", r.Metadata.Name, r.Metadata.UID, r.Metadata.CreationTimestamp,
			desired, r.Status.ReadyReplicas, r.Status.AvailableReplicas, r.Status.AvailableReplicas, desired-r.Status.AvailableReplicas)
	}
	for _, j := range st.Jobs {
		if ref := controllerOf(j.Metadata); ref != nil && ref.Kind == "CronJob" {
			// Reported under the CronJob workload; job metrics still carry the job.
			a := append(workloadAttrs(j.Metadata.Namespace, "Job", j.Metadata.Name, j.Metadata.UID), otlputil.Str(AttrCronJob, ref.Name))
			jobMetrics(b, &j, a)
			continue
		}
		ready := intOr(j.Status.Ready, 0)
		if a := workload(j.Metadata.Namespace, "Job", j.Metadata.Name, j.Metadata.UID, j.Metadata.CreationTimestamp,
			intOr(j.Spec.Completions, 1), ready, j.Status.Succeeded, j.Status.Failed, j.Status.Failed); a != nil {
			jobMetrics(b, &j, a)
		}
	}
	for _, c := range st.CronJobs {
		suspended := c.Spec.Suspend != nil && *c.Spec.Suspend
		if a := workload(c.Metadata.Namespace, "CronJob", c.Metadata.Name, c.Metadata.UID, c.Metadata.CreationTimestamp,
			0, len(c.Status.Active), 0, 0, 0, otlputil.Str("openlog.k8s.workload.suspended", boolStr(suspended))); a != nil {
			b.gaugeInt("k8s.cronjob.active_jobs", "{job}", int64(len(c.Status.Active)), a)
		}
	}
	for _, h := range st.HPAs {
		a := []*commonpb.KeyValue{otlputil.Str(AttrNamespace, h.Metadata.Namespace), otlputil.Str("k8s.hpa.name", h.Metadata.Name),
			otlputil.Str("k8s.hpa.uid", h.Metadata.UID), otlputil.Str("k8s.hpa.scaletargetref.kind", h.Spec.ScaleTargetRef.Kind),
			otlputil.Str("k8s.hpa.scaletargetref.name", h.Spec.ScaleTargetRef.Name)}
		b.gaugeInt("k8s.hpa.current_replicas", "{pod}", int64(h.Status.CurrentReplicas), a)
		b.gaugeInt("k8s.hpa.desired_replicas", "{pod}", int64(h.Status.DesiredReplicas), a)
		b.gaugeInt("k8s.hpa.min_replicas", "{pod}", int64(intOr(h.Spec.MinReplicas, 1)), a)
		b.gaugeInt("k8s.hpa.max_replicas", "{pod}", int64(h.Spec.MaxReplicas), a)
	}
	for _, n := range st.Namespaces {
		v := int64(0)
		if n.Status.Phase == "Active" {
			v = 1
		}
		b.gaugeInt("k8s.namespace.phase", "1", v, []*commonpb.KeyValue{otlputil.Str(AttrNamespace, n.Metadata.Name)})
	}
	for _, q := range st.Quotas {
		base := []*commonpb.KeyValue{otlputil.Str(AttrNamespace, q.Metadata.Namespace), otlputil.Str("k8s.resource_quota.name", q.Metadata.Name),
			otlputil.Str("k8s.resource_quota.uid", q.Metadata.UID)}
		for _, res := range sortedKeys(q.Status.Hard) {
			if v, ok := ParseQuantity(q.Status.Hard[res]); ok {
				b.gauge("k8s.resource_quota.hard_limit", "{resource}", v, withKV(base, otlputil.Str("resource", res)))
			}
			if v, ok := ParseQuantity(q.Status.Used[res]); ok {
				b.gauge("k8s.resource_quota.used", "{resource}", v, withKV(base, otlputil.Str("resource", res)))
			}
		}
	}
	return b.metrics()
}

func jobMetrics(b *builder, j *Job, a []*commonpb.KeyValue) {
	b.gaugeInt("k8s.job.active_pods", "{pod}", int64(j.Status.Active), a)
	b.gaugeInt("k8s.job.failed_pods", "{pod}", int64(j.Status.Failed), a)
	b.gaugeInt("k8s.job.successful_pods", "{pod}", int64(j.Status.Succeeded), a)
	if j.Spec.Completions != nil {
		b.gaugeInt("k8s.job.desired_successful_pods", "{pod}", int64(*j.Spec.Completions), a)
	}
	if j.Spec.Parallelism != nil {
		b.gaugeInt("k8s.job.max_parallel_pods", "{pod}", int64(*j.Spec.Parallelism), a)
	}
}

func sortedKeys(m ResourceList) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ClusterUID returns the uid of the kube-system namespace (k8s.cluster.uid).
func ClusterUID(ctx context.Context, c *Client) (string, error) {
	var ns Namespace
	if err := c.Get(ctx, "/api/v1/namespaces/kube-system", nil, &ns); err != nil {
		return "", err
	}
	if ns.Metadata.UID == "" {
		return "", errors.New("kubernetes: kube-system namespace has no uid")
	}
	return ns.Metadata.UID, nil
}
