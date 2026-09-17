package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/cost"
)

// Cost endpoints (docs/contracts/cost.md, api.md "Costs"): what the fleet costs per hour, split
// over services, hosts and containers, with the idle share as its own number.
//
// Nothing here is billing data. openlog prices a host from the instance facts its agent reported
// (semantic-conventions §1) against a static table in the repository, and splits that price with
// the CPU and memory the existing rollups already store. Every response carries the price table's
// version, date and caveat so a number is never shown without them.

func (s *Server) costRoutes(mux *http.ServeMux) {
	if s.costs == nil { // no price table loaded: the feature is off
		return
	}
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/costs/summary", s.costSummary)
	route("GET /api/v1/costs/hosts", s.costHosts)
	route("GET /api/v1/costs/services", s.costServices)
	route("GET /api/v1/costs/containers", s.costContainers)
	route("GET /api/v1/costs/hosts/{host_id}", s.costHost)
	route("GET /api/v1/costs/trend", s.costTrend)
	route("GET /api/v1/costs/prices", s.costPrices)
}

// Metric names the attribution reads. They are passed as query parameters, never inlined: the
// query builder rejects "system" and other reserved words inside SQL fragments.
var (
	costHostMetrics = []string{
		"system.cpu.logical.count", // host capacity: vCPUs
		"system.memory.limit",      // host capacity: bytes, and the per-host heartbeat for reported minutes
		"system.cpu.utilization",   // host usage by cpu.mode
		"system.memory.usage",      // host usage by system.memory.state
	}
	costContainerMetrics = []string{
		"container.cpu.utilization", // 0..1 of the host
		"container.memory.usage",    // bytes
	}
)

const (
	// costCapacityMetric is sent once per host per interval, so its distinct minutes are the
	// minutes the host reported.
	costCapacityMetric = "system.memory.limit"
	costCPUCountMetric = "system.cpu.logical.count"
	costCPUUtilMetric  = "system.cpu.utilization"
	costMemUsageMetric = "system.memory.usage"
	costCtrCPUMetric   = "container.cpu.utilization"
	costCtrMemMetric   = "container.memory.usage"

	// costMaxTrendPoints bounds the trend series.
	costMaxTrendPoints = 400
	// costMinTrendStep keeps a trend bucket at or above the rollup's own resolution.
	costMinTrendStep = time.Minute
)

// costDimExpr reads the one data point attribute that matters for the metric: cpu.mode for CPU
// series, system.memory.state for memory ones. The memory key is a parameter because the query
// builder forbids the token "system" in a fragment.
const costDimExpr = "concat(attributes['cpu.mode'], attributes[{c_mem_state:String}]) AS c_dim"

// ---- loading ----

// costAgg is one aggregated metric row: the mean is v_sum/v_cnt, v_max the peak, and minutes the
// number of distinct one-minute buckets the series appeared in.
type costAgg struct {
	sum     float64
	count   uint64
	max     float64
	minutes uint64
}

func (a costAgg) mean() float64 {
	if a.count == 0 {
		return 0
	}
	v := a.sum / float64(a.count)
	if !finite(v) {
		return 0
	}
	return v
}

// costLoad reads everything the attribution needs over [from, to]. hostID limits it to one host
// ("" = every host that reported).
func (s *Server) costLoad(r *http.Request, sc *query.Scope, from, to time.Time, hostID string) (cost.Input, error) {
	hosts, err := s.costHostFacts(r, sc, from, to, hostID)
	if err != nil {
		return cost.Input{}, err
	}
	if len(hosts) == 0 {
		return cost.Input{}, nil
	}
	byID := map[string]*cost.Host{}
	ids := make([]string, 0, len(hosts))
	for i := range hosts {
		byID[hosts[i].HostID] = &hosts[i]
		ids = append(ids, hosts[i].HostID)
	}
	if err := s.costHostMetrics(r, sc, from, to, ids, byID); err != nil {
		return cost.Input{}, err
	}
	workloads, err := s.costWorkloads(r, sc, from, to, ids, byID)
	if err != nil {
		return cost.Input{}, err
	}
	return cost.Input{Hosts: hosts, Workloads: workloads}, nil
}

// costHostFacts lists the hosts that reported in the range with their instance facts. The cloud
// attributes are read from the resource attribute map in Go: several of their keys contain tokens
// the query builder refuses inside a fragment.
func (s *Server) costHostFacts(r *http.Request, sc *query.Scope, from, to time.Time, hostID string) ([]cost.Host, error) {
	q := sc.From(query.Hosts).
		Columns("host_id", "argMax(host_name, last_seen) AS c_name", "argMax(resource_attributes, last_seen) AS c_attrs").
		Where("last_seen >= fromUnixTimestamp64Nano({c_from:Int64})").Param("c_from", from.UnixNano()).
		GroupBy("host_id").OrderBy("host_id").Limit(s.cfg.MaxRows)
	if hostID != "" {
		q.Where("host_id = {c_host:String}").Param("c_host", hostID)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []cost.Host{}
	for rows.Next() {
		var h cost.Host
		var attrs map[string]string
		if err := rows.Scan(&h.HostID, &h.HostName, &attrs); err != nil {
			return nil, err
		}
		h.Provider = attrs["cloud.provider"]
		h.InstanceType = attrs["host.type"]
		h.Region = attrs["cloud.region"]
		h.Zone = attrs["cloud.availability_zone"]
		h.Lifecycle = attrs["openlog.host.lifecycle"]
		out = append(out, h)
	}
	return out, rows.Err()
}

// costHostMetrics fills capacity, usage and reported hours from the 1-minute rollup.
func (s *Server) costHostMetrics(r *http.Request, sc *query.Scope, from, to time.Time, ids []string, byID map[string]*cost.Host) error {
	q := sc.From(query.Metrics1m).
		Columns("host_id", "metric_name", costDimExpr,
			"sum(value_sum) AS c_sum", "toUInt64(sum(value_count)) AS c_cnt",
			"max(value_max) AS c_max", "toUInt64(uniqExact(timestamp)) AS c_minutes").
		Param("c_mem_state", "system.memory.state").
		Where("has({c_names:Array(String)}, metric_name)").Param("c_names", costHostMetrics).
		Where("has({c_hosts:Array(String)}, host_id)").Param("c_hosts", ids).
		Where("timestamp >= fromUnixTimestamp64Nano({c_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({c_to:Int64})").
		Param("c_from", from.UnixNano()).Param("c_to", to.UnixNano()).
		GroupBy("host_id", "metric_name", "c_dim").Limit(s.cfg.MaxRows * 100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()

	// host id -> metric -> dimension -> aggregate
	agg := map[string]map[string]map[string]costAgg{}
	for rows.Next() {
		var id, metric, dim string
		var a costAgg
		if err := rows.Scan(&id, &metric, &dim, &a.sum, &a.count, &a.max, &a.minutes); err != nil {
			return err
		}
		if agg[id] == nil {
			agg[id] = map[string]map[string]costAgg{}
		}
		if agg[id][metric] == nil {
			agg[id][metric] = map[string]costAgg{}
		}
		agg[id][metric][dim] = a
	}
	if err := rows.Err(); err != nil {
		return err
	}

	maxHours := to.Sub(from).Hours()
	for id, metrics := range agg {
		h, ok := byID[id]
		if !ok {
			continue
		}
		h.VCPUs = metrics[costCPUCountMetric][""].max
		h.MemoryBytes = metrics[costCapacityMetric][""].max
		h.Hours = costHours(metrics, maxHours)
		h.UsedCores, h.UsedMemoryByte, h.UsageKnown = costHostUsage(metrics, h.VCPUs)
	}
	return nil
}

// costHours is the time the host actually reported, from the distinct minutes of its per-host
// series, never more than the range itself.
func costHours(metrics map[string]map[string]costAgg, maxHours float64) float64 {
	var minutes uint64
	for _, metric := range []string{costCapacityMetric, costCPUCountMetric} {
		if a, ok := metrics[metric][""]; ok && a.minutes > minutes {
			minutes = a.minutes
		}
	}
	hours := float64(minutes) / 60
	if hours > maxHours {
		return maxHours
	}
	return hours
}

// costHostUsage derives the mean busy cores and used bytes. CPU prefers "1 − idle", which is
// exact even when the agent reports a mode openlog does not know; the sum of the non-idle modes
// is the fallback for a host that sends no idle series.
func costHostUsage(metrics map[string]map[string]costAgg, vcpus float64) (cores, bytes float64, known bool) {
	if cpu, ok := metrics[costCPUUtilMetric]; ok {
		var busy float64
		if idle, hasIdle := cpu["idle"]; hasIdle {
			busy = 1 - idle.mean()
		} else {
			for mode, a := range cpu {
				if mode != "idle" {
					busy += a.mean()
				}
			}
		}
		if busy < 0 {
			busy = 0
		}
		if busy > 1 {
			busy = 1
		}
		cores = busy * vcpus
		known = true
	}
	if mem, ok := metrics[costMemUsageMetric]; ok {
		if used, hasUsed := mem["used"]; hasUsed {
			bytes = used.mean()
			known = true
		}
	}
	return cores, bytes, known
}

// costWorkloads reads the per-container usage and links each container to an APM service.
func (s *Server) costWorkloads(r *http.Request, sc *query.Scope, from, to time.Time, ids []string, byID map[string]*cost.Host) ([]cost.Workload, error) {
	q := sc.From(query.Metrics1m).
		Columns("host_id", "attributes['container.id'] AS c_ctr", "metric_name",
			"sum(value_sum) AS c_sum", "toUInt64(sum(value_count)) AS c_cnt", "toUInt64(uniqExact(timestamp)) AS c_minutes").
		Where("has({c_names:Array(String)}, metric_name)").Param("c_names", costContainerMetrics).
		Where("has({c_hosts:Array(String)}, host_id)").Param("c_hosts", ids).
		Where("attributes['container.id'] != ''").
		Where("timestamp >= fromUnixTimestamp64Nano({c_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({c_to:Int64})").
		Param("c_from", from.UnixNano()).Param("c_to", to.UnixNano()).
		GroupBy("host_id", "c_ctr", "metric_name").Limit(s.cfg.MaxRows * 100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type ctrKey struct{ host, id string }
	usage := map[ctrKey]map[string]costAgg{}
	for rows.Next() {
		var k ctrKey
		var metric string
		var a costAgg
		if err := rows.Scan(&k.host, &k.id, &metric, &a.sum, &a.count, &a.minutes); err != nil {
			return nil, err
		}
		if usage[k] == nil {
			usage[k] = map[string]costAgg{}
		}
		usage[k][metric] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(usage) == 0 {
		return nil, nil
	}

	ctrIDs := make([]string, 0, len(usage))
	for k := range usage {
		ctrIDs = append(ctrIDs, k.id)
	}
	sort.Strings(ctrIDs)
	names, err := s.costContainerNames(r, sc, ctrIDs)
	if err != nil {
		return nil, err
	}
	services, err := s.costContainerServices(r, sc, from, to, ctrIDs)
	if err != nil {
		return nil, err
	}

	maxHours := to.Sub(from).Hours()
	out := make([]cost.Workload, 0, len(usage))
	for k, metrics := range usage {
		w := cost.Workload{HostID: k.host, ContainerID: k.id, ContainerName: names[k.id]}
		// container.cpu.utilization is a fraction of the whole host, so it becomes cores with
		// the host's vCPU count.
		if h, ok := byID[k.host]; ok {
			w.Cores = metrics[costCtrCPUMetric].mean() * h.VCPUs
		}
		w.MemoryBytes = metrics[costCtrMemMetric].mean()
		var minutes uint64
		for _, a := range metrics {
			if a.minutes > minutes {
				minutes = a.minutes
			}
		}
		w.Hours = float64(minutes) / 60
		if w.Hours > maxHours {
			w.Hours = maxHours
		}
		if svc, ok := services[k.id]; ok {
			w.ServiceName, w.ServiceNamespace, w.Environment = svc.name, svc.namespace, svc.environment
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HostID != out[j].HostID {
			return out[i].HostID < out[j].HostID
		}
		return out[i].ContainerID < out[j].ContainerID
	})
	return out, nil
}

func (s *Server) costContainerNames(r *http.Request, sc *query.Scope, ids []string) (map[string]string, error) {
	q := sc.From(query.Containers).Columns("container_id", "argMaxMerge(name) AS c_cname").
		Where("has({c_ids:Array(String)}, container_id)").Param("c_ids", ids).
		GroupBy("container_id").Limit(s.cfg.MaxRows * 10)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

type costService struct{ name, namespace, environment string }

// costContainerServices maps each container to the service whose spans carried its container.id
// most recently (apm.md §1). A container that never emitted a span has no service and its cost
// becomes the host's "unallocated" bucket, never a guess.
func (s *Server) costContainerServices(r *http.Request, sc *query.Scope, from, to time.Time, ids []string) (map[string]costService, error) {
	q := sc.From(query.ApmServiceContainers).
		Columns("container_id", "service_name", "service_namespace", "deployment_environment", "max(last_seen) AS c_last").
		Where("has({c_ids:Array(String)}, container_id)").Param("c_ids", ids).
		Where("last_seen >= fromUnixTimestamp64Nano({c_from:Int64})").Param("c_from", from.UnixNano()).
		Where("first_seen <= fromUnixTimestamp64Nano({c_to:Int64})").Param("c_to", to.UnixNano()).
		GroupBy("container_id", "service_name", "service_namespace", "deployment_environment").
		Limit(s.cfg.MaxRows * 10)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]costService{}
	newest := map[string]time.Time{}
	for rows.Next() {
		var id string
		var svc costService
		var last time.Time
		if err := rows.Scan(&id, &svc.name, &svc.namespace, &svc.environment, &last); err != nil {
			return nil, err
		}
		if prev, ok := newest[id]; !ok || last.After(prev) {
			newest[id] = last
			out[id] = svc
		}
	}
	return out, rows.Err()
}

// ---- rendering ----

// costPricingJSON travels with every cost response: the estimate's version, date and caveat.
type costPricingJSON struct {
	Version  int    `json:"version"`
	Updated  string `json:"updated"`
	Currency string `json:"currency"`
	Note     string `json:"note"`
	// Estimated is always true: these numbers are never billing data.
	Estimated    bool   `json:"estimated"`
	OverrideFile string `json:"override_file,omitempty"`
}

func (s *Server) pricing() costPricingJSON {
	return costPricingJSON{
		Version: s.costs.Version, Updated: s.costs.Updated, Currency: s.costs.Currency,
		Note: s.costs.Note, Estimated: true, OverrideFile: s.costs.OverrideFile,
	}
}

// costResult runs the whole attribution for the range of the request.
func (s *Server) costResult(r *http.Request, sc *query.Scope, hostID string) (cost.Result, time.Time, time.Time, error) {
	from, to, err := s.timeRange(r)
	if err != nil {
		return cost.Result{}, from, to, err
	}
	in, err := s.costLoad(r, sc, from, to, hostID)
	if err != nil {
		return cost.Result{}, from, to, err
	}
	return cost.Attribute(s.costs, in), from, to, nil
}

func (s *Server) costSummary(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	res, from, to, err := s.costResult(r, sc, "")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": res.Summary, "pricing": s.pricing(),
		"from": from.UnixMilli(), "to": to.UnixMilli(),
	})
	return nil
}

func (s *Server) costHosts(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	res, _, _, err := s.costResult(r, sc, "")
	if err != nil {
		return err
	}
	hosts := res.Hosts
	if len(hosts) > limit {
		hosts = hosts[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hosts": hosts, "total": len(res.Hosts), "summary": res.Summary, "pricing": s.pricing(),
	})
	return nil
}

func (s *Server) costServices(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	res, _, _, err := s.costResult(r, sc, "")
	if err != nil {
		return err
	}
	services := res.Services
	if len(services) > limit {
		services = services[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"services": services, "total": len(res.Services), "summary": res.Summary, "pricing": s.pricing(),
	})
	return nil
}

func (s *Server) costContainers(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	res, _, _, err := s.costResult(r, sc, r.URL.Query().Get("host_id"))
	if err != nil {
		return err
	}
	containers := res.Containers
	if len(containers) > limit {
		containers = containers[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"containers": containers, "total": len(res.Containers), "summary": res.Summary, "pricing": s.pricing(),
	})
	return nil
}

// costHost is the cost card of one host: its own price and split, plus what runs on it.
func (s *Server) costHost(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	hostID := r.PathValue("host_id")
	if _, _, err := s.timeRange(r); err != nil { // parameter errors before the existence check
		return err
	}
	if err := requireHost(r, sc, hostID); err != nil {
		return err
	}
	res, from, to, err := s.costResult(r, sc, hostID)
	if err != nil {
		return err
	}
	if len(res.Hosts) == 0 {
		return notFound("host not found")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host": res.Hosts[0], "services": res.Services, "containers": res.Containers,
		"pricing": s.pricing(), "from": from.UnixMilli(), "to": to.UnixMilli(),
	})
	return nil
}

func (s *Server) costPrices(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
	writeJSON(w, http.StatusOK, s.costs)
	return nil
}

// ---- trend ----

type costTrendPoint struct {
	T     int64   `json:"t"`
	Total float64 `json:"total"`
	Idle  float64 `json:"idle"`
}

// costTrend is the run rate over time: what the fleet cost in each bucket and how much of that
// was idle. It prices each bucket from the same host facts, with the minutes and usage of that
// bucket alone.
func (s *Server) costTrend(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	step, err := chooseStep(r.URL.Query().Get("step"), from, to, true)
	if err != nil {
		return err
	}
	if min := to.Sub(from) / costMaxTrendPoints; step < min {
		step = min
	}
	if step < costMinTrendStep {
		step = costMinTrendStep
	}

	hosts, err := s.costHostFacts(r, sc, from, to, "")
	if err != nil {
		return err
	}
	byID := map[string]*cost.Host{}
	ids := make([]string, 0, len(hosts))
	for i := range hosts {
		byID[hosts[i].HostID] = &hosts[i]
		ids = append(ids, hosts[i].HostID)
	}
	points := []costTrendPoint{}
	if len(hosts) > 0 {
		// Capacity and price come from the whole range; only time and usage are per bucket.
		if err := s.costHostMetrics(r, sc, from, to, ids, byID); err != nil {
			return err
		}
		points, err = s.costTrendPoints(r, sc, from, to, step, ids, byID)
		if err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"step": formatStep(step), "points": points, "pricing": s.pricing(),
		"from": from.UnixMilli(), "to": to.UnixMilli(),
	})
	return nil
}

func (s *Server) costTrendPoints(r *http.Request, sc *query.Scope, from, to time.Time, step time.Duration,
	ids []string, byID map[string]*cost.Host) ([]costTrendPoint, error) {
	q := sc.From(query.Metrics1m).
		Columns("host_id", "metric_name", costDimExpr,
			"toInt64(toUnixTimestamp(toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})))) * 1000 AS c_t",
			"sum(value_sum) AS c_sum", "toUInt64(sum(value_count)) AS c_cnt",
			"max(value_max) AS c_max", "toUInt64(uniqExact(timestamp)) AS c_minutes").
		Param("c_mem_state", "system.memory.state").
		Where("has({c_names:Array(String)}, metric_name)").Param("c_names", costHostMetrics).
		Where("has({c_hosts:Array(String)}, host_id)").Param("c_hosts", ids).
		Where("timestamp >= fromUnixTimestamp64Nano({c_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({c_to:Int64})").
		Param("c_from", from.UnixNano()).Param("c_to", to.UnixNano()).
		Param("step", uint32(step/time.Second)).
		GroupBy("host_id", "metric_name", "c_dim", "c_t").OrderBy("c_t").Limit(s.cfg.MaxRows * 100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// bucket -> host -> metric -> dimension
	buckets := map[int64]map[string]map[string]map[string]costAgg{}
	for rows.Next() {
		var id, metric, dim string
		var t int64
		var a costAgg
		if err := rows.Scan(&id, &metric, &dim, &t, &a.sum, &a.count, &a.max, &a.minutes); err != nil {
			return nil, err
		}
		if buckets[t] == nil {
			buckets[t] = map[string]map[string]map[string]costAgg{}
		}
		if buckets[t][id] == nil {
			buckets[t][id] = map[string]map[string]costAgg{}
		}
		if buckets[t][id][metric] == nil {
			buckets[t][id][metric] = map[string]costAgg{}
		}
		buckets[t][id][metric][dim] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	stepHours := step.Hours()
	out := make([]costTrendPoint, 0, len(buckets))
	for t, hostsInBucket := range buckets {
		p := costTrendPoint{T: t}
		for id, metrics := range hostsInBucket {
			h, ok := byID[id]
			if !ok {
				continue
			}
			price := s.costs.Price(cost.Instance{
				Provider: h.Provider, Type: h.InstanceType, Region: h.Region,
				Lifecycle: h.Lifecycle, VCPUs: h.VCPUs, MemoryBytes: h.MemoryBytes,
			})
			if !price.Priced() {
				continue
			}
			hours := costHours(metrics, stepHours)
			if hours <= 0 {
				continue
			}
			total := price.USDPerHour * hours
			cores, bytes, known := costHostUsage(metrics, h.VCPUs)
			used := 0.0
			if known {
				used = cost.CPUWeight*safeRatio(cores, h.VCPUs) + cost.MemoryWeight*safeRatio(bytes, h.MemoryBytes)
			}
			if used > 1 {
				used = 1
			}
			p.Total += total
			p.Idle += total * (1 - used)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

// safeRatio is a/b, 0 when b is not positive or the result is not finite.
func safeRatio(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	v := a / b
	if !finite(v) || v < 0 {
		return 0
	}
	return v
}
