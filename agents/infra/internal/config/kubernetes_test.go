package config

import (
	"strings"
	"testing"
)

func TestKubernetesConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cfg := Default()
	if cfg.Kubernetes.Active(env(nil)) {
		t.Error("auto without KUBERNETES_SERVICE_HOST is active")
	}
	inPod := env(map[string]string{"KUBERNETES_SERVICE_HOST": "10.96.0.1", "OPENLOG_K8S_NODE_NAME": "node-a",
		"OPENLOG_K8S_HOST_IP": "172.18.0.2", "OPENLOG_K8S_CLUSTER_NAME": "prod", "OPENLOG_K8S_MODE": "cluster"})
	cfg.ApplyEnv(inPod)
	k := cfg.Kubernetes
	if !k.Active(inPod) || !k.ClusterMode(inPod) || k.NodeName != "node-a" || k.ClusterName != "prod" || k.Kubelet.Endpoint != "https://172.18.0.2:10250" {
		t.Errorf("env: %+v", k)
	}
	if errs := k.validate(inPod); len(errs) != 0 {
		t.Errorf("valid config: %v", errs)
	}
	v6 := Default()
	v6.ApplyEnv(env(map[string]string{"OPENLOG_K8S_HOST_IP": "fd00::1"}))
	if v6.Kubernetes.Kubelet.Endpoint != "https://[fd00::1]:10250" {
		t.Errorf("ipv6 endpoint %s", v6.Kubernetes.Kubelet.Endpoint)
	}
	off := Default().Kubernetes
	off.Enabled = "false"
	if off.Active(inPod) {
		t.Error("enabled=false is active")
	}

	bad := Default().Kubernetes
	bad.Enabled, bad.Mode = "true", "cluster"
	bad.Cluster.Interval = Duration(1)
	bad.Cluster.LeaseName = "Bad_Name"
	bad.Kubelet.Endpoint = "http://x"
	errs := bad.validate(env(nil))
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	for _, want := range []string{"kubernetes.cluster.interval", "kubernetes.cluster.lease_name"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	bad.Mode = "node"
	if errs := bad.validate(env(nil)); len(errs) != 1 || !strings.Contains(errs[0].Error(), "https") {
		t.Errorf("node mode errors: %v", errs)
	}
	if err := Parse([]byte("kubernetes:\n  enabled: \"true\"\n  mode: cluster\n  cluster:\n    interval: 45s\n"), cfg); err != nil || cfg.Kubernetes.Cluster.Interval.D().Seconds() != 45 {
		t.Errorf("parse: %v %+v", err, cfg.Kubernetes.Cluster)
	}
}
