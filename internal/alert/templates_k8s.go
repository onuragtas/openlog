package alert

// Kubernetes templates (semantic-conventions §7.4 cluster metrics): the cluster agent sends pod, node and workload
// state as data point attributes on the cluster resource, so rules filter them with attr.* and select a cluster with
// resource.k8s.cluster.name.

func pClusterName() TemplateParam {
	return TemplateParam{Key: "cluster_name", Kind: ParamText, Default: "", Label: txt("Cluster name (empty = all clusters)", "Küme adı (boş = tüm kümeler)")}
}

func pNamespace() TemplateParam {
	return TemplateParam{Key: "namespace", Kind: ParamText, Default: "", Label: txt("Namespace (empty = all)", "Namespace (boş = tümü)")}
}

// k8sCondition builds a metric condition scoped by the optional cluster_name, namespace and kind parameters.
func k8sCondition(p *tplParams, s metricSpec, threshold float64) map[string]any {
	fs := []any{}
	if v := p.str("cluster_name"); v != "" {
		fs = append(fs, filter("resource.k8s.cluster.name", "eq", v))
	}
	if v := p.str("namespace"); v != "" {
		fs = append(fs, filter("attr.k8s.namespace.name", "eq", v))
	}
	if v := p.str("kind"); v != "" {
		fs = append(fs, filter("attr.openlog.k8s.workload.kind", "eq", v))
	}
	fs = append(fs, s.extra...)
	group := append([]string{"resource.k8s.cluster.name"}, s.group...)
	c := map[string]any{"metric": s.metric, "aggregation": s.agg, "series_aggregation": s.seriesAgg, "window_seconds": int(p.num("window_seconds")),
		"filters": fs, "group_by": group, "operator": s.operator, "threshold": threshold}
	if s.missing != "" {
		c["missing_data"] = s.missing
	}
	return c
}

func k8sTemplates() []*Template {
	podGroup := []string{"attr.k8s.namespace.name", "attr.k8s.pod.name"}
	return []*Template{
		{ID: "k8s_pod_crashloop", Category: TemplateKubernetes, RuleType: TypeMetricThreshold, Severity: SeverityCritical, Metric: "openlog.k8s.pod.status",
			Name:        txt("Pod in CrashLoopBackOff", "Pod CrashLoopBackOff durumunda"),
			Description: txt("A pod's container keeps crashing and Kubernetes backs off restarting it (one series per cluster, namespace and pod).", "Bir pod'un konteyneri sürekli çöküyor ve Kubernetes yeniden başlatmayı erteliyor (küme, namespace ve pod başına bir seri)."),
			Params:      []TemplateParam{pClusterName(), pNamespace(), pWindow(300, 60, 21600), pFor(0)},
			build: func(p *tplParams) (map[string]any, error) {
				return k8sCondition(p, metricSpec{metric: "openlog.k8s.pod.status", agg: "last", seriesAgg: "sum", operator: "gt", group: podGroup, missing: "ok",
					extra: []any{filter("attr.openlog.k8s.pod.reason", "eq", "CrashLoopBackOff")}}, 0), nil
			}},
		{ID: "k8s_pod_not_ready", Category: TemplateKubernetes, RuleType: TypeMetricThreshold, Severity: SeverityWarning, Metric: "openlog.k8s.pod.status",
			Name:        txt("Pod not ready", "Pod hazır değil"),
			Description: txt("A pod that is neither completed nor failed is not ready (readiness probe failing, pending scheduling or image pull).", "Tamamlanmamış ve başarısız olmamış bir pod hazır değil (readiness probe başarısız, zamanlama veya imaj çekme bekliyor)."),
			Params:      []TemplateParam{pClusterName(), pNamespace(), pWindow(300, 60, 21600), pFor(300)},
			build: func(p *tplParams) (map[string]any, error) {
				return k8sCondition(p, metricSpec{metric: "openlog.k8s.pod.status", agg: "last", seriesAgg: "sum", operator: "gt", group: podGroup, missing: "ok",
					extra: []any{filter("attr.openlog.k8s.pod.ready", "eq", "false"), filter("attr.openlog.k8s.pod.phase", "not_in", "Succeeded", "Failed")}}, 0), nil
			}},
		{ID: "k8s_node_not_ready", Category: TemplateKubernetes, RuleType: TypeMetricThreshold, Severity: SeverityCritical, Metric: "k8s.node.condition",
			Name:        txt("Node not ready", "Node hazır değil"),
			Description: txt("A node's Ready condition is false or unknown (kubelet stopped posting status).", "Bir node'un Ready koşulu false veya unknown (kubelet durum göndermiyor)."),
			Params:      []TemplateParam{pClusterName(), pWindow(300, 60, 21600), pFor(120)},
			build: func(p *tplParams) (map[string]any, error) {
				return k8sCondition(p, metricSpec{metric: "k8s.node.condition", agg: "last", seriesAgg: "min", operator: "lt", group: []string{"attr.k8s.node.name"},
					extra: []any{filter("attr.condition", "eq", "Ready")}}, 1), nil
			}},
		{ID: "k8s_workload_replicas_unavailable", Category: TemplateKubernetes, RuleType: TypeMetricThreshold, Severity: SeverityWarning, Metric: "openlog.k8s.workload.unavailable",
			Name:        txt("Workload replicas unavailable", "Workload replikaları kullanılamıyor"),
			Description: txt("A Deployment, StatefulSet or DaemonSet had fewer available pods than desired for the whole window (Jobs: failed pods).", "Bir Deployment, StatefulSet veya DaemonSet pencere boyunca istenenden az kullanılabilir pod'a sahipti (Job'lar: başarısız pod'lar)."),
			Params: []TemplateParam{pClusterName(), pNamespace(), {Key: "kind", Kind: ParamText, Default: "",
				Label: txt("Workload kind (empty = all, e.g. Deployment)", "Workload türü (boş = tümü, ör. Deployment)")}, pWindow(600, 60, 21600), pFor(0)},
			build: func(p *tplParams) (map[string]any, error) {
				return k8sCondition(p, metricSpec{metric: "openlog.k8s.workload.unavailable", agg: "min", seriesAgg: "max", operator: "gt",
					group: []string{"attr.k8s.namespace.name", "attr.openlog.k8s.workload.kind", "attr.openlog.k8s.workload.name"}, missing: "ok"}, 0), nil
			}},
	}
}
