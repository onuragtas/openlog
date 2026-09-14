package k8s

import (
	"path"
	"regexp"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// Attribute keys (semantic-conventions §7).
const (
	AttrClusterName    = "k8s.cluster.name"
	AttrClusterUID     = "k8s.cluster.uid"
	AttrNodeName       = "k8s.node.name"
	AttrNodeUID        = "k8s.node.uid"
	AttrNamespace      = "k8s.namespace.name"
	AttrPodName        = "k8s.pod.name"
	AttrPodUID         = "k8s.pod.uid"
	AttrContainerName  = "k8s.container.name"
	AttrContainerID    = "container.id"
	AttrReplicaSet     = "k8s.replicaset.name"
	AttrDeployment     = "k8s.deployment.name"
	AttrStatefulSet    = "k8s.statefulset.name"
	AttrDaemonSet      = "k8s.daemonset.name"
	AttrJob            = "k8s.job.name"
	AttrCronJob        = "k8s.cronjob.name"
	AttrWorkloadKind   = "openlog.k8s.workload.kind"
	AttrWorkloadName   = "openlog.k8s.workload.name"
	AttrWorkloadUID    = "openlog.k8s.workload.uid"
	AttrAgentMode      = "openlog.agent.mode"
	PodLabelPrefix     = "k8s.pod.label."
	EventName          = "k8s.event"
	EntityTypeCluster  = "k8s_cluster"
	ModeNode           = "node"
	ModeCluster        = "cluster"
	podTemplateHashKey = "pod-template-hash"
)

// DefaultLabelAllowlist are the pod labels sent as k8s.pod.label.<key> by default.
var DefaultLabelAllowlist = []string{"app", "app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/version", "app.kubernetes.io/component"}

// Owner is the controller chain of a pod.
type Owner struct {
	ReplicaSet, Deployment, StatefulSet, DaemonSet, Job, CronJob string
	// Kind and Name of the top-level workload (Deployment, StatefulSet, DaemonSet, CronJob, Job, ReplicaSet or Pod).
	Kind, Name, UID string
}

// cronJobJob matches the name of a Job created by a CronJob: "<cronjob>-<scheduled time in minutes>".
var cronJobJob = regexp.MustCompile(`^(.+)-[0-9]{8,}$`)

// Resolver resolves exact owners in cluster mode; nil lookups fall back to name heuristics.
type Resolver struct {
	// ReplicaSetOwner returns the controller of a ReplicaSet (kind, name, uid); ok false when unknown.
	ReplicaSetOwner func(namespace, name string) (OwnerReference, bool)
	// JobOwner returns the controller of a Job.
	JobOwner func(namespace, name string) (OwnerReference, bool)
}

// ResolveOwner returns the owner chain of pod. Without a lookup (node mode) the Deployment of a ReplicaSet is its name
// without the "-<pod-template-hash>" suffix, and the CronJob of a Job is its name without the "-<digits>" suffix.
func (r *Resolver) ResolveOwner(pod *Pod) Owner {
	o := Owner{Kind: "Pod", Name: pod.Metadata.Name, UID: pod.Metadata.UID}
	ref := controllerOf(pod.Metadata)
	if ref == nil || ref.Kind == "Node" {
		return o // bare pod, or a static (mirror) pod owned by its Node
	}
	ns := pod.Metadata.Namespace
	o.Kind, o.Name, o.UID = ref.Kind, ref.Name, ref.UID
	switch ref.Kind {
	case "ReplicaSet":
		o.ReplicaSet = ref.Name
		if r != nil && r.ReplicaSetOwner != nil {
			if up, ok := r.ReplicaSetOwner(ns, ref.Name); ok {
				if up.Kind == "Deployment" {
					o.Deployment = up.Name
				}
				o.Kind, o.Name, o.UID = up.Kind, up.Name, up.UID
			}
			return o
		}
		if h := pod.Metadata.Labels[podTemplateHashKey]; h != "" && strings.HasSuffix(ref.Name, "-"+h) {
			o.Deployment = strings.TrimSuffix(ref.Name, "-"+h)
			o.Kind, o.Name, o.UID = "Deployment", o.Deployment, ""
		}
	case "StatefulSet":
		o.StatefulSet = ref.Name
	case "DaemonSet":
		o.DaemonSet = ref.Name
	case "Job":
		o.Job = ref.Name
		if r != nil && r.JobOwner != nil {
			if up, ok := r.JobOwner(ns, ref.Name); ok && up.Kind == "CronJob" {
				o.CronJob = up.Name
				o.Kind, o.Name, o.UID = up.Kind, up.Name, up.UID
			}
			return o
		}
		if m := cronJobJob.FindStringSubmatch(ref.Name); m != nil {
			o.CronJob = m[1]
			o.Kind, o.Name, o.UID = "CronJob", m[1], ""
		}
	}
	return o
}

// Attributes returns the owner attributes (§7.2).
func (o Owner) Attributes() []*commonpb.KeyValue {
	var out []*commonpb.KeyValue
	add := func(k, v string) {
		if v != "" {
			out = append(out, otlputil.Str(k, v))
		}
	}
	add(AttrReplicaSet, o.ReplicaSet)
	add(AttrDeployment, o.Deployment)
	add(AttrStatefulSet, o.StatefulSet)
	add(AttrDaemonSet, o.DaemonSet)
	add(AttrJob, o.Job)
	add(AttrCronJob, o.CronJob)
	add(AttrWorkloadKind, o.Kind)
	add(AttrWorkloadName, o.Name)
	return out
}

// LabelAttributes returns k8s.pod.label.<key> for the allowlisted labels (sorted by allowlist order).
func LabelAttributes(labels map[string]string, allow []string) []*commonpb.KeyValue {
	var out []*commonpb.KeyValue
	for _, k := range allow {
		if v, ok := labels[k]; ok {
			out = append(out, otlputil.Str(PodLabelPrefix+k, truncate(v, 256)))
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// PodLogPath is the identity encoded in a CRI log path.
type PodLogPath struct {
	Namespace, Pod, PodUID, Container string
}

// ParsePodLogPath parses /var/log/pods/<namespace>_<pod>_<pod-uid>/<container>/<restart>.log. Namespaces and pod
// names cannot contain "_" (DNS labels/subdomains), so the directory splits unambiguously.
func ParsePodLogPath(p string) (PodLogPath, bool) {
	p = path.Clean(p)
	dir, file := path.Split(p)
	if !strings.HasSuffix(file, ".log") && !strings.Contains(file, ".log.") {
		return PodLogPath{}, false
	}
	dir = strings.TrimSuffix(dir, "/")
	container := path.Base(dir)
	podDir := path.Dir(dir)
	if path.Dir(podDir) != "/var/log/pods" {
		return PodLogPath{}, false
	}
	parts := strings.Split(path.Base(podDir), "_")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || container == "" {
		return PodLogPath{}, false
	}
	return PodLogPath{Namespace: parts[0], Pod: parts[1], PodUID: parts[2], Container: container}, true
}
