package alert

// extraTemplates returns templates for metrics that only newer collectors send (semantic-conventions §6.3 nginx Plus
// and VTS response codes and upstream peers, §6.4 Redis Cluster). integ registers an integration template; fixed
// builds a fixed-threshold metric condition.
func extraTemplates(integ func(t *Template, required bool) *Template, fixed func(s metricSpec) func(p *tplParams) (map[string]any, error)) []*Template {
	return []*Template{
		integ(&Template{ID: "nginx_5xx_responses", Integration: "nginx", Severity: SeverityCritical, Metric: "nginx.http.response.status",
			Name:        txt("nginx 5xx responses", "nginx 5xx yanıtları"),
			Description: txt("Server errors (5xx) per second over all server zones are above the threshold (nginx Plus API or VTS module; stub_status has no status codes).", "Tüm server zone'larda saniyedeki sunucu hatası (5xx) yanıtları eşiğin üstünde (nginx Plus API veya VTS modülü; stub_status durum kodu vermez)."),
			Params:      []TemplateParam{pThreshold("per_second", 1, 0, 1e9, "5xx responses per second above", "Saniyede 5xx yanıt şunun üstünde"), pWindow(300, 60, 21600), pFor(0)},
			build: fixed(metricSpec{metric: "nginx.http.response.status", agg: "rate", seriesAgg: "sum", operator: "gt",
				extra: []any{filter("attr.nginx.status_range", "eq", "5xx")}, missing: "ok"})}, false),
		integ(&Template{ID: "nginx_upstream_peer_down", Integration: "nginx", Severity: SeverityCritical, Metric: "nginx.http.upstream.peer.state",
			Name:        txt("nginx upstream peers down", "nginx upstream sunucuları kapalı"),
			Description: txt("Upstream peers are down, unavailable or unhealthy (nginx Plus API).", "Upstream sunucuları kapalı, erişilemez veya sağlıksız (nginx Plus API)."),
			Params:      []TemplateParam{pWindow(120, 60, 3600), pFor(60)},
			build: func(p *tplParams) (map[string]any, error) {
				return metricCondition(p, metricSpec{metric: "nginx.http.upstream.peer.state", agg: "last", seriesAgg: "sum", operator: "gt", missing: "ok",
					extra: []any{filter("attr.nginx.peer.state", "in", "DOWN", "UNAVAILABLE", "UNHEALTHY")}}, 0), nil
			}}, false),
		integ(&Template{ID: "redis_cluster_state_fail", Integration: "redis", Severity: SeverityCritical, Metric: "redis.cluster.state",
			Name:        txt("Redis Cluster state is fail", "Redis Cluster durumu fail"),
			Description: txt("A cluster node reports cluster_state:fail (slots not served).", "Bir cluster düğümü cluster_state:fail bildiriyor (slotlar hizmet vermiyor)."),
			Params:      []TemplateParam{pWindow(120, 60, 3600), pFor(60)},
			build: func(p *tplParams) (map[string]any, error) {
				return metricCondition(p, metricSpec{metric: "redis.cluster.state", agg: "last", seriesAgg: "min", operator: "lt"}, 1), nil
			}}, false),
		integ(&Template{ID: "redis_cluster_slots_fail", Integration: "redis", Severity: SeverityCritical, Metric: "redis.cluster.slots_fail",
			Name:        txt("Redis Cluster slots failing", "Redis Cluster slotları hatalı"),
			Description: txt("Hash slots are in fail or pfail state.", "Hash slotları fail veya pfail durumunda."),
			Params:      []TemplateParam{pThreshold("count", 0, 0, 16384, "Failing slots above", "Hatalı slot şunun üstünde"), pWindow(120, 60, 3600), pFor(60)},
			build:       fixed(metricSpec{metric: "redis.cluster.slots_fail", agg: "last", seriesAgg: "max", operator: "gt"})}, false),
	}
}
