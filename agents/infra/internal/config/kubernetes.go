package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Kubernetes modes (semantic-conventions §7).
const (
	KubernetesModeNode    = "node"
	KubernetesModeCluster = "cluster"
)

// KubernetesConfig configures Kubernetes support (docs/operations/kubernetes.md, D-070).
type KubernetesConfig struct {
	// Enabled: "auto" (default: on when KUBERNETES_SERVICE_HOST is set), "true" or "false".
	Enabled string `yaml:"enabled"`
	// Mode: node (DaemonSet: host, containers, kubelet, pod metadata) or cluster (Deployment: cluster metrics, events).
	Mode        string `yaml:"mode"`
	ClusterName string `yaml:"cluster_name"`
	// NodeName is the node of a node-mode agent (env OPENLOG_K8S_NODE_NAME, downward API spec.nodeName).
	NodeName string `yaml:"node_name"`
	// PodResync is the period of a full pod relist (node mode).
	PodResync      Duration                `yaml:"pod_resync"`
	LabelAllowlist []string                `yaml:"label_allowlist"`
	Kubelet        KubernetesKubelet       `yaml:"kubelet"`
	Cluster        KubernetesClusterConfig `yaml:"cluster"`
}

// KubernetesKubelet configures kubelet /stats/summary collection (node mode).
type KubernetesKubelet struct {
	Enabled bool `yaml:"enabled"`
	// Endpoint is the kubelet URL; empty = https://$OPENLOG_K8S_HOST_IP:10250 (downward API status.hostIP).
	Endpoint           string `yaml:"endpoint"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

// KubernetesClusterConfig configures cluster mode.
type KubernetesClusterConfig struct {
	Interval Duration `yaml:"interval"`
	Events   bool     `yaml:"events"`
	MaxPods  int      `yaml:"max_pods"`
	// LeaseName/LeaseNamespace of the leader election Lease; empty namespace = the agent pod's namespace.
	LeaseName      string `yaml:"lease_name"`
	LeaseNamespace string `yaml:"lease_namespace"`
}

func defaultKubernetes() KubernetesConfig {
	return KubernetesConfig{
		Enabled: "auto", Mode: KubernetesModeNode, PodResync: Duration(5 * time.Minute),
		LabelAllowlist: []string{"app", "app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/version", "app.kubernetes.io/component"},
		Kubelet:        KubernetesKubelet{Enabled: true},
		Cluster:        KubernetesClusterConfig{Interval: Duration(30 * time.Second), Events: true, MaxPods: 10000, LeaseName: "openlog-agent-cluster"},
	}
}

// applyKubernetesEnv applies OPENLOG_K8S_* overrides.
func (k *KubernetesConfig) applyEnv(getenv func(string) string) {
	if v := getenv("OPENLOG_K8S_MODE"); v != "" {
		k.Mode = v
	}
	if v := getenv("OPENLOG_K8S_CLUSTER_NAME"); v != "" {
		k.ClusterName = v
	}
	if v := getenv("OPENLOG_K8S_NODE_NAME"); v != "" {
		k.NodeName = v
	}
	if k.Kubelet.Endpoint == "" {
		if ip := getenv("OPENLOG_K8S_HOST_IP"); ip != "" {
			host := ip
			if strings.Contains(ip, ":") {
				host = "[" + ip + "]"
			}
			k.Kubelet.Endpoint = "https://" + host + ":10250"
		}
	}
}

// Active reports whether Kubernetes support is on (getenv resolves "auto").
func (k *KubernetesConfig) Active(getenv func(string) string) bool {
	switch k.Enabled {
	case "true":
		return true
	case "false":
		return false
	}
	return getenv("KUBERNETES_SERVICE_HOST") != ""
}

// ClusterMode reports whether the agent runs as the cluster collector.
func (k *KubernetesConfig) ClusterMode(getenv func(string) string) bool {
	return k.Active(getenv) && k.Mode == KubernetesModeCluster
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

func (k *KubernetesConfig) validate(getenv func(string) string) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	switch k.Enabled {
	case "auto", "true", "false":
	default:
		add("kubernetes.enabled must be auto, true or false")
	}
	if k.Mode != KubernetesModeNode && k.Mode != KubernetesModeCluster {
		add("kubernetes.mode must be node or cluster")
	}
	if !k.Active(getenv) {
		return errs
	}
	if d := k.PodResync.D(); d < time.Minute || d > 24*time.Hour {
		add("kubernetes.pod_resync must be between 1m and 24h")
	}
	if len(k.ClusterName) > 253 {
		add("kubernetes.cluster_name must be at most 253 bytes")
	}
	// node_name and kubelet.endpoint may be empty in node mode: the agent then uses the kernel host name and
	// https://127.0.0.1:10250 (hostNetwork) and logs a warning.
	if k.Mode == KubernetesModeNode && k.Kubelet.Enabled && k.Kubelet.Endpoint != "" {
		if u, err := url.Parse(k.Kubelet.Endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
			add("kubernetes.kubelet.endpoint must be an https URL")
		}
	}
	if k.Mode == KubernetesModeCluster {
		if d := k.Cluster.Interval.D(); d < 10*time.Second || d > 10*time.Minute {
			add("kubernetes.cluster.interval must be between 10s and 10m")
		}
		if k.Cluster.MaxPods < 1 || k.Cluster.MaxPods > 200000 {
			add("kubernetes.cluster.max_pods must be between 1 and 200000")
		}
		if !dnsLabel.MatchString(k.Cluster.LeaseName) {
			add("kubernetes.cluster.lease_name must be a DNS subdomain name")
		}
	}
	return errs
}
