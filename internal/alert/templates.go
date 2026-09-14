package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// Recommended alert templates (alerting.md §2.8): a catalog of prebuilt rules per integration and for hosts,
// containers and APM services. A template renders to a regular RuleInput from a few parameters (target and
// thresholds); the UI previews it with POST /alerts/rules/preview and creates it with POST /alerts/rules.

// Template categories.
const (
	TemplateHost        = "host"
	TemplateContainer   = "container"
	TemplateAPM         = "apm"
	TemplateIntegration = "integration"
	TemplateKubernetes  = "kubernetes" // templates_k8s.go
)

// TemplateText holds a text per language ("en", "tr").
type TemplateText map[string]string

// Param kinds and units (the UI renders a matching input).
const (
	ParamNumber      = "number"      // Unit: ratio (0..1, shown as %), seconds, count, per_second, ms, bytes
	ParamDuration    = "duration"    // seconds
	ParamHost        = "host"        // host.id
	ParamInstance    = "instance"    // discovered service instance of the template's integration
	ParamService     = "service"     // APM service.name
	ParamEnvironment = "environment" // deployment.environment
	ParamText        = "text"
)

// TemplateParam describes one template parameter.
type TemplateParam struct {
	Key      string       `json:"key"`
	Kind     string       `json:"kind"`
	Unit     string       `json:"unit,omitempty"`
	Required bool         `json:"required"`
	Default  any          `json:"default"`
	Min      *float64     `json:"min,omitempty"`
	Max      *float64     `json:"max,omitempty"`
	Label    TemplateText `json:"label"`
}

// Template is one catalog entry.
type Template struct {
	ID          string          `json:"id"`
	Category    string          `json:"category"`
	Integration string          `json:"integration,omitempty"`
	RuleType    string          `json:"rule_type"`
	Severity    string          `json:"severity"`
	Metric      string          `json:"metric,omitempty"`
	Reference   string          `json:"reference_metric,omitempty"`
	Name        TemplateText    `json:"name"`
	Description TemplateText    `json:"description"`
	Params      []TemplateParam `json:"params"`

	build func(p *tplParams) (map[string]any, error)
}

// TemplateRenderInput is the body of POST /alerts/templates/{id}/render.
type TemplateRenderInput struct {
	Params     map[string]any `json:"params"`
	Language   string         `json:"language"`
	Name       string         `json:"name"`
	ChannelIDs []string       `json:"channel_ids"`
}

// TemplateReference is the resolved reference value of a ratio threshold.
type TemplateReference struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Ratio  float64 `json:"ratio"`
}

// TemplateRender is a rendered template.
type TemplateRender struct {
	Rule      RuleInput          `json:"rule"`
	Reference *TemplateReference `json:"reference"`
}

// referenceResolver returns the latest value of metric under filters (ok false: no data).
type referenceResolver func(ctx context.Context, metric string, filters []Filter) (float64, bool, error)

type tplParams struct {
	t        *Template
	values   map[string]any
	resolve  referenceResolver
	ctx      context.Context
	ref      *TemplateReference
	language string
}

func (p *tplParams) num(key string) float64 {
	v, _ := p.values[key].(float64)
	return v
}

func (p *tplParams) str(key string) string {
	v, _ := p.values[key].(string)
	return v
}

func (p *tplParams) has(key string) bool {
	_, ok := p.values[key]
	return ok
}

func f64(v float64) *float64 { return &v }

func txt(en, tr string) TemplateText { return TemplateText{"en": en, "tr": tr} }

func filter(field, op string, values ...string) map[string]any {
	return map[string]any{"field": field, "op": op, "values": values}
}

// ---- parameter helpers ----

func pHost() TemplateParam {
	return TemplateParam{Key: "host_id", Kind: ParamHost, Default: "", Label: txt("Host (empty = all hosts)", "Host (boş = tüm hostlar)")}
}

func pHostName() TemplateParam {
	return TemplateParam{Key: "host_name", Kind: ParamText, Default: "", Label: txt("Host name (for the rule name)", "Host adı (kural adı için)")}
}

func pInstance(required bool) []TemplateParam {
	label := txt("Instance (empty = every instance)", "Instance (boş = tüm instance'lar)")
	if required {
		label = txt("Instance", "Instance")
	}
	return []TemplateParam{
		{Key: "discovery_id", Kind: ParamText, Required: required, Default: "", Label: txt("Discovery rule id", "Keşif kuralı kimliği")},
		{Key: "instance", Kind: ParamInstance, Required: required, Default: "", Label: label},
	}
}

func pThreshold(unit string, def, min, max float64, en, tr string) TemplateParam {
	return TemplateParam{Key: "threshold", Kind: ParamNumber, Unit: unit, Required: true, Default: def, Min: f64(min), Max: f64(max), Label: txt(en, tr)}
}

func pWindow(def, min, max float64) TemplateParam {
	return TemplateParam{Key: "window_seconds", Kind: ParamDuration, Unit: "seconds", Required: true, Default: def, Min: f64(min), Max: f64(max),
		Label: txt("Window", "Pencere")}
}

func pFor(def float64) TemplateParam {
	return TemplateParam{Key: "for_seconds", Kind: ParamDuration, Unit: "seconds", Default: def, Min: f64(0), Max: f64(86400),
		Label: txt("Must hold for", "Şu kadar sürerse")}
}

func pService(required bool) TemplateParam {
	label := txt("Service", "Servis")
	if !required {
		label = txt("Service (empty = every service)", "Servis (boş = tüm servisler)")
	}
	return TemplateParam{Key: "service_name", Kind: ParamService, Required: required, Default: "", Label: label}
}

func pEnvironment() TemplateParam {
	return TemplateParam{Key: "environment", Kind: ParamEnvironment, Default: "", Label: txt("Environment (empty = all)", "Ortam (boş = tümü)")}
}

func pMinRequests() TemplateParam {
	return TemplateParam{Key: "min_requests", Kind: ParamNumber, Unit: "count", Default: 10.0, Min: f64(0), Max: f64(1e9),
		Label: txt("Minimum requests in the window", "Penceredeki en az istek")}
}

func pRatio(def float64, en, tr string) TemplateParam {
	return TemplateParam{Key: "ratio", Kind: ParamNumber, Unit: "ratio", Required: true, Default: def, Min: f64(0.01), Max: f64(1), Label: txt(en, tr)}
}

// ---- condition helpers ----

func hostFilter(p *tplParams) []any {
	if id := p.str("host_id"); id != "" {
		return []any{filter("host.id", "eq", id)}
	}
	return []any{}
}

// instanceScope selects one instance (discovery id + instance) or every instance of the integration (one series
// per host and instance).
func instanceScope(p *tplParams, extra ...any) ([]any, []string) {
	fs := hostFilter(p)
	group := []string{"host"}
	if inst := p.str("instance"); inst != "" {
		fs = append(fs, filter("resource.openlog.discovery.id", "eq", p.str("discovery_id")), filter("resource.openlog.discovery.instance", "eq", inst))
	} else {
		fs = append(fs, filter("resource.openlog.integration.id", "eq", p.t.Integration))
		group = append(group, "resource.openlog.discovery.instance")
	}
	return append(fs, extra...), group
}

func filterList(fs []any) []Filter {
	out := make([]Filter, 0, len(fs))
	for _, f := range fs {
		m := f.(map[string]any)
		out = append(out, Filter{Field: m["field"].(string), Op: m["op"].(string), Values: m["values"].([]string)})
	}
	return out
}

type metricSpec struct {
	metric, agg, seriesAgg, operator string
	extra                            []any
	group                            []string // host templates: group_by (instance templates derive it)
	missing                          string
}

func metricCondition(p *tplParams, s metricSpec, threshold float64) map[string]any {
	var fs []any
	group := s.group
	if p.t.Category == TemplateIntegration {
		fs, group = instanceScope(p, s.extra...)
	} else {
		fs = append(hostFilter(p), s.extra...)
	}
	c := map[string]any{"metric": s.metric, "aggregation": s.agg, "window_seconds": int(p.num("window_seconds")), "filters": fs,
		"group_by": group, "operator": s.operator, "threshold": threshold}
	if s.seriesAgg != "" {
		c["series_aggregation"] = s.seriesAgg
	}
	if s.missing != "" {
		c["missing_data"] = s.missing
	}
	return c
}

// ratioThreshold resolves threshold = ratio × latest value of the template's reference metric on one instance.
func ratioThreshold(p *tplParams) (float64, error) {
	if p.str("instance") == "" || p.str("discovery_id") == "" || p.str("host_id") == "" {
		return 0, invalid("params.instance", "this template needs one instance (host_id, discovery_id and instance): the limit differs per instance")
	}
	fs, _ := instanceScope(p)
	v, ok, err := p.resolve(p.ctx, p.t.Reference, filterList(fs))
	if err != nil {
		return 0, err
	}
	if !ok || !finite(v) || v <= 0 {
		return 0, &PreconditionError{Msg: fmt.Sprintf("%s has no positive value for this instance in the last hour (0 = unlimited)", p.t.Reference)}
	}
	ratio := p.num("ratio")
	p.ref = &TemplateReference{Metric: p.t.Reference, Value: v, Ratio: ratio}
	return math.Round(v * ratio), nil
}

func apmEnv(p *tplParams) any {
	if e := p.str("environment"); e != "" {
		return e
	}
	return nil
}

func apmCondition(p *tplParams, metric, op string) map[string]any {
	return map[string]any{"service_name": p.str("service_name"), "environment": apmEnv(p), "metric": metric,
		"window_seconds": int(p.num("window_seconds")), "min_requests": p.num("min_requests"), "operator": op, "threshold": p.num("threshold")}
}

// ---- catalog ----

var templateCatalog = buildTemplateCatalog()

func buildTemplateCatalog() []*Template {
	integ := func(t *Template, required bool) *Template {
		t.Category = TemplateIntegration
		if t.RuleType == "" {
			t.RuleType = TypeMetricThreshold
		}
		t.Params = append(append([]TemplateParam{pHost(), pHostName()}, pInstance(required)...), t.Params...)
		return t
	}
	fixed := func(s metricSpec) func(p *tplParams) (map[string]any, error) {
		return func(p *tplParams) (map[string]any, error) { return metricCondition(p, s, p.num("threshold")), nil }
	}
	list := []*Template{
		// ---- hosts ----
		{ID: "host_cpu_high", Category: TemplateHost, RuleType: TypeMetricThreshold, Severity: SeverityWarning, Metric: "system.cpu.utilization",
			Name:        txt("High CPU usage", "Yüksek CPU kullanımı"),
			Description: txt("Busy CPU time (everything but idle) is above the threshold for the whole window.", "Boşta olmayan CPU zamanı pencere boyunca eşiğin üstünde."),
			Params:      []TemplateParam{pHost(), pHostName(), pThreshold("ratio", 0.9, 0.01, 1, "CPU usage above", "CPU kullanımı şunun üstünde"), pWindow(300, 60, 21600), pFor(300)},
			build: fixed(metricSpec{metric: "system.cpu.utilization", agg: "avg", seriesAgg: "sum", operator: "gt", group: []string{"host"},
				extra: []any{filter("attr.cpu.mode", "not_in", "idle")}})},
		{ID: "host_memory_high", Category: TemplateHost, RuleType: TypeMetricThreshold, Severity: SeverityWarning, Metric: "system.memory.utilization",
			Name:        txt("High memory usage", "Yüksek bellek kullanımı"),
			Description: txt("Used memory (without caches and buffers) is above the threshold.", "Kullanılan bellek (önbellek ve tamponlar hariç) eşiğin üstünde."),
			Params:      []TemplateParam{pHost(), pHostName(), pThreshold("ratio", 0.9, 0.01, 1, "Memory usage above", "Bellek kullanımı şunun üstünde"), pWindow(300, 60, 21600), pFor(300)},
			build: fixed(metricSpec{metric: "system.memory.utilization", agg: "avg", seriesAgg: "sum", operator: "gt", group: []string{"host"},
				extra: []any{filter("attr.system.memory.state", "eq", "used")}})},
		{ID: "host_disk_full", Category: TemplateHost, RuleType: TypeMetricThreshold, Severity: SeverityCritical, Metric: "system.filesystem.utilization",
			Name:        txt("Disk almost full", "Disk dolmak üzere"),
			Description: txt("A filesystem is fuller than the threshold (one series per host and mount point).", "Bir dosya sistemi eşikten daha dolu (host ve bağlama noktası başına bir seri)."),
			Params:      []TemplateParam{pHost(), pHostName(), pThreshold("ratio", 0.9, 0.01, 1, "Disk usage above", "Disk kullanımı şunun üstünde"), pWindow(600, 60, 21600), pFor(0)},
			build: fixed(metricSpec{metric: "system.filesystem.utilization", agg: "last", seriesAgg: "max", operator: "gt",
				group: []string{"host", "attr.system.filesystem.mountpoint"}})},
		{ID: "host_not_reporting", Category: TemplateHost, RuleType: TypeNoData, Severity: SeverityCritical,
			Name:        txt("Host stopped reporting", "Host veri göndermiyor"),
			Description: txt("A host that reported in the last day sent no telemetry for the whole window.", "Son bir günde veri gönderen bir host pencere boyunca hiç veri göndermedi."),
			Params:      []TemplateParam{pHost(), pHostName(), pWindow(300, 60, 86400)},
			build: func(p *tplParams) (map[string]any, error) {
				return map[string]any{"signal": "host", "filters": hostFilter(p), "group_by": []string{"host"}, "window_seconds": int(p.num("window_seconds")),
					"lookback_seconds": 86400}, nil
			}},
		{ID: "service_disappeared", Category: TemplateHost, RuleType: TypeDiscovery, Severity: SeverityWarning,
			Name:        txt("Discovered service disappeared", "Keşfedilen servis kayboldu"),
			Description: txt("A discovered service (e.g. redis, mysql, docker) is no longer seen on a host that still reports.", "Keşfedilen bir servis (ör. redis, mysql, docker) veri göndermeye devam eden bir hostta artık görünmüyor."),
			Params: []TemplateParam{pHost(), pHostName(), {Key: "match", Kind: ParamText, Required: true, Default: "",
				Label: txt("Service key contains (e.g. redis)", "Servis anahtarı şunu içerir (ör. redis)")}, pWindow(900, 300, 86400)},
			build: func(p *tplParams) (map[string]any, error) {
				return map[string]any{"event": "service_disappeared", "filters": hostFilter(p), "match": p.str("match"),
					"window_seconds": int(p.num("window_seconds")), "lookback_seconds": 86400}, nil
			}},
		// ---- containers ----
		{ID: "container_restarts", Category: TemplateContainer, RuleType: TypeMetricThreshold, Severity: SeverityWarning, Metric: "container.restarts",
			Name:        txt("Container restarting", "Konteyner yeniden başlıyor"),
			Description: txt("A container restarted at least the given number of times within the window (Docker restart count).", "Bir konteyner pencere içinde en az verilen sayıda yeniden başladı (Docker yeniden başlatma sayısı)."),
			Params: []TemplateParam{pHost(), pHostName(), {Key: "restarts", Kind: ParamNumber, Unit: "count", Required: true, Default: 1.0, Min: f64(1), Max: f64(1000),
				Label: txt("Restarts at least", "En az yeniden başlatma")}, pWindow(900, 60, 21600)},
			build: func(p *tplParams) (map[string]any, error) {
				// rate = restarts / window; half a restart below the count avoids float equality at the boundary.
				th := (p.num("restarts") - 0.5) / p.num("window_seconds")
				return metricCondition(p, metricSpec{metric: "container.restarts", agg: "rate", seriesAgg: "sum", operator: "gt",
					group: []string{"host", "attr.container.name"}, missing: "ok"}, th), nil
			}},
		{ID: "container_cpu_high", Category: TemplateContainer, RuleType: TypeMetricThreshold, Severity: SeverityWarning, Metric: "container.cpu.utilization",
			Name:        txt("Container CPU usage high", "Konteyner CPU kullanımı yüksek"),
			Description: txt("A container uses more than the threshold of the host's CPU capacity.", "Bir konteyner hostun CPU kapasitesinin eşikten fazlasını kullanıyor."),
			Params:      []TemplateParam{pHost(), pHostName(), pThreshold("ratio", 0.8, 0.01, 1, "CPU usage above (of the host)", "CPU kullanımı şunun üstünde (hostun)"), pWindow(300, 60, 21600), pFor(300)},
			build: fixed(metricSpec{metric: "container.cpu.utilization", agg: "avg", seriesAgg: "sum", operator: "gt", group: []string{"host", "attr.container.name"},
				missing: "ok"})},
		{ID: "container_unhealthy", Category: TemplateContainer, RuleType: TypeMetricThreshold, Severity: SeverityCritical, Metric: "openlog.container.status",
			Name:        txt("Container unhealthy", "Konteyner sağlıksız"),
			Description: txt("A container's Docker healthcheck reports unhealthy.", "Bir konteynerin Docker sağlık kontrolü sağlıksız bildiriyor."),
			Params:      []TemplateParam{pHost(), pHostName(), pWindow(120, 60, 3600), pFor(60)},
			build: func(p *tplParams) (map[string]any, error) {
				return metricCondition(p, metricSpec{metric: "openlog.container.status", agg: "last", seriesAgg: "sum", operator: "gt",
					group: []string{"host", "attr.container.name"}, missing: "ok", extra: []any{filter("attr.openlog.container.health", "eq", "unhealthy")}}, 0), nil
			}},
		// ---- APM ----
		{ID: "apm_error_rate", Category: TemplateAPM, RuleType: TypeAPM, Severity: SeverityCritical,
			Name:        txt("High error rate", "Yüksek hata oranı"),
			Description: txt("The share of failed transactions is above the threshold.", "Hatalı transaction oranı eşiğin üstünde."),
			Params:      []TemplateParam{pService(true), pEnvironment(), pThreshold("ratio", 0.05, 0.001, 1, "Error rate above", "Hata oranı şunun üstünde"), pMinRequests(), pWindow(300, 60, 21600), pFor(0)},
			build:       func(p *tplParams) (map[string]any, error) { return apmCondition(p, "error_rate", "gt"), nil }},
		{ID: "apm_apdex_low", Category: TemplateAPM, RuleType: TypeAPM, Severity: SeverityWarning,
			Name:        txt("Apdex score low", "Apdex puanı düşük"),
			Description: txt("User satisfaction (Apdex with the service's T) is below the threshold.", "Kullanıcı memnuniyeti (servisin T değeriyle Apdex) eşiğin altında."),
			Params: []TemplateParam{pService(true), pEnvironment(), {Key: "threshold", Kind: ParamNumber, Unit: "score", Required: true, Default: 0.7, Min: f64(0), Max: f64(1),
				Label: txt("Apdex below", "Apdex şunun altında")}, pMinRequests(), pWindow(300, 60, 21600), pFor(0)},
			build: func(p *tplParams) (map[string]any, error) { return apmCondition(p, "apdex", "lt"), nil }},
		{ID: "apm_latency_p95", Category: TemplateAPM, RuleType: TypeAPM, Severity: SeverityWarning,
			Name:        txt("Slow responses (p95)", "Yavaş yanıtlar (p95)"),
			Description: txt("The 95th percentile response time is above the threshold.", "Yanıt süresinin 95. yüzdeliği eşiğin üstünde."),
			Params:      []TemplateParam{pService(true), pEnvironment(), pThreshold("ms", 1000, 1, 600000, "p95 above (ms)", "p95 şunun üstünde (ms)"), pMinRequests(), pWindow(300, 60, 21600), pFor(0)},
			build:       func(p *tplParams) (map[string]any, error) { return apmCondition(p, "p95_ms", "gt"), nil }},
		{ID: "apm_service_silent", Category: TemplateAPM, RuleType: TypeAPMNoData, Severity: SeverityCritical,
			Name:        txt("Service stopped reporting", "Servis veri göndermiyor"),
			Description: txt("An APM service that reported in the last day sent no transactions for the whole window.", "Son bir günde veri gönderen bir APM servisi pencere boyunca hiç transaction göndermedi."),
			Params:      []TemplateParam{pService(false), pEnvironment(), pWindow(600, 60, 86400)},
			build: func(p *tplParams) (map[string]any, error) {
				return map[string]any{"service_name": p.str("service_name"), "environment": apmEnv(p), "window_seconds": int(p.num("window_seconds")),
					"lookback_seconds": 86400}, nil
			}},
		// ---- integrations ----
		integ(&Template{ID: "nginx_no_requests", Integration: "nginx", Severity: SeverityWarning, Metric: "nginx.requests",
			Name:        txt("nginx serves no requests", "nginx istek almıyor"),
			Description: txt("nginx handled no requests during the window.", "nginx pencere boyunca hiç istek işlemedi."),
			Params:      []TemplateParam{pWindow(600, 60, 21600)},
			build: func(p *tplParams) (map[string]any, error) {
				return metricCondition(p, metricSpec{metric: "nginx.requests", agg: "rate", seriesAgg: "sum", operator: "lte"}, 0), nil
			}}, false),
		integ(&Template{ID: "nginx_waiting_connections", Integration: "nginx", Severity: SeverityWarning, Metric: "nginx.connections_current",
			Name:        txt("nginx waiting connections high", "nginx bekleyen bağlantılar yüksek"),
			Description: txt("Idle keep-alive connections are above the threshold (worker_connections pressure).", "Boşta bekleyen keep-alive bağlantıları eşiğin üstünde (worker_connections baskısı)."),
			Params:      []TemplateParam{pThreshold("count", 500, 1, 1e7, "Waiting connections above", "Bekleyen bağlantı şunun üstünde"), pWindow(300, 60, 21600), pFor(0)},
			build:       fixed(metricSpec{metric: "nginx.connections_current", agg: "avg", seriesAgg: "sum", operator: "gt", extra: []any{filter("attr.state", "eq", "waiting")}})}, false),
		integ(&Template{ID: "redis_memory_high", Integration: "redis", Severity: SeverityCritical, Metric: "redis.memory.used", Reference: "redis.maxmemory",
			Name:        txt("Redis memory near maxmemory", "Redis belleği maxmemory sınırına yakın"),
			Description: txt("Used memory is above the given share of maxmemory (resolved from the instance's current maxmemory; not available when maxmemory is 0).", "Kullanılan bellek maxmemory'nin verilen oranının üstünde (instance'ın güncel maxmemory değerinden hesaplanır; maxmemory 0 ise kullanılamaz)."),
			Params:      []TemplateParam{pRatio(0.9, "Share of maxmemory", "maxmemory oranı"), pWindow(300, 60, 21600), pFor(0)},
			build: func(p *tplParams) (map[string]any, error) {
				th, err := ratioThreshold(p)
				if err != nil {
					return nil, err
				}
				return metricCondition(p, metricSpec{metric: "redis.memory.used", agg: "avg", seriesAgg: "max", operator: "gt"}, th), nil
			}}, true),
		integ(&Template{ID: "redis_evicted_keys", Integration: "redis", Severity: SeverityWarning, Metric: "redis.keys.evicted",
			Name:        txt("Redis evicting keys", "Redis anahtar çıkarıyor"),
			Description: txt("Keys are evicted because maxmemory was reached.", "maxmemory sınırına ulaşıldığı için anahtarlar çıkarılıyor."),
			Params:      []TemplateParam{pThreshold("per_second", 0, 0, 1e9, "Evictions per second above", "Saniyede çıkarma şunun üstünde"), pWindow(300, 60, 21600), pFor(0)},
			build:       fixed(metricSpec{metric: "redis.keys.evicted", agg: "rate", seriesAgg: "sum", operator: "gt"})}, false),
		integ(&Template{ID: "redis_rejected_connections", Integration: "redis", Severity: SeverityWarning, Metric: "redis.connections.rejected",
			Name:        txt("Redis rejecting connections", "Redis bağlantıları reddediyor"),
			Description: txt("Connections are rejected because maxclients was reached.", "maxclients sınırına ulaşıldığı için bağlantılar reddediliyor."),
			Params:      []TemplateParam{pThreshold("per_second", 0, 0, 1e9, "Rejections per second above", "Saniyede reddetme şunun üstünde"), pWindow(300, 60, 21600), pFor(0)},
			build:       fixed(metricSpec{metric: "redis.connections.rejected", agg: "rate", seriesAgg: "sum", operator: "gt"})}, false),
		integ(&Template{ID: "mysql_replica_lag", Integration: "mysql", Severity: SeverityCritical, Metric: "mysql.replica.time_behind_source",
			Name:        txt("MySQL replication lag", "MySQL replikasyon gecikmesi"),
			Description: txt("The replica is behind its source by more than the threshold.", "Replika kaynağının eşikten daha fazla gerisinde."),
			Params:      []TemplateParam{pThreshold("seconds", 30, 1, 86400, "Lag above (s)", "Gecikme şunun üstünde (sn)"), pWindow(300, 60, 21600), pFor(0)},
			build:       fixed(metricSpec{metric: "mysql.replica.time_behind_source", agg: "max", seriesAgg: "max", operator: "gt"})}, false),
		integ(&Template{ID: "mysql_slow_queries", Integration: "mysql", Severity: SeverityWarning, Metric: "mysql.query.slow.count",
			Name:        txt("MySQL slow queries", "MySQL yavaş sorgular"),
			Description: txt("Queries slower than long_query_time per second are above the threshold.", "Saniyede long_query_time'dan yavaş sorgu sayısı eşiğin üstünde."),
			Params:      []TemplateParam{pThreshold("per_second", 0.1, 0, 1e9, "Slow queries per second above", "Saniyede yavaş sorgu şunun üstünde"), pWindow(300, 60, 21600), pFor(0)},
			build:       fixed(metricSpec{metric: "mysql.query.slow.count", agg: "rate", seriesAgg: "sum", operator: "gt"})}, false),
		integ(&Template{ID: "mysql_row_lock_waits", Integration: "mysql", Severity: SeverityWarning, Metric: "mysql.row_locks",
			Name:        txt("MySQL row lock waits", "MySQL satır kilidi beklemeleri"),
			Description: txt("InnoDB row lock waits per second are above the threshold.", "Saniyede InnoDB satır kilidi bekleme sayısı eşiğin üstünde."),
			Params:      []TemplateParam{pThreshold("per_second", 1, 0, 1e9, "Lock waits per second above", "Saniyede kilit bekleme şunun üstünde"), pWindow(300, 60, 21600), pFor(0)},
			build:       fixed(metricSpec{metric: "mysql.row_locks", agg: "rate", seriesAgg: "sum", operator: "gt", extra: []any{filter("attr.kind", "eq", "waits")}})}, false),
		integ(&Template{ID: "postgresql_connections_high", Integration: "postgresql", Severity: SeverityCritical, Metric: "postgresql.backends", Reference: "postgresql.connection.max",
			Name:        txt("PostgreSQL connections near max_connections", "PostgreSQL bağlantıları max_connections sınırına yakın"),
			Description: txt("Backends over all databases are above the given share of max_connections (resolved from the instance's current setting).", "Tüm veritabanlarındaki backend sayısı max_connections'ın verilen oranının üstünde (instance'ın güncel ayarından hesaplanır)."),
			Params:      []TemplateParam{pRatio(0.8, "Share of max_connections", "max_connections oranı"), pWindow(300, 60, 21600), pFor(0)},
			build: func(p *tplParams) (map[string]any, error) {
				th, err := ratioThreshold(p)
				if err != nil {
					return nil, err
				}
				return metricCondition(p, metricSpec{metric: "postgresql.backends", agg: "avg", seriesAgg: "sum", operator: "gt"}, th), nil
			}}, true),
		integ(&Template{ID: "postgresql_deadlocks", Integration: "postgresql", Severity: SeverityWarning, Metric: "postgresql.deadlocks",
			Name:        txt("PostgreSQL deadlocks", "PostgreSQL kilitlenmeleri (deadlock)"),
			Description: txt("Deadlocks were detected during the window.", "Pencere içinde deadlock tespit edildi."),
			Params:      []TemplateParam{pThreshold("per_second", 0, 0, 1e9, "Deadlocks per second above", "Saniyede deadlock şunun üstünde"), pWindow(300, 60, 21600), pFor(0)},
			build:       fixed(metricSpec{metric: "postgresql.deadlocks", agg: "rate", seriesAgg: "sum", operator: "gt"})}, false),
		integ(&Template{ID: "postgresql_replication_lag", Integration: "postgresql", Severity: SeverityCritical, Metric: "postgresql.wal.lag",
			Name:        txt("PostgreSQL replication lag", "PostgreSQL replikasyon gecikmesi"),
			Description: txt("A standby replays WAL later than the threshold (measured on the primary).", "Bir standby WAL'ı eşikten daha geç uyguluyor (primary üzerinde ölçülür)."),
			Params:      []TemplateParam{pThreshold("seconds", 30, 1, 86400, "Replay lag above (s)", "Uygulama gecikmesi şunun üstünde (sn)"), pWindow(300, 60, 21600), pFor(0)},
			build:       fixed(metricSpec{metric: "postgresql.wal.lag", agg: "max", seriesAgg: "max", operator: "gt", extra: []any{filter("attr.operation", "eq", "replay")}})}, false),
	}
	list = append(list, extraTemplates(integ, fixed)...)
	list = append(list, k8sTemplates()...)
	for _, t := range list {
		if t.Params == nil {
			t.Params = []TemplateParam{}
		}
	}
	return list
}

// Templates lists the catalog, optionally filtered by category and integration.
func Templates(category, integration string) []Template {
	out := []Template{}
	for _, t := range templateCatalog {
		if (category == "" || t.Category == category) && (integration == "" || t.Integration == integration) {
			out = append(out, *t)
		}
	}
	return out
}

func findTemplate(id string) *Template {
	for _, t := range templateCatalog {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// parseTemplateParams validates the given parameters against the template (unknown keys are errors) and fills defaults.
func parseTemplateParams(t *Template, in map[string]any) (map[string]any, error) {
	known := map[string]bool{}
	out := map[string]any{}
	for _, p := range t.Params {
		known[p.Key] = true
		field := "params." + p.Key
		raw, given := in[p.Key]
		if !given || raw == nil || raw == "" {
			if p.Required && (p.Default == nil || p.Default == "") {
				return nil, invalid(field, "required")
			}
			if p.Default != nil {
				out[p.Key] = p.Default
			}
			continue
		}
		switch p.Kind {
		case ParamNumber, ParamDuration:
			var v float64
			switch x := raw.(type) {
			case float64:
				v = x
			case int:
				v = float64(x)
			case int64:
				v = float64(x)
			case json.Number:
				f, err := x.Float64()
				if err != nil {
					return nil, invalid(field, "must be a number")
				}
				v = f
			case string:
				f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
				if err != nil {
					return nil, invalid(field, "must be a number")
				}
				v = f
			default:
				return nil, invalid(field, "must be a number")
			}
			if !finite(v) || (p.Min != nil && v < *p.Min) || (p.Max != nil && v > *p.Max) {
				return nil, invalid(field, "must be between %g and %g", deref(p.Min, math.Inf(-1)), deref(p.Max, math.Inf(1)))
			}
			if p.Kind == ParamDuration {
				v = math.Round(v)
			}
			out[p.Key] = v
		default:
			s, ok := raw.(string)
			if !ok {
				return nil, invalid(field, "must be a string")
			}
			if s = strings.TrimSpace(s); len(s) > maxValueBytes {
				return nil, invalid(field, "at most %d bytes", maxValueBytes)
			}
			out[p.Key] = s
		}
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !known[k] {
			return nil, invalid("params."+k, "unknown parameter for template %s", t.ID)
		}
	}
	if (out["instance"] != nil && out["instance"] != "") != (out["discovery_id"] != nil && out["discovery_id"] != "") {
		return nil, invalid("params.discovery_id", "discovery_id and instance must be given together")
	}
	return out, nil
}

func deref(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

// renderTemplate turns a template and parameters into a validated rule input.
func renderTemplate(ctx context.Context, id string, in TemplateRenderInput, resolve referenceResolver) (*TemplateRender, error) {
	t := findTemplate(id)
	if t == nil {
		return nil, ErrNotFound
	}
	values, err := parseTemplateParams(t, in.Params)
	if err != nil {
		return nil, err
	}
	lang := in.Language
	if lang != "tr" {
		lang = "en"
	}
	p := &tplParams{t: t, values: values, resolve: resolve, ctx: ctx, language: lang}
	cond, err := t.build(p)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(cond)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = t.Name[lang]
		target := p.str("service_name")
		if inst := p.str("instance"); inst != "" {
			target = inst
		}
		if host := p.str("host_name"); host != "" {
			if target != "" {
				target = host + " / " + target
			} else {
				target = host
			}
		} else if target == "" {
			target = p.str("host_id")
		}
		if target != "" {
			name += " – " + target
		}
		if r := []rune(name); len(r) > 200 {
			name = string(r[:200])
		}
	}
	rule := RuleInput{Name: name, Description: t.Description[lang], Type: t.RuleType, Severity: t.Severity, Condition: raw,
		ForSeconds: int(p.num("for_seconds")), ChannelIDs: in.ChannelIDs, Labels: map[string]string{"openlog.template": t.ID}}
	if rule.ChannelIDs == nil {
		rule.ChannelIDs = []string{}
	}
	def, err := rule.Validate()
	if err != nil {
		return nil, err
	}
	rule.Condition = def.ConditionJSON
	rule.ForSeconds = def.ForSeconds
	return &TemplateRender{Rule: rule, Reference: p.ref}, nil
}

// RenderTemplate renders a template for the organization. Ratio thresholds read the reference metric's latest value
// (last hour) through sc, the caller's tenant scope.
func (m *Manager) RenderTemplate(ctx context.Context, sc *query.Scope, id string, in TemplateRenderInput) (*TemplateRender, error) {
	resolve := func(ctx context.Context, metric string, filters []Filter) (float64, bool, error) {
		th := 0.0
		c := &MetricCondition{Metric: metric, Aggregation: "last", SeriesAggregation: "max", WindowSeconds: 3600, Filters: filters,
			GroupBy: []string{}, Operator: "gt", Threshold: &th, MissingData: "keep"}
		raw, err := json.Marshal(c)
		if err != nil {
			return 0, false, err
		}
		cond, err := metricType{}.Parse(raw)
		if err != nil {
			return 0, false, err
		}
		qctx, cancel := context.WithTimeout(ctx, m.o.QueryTimeout)
		defer cancel()
		res, err := cond.Evaluate(qctx, sc, m.o.Now().Add(-m.o.Delay).Truncate(time.Second), m.o.Limits)
		if err != nil {
			return 0, false, err
		}
		best, ok := math.Inf(-1), false
		for _, s := range res.Samples {
			if finite(s.Value) && s.Value > best {
				best, ok = s.Value, true
			}
		}
		return best, ok, nil
	}
	return renderTemplate(ctx, id, in, resolve)
}
