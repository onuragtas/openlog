package agent

import (
	"context"
	"os"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/k8s"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/resource"
)

// k8sNode is the Kubernetes node-mode support of a host agent (semantic-conventions §7.1–7.3): the pod cache that
// enriches container metrics and logs and the kubelet collector.
type k8sNode struct {
	cache *k8s.PodCache
}

// kubernetesDefaultKubelet is used when neither kubernetes.kubelet.endpoint nor OPENLOG_K8S_HOST_IP is set.
const kubernetesDefaultKubelet = "https://127.0.0.1:10250"

// setupKubernetes enables node mode when Kubernetes is detected. It must run after the container source and the
// metric set exist and before the resource is shared with logs and integrations. Failures only disable the feature.
func (a *Agent) setupKubernetes(forExport bool) *k8sNode {
	kc := a.cfg.Kubernetes
	if !kc.Active(os.Getenv) || kc.Mode != k8s.ModeNode {
		return nil
	}
	log := a.log.With("component", "kubernetes")
	ua := resource.AgentName + "/" + a.version
	client, err := k8s.InCluster(os.Getenv, "", ua)
	if err != nil {
		log.Warn("kubernetes detected but the API server is not usable; pod metadata and kubelet metrics disabled", "error", err)
		return nil
	}
	node := kc.NodeName
	if node == "" {
		node = resource.Hostname(a.fs)
		log.Warn("kubernetes.node_name not set (OPENLOG_K8S_NODE_NAME); using the host name", "node", node)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	uid, err := k8s.ClusterUID(ctx, client)
	cancel()
	if err != nil {
		log.Warn("k8s.cluster.uid unavailable (get namespaces/kube-system)", "error", err)
	}
	attrs := a.res.Attributes
	add := func(k, v string) {
		if v == "" {
			return
		}
		for _, kv := range attrs {
			if kv.Key == k {
				return // host.extra_attributes win
			}
		}
		attrs = append(attrs, otlputil.Str(k, v))
	}
	add(k8s.AttrClusterName, kc.ClusterName)
	add(k8s.AttrClusterUID, uid)
	add(k8s.AttrNodeName, node)
	add(k8s.AttrAgentMode, k8s.ModeNode)
	a.res.Attributes = attrs

	cache := k8s.NewPodCache(client, node, kc.PodResync.D(), kc.LabelAllowlist, log)
	if !forExport {
		lctx, lcancel := context.WithTimeout(context.Background(), 10*time.Second)
		if _, err := cache.List(lctx); err != nil {
			log.Warn("pod list failed", "error", err)
		}
		lcancel()
	}
	if a.ctr != nil {
		a.ctr.Enrich = (&k8s.Enricher{Cache: cache, NodeName: node, ClusterName: kc.ClusterName}).Enrich
	}
	if kc.Kubelet.Enabled {
		endpoint := kc.Kubelet.Endpoint
		if endpoint == "" {
			endpoint = kubernetesDefaultKubelet
			log.Warn("kubernetes.kubelet.endpoint not set (OPENLOG_K8S_HOST_IP); using the loopback address", "endpoint", endpoint)
		}
		kubelet, err := k8s.NewKubeletClient(endpoint, "", kc.Kubelet.InsecureSkipVerify, ua)
		if err != nil {
			log.Warn("kubelet metrics disabled", "error", err)
		} else {
			a.metrics.Add(&k8s.Kubelet{Client: kubelet, NodeName: node, Pods: cache})
		}
	}
	log.Info("kubernetes node mode", "node", node, "cluster", kc.ClusterName, "cluster_uid", uid, "kubelet", kc.Kubelet.Enabled)
	return &k8sNode{cache: cache}
}

// start runs the pod watch until ctx ends.
func (n *k8sNode) start(ctx context.Context) {
	if n == nil {
		return
	}
	go n.cache.Run(ctx)
}
