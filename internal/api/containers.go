package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
)

// Container endpoints (docs/contracts/api.md "Containers"): the containers entity table
// (0009_containers, semantic-conventions.md §2 container metrics), container metrics and the
// service <-> container links (apm.md §1). Every read goes through the tenant-scoped query layer;
// the aggregating tables are always re-aggregated with GROUP BY.

func (s *Server) containerRoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/containers", s.listContainers)
	route("GET /api/v1/containers/groups", s.containerGroups)
	route("GET /api/v1/containers/{container_id}", s.getContainer)
	route("GET /api/v1/containers/{container_id}/timeseries", s.containerTimeseries)
	route("GET /api/v1/containers/{container_id}/services", s.containerServices)
	route("GET /api/v1/apm/services/{service}/containers", s.apmServiceContainers)
}

const (
	// containerReportingWindow: a container whose last data point is older is not reporting
	// (the agent stopped, the container was removed, or the agent lost Docker access).
	containerReportingWindow = 5 * time.Minute
	containerSparkPoints     = 30
	maxContainerRows         = 10000
	maxContainerFilterBytes  = 256
)

var containerStates = map[string]bool{"running": true, "paused": true, "restarting": true, "exited": true, "created": true, "dead": true, "removing": true, "unknown": true}

type containerJSON struct {
	ContainerID      string   `json:"container_id"`
	Name             string   `json:"name"`
	ImageName        string   `json:"image_name"`
	ImageTags        []string `json:"image_tags"`
	Runtime          string   `json:"runtime"`
	HostID           string   `json:"host_id"`
	HostName         string   `json:"host_name"`
	ComposeProject   string   `json:"compose_project"`
	ComposeService   string   `json:"compose_service"`
	K8sPodName       string   `json:"k8s_pod_name"`
	K8sNamespaceName string   `json:"k8s_namespace_name"`
	K8sContainerName string   `json:"k8s_container_name"`
	// State from openlog.container.status ("" when the agent sends none).
	State        string  `json:"state"`
	Health       string  `json:"health"`
	StartedAt    *string `json:"started_at"`
	RestartCount int64   `json:"restart_count"`
	FirstSeen    string  `json:"first_seen"`
	LastSeen     string  `json:"last_seen"`
	Reporting    bool    `json:"reporting"`
	// Latest bucket of the time range; null without data points.
	CPUUtilization  *float64     `json:"cpu_utilization"`
	MemoryUsage     *float64     `json:"memory_usage"`
	MemoryLimit     *float64     `json:"memory_limit"`
	CPUSparkline    [][2]float64 `json:"cpu_sparkline"`
	MemorySparkline [][2]float64 `json:"memory_sparkline"`
	// Detail only: data point attributes of the latest status point.
	Attributes map[string]string `json:"attributes,omitempty"`

	lastSeen time.Time
}

func containersQuery(sc *query.Scope) *query.Select {
	return sc.From(query.Containers).Columns("container_id", "host_id",
		"argMaxMerge(host_name)", "argMaxMerge(name)", "argMaxMerge(image_name)", "argMaxMerge(image_tags)", "argMaxMerge(runtime)",
		"argMaxMerge(compose_project)", "argMaxMerge(compose_service)",
		"argMaxMerge(k8s_pod_name)", "argMaxMerge(k8s_namespace_name)", "argMaxMerge(k8s_container_name)",
		"argMaxMerge(state)", "argMaxMerge(health)", "argMaxMerge(started_at)", "argMaxMerge(restarts)",
		"min(first_seen) AS c_first", "max(last_seen) AS c_last",
	).GroupBy("container_id", "host_id")
}

// parseImageTags reads container.image.tags as stored in the attribute map (a JSON array, or comma-separated).
func parseImageTags(s string) []string {
	out := []string{}
	if s == "" {
		return out
	}
	if strings.HasPrefix(s, "[") {
		var tags []string
		if json.Unmarshal([]byte(s), &tags) == nil {
			return tags
		}
	}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// scanContainers scans containersQuery rows; withAttrs expects one more column, argMaxMerge(attributes).
func (s *Server) scanContainers(rows query.Rows, withAttrs bool) ([]*containerJSON, error) {
	defer rows.Close()
	now := s.now()
	out := []*containerJSON{}
	for rows.Next() {
		c := &containerJSON{CPUSparkline: [][2]float64{}, MemorySparkline: [][2]float64{}}
		var tags, started string
		var restarts float64
		var first, last time.Time
		dest := []any{&c.ContainerID, &c.HostID, &c.HostName, &c.Name, &c.ImageName, &tags, &c.Runtime, &c.ComposeProject, &c.ComposeService,
			&c.K8sPodName, &c.K8sNamespaceName, &c.K8sContainerName, &c.State, &c.Health, &started, &restarts, &first, &last}
		if withAttrs {
			dest = append(dest, &c.Attributes)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		c.ImageTags = parseImageTags(tags)
		if started != "" {
			c.StartedAt = &started
		}
		if finite(restarts) {
			c.RestartCount = int64(restarts)
		}
		c.FirstSeen, c.LastSeen, c.lastSeen = formatTime(first), formatTime(last), last
		c.Reporting = now.Sub(last) <= containerReportingWindow
		if withAttrs {
			c.Attributes = nonNilMap(c.Attributes)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// containerFilter holds the list filters; nil project/service: any, "" : containers without one.
type containerFilter struct {
	hostID           string
	project, service *string
	state, q         string
}

func parseContainerFilter(r *http.Request) (containerFilter, error) {
	qp := r.URL.Query()
	f := containerFilter{hostID: qp.Get("host_id"), project: optionalParam(qp, "compose_project"), service: optionalParam(qp, "compose_service"),
		state: qp.Get("state"), q: strings.TrimSpace(qp.Get("q"))}
	if f.state != "" && !containerStates[f.state] {
		return f, badRequest("state must be one of running, paused, restarting, exited, created, dead, removing, unknown")
	}
	for name, v := range map[string]string{"host_id": f.hostID, "q": f.q} {
		if len(v) > maxContainerFilterBytes {
			return f, badRequest("%s must be at most %d bytes", name, maxContainerFilterBytes)
		}
	}
	return f, nil
}

func (f containerFilter) match(c *containerJSON) bool {
	if f.project != nil && c.ComposeProject != *f.project {
		return false
	}
	if f.service != nil && c.ComposeService != *f.service {
		return false
	}
	if f.state != "" {
		st := c.State
		if st == "" {
			st = "unknown"
		}
		if st != f.state {
			return false
		}
	}
	if f.q != "" {
		text := strings.ToLower(strings.Join([]string{c.ContainerID, c.Name, c.ImageName, strings.Join(c.ImageTags, " "), c.HostName, c.HostID,
			c.ComposeProject, c.ComposeService, c.K8sPodName, c.K8sNamespaceName, c.K8sContainerName}, " "))
		for _, term := range strings.Fields(strings.ToLower(f.q)) {
			if !strings.Contains(text, term) {
				return false
			}
		}
	}
	return true
}

func sortContainers(cs []*containerJSON) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		switch {
		case a.ComposeProject != b.ComposeProject:
			return a.ComposeProject < b.ComposeProject
		case a.ComposeService != b.ComposeService:
			return a.ComposeService < b.ComposeService
		case a.Name != b.Name:
			return a.Name < b.Name
		}
		return a.ContainerID < b.ContainerID
	})
}

// loadContainers returns the containers seen in [from, to] that match f, sorted by compose project, service and name.
func (s *Server) loadContainers(r *http.Request, sc *query.Scope, f containerFilter, from, to time.Time) ([]*containerJSON, error) {
	q := containersQuery(sc).
		Where("last_seen >= fromUnixTimestamp64Nano({t_from:Int64})").Param("t_from", from.UnixNano()).
		Where("first_seen <= fromUnixTimestamp64Nano({t_to:Int64})").Param("t_to", to.UnixNano()).
		OrderBy("container_id").Limit(maxContainerRows)
	if f.hostID != "" {
		q.Where("host_id = {host_id:String}").Param("host_id", f.hostID)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	all, err := s.scanContainers(rows, false)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if f.match(c) {
			out = append(out, c)
		}
	}
	sortContainers(out)
	return out, nil
}

var containerStatMetrics = []string{"container.cpu.utilization", "container.memory.usage", "container.memory.limit"}

// sparkStep is the bucket width of list sparklines: ≈ containerSparkPoints buckets, whole 10 s.
func sparkStep(from, to time.Time) time.Duration {
	step := max(to.Sub(from)/containerSparkPoints, minStep)
	if rem := step % minStep; rem != 0 {
		step += minStep - rem
	}
	return step
}

// addContainerStats fills latest CPU/memory values and sparklines from container metrics.
func (s *Server) addContainerStats(r *http.Request, sc *query.Scope, cs []*containerJSON, from, to time.Time) error {
	if len(cs) == 0 {
		return nil
	}
	byKey := make(map[string]*containerJSON, len(cs))
	ids := make([]string, 0, len(cs))
	hostSet := map[string]bool{}
	var hosts []string
	for _, c := range cs {
		byKey[c.HostID+"/"+c.ContainerID] = c
		ids = append(ids, c.ContainerID)
		if !hostSet[c.HostID] {
			hostSet[c.HostID] = true
			hosts = append(hosts, c.HostID)
		}
	}
	step := sparkStep(from, to)
	q := sc.From(query.Metrics).Columns("host_id", "attributes['container.id'] AS cid", "metric_name",
		"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t", "avg(value) AS v").
		Where("has({names:Array(String)}, metric_name)").Param("names", containerStatMetrics).
		Where("has({host_ids:Array(String)}, host_id)").Param("host_ids", hosts).
		Where("has({cids:Array(String)}, attributes['container.id'])").Param("cids", ids).
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).Param("step", uint32(step/time.Second)).
		GroupBy("host_id", "cid", "metric_name", "t").OrderBy("t").Limit(s.cfg.MaxRows * 100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hid, cid, name string
		var t time.Time
		var v float64
		if err := rows.Scan(&hid, &cid, &name, &t, &v); err != nil {
			return err
		}
		c := byKey[hid+"/"+cid]
		if c == nil || !finite(v) {
			continue
		}
		p := [2]float64{float64(t.UnixMilli()), v}
		switch name {
		case "container.cpu.utilization":
			c.CPUSparkline, c.CPUUtilization = append(c.CPUSparkline, p), num(v)
		case "container.memory.usage":
			c.MemorySparkline, c.MemoryUsage = append(c.MemorySparkline, p), num(v)
		case "container.memory.limit":
			c.MemoryLimit = num(v)
		}
	}
	return rows.Err()
}

// ---- GET /containers ----

func (s *Server) listContainers(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	f, err := parseContainerFilter(r)
	if err != nil {
		return err
	}
	cs, err := s.loadContainers(r, sc, f, from, to)
	if err != nil {
		return err
	}
	total := len(cs)
	cs = cs[:min(len(cs), limit)]
	if err := s.addContainerStats(r, sc, cs, from, to); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"containers": cs, "total": total, "step": formatStep(sparkStep(from, to))})
	return nil
}

// ---- GET /containers/groups ----

type composeServiceJSON struct {
	ComposeService string   `json:"compose_service"`
	Containers     int      `json:"containers"`
	Running        int      `json:"running"`
	CPUUtilization *float64 `json:"cpu_utilization"`
	MemoryUsage    *float64 `json:"memory_usage"`
}

type composeProjectJSON struct {
	ComposeProject string               `json:"compose_project"`
	HostIDs        []string             `json:"host_ids"`
	Containers     int                  `json:"containers"`
	Running        int                  `json:"running"`
	Services       []composeServiceJSON `json:"services"`
}

func addPtr(a *float64, b *float64) *float64 {
	if b == nil {
		return a
	}
	if a == nil {
		v := *b
		return &v
	}
	v := *a + *b
	return &v
}

// groupContainers groups sorted containers by compose project and service. CPU and memory sum the
// latest values of reporting containers; running counts reporting containers in state running.
func groupContainers(cs []*containerJSON) []composeProjectJSON {
	out := []composeProjectJSON{}
	for _, c := range cs {
		if len(out) == 0 || out[len(out)-1].ComposeProject != c.ComposeProject {
			out = append(out, composeProjectJSON{ComposeProject: c.ComposeProject, HostIDs: []string{}, Services: []composeServiceJSON{}})
		}
		p := &out[len(out)-1]
		if len(p.Services) == 0 || p.Services[len(p.Services)-1].ComposeService != c.ComposeService {
			p.Services = append(p.Services, composeServiceJSON{ComposeService: c.ComposeService})
		}
		sv := &p.Services[len(p.Services)-1]
		if !contains(p.HostIDs, c.HostID) {
			p.HostIDs = append(p.HostIDs, c.HostID)
		}
		p.Containers++
		sv.Containers++
		if c.Reporting && c.State == "running" {
			p.Running++
			sv.Running++
		}
		if c.Reporting {
			sv.CPUUtilization = addPtr(sv.CPUUtilization, c.CPUUtilization)
			sv.MemoryUsage = addPtr(sv.MemoryUsage, c.MemoryUsage)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Server) containerGroups(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	f, err := parseContainerFilter(r)
	if err != nil {
		return err
	}
	cs, err := s.loadContainers(r, sc, f, from, to)
	if err != nil {
		return err
	}
	if len(cs) <= s.cfg.MaxRows {
		if err := s.addContainerStats(r, sc, cs, from, to); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": groupContainers(cs)})
	return nil
}

// ---- GET /containers/{container_id} ----

func containerIDParam(r *http.Request) (string, error) {
	id := strings.ToLower(r.PathValue("container_id"))
	if !isHex(id, 64) {
		return "", badRequest("container_id must be 64 hex characters")
	}
	return id, nil
}

// findContainer returns the latest record of a container (the host it was seen on last), or 404.
func (s *Server) findContainer(r *http.Request, sc *query.Scope, id string) (*containerJSON, error) {
	q := containersQuery(sc).Columns("argMaxMerge(attributes)").
		Where("container_id = {container_id:String}").Param("container_id", id).OrderBy("c_last DESC").Limit(1)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	cs, err := s.scanContainers(rows, true)
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, notFound("container not found")
	}
	return cs[0], nil
}

func (s *Server) getContainer(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	id, err := containerIDParam(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	c, err := s.findContainer(r, sc, id)
	if err != nil {
		return err
	}
	if err := s.addContainerStats(r, sc, []*containerJSON{c}, from, to); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

// ---- GET /containers/{container_id}/timeseries ----

var containerSeriesMetrics = []string{"container.cpu.utilization", "container.memory.usage", "container.memory.limit", "container.network.io", "container.blockio.io"}

type containerSeriesJSON struct {
	CPUUtilization  [][2]float64 `json:"cpu_utilization"`
	MemoryUsage     [][2]float64 `json:"memory_usage"`
	MemoryLimit     [][2]float64 `json:"memory_limit"`
	NetworkReceive  [][2]float64 `json:"network_receive"`
	NetworkTransmit [][2]float64 `json:"network_transmit"`
	BlockIORead     [][2]float64 `json:"blockio_read"`
	BlockIOWrite    [][2]float64 `json:"blockio_write"`
}

// containerSeriesRow is one (metric, direction, series, bucket) aggregate.
type containerSeriesRow struct {
	metric, dir string
	series      uint64
	t           int64 // unix ms
	avg, last   float64
}

// buildContainerSeries turns bucket rows (ordered by t) into chart series: gauges average all
// series of a bucket; cumulative counters become per-second rates per series (resets clamp to 0)
// that are summed per bucket.
func buildContainerSeries(rows []containerSeriesRow) containerSeriesJSON {
	type acc struct{ sum, n float64 }
	type key struct {
		metric, dir string
		t           int64
	}
	gauges := map[key]*acc{}
	rates := map[key]float64{}
	type prevPoint struct {
		t    int64
		last float64
	}
	prev := map[[3]string]prevPoint{}
	var order []key
	seen := map[key]bool{}
	for _, row := range rows {
		k := key{row.metric, row.dir, row.t}
		switch row.metric {
		case "container.network.io", "container.blockio.io":
			sk := [3]string{row.metric, row.dir, formatUint(row.series)}
			p, ok := prev[sk]
			prev[sk] = prevPoint{row.t, row.last}
			if !ok || row.t <= p.t {
				continue
			}
			rates[k] += max(row.last-p.last, 0) / (float64(row.t-p.t) / 1000)
		default:
			a := gauges[k]
			if a == nil {
				a = &acc{}
				gauges[k] = a
			}
			a.sum += row.avg
			a.n++
		}
		if !seen[k] {
			seen[k] = true
			order = append(order, k)
		}
	}
	out := containerSeriesJSON{CPUUtilization: [][2]float64{}, MemoryUsage: [][2]float64{}, MemoryLimit: [][2]float64{},
		NetworkReceive: [][2]float64{}, NetworkTransmit: [][2]float64{}, BlockIORead: [][2]float64{}, BlockIOWrite: [][2]float64{}}
	sort.SliceStable(order, func(i, j int) bool { return order[i].t < order[j].t })
	for _, k := range order {
		var v float64
		if a := gauges[k]; a != nil {
			v = a.sum / a.n
		} else if r, ok := rates[k]; ok {
			v = r
		} else {
			continue
		}
		if !finite(v) {
			continue
		}
		p := [2]float64{float64(k.t), v}
		switch {
		case k.metric == "container.cpu.utilization":
			out.CPUUtilization = append(out.CPUUtilization, p)
		case k.metric == "container.memory.usage":
			out.MemoryUsage = append(out.MemoryUsage, p)
		case k.metric == "container.memory.limit":
			out.MemoryLimit = append(out.MemoryLimit, p)
		case k.metric == "container.network.io" && k.dir == "receive":
			out.NetworkReceive = append(out.NetworkReceive, p)
		case k.metric == "container.network.io" && k.dir == "transmit":
			out.NetworkTransmit = append(out.NetworkTransmit, p)
		case k.metric == "container.blockio.io" && k.dir == "read":
			out.BlockIORead = append(out.BlockIORead, p)
		case k.metric == "container.blockio.io" && k.dir == "write":
			out.BlockIOWrite = append(out.BlockIOWrite, p)
		}
	}
	return out
}

func formatUint(v uint64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *Server) containerTimeseries(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	id, err := containerIDParam(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	step, err := chooseStep(r.URL.Query().Get("step"), from, to, false)
	if err != nil {
		return err
	}
	c, err := s.findContainer(r, sc, id)
	if err != nil {
		return err
	}
	q := sc.From(query.Metrics).Columns("metric_name", "concat(attributes['network.io.direction'], attributes['disk.io.direction']) AS dir",
		"series_id", "toInt64(toUnixTimestamp(toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})))) * 1000 AS t",
		"avg(value) AS v_avg", "argMax(value, timestamp) AS v_last").
		Where("has({names:Array(String)}, metric_name)").Param("names", containerSeriesMetrics).
		Where("host_id = {host_id:String}").Param("host_id", c.HostID).
		Where("attributes['container.id'] = {container_id:String}").Param("container_id", id).
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).Param("step", uint32(step/time.Second)).
		GroupBy("metric_name", "dir", "series_id", "t").OrderBy("t").Limit(s.cfg.MaxRows * 100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	var data []containerSeriesRow
	for rows.Next() {
		var row containerSeriesRow
		if err := rows.Scan(&row.metric, &row.dir, &row.series, &row.t, &row.avg, &row.last); err != nil {
			return err
		}
		data = append(data, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"container_id": id, "host_id": c.HostID, "step": formatStep(step),
		"from": from.UnixMilli(), "to": to.UnixMilli(), "series": buildContainerSeries(data)})
	return nil
}

// ---- GET /containers/{container_id}/services ----

type containerServiceJSON struct {
	serviceIdentity
	FirstSeen string  `json:"first_seen"`
	LastSeen  string  `json:"last_seen"`
	ApdexTMs  float64 `json:"apdex_t_ms"`
	redJSON
}

func (s *Server) containerServices(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	id, err := containerIDParam(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	idCols := []string{"service_name", "service_namespace", "deployment_environment"}
	q := sc.From(query.ApmServiceContainers).Columns(cols(idCols, []string{"min(first_seen) AS m_first", "max(last_seen) AS m_last"})...).
		Where("container_id = {container_id:String}").Param("container_id", id).
		GroupBy(idCols...).OrderBy(idCols...).Limit(1000)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	services := []*containerServiceJSON{}
	var names []string
	for rows.Next() {
		sv := &containerServiceJSON{}
		var first, last time.Time
		if err := rows.Scan(&sv.ServiceName, &sv.ServiceNamespace, &sv.Environment, &first, &last); err != nil {
			rows.Close()
			return err
		}
		sv.FirstSeen, sv.LastSeen = formatTime(first), formatTime(last)
		services = append(services, sv)
		if !contains(names, sv.ServiceName) {
			names = append(names, sv.ServiceName)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(services) > 0 {
		totals := minuteRange(sc.From(query.ApmTransactions1m).Columns(cols(idCols, txAggColumns)...), from, to).
			Where("has({svc_names:Array(String)}, service_name)").Param("svc_names", names).GroupBy(idCols...)
		trows, err := sc.Query(r.Context(), totals)
		if err != nil {
			return err
		}
		aggs := map[apm.ServiceKey]redAgg{}
		for trows.Next() {
			var sid serviceIdentity
			dest, build := txAggDest()
			if err := trows.Scan(append([]any{&sid.ServiceName, &sid.ServiceNamespace, &sid.Environment}, dest...)...); err != nil {
				trows.Close()
				return err
			}
			aggs[sid.key()] = build()
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return err
		}
		settings := s.apmSettings(r)
		minutes := rangeMinutes(from, to)
		for _, sv := range services {
			tMs, _ := s.apdexTMs(settings, sv.key())
			sv.ApdexTMs = tMs
			sv.redJSON = aggs[sv.key()].red(minutes, tMs)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": services})
	return nil
}

// ---- GET /apm/services/{service}/containers ----

type serviceContainerJSON struct {
	ContainerID string `json:"container_id"`
	Name        string `json:"name"`
	HostID      string `json:"host_id"`
	HostName    string `json:"host_name"`
	FirstSeen   string `json:"first_seen"`
	LastSeen    string `json:"last_seen"`
	// Known: the container has infra agent data (containers table); state and metrics are set only then.
	Known          bool     `json:"known"`
	State          string   `json:"state"`
	Reporting      bool     `json:"reporting"`
	CPUUtilization *float64 `json:"cpu_utilization"`
	MemoryUsage    *float64 `json:"memory_usage"`
	MemoryLimit    *float64 `json:"memory_limit"`
}

func (s *Server) apmServiceContainers(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	q := f.apply(sc.From(query.ApmServiceContainers).Columns("container_id", "min(first_seen) AS m_first", "max(last_seen) AS m_last",
		"argMaxMerge(host_id) AS m_host", "argMaxMerge(container_name) AS m_name")).
		Where("last_seen >= fromUnixTimestamp64Nano({t_seen:Int64})").Param("t_seen", from.UnixNano()).
		GroupBy("container_id").OrderBy("m_last DESC").Limit(1000)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	out := []*serviceContainerJSON{}
	var ids []string
	for rows.Next() {
		link := &serviceContainerJSON{}
		var first, last time.Time
		if err := rows.Scan(&link.ContainerID, &first, &last, &link.HostID, &link.Name); err != nil {
			rows.Close()
			return err
		}
		link.FirstSeen, link.LastSeen = formatTime(first), formatTime(last)
		out = append(out, link)
		ids = append(ids, link.ContainerID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) > 0 {
		cq := containersQuery(sc).Where("has({cids:Array(String)}, container_id)").Param("cids", ids).OrderBy("c_last DESC").LimitBy(1, "container_id").Limit(1000)
		crows, err := sc.Query(r.Context(), cq)
		if err != nil {
			return err
		}
		known, err := s.scanContainers(crows, false)
		if err != nil {
			return err
		}
		if err := s.addContainerStats(r, sc, known, from, to); err != nil {
			return err
		}
		byID := map[string]*containerJSON{}
		for _, c := range known {
			byID[c.ContainerID] = c
		}
		for _, l := range out {
			c := byID[l.ContainerID]
			if c == nil {
				continue
			}
			l.Known, l.State, l.Reporting = true, c.State, c.Reporting
			l.HostID, l.HostName = c.HostID, c.HostName
			if c.Name != "" {
				l.Name = c.Name
			}
			l.CPUUtilization, l.MemoryUsage, l.MemoryLimit = c.CPUUtilization, c.MemoryUsage, c.MemoryLimit
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"containers": out})
	return nil
}
