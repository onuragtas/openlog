package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// Kubernetes endpoints (docs/contracts/api.md "Kubernetes", semantic-conventions.md §7): the entity tables
// k8s_clusters, k8s_nodes, k8s_workloads and k8s_pods (schema 0040–0042), kubelet and cluster metrics, and
// Kubernetes events (logs with event_name k8s.event). Every read goes through the tenant-scoped query layer; the
// aggregating tables are always re-aggregated with GROUP BY.

func (s *Server) kubernetesRoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/kubernetes/clusters", s.k8sListClusters)
	route("GET /api/v1/kubernetes/clusters/{cluster_uid}", s.k8sGetCluster)
	route("GET /api/v1/kubernetes/nodes", s.k8sListNodes)
	route("GET /api/v1/kubernetes/workloads", s.k8sListWorkloads)
	route("GET /api/v1/kubernetes/workloads/{cluster_uid}/{namespace}/{kind}/{name}", s.k8sGetWorkload)
	route("GET /api/v1/kubernetes/workloads/{cluster_uid}/{namespace}/{kind}/{name}/timeseries", s.k8sWorkloadTimeseries)
	route("GET /api/v1/kubernetes/pods", s.k8sListPods)
	route("GET /api/v1/kubernetes/pods/{pod_uid}", s.k8sGetPod)
	route("GET /api/v1/kubernetes/pods/{pod_uid}/timeseries", s.k8sPodTimeseries)
	route("GET /api/v1/kubernetes/pods/{pod_uid}/events", s.k8sPodEvents)
	route("GET /api/v1/kubernetes/events", s.k8sListEvents)
	route("GET /api/v1/apm/services/{service}/kubernetes", s.apmServiceKubernetes)
}

const (
	// k8sReportingWindow: an entity whose last data point is older is not reporting (cluster agent interval 30 s).
	k8sReportingWindow = 5 * time.Minute
	// k8sCurrentWindow: child entities (nodes, pods, workloads) count as current when their last point is at most this
	// much older than the latest point of their parent (the same collection of the cluster agent), so a cluster that
	// stopped reporting keeps its last known counts and deleted objects drop out after one interval.
	k8sCurrentWindow   = 2 * time.Minute
	maxK8sRows         = 10000
	maxK8sFilterBytes  = 256
	defaultEventsLimit = 100
	maxEventsLimit     = 1000
	k8sEventName       = "k8s.event"
)

var (
	k8sWorkloadKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "Job": true, "CronJob": true, "ReplicaSet": true, "Pod": true}
	k8sHealthValues  = map[string]bool{"healthy": true, "degraded": true, "unavailable": true, "unknown": true}
	k8sPodPhases     = map[string]bool{"Pending": true, "Running": true, "Succeeded": true, "Failed": true, "Unknown": true}
	k8sEventTypes    = map[string]bool{"Normal": true, "Warning": true}
)

func k8sOptString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func matchTerms(q string, fields ...string) bool {
	if q == "" {
		return true
	}
	text := strings.ToLower(strings.Join(fields, " "))
	for _, term := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

// k8sParams reads and length-checks query parameters.
func k8sParams(r *http.Request, names ...string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	qp := r.URL.Query()
	for _, n := range names {
		v := strings.TrimSpace(qp.Get(n))
		if len(v) > maxK8sFilterBytes {
			return nil, badRequest("%s must be at most %d bytes", n, maxK8sFilterBytes)
		}
		out[n] = v
	}
	return out, nil
}

func k8sPathParam(r *http.Request, name string) (string, error) {
	v := r.PathValue(name)
	if v == "" || len(v) > maxK8sFilterBytes {
		return "", badRequest("%s must be 1-%d bytes", name, maxK8sFilterBytes)
	}
	return v, nil
}

// seenIn restricts an entity table to rows seen in [from, to].
func seenIn(q *query.Select, from, to time.Time) *query.Select {
	return q.Where("last_seen >= fromUnixTimestamp64Nano({t_from:Int64})").Param("t_from", from.UnixNano()).
		Where("first_seen <= fromUnixTimestamp64Nano({t_to:Int64})").Param("t_to", to.UnixNano())
}

// ---- latest metric values ----

type k8sLatest struct {
	v float64
	t time.Time
}

// k8sLatestValues returns the latest value in [from, to] per metric and key (keyExpr is a fixed SQL expression).
func (s *Server) k8sLatestValues(r *http.Request, sc *query.Scope, names []string, keyExpr string, from, to time.Time, filter func(q *query.Select)) (map[string]map[string]k8sLatest, error) {
	q := sc.From(query.Metrics).Columns("metric_name", keyExpr+" AS l_key", "argMax(value, timestamp) AS l_v", "max(timestamp) AS l_t").
		Where("has({l_names:Array(String)}, metric_name)").Param("l_names", names).
		Where("timestamp >= fromUnixTimestamp64Nano({l_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({l_to:Int64})").
		Param("l_from", from.UnixNano()).Param("l_to", to.UnixNano()).
		GroupBy("metric_name", "l_key").Limit(s.cfg.MaxRows * 100)
	filter(q)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]k8sLatest{}
	for rows.Next() {
		var name, key string
		var l k8sLatest
		if err := rows.Scan(&name, &key, &l.v, &l.t); err != nil {
			return nil, err
		}
		if !finite(l.v) {
			continue
		}
		if out[name] == nil {
			out[name] = map[string]k8sLatest{}
		}
		out[name][key] = l
	}
	return out, rows.Err()
}

// sumCurrent sums the values whose time is within k8sCurrentWindow of the newest one (nil without values).
func sumCurrent(vals []k8sLatest) *float64 {
	if len(vals) == 0 {
		return nil
	}
	newest := vals[0].t
	for _, l := range vals {
		if l.t.After(newest) {
			newest = l.t
		}
	}
	sum := 0.0
	for _, l := range vals {
		if !l.t.Before(newest.Add(-k8sCurrentWindow)) {
			sum += l.v
		}
	}
	return &sum
}

func k8sKey(parts ...string) string { return strings.Join(parts, "|") }

// ---- events ----

type k8sEventJSON struct {
	Timestamp   string `json:"timestamp"`
	Type        string `json:"type"`
	Reason      string `json:"reason"`
	Message     string `json:"message"`
	Count       int64  `json:"count"`
	Namespace   string `json:"namespace"`
	ObjectKind  string `json:"object_kind"`
	ObjectName  string `json:"object_name"`
	ObjectUID   string `json:"object_uid"`
	Source      string `json:"source"`
	ClusterUID  string `json:"cluster_uid"`
	ClusterName string `json:"cluster_name"`
}

type k8sEventFilter struct {
	clusterUID, namespace, typ, objectKind, objectName, objectUID, reason string
	limit                                                                 int
}

func parseEventFilter(r *http.Request) (k8sEventFilter, error) {
	p, err := k8sParams(r, "cluster_uid", "namespace", "type", "object_kind", "object_name", "object_uid", "reason")
	if err != nil {
		return k8sEventFilter{}, err
	}
	f := k8sEventFilter{clusterUID: p["cluster_uid"], namespace: p["namespace"], typ: p["type"], objectKind: p["object_kind"],
		objectName: p["object_name"], objectUID: p["object_uid"], reason: p["reason"], limit: defaultEventsLimit}
	if f.typ != "" && !k8sEventTypes[f.typ] {
		return f, badRequest("type must be Normal or Warning")
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return f, badRequest("limit must be a positive integer")
		}
		f.limit = min(n, maxEventsLimit)
	}
	return f, nil
}

// loadEvents reads Kubernetes events (semantic-conventions §7.5), newest first. Updates of one event (same
// k8s.event.uid, growing count) are returned once, as their latest record.
func (s *Server) loadEvents(r *http.Request, sc *query.Scope, f k8sEventFilter, from, to time.Time) ([]k8sEventJSON, error) {
	q := sc.From(query.Logs).Columns("timestamp", "body", "attributes", "resource_attributes").
		Where("event_name = {ev_name:String}").Param("ev_name", k8sEventName).
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).
		OrderBy("timestamp DESC").
		LimitBy(1, "if(attributes['k8s.event.uid'] = '', toString(cityHash64(body, timestamp)), attributes['k8s.event.uid'])").
		Limit(f.limit)
	for _, c := range []struct{ expr, param, v string }{
		{"resource_attributes['k8s.cluster.uid']", "ev_cluster", f.clusterUID},
		{"attributes['k8s.namespace.name']", "ev_ns", f.namespace},
		{"attributes['k8s.event.type']", "ev_type", f.typ},
		{"attributes['k8s.object.kind']", "ev_okind", f.objectKind},
		{"attributes['k8s.object.name']", "ev_oname", f.objectName},
		{"attributes['k8s.object.uid']", "ev_ouid", f.objectUID},
		{"attributes['k8s.event.reason']", "ev_reason", f.reason},
	} {
		if c.v != "" {
			q.Where(c.expr+" = {"+c.param+":String}").Param(c.param, c.v)
		}
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []k8sEventJSON{}
	for rows.Next() {
		var ts time.Time
		var body string
		var attrs, res map[string]string
		if err := rows.Scan(&ts, &body, &attrs, &res); err != nil {
			return nil, err
		}
		n, _ := strconv.ParseInt(attrs["k8s.event.count"], 10, 64)
		out = append(out, k8sEventJSON{Timestamp: formatTime(ts), Type: attrs["k8s.event.type"], Reason: attrs["k8s.event.reason"], Message: body,
			Count: n, Namespace: attrs["k8s.namespace.name"], ObjectKind: attrs["k8s.object.kind"], ObjectName: attrs["k8s.object.name"],
			ObjectUID: attrs["k8s.object.uid"], Source: attrs["k8s.event.source"], ClusterUID: res["k8s.cluster.uid"], ClusterName: res["k8s.cluster.name"]})
	}
	return out, rows.Err()
}

func (s *Server) k8sListEvents(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	f, err := parseEventFilter(r)
	if err != nil {
		return err
	}
	events, err := s.loadEvents(r, sc, f, from, to)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
	return nil
}

func (s *Server) k8sPodEvents(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	uid, err := k8sPathParam(r, "pod_uid")
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	f, err := parseEventFilter(r)
	if err != nil {
		return err
	}
	f.objectUID = uid
	events, err := s.loadEvents(r, sc, f, from, to)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
	return nil
}

// ---- hosts of nodes ----

type k8sHostRef struct {
	id, name string
	last     time.Time
}

// nodeHosts maps cluster name|node name to the host whose resource attributes carry them (latest host wins).
func (s *Server) nodeHosts(r *http.Request, sc *query.Scope, nodeNames []string) (map[string]k8sHostRef, error) {
	out := map[string]k8sHostRef{}
	if len(nodeNames) == 0 {
		return out, nil
	}
	q := sc.From(query.Hosts).Columns("host_id", "argMax(host_name, last_seen)", "argMax(resource_attributes['k8s.cluster.name'], last_seen)",
		"argMax(resource_attributes['k8s.node.name'], last_seen)", "max(last_seen) AS h_last").
		Where("has({node_names:Array(String)}, resource_attributes['k8s.node.name'])").Param("node_names", nodeNames).
		GroupBy("host_id").Limit(maxK8sRows)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h k8sHostRef
		var cluster, node string
		if err := rows.Scan(&h.id, &h.name, &cluster, &node, &h.last); err != nil {
			return nil, err
		}
		k := k8sKey(cluster, node)
		if old, ok := out[k]; !ok || h.last.After(old.last) {
			out[k] = h
		}
	}
	return out, rows.Err()
}

// ---- pods ----

type k8sPodJSON struct {
	ClusterUID       string   `json:"cluster_uid"`
	ClusterName      string   `json:"cluster_name"`
	Namespace        string   `json:"namespace"`
	PodName          string   `json:"pod_name"`
	PodUID           string   `json:"pod_uid"`
	NodeName         string   `json:"node_name"`
	WorkloadKind     string   `json:"workload_kind"`
	WorkloadName     string   `json:"workload_name"`
	Phase            string   `json:"phase"`
	Ready            bool     `json:"ready"`
	Reason           string   `json:"reason"`
	Status           string   `json:"status"`
	Restarts         int64    `json:"restarts"`
	PodIP            string   `json:"pod_ip"`
	QOSClass         string   `json:"qos_class"`
	CreatedAt        *string  `json:"created_at"`
	StartedAt        *string  `json:"started_at"`
	CPUUsage         *float64 `json:"cpu_usage"`
	MemoryWorkingSet *float64 `json:"memory_working_set"`
	FirstSeen        string   `json:"first_seen"`
	LastSeen         string   `json:"last_seen"`
	Reporting        bool     `json:"reporting"`

	containersJSON string
	lastSeen       time.Time
}

// podsQuery merges the pod table per pod; the outer select filters on the merged values.
func podsQuery(sc *query.Scope, from, to time.Time) *query.Select {
	inner := seenIn(sc.From(query.K8sPods).Columns("cluster_uid", "pod_uid", "argMaxMerge(cluster_name) AS p_cluster", "argMaxMerge(namespace) AS p_ns",
		"argMaxMerge(pod_name) AS p_name", "argMaxMerge(node_name) AS p_node", "argMaxMerge(workload_kind) AS p_wkind", "argMaxMerge(workload_name) AS p_wname",
		"argMaxMerge(phase) AS p_phase", "argMaxMerge(ready) AS p_ready", "argMaxMerge(reason) AS p_reason", "argMaxMerge(restarts) AS p_restarts",
		"argMaxMerge(pod_ip) AS p_ip", "argMaxMerge(qos_class) AS p_qos", "argMaxMerge(created_at) AS p_created", "argMaxMerge(started_at) AS p_started",
		"argMaxMerge(containers_json) AS p_cjson", "min(first_seen) AS p_first", "max(last_seen) AS p_last"), from, to).
		GroupBy("cluster_uid", "pod_uid")
	return sc.FromSub(inner).Columns("cluster_uid", "pod_uid", "p_cluster", "p_ns", "p_name", "p_node", "p_wkind", "p_wname", "p_phase", "p_ready",
		"p_reason", "p_restarts", "p_ip", "p_qos", "p_created", "p_started", "p_cjson", "p_first", "p_last")
}

func (s *Server) scanPods(rows query.Rows) ([]*k8sPodJSON, error) {
	defer rows.Close()
	now := s.now()
	out := []*k8sPodJSON{}
	for rows.Next() {
		p := &k8sPodJSON{}
		var ready, created, started string
		var first, last time.Time
		if err := rows.Scan(&p.ClusterUID, &p.PodUID, &p.ClusterName, &p.Namespace, &p.PodName, &p.NodeName, &p.WorkloadKind, &p.WorkloadName,
			&p.Phase, &ready, &p.Reason, &p.Restarts, &p.PodIP, &p.QOSClass, &created, &started, &p.containersJSON, &first, &last); err != nil {
			return nil, err
		}
		p.Ready = ready == "true"
		p.Status = p.Phase
		if p.Reason != "" {
			p.Status = p.Reason
		}
		p.CreatedAt, p.StartedAt = k8sOptString(created), k8sOptString(started)
		p.FirstSeen, p.LastSeen, p.lastSeen = formatTime(first), formatTime(last), last
		p.Reporting = now.Sub(last) <= k8sReportingWindow
		out = append(out, p)
	}
	return out, rows.Err()
}

type k8sPodFilter struct {
	clusterUID, namespace, node, workloadKind, workloadName, phase, q string
}

func (f k8sPodFilter) apply(q *query.Select) {
	for _, c := range []struct{ col, param, v string }{
		{"cluster_uid", "pf_cluster", f.clusterUID}, {"p_ns", "pf_ns", f.namespace}, {"p_node", "pf_node", f.node},
		{"p_wkind", "pf_wkind", f.workloadKind}, {"p_wname", "pf_wname", f.workloadName}, {"p_phase", "pf_phase", f.phase},
	} {
		if c.v != "" {
			q.Where(c.col+" = {"+c.param+":String}").Param(c.param, c.v)
		}
	}
}

func (s *Server) loadPods(r *http.Request, sc *query.Scope, f k8sPodFilter, from, to time.Time) ([]*k8sPodJSON, error) {
	q := podsQuery(sc, from, to).OrderBy("p_cluster", "p_ns", "p_name", "pod_uid").Limit(maxK8sRows)
	f.apply(q)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	all, err := s.scanPods(rows)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, p := range all {
		if matchTerms(f.q, p.PodName, p.PodUID, p.Namespace, p.NodeName, p.WorkloadName, p.ClusterName, p.Status, p.PodIP) {
			out = append(out, p)
		}
	}
	return out, nil
}

// addPodStats sets the latest kubelet CPU and working set of each pod.
func (s *Server) addPodStats(r *http.Request, sc *query.Scope, pods []*k8sPodJSON, from, to time.Time) error {
	if len(pods) == 0 {
		return nil
	}
	uids := make([]string, 0, len(pods))
	for _, p := range pods {
		uids = append(uids, p.PodUID)
	}
	vals, err := s.k8sLatestValues(r, sc, []string{"k8s.pod.cpu.usage", "k8s.pod.memory.working_set"}, "attributes['k8s.pod.uid']", from, to,
		func(q *query.Select) {
			q.Where("has({pod_uids:Array(String)}, attributes['k8s.pod.uid'])").Param("pod_uids", uids)
		})
	if err != nil {
		return err
	}
	for _, p := range pods {
		if l, ok := vals["k8s.pod.cpu.usage"][p.PodUID]; ok {
			p.CPUUsage = num(l.v)
		}
		if l, ok := vals["k8s.pod.memory.working_set"][p.PodUID]; ok {
			p.MemoryWorkingSet = num(l.v)
		}
	}
	return nil
}

func (s *Server) k8sListPods(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	p, err := k8sParams(r, "cluster_uid", "namespace", "node", "workload_kind", "workload_name", "phase", "q")
	if err != nil {
		return err
	}
	f := k8sPodFilter{clusterUID: p["cluster_uid"], namespace: p["namespace"], node: p["node"], workloadKind: p["workload_kind"],
		workloadName: p["workload_name"], phase: p["phase"], q: p["q"]}
	if f.phase != "" && !k8sPodPhases[f.phase] {
		return badRequest("phase must be one of Pending, Running, Succeeded, Failed, Unknown")
	}
	if f.workloadKind != "" && !k8sWorkloadKinds[f.workloadKind] {
		return badRequest("workload_kind must be one of Deployment, StatefulSet, DaemonSet, Job, CronJob, ReplicaSet, Pod")
	}
	pods, err := s.loadPods(r, sc, f, from, to)
	if err != nil {
		return err
	}
	total := len(pods)
	pods = pods[:min(len(pods), limit)]
	if err := s.addPodStats(r, sc, pods, from, to); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"pods": pods, "total": total})
	return nil
}

// ---- GET /kubernetes/pods/{pod_uid} ----

type k8sPodContainerJSON struct {
	Name             string   `json:"name"`
	ContainerID      string   `json:"container_id"`
	Image            string   `json:"image"`
	Ready            bool     `json:"ready"`
	Restarts         int64    `json:"restarts"`
	State            string   `json:"state"`
	Reason           string   `json:"reason"`
	Known            bool     `json:"known"`
	HostID           *string  `json:"host_id"`
	CPUUsage         *float64 `json:"cpu_usage"`
	MemoryWorkingSet *float64 `json:"memory_working_set"`
	CPURequest       *float64 `json:"cpu_request"`
	CPULimit         *float64 `json:"cpu_limit"`
	MemoryRequest    *float64 `json:"memory_request"`
	MemoryLimit      *float64 `json:"memory_limit"`
}

type k8sPodServiceJSON struct {
	ServiceName           string `json:"service_name"`
	ServiceNamespace      string `json:"service_namespace"`
	DeploymentEnvironment string `json:"deployment_environment"`
}

type k8sPodDetailJSON struct {
	*k8sPodJSON
	Containers []*k8sPodContainerJSON `json:"containers"`
	Labels     map[string]string      `json:"labels"`
	Services   []k8sPodServiceJSON    `json:"services"`
	HostID     *string                `json:"host_id"`
	HostName   *string                `json:"host_name"`
}

// parsePodContainers reads openlog.k8s.pod.containers leniently (numbers and booleans may arrive as strings).
func parsePodContainers(s string) []*k8sPodContainerJSON {
	out := []*k8sPodContainerJSON{}
	var raw []map[string]any
	if s == "" || json.Unmarshal([]byte(s), &raw) != nil {
		return out
	}
	str := func(v any) string {
		switch x := v.(type) {
		case string:
			return x
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(x)
		}
		return ""
	}
	for _, m := range raw {
		c := &k8sPodContainerJSON{Name: str(m["name"]), ContainerID: strings.ToLower(str(m["container_id"])), Image: str(m["image"]),
			State: str(m["state"]), Reason: str(m["reason"]), Ready: str(m["ready"]) == "true"}
		if n, err := strconv.ParseFloat(str(m["restarts"]), 64); err == nil && finite(n) {
			c.Restarts = int64(n)
		}
		out = append(out, c)
	}
	return out
}

// findPod returns the latest record of a pod, or 404.
func (s *Server) findPod(r *http.Request, sc *query.Scope, uid string, from, to time.Time) (*k8sPodJSON, error) {
	// Any time within retention: the detail of a pod that was deleted before from still opens.
	q := podsQuery(sc, time.Unix(0, 0), to).Where("pod_uid = {pod_uid:String}").Param("pod_uid", uid).OrderBy("p_last DESC").Limit(1)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	pods, err := s.scanPods(rows)
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		return nil, notFound("pod not found")
	}
	return pods[0], nil
}

var k8sContainerStatMetrics = []string{"k8s.container.cpu.usage", "k8s.container.memory.working_set", "k8s.container.cpu_request",
	"k8s.container.cpu_limit", "k8s.container.memory_request", "k8s.container.memory_limit"}

func (s *Server) k8sGetPod(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	uid, err := k8sPathParam(r, "pod_uid")
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	pod, err := s.findPod(r, sc, uid, from, to)
	if err != nil {
		return err
	}
	if err := s.addPodStats(r, sc, []*k8sPodJSON{pod}, from, to); err != nil {
		return err
	}
	d := &k8sPodDetailJSON{k8sPodJSON: pod, Containers: parsePodContainers(pod.containersJSON), Labels: map[string]string{}, Services: []k8sPodServiceJSON{}}
	var cids []string
	for _, c := range d.Containers {
		if isHex(c.ContainerID, 64) {
			cids = append(cids, c.ContainerID)
		}
	}
	stats, err := s.k8sLatestValues(r, sc, k8sContainerStatMetrics, "attributes['k8s.container.name']", from, to,
		func(q *query.Select) { q.Where("attributes['k8s.pod.uid'] = {pod_uid:String}").Param("pod_uid", uid) })
	if err != nil {
		return err
	}
	for _, c := range d.Containers {
		val := func(metric string) *float64 {
			if l, ok := stats[metric][c.Name]; ok {
				return num(l.v)
			}
			return nil
		}
		c.CPUUsage, c.MemoryWorkingSet = val("k8s.container.cpu.usage"), val("k8s.container.memory.working_set")
		c.CPURequest, c.CPULimit = val("k8s.container.cpu_request"), val("k8s.container.cpu_limit")
		c.MemoryRequest, c.MemoryLimit = val("k8s.container.memory_request"), val("k8s.container.memory_limit")
	}
	if len(cids) > 0 {
		cq := containersQuery(sc).Columns("argMaxMerge(attributes)").Where("has({cids:Array(String)}, container_id)").Param("cids", cids).
			OrderBy("c_last DESC").LimitBy(1, "container_id").Limit(1000)
		crows, err := sc.Query(r.Context(), cq)
		if err != nil {
			return err
		}
		known, err := s.scanContainers(crows, true)
		if err != nil {
			return err
		}
		byID := map[string]*containerJSON{}
		for _, c := range known {
			byID[c.ContainerID] = c
			for k, v := range c.Attributes {
				if key, ok := strings.CutPrefix(k, "k8s.pod.label."); ok {
					d.Labels[key] = v
				}
			}
		}
		for _, c := range d.Containers {
			if kc := byID[c.ContainerID]; kc != nil {
				c.Known = true
				id := kc.HostID
				c.HostID = &id
			}
		}
		idCols := []string{"service_name", "service_namespace", "deployment_environment"}
		sq := sc.From(query.ApmServiceContainers).Columns(idCols...).Where("has({cids:Array(String)}, container_id)").Param("cids", cids).
			GroupBy(idCols...).OrderBy(idCols...).Limit(1000)
		srows, err := sc.Query(r.Context(), sq)
		if err != nil {
			return err
		}
		for srows.Next() {
			var sv k8sPodServiceJSON
			if err := srows.Scan(&sv.ServiceName, &sv.ServiceNamespace, &sv.DeploymentEnvironment); err != nil {
				srows.Close()
				return err
			}
			d.Services = append(d.Services, sv)
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return err
		}
	}
	if pod.NodeName != "" {
		hosts, err := s.nodeHosts(r, sc, []string{pod.NodeName})
		if err != nil {
			return err
		}
		if h, ok := hosts[k8sKey(pod.ClusterName, pod.NodeName)]; ok {
			d.HostID, d.HostName = &h.id, &h.name
		}
	}
	writeJSON(w, http.StatusOK, d)
	return nil
}

// ---- GET /kubernetes/pods/{pod_uid}/timeseries ----

type k8sPodSeriesJSON struct {
	CPUUsage         [][2]float64 `json:"cpu_usage"`
	MemoryWorkingSet [][2]float64 `json:"memory_working_set"`
	NetworkReceive   [][2]float64 `json:"network_receive"`
	NetworkTransmit  [][2]float64 `json:"network_transmit"`
	Restarts         [][2]float64 `json:"restarts"`
}

// k8sToContainerMetric maps kubelet pod metrics onto the container series builder (gauges averaged per bucket,
// cumulative network counters turned into per-second rates).
var k8sToContainerMetric = map[string]string{"k8s.pod.cpu.usage": "container.cpu.utilization", "k8s.pod.memory.working_set": "container.memory.usage",
	"k8s.pod.network.io": "container.network.io"}

// bucketSeries reads (t ms, value) rows ordered by t.
func bucketSeries(rows query.Rows) ([][2]float64, error) {
	defer rows.Close()
	out := [][2]float64{}
	for rows.Next() {
		var t int64
		var v float64
		if err := rows.Scan(&t, &v); err != nil {
			return nil, err
		}
		if finite(v) {
			out = append(out, [2]float64{float64(t), v})
		}
	}
	return out, rows.Err()
}

const bucketExpr = "toInt64(toUnixTimestamp(toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})))) * 1000 AS t"

func metricRange(q *query.Select, from, to time.Time, step time.Duration) *query.Select {
	return q.Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).Param("step", uint32(step/time.Second))
}

// restartSeries sums the latest restart count per pod per bucket (openlog.k8s.pod.status); filter selects the pods.
func (s *Server) restartSeries(r *http.Request, sc *query.Scope, from, to time.Time, step time.Duration, filter func(q *query.Select)) ([][2]float64, error) {
	inner := metricRange(sc.From(query.Metrics).Columns("attributes['k8s.pod.uid'] AS r_pod", bucketExpr,
		"argMax(toFloat64OrZero(attributes[{ak_restarts:String}]), timestamp) AS r_v"), from, to, step).
		Param("ak_restarts", "openlog.k8s.pod.restarts").
		Where("metric_name = {r_metric:String}").Param("r_metric", "openlog.k8s.pod.status").
		GroupBy("r_pod", "t")
	filter(inner)
	q := sc.FromSub(inner).Columns("t", "sum(r_v)").GroupBy("t").OrderBy("t").Limit(s.cfg.MaxRows * 10)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	return bucketSeries(rows)
}

func (s *Server) k8sPodTimeseries(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	uid, err := k8sPathParam(r, "pod_uid")
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
	if _, err := s.findPod(r, sc, uid, from, to); err != nil {
		return err
	}
	names := make([]string, 0, len(k8sToContainerMetric))
	for n := range k8sToContainerMetric {
		names = append(names, n)
	}
	sort.Strings(names)
	q := metricRange(sc.From(query.Metrics).Columns("metric_name", "attributes['network.io.direction'] AS dir", "series_id", bucketExpr,
		"avg(value) AS v_avg", "argMax(value, timestamp) AS v_last"), from, to, step).
		Where("has({names:Array(String)}, metric_name)").Param("names", names).
		Where("attributes['k8s.pod.uid'] = {pod_uid:String}").Param("pod_uid", uid).
		GroupBy("metric_name", "dir", "series_id", "t").OrderBy("t").Limit(s.cfg.MaxRows * 100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	var data []containerSeriesRow
	for rows.Next() {
		var row containerSeriesRow
		if err := rows.Scan(&row.metric, &row.dir, &row.series, &row.t, &row.avg, &row.last); err != nil {
			rows.Close()
			return err
		}
		row.metric = k8sToContainerMetric[row.metric]
		data = append(data, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	cs := buildContainerSeries(data)
	restarts, err := s.restartSeries(r, sc, from, to, step, func(q *query.Select) {
		q.Where("attributes['k8s.pod.uid'] = {pod_uid:String}").Param("pod_uid", uid)
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"step": formatStep(step), "from": from.UnixMilli(), "to": to.UnixMilli(),
		"series": k8sPodSeriesJSON{CPUUsage: cs.CPUUtilization, MemoryWorkingSet: cs.MemoryUsage, NetworkReceive: cs.NetworkReceive,
			NetworkTransmit: cs.NetworkTransmit, Restarts: restarts}})
	return nil
}

// ---- workloads ----

type k8sHPAJSON struct {
	Name            string   `json:"name"`
	MinReplicas     *float64 `json:"min_replicas"`
	MaxReplicas     *float64 `json:"max_replicas"`
	CurrentReplicas *float64 `json:"current_replicas"`
	DesiredReplicas *float64 `json:"desired_replicas"`
}

type k8sWorkloadJSON struct {
	ClusterUID       string       `json:"cluster_uid"`
	ClusterName      string       `json:"cluster_name"`
	Namespace        string       `json:"namespace"`
	Kind             string       `json:"kind"`
	Name             string       `json:"name"`
	UID              string       `json:"uid"`
	Desired          int64        `json:"desired"`
	Ready            int64        `json:"ready"`
	Available        int64        `json:"available"`
	Updated          int64        `json:"updated"`
	Health           string       `json:"health"`
	Pods             int          `json:"pods"`
	Restarts         int64        `json:"restarts"`
	CPUUsage         *float64     `json:"cpu_usage"`
	MemoryWorkingSet *float64     `json:"memory_working_set"`
	CPUSparkline     [][2]float64 `json:"cpu_sparkline"`
	MemorySparkline  [][2]float64 `json:"memory_sparkline"`
	CreatedAt        *string      `json:"created_at"`
	FirstSeen        string       `json:"first_seen"`
	LastSeen         string       `json:"last_seen"`
	Reporting        bool         `json:"reporting"`

	lastSeen time.Time
}

func (wl *k8sWorkloadJSON) key() string {
	return k8sKey(wl.ClusterUID, wl.Namespace, wl.Kind, wl.Name)
}

// workloadHealth computes the health of a workload (api.md "Kubernetes").
func workloadHealth(kind string, desired, available, updated int64, reporting bool) string {
	if !reporting {
		return "unknown"
	}
	switch kind {
	case "CronJob":
		return "healthy"
	case "Job":
		// Job: available = succeeded, updated = failed pods.
		if updated > 0 && available < desired {
			return "degraded"
		}
		return "healthy"
	}
	switch {
	case available >= desired:
		return "healthy"
	case available <= 0:
		return "unavailable"
	}
	return "degraded"
}

func workloadsQuery(sc *query.Scope) *query.Select {
	return sc.From(query.K8sWorkloads).Columns("cluster_uid", "namespace", "kind", "name", "argMaxMerge(cluster_name)", "argMaxMerge(uid)",
		"argMaxMerge(desired)", "argMaxMerge(ready)", "argMaxMerge(available)", "argMaxMerge(updated)", "argMaxMerge(created_at)",
		"min(first_seen) AS w_first", "max(last_seen) AS w_last").
		GroupBy("cluster_uid", "namespace", "kind", "name")
}

// scanWorkloads scans workloadsQuery rows; withAttrs expects one more column, argMaxMerge(attributes).
func (s *Server) scanWorkloads(rows query.Rows, withAttrs bool) ([]*k8sWorkloadJSON, []map[string]string, error) {
	defer rows.Close()
	now := s.now()
	out := []*k8sWorkloadJSON{}
	var attrs []map[string]string
	for rows.Next() {
		wl := &k8sWorkloadJSON{CPUSparkline: [][2]float64{}, MemorySparkline: [][2]float64{}}
		var created string
		var first, last time.Time
		var a map[string]string
		dest := []any{&wl.ClusterUID, &wl.Namespace, &wl.Kind, &wl.Name, &wl.ClusterName, &wl.UID, &wl.Desired, &wl.Ready, &wl.Available, &wl.Updated,
			&created, &first, &last}
		if withAttrs {
			dest = append(dest, &a)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, err
		}
		wl.CreatedAt = k8sOptString(created)
		wl.FirstSeen, wl.LastSeen, wl.lastSeen = formatTime(first), formatTime(last), last
		wl.Reporting = now.Sub(last) <= k8sReportingWindow
		wl.Health = workloadHealth(wl.Kind, wl.Desired, wl.Available, wl.Updated, wl.Reporting)
		out = append(out, wl)
		attrs = append(attrs, nonNilMap(a))
	}
	return out, attrs, rows.Err()
}

type k8sWorkloadFilter struct {
	clusterUID, namespace, kind, health, q string
	clusterUIDs                            []string
}

func (s *Server) loadWorkloads(r *http.Request, sc *query.Scope, f k8sWorkloadFilter, from, to time.Time) ([]*k8sWorkloadJSON, error) {
	q := seenIn(workloadsQuery(sc), from, to).OrderBy("cluster_uid", "namespace", "kind", "name").Limit(maxK8sRows)
	if f.clusterUID != "" {
		q.Where("cluster_uid = {w_cluster:String}").Param("w_cluster", f.clusterUID)
	}
	if len(f.clusterUIDs) > 0 {
		q.Where("has({w_clusters:Array(String)}, cluster_uid)").Param("w_clusters", f.clusterUIDs)
	}
	if f.namespace != "" {
		q.Where("namespace = {w_ns:String}").Param("w_ns", f.namespace)
	}
	if f.kind != "" {
		q.Where("kind = {w_kind:String}").Param("w_kind", f.kind)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	all, _, err := s.scanWorkloads(rows, false)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, wl := range all {
		if (f.health == "" || wl.Health == f.health) && matchTerms(f.q, wl.Name, wl.Namespace, wl.Kind, wl.ClusterName, wl.UID) {
			out = append(out, wl)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.ClusterName != b.ClusterName:
			return a.ClusterName < b.ClusterName
		case a.Namespace != b.Namespace:
			return a.Namespace < b.Namespace
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return out, nil
}

// addWorkloadPods counts the current pods of each workload and sums their restarts.
func (s *Server) addWorkloadPods(r *http.Request, sc *query.Scope, wls []*k8sWorkloadJSON, from, to time.Time) error {
	if len(wls) == 0 {
		return nil
	}
	byKey := map[string]*k8sWorkloadJSON{}
	var clusters, names []string
	for _, wl := range wls {
		byKey[wl.key()] = wl
		if !contains(clusters, wl.ClusterUID) {
			clusters = append(clusters, wl.ClusterUID)
		}
		if !contains(names, wl.Name) {
			names = append(names, wl.Name)
		}
	}
	inner := podsQuery(sc, from, to).Where("has({wp_clusters:Array(String)}, cluster_uid)").Param("wp_clusters", clusters).
		Where("has({wp_names:Array(String)}, p_wname)").Param("wp_names", names)
	q := sc.FromSub(inner).Columns("cluster_uid", "p_ns", "p_wkind", "p_wname", "toInt64(toUnixTimestamp(p_last)) AS p_sec", "count() AS n", "sum(p_restarts) AS rs").
		GroupBy("cluster_uid", "p_ns", "p_wkind", "p_wname", "p_sec").Limit(s.cfg.MaxRows * 100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cluster, ns, kind, name string
		var sec, restarts int64
		var n uint64
		if err := rows.Scan(&cluster, &ns, &kind, &name, &sec, &n, &restarts); err != nil {
			return err
		}
		wl := byKey[k8sKey(cluster, ns, kind, name)]
		if wl == nil || time.Unix(sec+1, 0).Before(wl.lastSeen.Add(-k8sCurrentWindow)) {
			continue
		}
		wl.Pods += int(n)
		wl.Restarts += restarts
	}
	return rows.Err()
}

var k8sPodUsageMetrics = []string{"k8s.pod.cpu.usage", "k8s.pod.memory.working_set"}

// workloadMetricKey is the workload of a kubelet pod metric point: cluster name|namespace|kind|name. openlog.* keys
// are bound as parameters (the query layer rejects "openlog" in fragments); workloadMetricFilter binds them.
const workloadMetricKey = "concat(resource_attributes['k8s.cluster.name'], '|', attributes['k8s.namespace.name'], '|', " +
	"attributes[{ak_wkind:String}], '|', attributes[{ak_wname:String}])"

// bindWorkloadKeys binds the openlog.k8s.workload.kind/name attribute keys used as {ak_wkind} and {ak_wname}.
func bindWorkloadKeys(q *query.Select) *query.Select {
	return q.Param("ak_wkind", "openlog.k8s.workload.kind").Param("ak_wname", "openlog.k8s.workload.name")
}

func workloadMetricFilter(q *query.Select, wls []*k8sWorkloadJSON) {
	bindWorkloadKeys(q)
	var names, clusters []string
	for _, wl := range wls {
		if !contains(names, wl.Name) {
			names = append(names, wl.Name)
		}
		if !contains(clusters, wl.ClusterName) {
			clusters = append(clusters, wl.ClusterName)
		}
	}
	q.Where("has({wm_names:Array(String)}, attributes[{ak_wname:String}])").Param("wm_names", names).
		Where("has({wm_clusters:Array(String)}, resource_attributes['k8s.cluster.name'])").Param("wm_clusters", clusters)
}

func (wl *k8sWorkloadJSON) metricKey() string {
	return k8sKey(wl.ClusterName, wl.Namespace, wl.Kind, wl.Name)
}

// usageSeries sums the per-pod average of kubelet pod metrics per workload and bucket.
func (s *Server) usageSeries(r *http.Request, sc *query.Scope, wls []*k8sWorkloadJSON, from, to time.Time, step time.Duration) (map[string]map[string][][2]float64, error) {
	inner := metricRange(sc.From(query.Metrics).Columns("metric_name", workloadMetricKey+" AS wk", "attributes['k8s.pod.uid'] AS pu", bucketExpr, "avg(value) AS v"), from, to, step).
		Where("has({u_names:Array(String)}, metric_name)").Param("u_names", k8sPodUsageMetrics).
		GroupBy("metric_name", "wk", "pu", "t")
	workloadMetricFilter(inner, wls)
	q := sc.FromSub(inner).Columns("metric_name", "wk", "t", "sum(v)").GroupBy("metric_name", "wk", "t").OrderBy("t").Limit(s.cfg.MaxRows * 100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string][][2]float64{}
	for rows.Next() {
		var name, key string
		var t int64
		var v float64
		if err := rows.Scan(&name, &key, &t, &v); err != nil {
			return nil, err
		}
		if !finite(v) {
			continue
		}
		if out[name] == nil {
			out[name] = map[string][][2]float64{}
		}
		out[name][key] = append(out[name][key], [2]float64{float64(t), v})
	}
	return out, rows.Err()
}

// addWorkloadStats fills latest CPU / working set (sum over current pods) and sparklines.
func (s *Server) addWorkloadStats(r *http.Request, sc *query.Scope, wls []*k8sWorkloadJSON, from, to time.Time) error {
	if len(wls) == 0 {
		return nil
	}
	latest, err := s.k8sLatestValues(r, sc, k8sPodUsageMetrics, "concat("+workloadMetricKey+", '|', attributes['k8s.pod.uid'])", from, to,
		func(q *query.Select) { workloadMetricFilter(q, wls) })
	if err != nil {
		return err
	}
	grouped := map[string]map[string][]k8sLatest{}
	for metric, byKey := range latest {
		grouped[metric] = map[string][]k8sLatest{}
		for key, l := range byKey {
			wk := key[:max(strings.LastIndex(key, "|"), 0)]
			grouped[metric][wk] = append(grouped[metric][wk], l)
		}
	}
	series, err := s.usageSeries(r, sc, wls, from, to, sparkStep(from, to))
	if err != nil {
		return err
	}
	for _, wl := range wls {
		k := wl.metricKey()
		wl.CPUUsage = sumCurrent(grouped["k8s.pod.cpu.usage"][k])
		wl.MemoryWorkingSet = sumCurrent(grouped["k8s.pod.memory.working_set"][k])
		if p := series["k8s.pod.cpu.usage"][k]; p != nil {
			wl.CPUSparkline = p
		}
		if p := series["k8s.pod.memory.working_set"][k]; p != nil {
			wl.MemorySparkline = p
		}
	}
	return nil
}

func parseWorkloadFilter(r *http.Request) (k8sWorkloadFilter, error) {
	p, err := k8sParams(r, "cluster_uid", "namespace", "kind", "health", "q")
	if err != nil {
		return k8sWorkloadFilter{}, err
	}
	f := k8sWorkloadFilter{clusterUID: p["cluster_uid"], namespace: p["namespace"], kind: p["kind"], health: p["health"], q: p["q"]}
	if f.kind != "" && !k8sWorkloadKinds[f.kind] {
		return f, badRequest("kind must be one of Deployment, StatefulSet, DaemonSet, Job, CronJob, ReplicaSet, Pod")
	}
	if f.health != "" && !k8sHealthValues[f.health] {
		return f, badRequest("health must be one of healthy, degraded, unavailable, unknown")
	}
	return f, nil
}

func (s *Server) k8sListWorkloads(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	f, err := parseWorkloadFilter(r)
	if err != nil {
		return err
	}
	wls, err := s.loadWorkloads(r, sc, f, from, to)
	if err != nil {
		return err
	}
	total := len(wls)
	wls = wls[:min(len(wls), limit)]
	if err := s.addWorkloadPods(r, sc, wls, from, to); err != nil {
		return err
	}
	if err := s.addWorkloadStats(r, sc, wls, from, to); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"workloads": wls, "total": total, "step": formatStep(sparkStep(from, to))})
	return nil
}

type k8sWorkloadPath struct{ clusterUID, namespace, kind, name string }

func parseWorkloadPath(r *http.Request) (k8sWorkloadPath, error) {
	var p k8sWorkloadPath
	var err error
	if p.clusterUID, err = k8sPathParam(r, "cluster_uid"); err != nil {
		return p, err
	}
	if p.namespace, err = k8sPathParam(r, "namespace"); err != nil {
		return p, err
	}
	if p.kind, err = k8sPathParam(r, "kind"); err != nil {
		return p, err
	}
	if !k8sWorkloadKinds[p.kind] {
		return p, badRequest("kind must be one of Deployment, StatefulSet, DaemonSet, Job, CronJob, ReplicaSet, Pod")
	}
	if p.name, err = k8sPathParam(r, "name"); err != nil {
		return p, err
	}
	return p, nil
}

// findWorkload returns a workload (any time within retention) with its latest status attributes, or 404.
func (s *Server) findWorkload(r *http.Request, sc *query.Scope, p k8sWorkloadPath) (*k8sWorkloadJSON, map[string]string, error) {
	q := workloadsQuery(sc).Columns("argMaxMerge(attributes)").
		Where("cluster_uid = {w_cluster:String}").Param("w_cluster", p.clusterUID).
		Where("namespace = {w_ns:String}").Param("w_ns", p.namespace).
		Where("kind = {w_kind:String}").Param("w_kind", p.kind).
		Where("name = {w_name:String}").Param("w_name", p.name).Limit(1)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, nil, err
	}
	wls, attrs, err := s.scanWorkloads(rows, true)
	if err != nil {
		return nil, nil, err
	}
	if len(wls) == 0 {
		return nil, nil, notFound("workload not found")
	}
	return wls[0], attrs[0], nil
}

type k8sWorkloadDetailJSON struct {
	*k8sWorkloadJSON
	PodList    []*k8sPodJSON     `json:"pod_list"`
	HPA        *k8sHPAJSON       `json:"hpa"`
	Attributes map[string]string `json:"attributes"`
}

func (s *Server) k8sGetWorkload(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	p, err := parseWorkloadPath(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	wl, attrs, err := s.findWorkload(r, sc, p)
	if err != nil {
		return err
	}
	if err := s.addWorkloadPods(r, sc, []*k8sWorkloadJSON{wl}, from, to); err != nil {
		return err
	}
	if err := s.addWorkloadStats(r, sc, []*k8sWorkloadJSON{wl}, from, to); err != nil {
		return err
	}
	pods, err := s.loadPods(r, sc, k8sPodFilter{clusterUID: p.clusterUID, namespace: p.namespace, workloadKind: p.kind, workloadName: p.name}, from, to)
	if err != nil {
		return err
	}
	pods = pods[:min(len(pods), s.cfg.MaxRows)]
	if err := s.addPodStats(r, sc, pods, from, to); err != nil {
		return err
	}
	d := &k8sWorkloadDetailJSON{k8sWorkloadJSON: wl, PodList: pods, Attributes: attrs}
	hpaMetrics := map[string]func(h *k8sHPAJSON, v *float64){
		"k8s.hpa.min_replicas":     func(h *k8sHPAJSON, v *float64) { h.MinReplicas = v },
		"k8s.hpa.max_replicas":     func(h *k8sHPAJSON, v *float64) { h.MaxReplicas = v },
		"k8s.hpa.current_replicas": func(h *k8sHPAJSON, v *float64) { h.CurrentReplicas = v },
		"k8s.hpa.desired_replicas": func(h *k8sHPAJSON, v *float64) { h.DesiredReplicas = v },
	}
	names := make([]string, 0, len(hpaMetrics))
	for n := range hpaMetrics {
		names = append(names, n)
	}
	sort.Strings(names)
	hpa, err := s.k8sLatestValues(r, sc, names, "attributes['k8s.hpa.name']", from, to, func(q *query.Select) {
		q.Where("resource_attributes['k8s.cluster.uid'] = {h_cluster:String}").Param("h_cluster", p.clusterUID).
			Where("attributes['k8s.namespace.name'] = {h_ns:String}").Param("h_ns", p.namespace).
			Where("attributes['k8s.hpa.scaletargetref.kind'] = {h_kind:String}").Param("h_kind", p.kind).
			Where("attributes['k8s.hpa.scaletargetref.name'] = {h_name:String}").Param("h_name", p.name)
	})
	if err != nil {
		return err
	}
	var newest time.Time
	for _, metric := range names {
		for name, l := range hpa[metric] {
			if d.HPA == nil || (name != d.HPA.Name && l.t.After(newest)) {
				d.HPA = &k8sHPAJSON{Name: name}
			}
			if name == d.HPA.Name {
				if l.t.After(newest) {
					newest = l.t
				}
				hpaMetrics[metric](d.HPA, num(l.v))
			}
		}
	}
	writeJSON(w, http.StatusOK, d)
	return nil
}

type k8sWorkloadSeriesJSON struct {
	CPUUsage         [][2]float64 `json:"cpu_usage"`
	MemoryWorkingSet [][2]float64 `json:"memory_working_set"`
	Ready            [][2]float64 `json:"ready"`
	Desired          [][2]float64 `json:"desired"`
	Restarts         [][2]float64 `json:"restarts"`
}

func (s *Server) k8sWorkloadTimeseries(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	p, err := parseWorkloadPath(r)
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
	wl, _, err := s.findWorkload(r, sc, p)
	if err != nil {
		return err
	}
	out := k8sWorkloadSeriesJSON{CPUUsage: [][2]float64{}, MemoryWorkingSet: [][2]float64{}, Ready: [][2]float64{}, Desired: [][2]float64{}}
	usage, err := s.usageSeries(r, sc, []*k8sWorkloadJSON{wl}, from, to, step)
	if err != nil {
		return err
	}
	if v := usage["k8s.pod.cpu.usage"][wl.metricKey()]; v != nil {
		out.CPUUsage = v
	}
	if v := usage["k8s.pod.memory.working_set"][wl.metricKey()]; v != nil {
		out.MemoryWorkingSet = v
	}
	workloadWhere := func(q *query.Select) {
		bindWorkloadKeys(q).
			Where("resource_attributes['k8s.cluster.uid'] = {s_cluster:String}").Param("s_cluster", p.clusterUID).
			Where("attributes['k8s.namespace.name'] = {s_ns:String}").Param("s_ns", p.namespace).
			Where("attributes[{ak_wkind:String}] = {s_kind:String}").Param("s_kind", p.kind).
			Where("attributes[{ak_wname:String}] = {s_name:String}").Param("s_name", p.name)
	}
	sq := metricRange(sc.From(query.Metrics).Columns(bucketExpr, "argMax(toFloat64OrZero(attributes[{ak_ready:String}]), timestamp)",
		"argMax(toFloat64OrZero(attributes[{ak_desired:String}]), timestamp)"), from, to, step).
		Param("ak_ready", "openlog.k8s.workload.ready").Param("ak_desired", "openlog.k8s.workload.desired").
		Where("metric_name = {s_metric:String}").Param("s_metric", "openlog.k8s.workload.status").
		GroupBy("t").OrderBy("t").Limit(s.cfg.MaxRows * 10)
	workloadWhere(sq)
	rows, err := sc.Query(r.Context(), sq)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t int64
		var ready, desired float64
		if err := rows.Scan(&t, &ready, &desired); err != nil {
			rows.Close()
			return err
		}
		out.Ready = append(out.Ready, [2]float64{float64(t), ready})
		out.Desired = append(out.Desired, [2]float64{float64(t), desired})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	out.Restarts, err = s.restartSeries(r, sc, from, to, step, workloadWhere)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"step": formatStep(step), "from": from.UnixMilli(), "to": to.UnixMilli(), "series": out})
	return nil
}

// ---- nodes ----

type k8sNodeConditionJSON struct {
	Condition string `json:"condition"`
	Status    string `json:"status"`
}

type k8sNodeJSON struct {
	ClusterUID        string                 `json:"cluster_uid"`
	ClusterName       string                 `json:"cluster_name"`
	NodeName          string                 `json:"node_name"`
	NodeUID           string                 `json:"node_uid"`
	Ready             string                 `json:"ready"`
	Unschedulable     bool                   `json:"unschedulable"`
	Roles             []string               `json:"roles"`
	KubeletVersion    string                 `json:"kubelet_version"`
	OSImage           string                 `json:"os_image"`
	ContainerRuntime  string                 `json:"container_runtime"`
	InternalIP        string                 `json:"internal_ip"`
	CreatedAt         *string                `json:"created_at"`
	AllocatableCPU    *float64               `json:"allocatable_cpu"`
	AllocatableMemory *float64               `json:"allocatable_memory"`
	AllocatablePods   *float64               `json:"allocatable_pods"`
	CPUUsage          *float64               `json:"cpu_usage"`
	MemoryWorkingSet  *float64               `json:"memory_working_set"`
	Pods              int                    `json:"pods"`
	HostID            *string                `json:"host_id"`
	HostName          *string                `json:"host_name"`
	FirstSeen         string                 `json:"first_seen"`
	LastSeen          string                 `json:"last_seen"`
	Reporting         bool                   `json:"reporting"`
	Conditions        []k8sNodeConditionJSON `json:"conditions"`

	lastSeen time.Time
}

func splitList(s string) []string {
	out := []string{}
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) loadNodes(r *http.Request, sc *query.Scope, clusterUIDs []string, from, to time.Time) ([]*k8sNodeJSON, error) {
	q := seenIn(sc.From(query.K8sNodes).Columns("cluster_uid", "node_name", "argMaxMerge(cluster_name)", "argMaxMerge(node_uid)", "argMaxMerge(ready)",
		"argMaxMerge(unschedulable)", "argMaxMerge(roles)", "argMaxMerge(kubelet_version)", "argMaxMerge(os_image)", "argMaxMerge(container_runtime)",
		"argMaxMerge(internal_ip)", "argMaxMerge(created_at)", "min(first_seen) AS n_first", "max(last_seen) AS n_last"), from, to).
		GroupBy("cluster_uid", "node_name").OrderBy("cluster_uid", "node_name").Limit(maxK8sRows)
	if clusterUIDs != nil {
		q.Where("has({n_clusters:Array(String)}, cluster_uid)").Param("n_clusters", clusterUIDs)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.now()
	out := []*k8sNodeJSON{}
	for rows.Next() {
		n := &k8sNodeJSON{Conditions: []k8sNodeConditionJSON{}}
		var unsched, roles, created string
		var first, last time.Time
		if err := rows.Scan(&n.ClusterUID, &n.NodeName, &n.ClusterName, &n.NodeUID, &n.Ready, &unsched, &roles, &n.KubeletVersion, &n.OSImage,
			&n.ContainerRuntime, &n.InternalIP, &created, &first, &last); err != nil {
			return nil, err
		}
		if n.Ready != "true" && n.Ready != "false" {
			n.Ready = "unknown"
		}
		n.Unschedulable = unsched == "true"
		n.Roles = splitList(roles)
		n.CreatedAt = k8sOptString(created)
		n.FirstSeen, n.LastSeen, n.lastSeen = formatTime(first), formatTime(last), last
		n.Reporting = now.Sub(last) <= k8sReportingWindow
		out = append(out, n)
	}
	return out, rows.Err()
}

// k8sPodBucket counts pods by (cluster, dimension, phase, ready) and the second of their last point.
type k8sPodBucket struct {
	cluster, dim, phase, ready string
	sec                        int64
	n                          uint64
}

// podBuckets groups the pods of clusters seen in [from, to] by dim (p_ns or p_node).
func (s *Server) podBuckets(r *http.Request, sc *query.Scope, clusterUIDs []string, dim string, from, to time.Time) ([]k8sPodBucket, error) {
	if len(clusterUIDs) == 0 {
		return nil, nil
	}
	inner := podsQuery(sc, from, to).Where("has({pb_clusters:Array(String)}, cluster_uid)").Param("pb_clusters", clusterUIDs)
	q := sc.FromSub(inner).Columns("cluster_uid", dim, "p_phase", "p_ready", "toInt64(toUnixTimestamp(p_last)) AS p_sec", "count() AS n").
		GroupBy("cluster_uid", dim, "p_phase", "p_ready", "p_sec").Limit(s.cfg.MaxRows * 100)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []k8sPodBucket
	for rows.Next() {
		var b k8sPodBucket
		if err := rows.Scan(&b.cluster, &b.dim, &b.phase, &b.ready, &b.sec, &b.n); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// current reports whether a child last seen at sec (unix seconds) belongs to the latest state of a parent last seen at parent.
func current(sec int64, parent time.Time) bool {
	return !time.Unix(sec+1, 0).Before(parent.Add(-k8sCurrentWindow))
}

var k8sNodeMetrics = []string{"k8s.node.cpu.usage", "k8s.node.memory.working_set", "k8s.node.allocatable_cpu", "k8s.node.allocatable_memory",
	"k8s.node.allocatable_pods", "k8s.node.condition"}

// addNodeStats fills allocatable resources and conditions (cluster agent, matched by cluster uid), usage (node
// agents, matched by cluster name), pod counts and the linked hosts.
func (s *Server) addNodeStats(r *http.Request, sc *query.Scope, nodes []*k8sNodeJSON, from, to time.Time) error {
	if len(nodes) == 0 {
		return nil
	}
	var names, clusters []string
	for _, n := range nodes {
		if !contains(names, n.NodeName) {
			names = append(names, n.NodeName)
		}
		if !contains(clusters, n.ClusterUID) {
			clusters = append(clusters, n.ClusterUID)
		}
	}
	vals, err := s.k8sLatestValues(r, sc, k8sNodeMetrics, "concat(resource_attributes['k8s.cluster.uid'], '|', resource_attributes['k8s.cluster.name'], '|', "+
		"attributes['k8s.node.name'], '|', attributes['condition'])", from, to,
		func(q *query.Select) {
			q.Where("has({node_names:Array(String)}, attributes['k8s.node.name'])").Param("node_names", names)
		})
	if err != nil {
		return err
	}
	type nodeVals struct {
		byUID, byName map[string]k8sLatest
		conditions    map[string]k8sLatest
	}
	idx := map[string]*nodeVals{}
	get := func(k string) *nodeVals {
		if idx[k] == nil {
			idx[k] = &nodeVals{byUID: map[string]k8sLatest{}, byName: map[string]k8sLatest{}, conditions: map[string]k8sLatest{}}
		}
		return idx[k]
	}
	for metric, byKey := range vals {
		for key, l := range byKey {
			parts := strings.SplitN(key, "|", 4)
			if len(parts) != 4 {
				continue
			}
			switch metric {
			case "k8s.node.condition":
				if parts[0] != "" {
					get("uid:" + k8sKey(parts[0], parts[2])).conditions[parts[3]] = l
				}
			case "k8s.node.cpu.usage", "k8s.node.memory.working_set":
				nv := get("name:" + k8sKey(parts[1], parts[2]))
				if old, ok := nv.byName[metric]; !ok || l.t.After(old.t) {
					nv.byName[metric] = l
				}
			default:
				if parts[0] != "" {
					get("uid:" + k8sKey(parts[0], parts[2])).byUID[metric] = l
				}
			}
		}
	}
	buckets, err := s.podBuckets(r, sc, clusters, "p_node", from, to)
	if err != nil {
		return err
	}
	hosts, err := s.nodeHosts(r, sc, names)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if nv := idx["uid:"+k8sKey(n.ClusterUID, n.NodeName)]; nv != nil {
			for metric, dst := range map[string]**float64{"k8s.node.allocatable_cpu": &n.AllocatableCPU, "k8s.node.allocatable_memory": &n.AllocatableMemory,
				"k8s.node.allocatable_pods": &n.AllocatablePods} {
				if l, ok := nv.byUID[metric]; ok {
					*dst = num(l.v)
				}
			}
			conds := make([]string, 0, len(nv.conditions))
			for c := range nv.conditions {
				conds = append(conds, c)
			}
			sort.Strings(conds)
			for _, c := range conds {
				st := "unknown"
				switch v := nv.conditions[c].v; {
				case v == 1:
					st = "true"
				case v == 0:
					st = "false"
				}
				n.Conditions = append(n.Conditions, k8sNodeConditionJSON{Condition: c, Status: st})
			}
		}
		if nv := idx["name:"+k8sKey(n.ClusterName, n.NodeName)]; nv != nil {
			if l, ok := nv.byName["k8s.node.cpu.usage"]; ok {
				n.CPUUsage = num(l.v)
			}
			if l, ok := nv.byName["k8s.node.memory.working_set"]; ok {
				n.MemoryWorkingSet = num(l.v)
			}
		}
		for _, b := range buckets {
			if b.cluster == n.ClusterUID && b.dim == n.NodeName && (b.phase == "Running" || b.phase == "Pending") && current(b.sec, n.lastSeen) {
				n.Pods += int(b.n)
			}
		}
		if h, ok := hosts[k8sKey(n.ClusterName, n.NodeName)]; ok {
			id, name := h.id, h.name
			n.HostID, n.HostName = &id, &name
		}
	}
	return nil
}

func (s *Server) k8sListNodes(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	p, err := k8sParams(r, "cluster_uid", "q")
	if err != nil {
		return err
	}
	var clusters []string
	if p["cluster_uid"] != "" {
		clusters = []string{p["cluster_uid"]}
	}
	all, err := s.loadNodes(r, sc, clusters, from, to)
	if err != nil {
		return err
	}
	nodes := all[:0]
	for _, n := range all {
		if matchTerms(p["q"], n.NodeName, n.NodeUID, n.ClusterName, strings.Join(n.Roles, " "), n.InternalIP, n.KubeletVersion, n.OSImage) {
			nodes = append(nodes, n)
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].ClusterName != nodes[j].ClusterName {
			return nodes[i].ClusterName < nodes[j].ClusterName
		}
		return nodes[i].NodeName < nodes[j].NodeName
	})
	total := len(nodes)
	nodes = nodes[:min(len(nodes), limit)]
	if err := s.addNodeStats(r, sc, nodes, from, to); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "total": total})
	return nil
}

// ---- clusters ----

type k8sPodPhaseCounts struct {
	Pending   int `json:"Pending"`
	Running   int `json:"Running"`
	Succeeded int `json:"Succeeded"`
	Failed    int `json:"Failed"`
	Unknown   int `json:"Unknown"`
}

func (c *k8sPodPhaseCounts) add(phase string, n int) {
	switch phase {
	case "Pending":
		c.Pending += n
	case "Running":
		c.Running += n
	case "Succeeded":
		c.Succeeded += n
	case "Failed":
		c.Failed += n
	default:
		c.Unknown += n
	}
}

type k8sClusterJSON struct {
	ClusterUID         string            `json:"cluster_uid"`
	ClusterName        string            `json:"cluster_name"`
	Version            string            `json:"version"`
	FirstSeen          string            `json:"first_seen"`
	LastSeen           string            `json:"last_seen"`
	Reporting          bool              `json:"reporting"`
	Nodes              int               `json:"nodes"`
	NodesReady         int               `json:"nodes_ready"`
	Pods               k8sPodPhaseCounts `json:"pods"`
	PodsNotReady       int               `json:"pods_not_ready"`
	Workloads          int               `json:"workloads"`
	WorkloadsUnhealthy int               `json:"workloads_unhealthy"`
	Namespaces         []string          `json:"namespaces"`

	lastSeen time.Time
}

func (s *Server) loadClusters(r *http.Request, sc *query.Scope, uid string, from, to time.Time) ([]*k8sClusterJSON, error) {
	q := sc.From(query.K8sClusters).Columns("cluster_uid", "argMaxMerge(cluster_name)", "argMaxMerge(version)", "min(first_seen) AS k_first", "max(last_seen) AS k_last").
		GroupBy("cluster_uid").OrderBy("cluster_uid").Limit(maxK8sRows)
	if uid != "" {
		// A single cluster opens at any time within retention.
		q.Where("cluster_uid = {k_cluster:String}").Param("k_cluster", uid)
	} else {
		seenIn(q, from, to)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.now()
	out := []*k8sClusterJSON{}
	for rows.Next() {
		c := &k8sClusterJSON{Namespaces: []string{}}
		var first, last time.Time
		if err := rows.Scan(&c.ClusterUID, &c.ClusterName, &c.Version, &first, &last); err != nil {
			return nil, err
		}
		c.FirstSeen, c.LastSeen, c.lastSeen = formatTime(first), formatTime(last), last
		c.Reporting = now.Sub(last) <= k8sReportingWindow
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ClusterName < out[j].ClusterName })
	return out, nil
}

// clusterSummary fills node, pod and workload counts (current objects: seen within k8sCurrentWindow of the cluster's
// last point) and namespaces (every namespace with pods or workloads in [from, to]). It returns the workloads.
func (s *Server) clusterSummary(r *http.Request, sc *query.Scope, clusters []*k8sClusterJSON, from, to time.Time) ([]*k8sWorkloadJSON, error) {
	if len(clusters) == 0 {
		return nil, nil
	}
	byUID := map[string]*k8sClusterJSON{}
	uids := make([]string, 0, len(clusters))
	for _, c := range clusters {
		byUID[c.ClusterUID] = c
		uids = append(uids, c.ClusterUID)
	}
	nsSets := map[string]map[string]bool{}
	addNS := func(c *k8sClusterJSON, ns string) {
		if ns == "" {
			return
		}
		if nsSets[c.ClusterUID] == nil {
			nsSets[c.ClusterUID] = map[string]bool{}
		}
		nsSets[c.ClusterUID][ns] = true
	}
	nodes, err := s.loadNodes(r, sc, uids, from, to)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if c := byUID[n.ClusterUID]; c != nil && !n.lastSeen.Before(c.lastSeen.Add(-k8sCurrentWindow)) {
			c.Nodes++
			if n.Ready == "true" {
				c.NodesReady++
			}
		}
	}
	buckets, err := s.podBuckets(r, sc, uids, "p_ns", from, to)
	if err != nil {
		return nil, err
	}
	for _, b := range buckets {
		c := byUID[b.cluster]
		if c == nil {
			continue
		}
		addNS(c, b.dim)
		if !current(b.sec, c.lastSeen) {
			continue
		}
		c.Pods.add(b.phase, int(b.n))
		if b.phase == "Running" && b.ready != "true" {
			c.PodsNotReady += int(b.n)
		}
	}
	all, err := s.loadWorkloads(r, sc, k8sWorkloadFilter{clusterUIDs: uids}, from, to)
	if err != nil {
		return nil, err
	}
	var cur []*k8sWorkloadJSON
	for _, wl := range all {
		c := byUID[wl.ClusterUID]
		if c == nil {
			continue
		}
		addNS(c, wl.Namespace)
		if wl.lastSeen.Before(c.lastSeen.Add(-k8sCurrentWindow)) {
			continue
		}
		cur = append(cur, wl)
		c.Workloads++
		if wl.Health == "degraded" || wl.Health == "unavailable" {
			c.WorkloadsUnhealthy++
		}
	}
	for _, c := range clusters {
		for ns := range nsSets[c.ClusterUID] {
			c.Namespaces = append(c.Namespaces, ns)
		}
		sort.Strings(c.Namespaces)
	}
	return cur, nil
}

func (s *Server) k8sListClusters(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	clusters, err := s.loadClusters(r, sc, "", from, to)
	if err != nil {
		return err
	}
	if _, err := s.clusterSummary(r, sc, clusters, from, to); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"clusters": clusters})
	return nil
}

type k8sKindCountJSON struct {
	Kind        string `json:"kind"`
	Total       int    `json:"total"`
	Healthy     int    `json:"healthy"`
	Degraded    int    `json:"degraded"`
	Unavailable int    `json:"unavailable"`
	Unknown     int    `json:"unknown"`
}

type k8sClusterDetailJSON struct {
	*k8sClusterJSON
	WorkloadsByKind   []*k8sKindCountJSON `json:"workloads_by_kind"`
	WarningEvents     []k8sEventJSON      `json:"warning_events"`
	CPUUsage          *float64            `json:"cpu_usage"`
	MemoryWorkingSet  *float64            `json:"memory_working_set"`
	AllocatableCPU    *float64            `json:"allocatable_cpu"`
	AllocatableMemory *float64            `json:"allocatable_memory"`
}

func countByKind(wls []*k8sWorkloadJSON) []*k8sKindCountJSON {
	out := []*k8sKindCountJSON{}
	byKind := map[string]*k8sKindCountJSON{}
	for _, wl := range wls {
		k := byKind[wl.Kind]
		if k == nil {
			k = &k8sKindCountJSON{Kind: wl.Kind}
			byKind[wl.Kind] = k
			out = append(out, k)
		}
		k.Total++
		switch wl.Health {
		case "healthy":
			k.Healthy++
		case "degraded":
			k.Degraded++
		case "unavailable":
			k.Unavailable++
		default:
			k.Unknown++
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

func (s *Server) k8sGetCluster(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	uid, err := k8sPathParam(r, "cluster_uid")
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	clusters, err := s.loadClusters(r, sc, uid, from, to)
	if err != nil {
		return err
	}
	if len(clusters) == 0 {
		return notFound("cluster not found")
	}
	c := clusters[0]
	wls, err := s.clusterSummary(r, sc, clusters, from, to)
	if err != nil {
		return err
	}
	d := &k8sClusterDetailJSON{k8sClusterJSON: c, WorkloadsByKind: countByKind(wls)}
	if d.WarningEvents, err = s.loadEvents(r, sc, k8sEventFilter{clusterUID: uid, typ: "Warning", limit: 20}, from, to); err != nil {
		return err
	}
	vals, err := s.k8sLatestValues(r, sc, []string{"k8s.node.cpu.usage", "k8s.node.memory.working_set", "k8s.node.allocatable_cpu", "k8s.node.allocatable_memory"},
		"attributes['k8s.node.name']", from, to, func(q *query.Select) {
			q.Where("(resource_attributes['k8s.cluster.uid'] = {c_uid:String} AND has({c_alloc:Array(String)}, metric_name)) OR "+
				"(resource_attributes['k8s.cluster.name'] = {c_name:String} AND NOT has({c_alloc:Array(String)}, metric_name))").
				Param("c_uid", uid).Param("c_name", c.ClusterName).Param("c_alloc", []string{"k8s.node.allocatable_cpu", "k8s.node.allocatable_memory"})
		})
	if err != nil {
		return err
	}
	sum := func(metric string) *float64 {
		var ls []k8sLatest
		for _, l := range vals[metric] {
			ls = append(ls, l)
		}
		return sumCurrent(ls)
	}
	d.CPUUsage, d.MemoryWorkingSet = sum("k8s.node.cpu.usage"), sum("k8s.node.memory.working_set")
	d.AllocatableCPU, d.AllocatableMemory = sum("k8s.node.allocatable_cpu"), sum("k8s.node.allocatable_memory")
	writeJSON(w, http.StatusOK, d)
	return nil
}

// ---- GET /apm/services/{service}/kubernetes ----

type k8sServicePodJSON struct {
	ClusterUID   string `json:"cluster_uid"`
	ClusterName  string `json:"cluster_name"`
	Namespace    string `json:"namespace"`
	PodName      string `json:"pod_name"`
	PodUID       string `json:"pod_uid"`
	WorkloadKind string `json:"workload_kind"`
	WorkloadName string `json:"workload_name"`
	NodeName     string `json:"node_name"`
	Phase        string `json:"phase"`
	Ready        bool   `json:"ready"`
	Reporting    bool   `json:"reporting"`
}

// apmServiceKubernetes lists the pods of a service: pods whose containers carried the service's container.id in span
// resources (apm_service_containers), or whose uid span resources carried as k8s.pod.uid in [from, to].
func (s *Server) apmServiceKubernetes(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseSvcFilter(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	cq := f.apply(sc.From(query.ApmServiceContainers).Columns("container_id")).
		Where("last_seen >= fromUnixTimestamp64Nano({t_seen:Int64})").Param("t_seen", from.UnixNano()).
		GroupBy("container_id").Limit(1000)
	var cids []string
	crows, err := sc.Query(r.Context(), cq)
	if err != nil {
		return err
	}
	for crows.Next() {
		var id string
		if err := crows.Scan(&id); err != nil {
			crows.Close()
			return err
		}
		if isHex(id, 64) {
			cids = append(cids, id)
		}
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return err
	}
	pq := f.apply(spanRange(sc.From(query.Spans).Columns("resource_attributes['k8s.pod.uid'] AS s_pod"), from, to)).
		Where("resource_attributes['k8s.pod.uid'] != ''").GroupBy("s_pod").Limit(1000)
	var uids []string
	prows, err := sc.Query(r.Context(), pq)
	if err != nil {
		return err
	}
	for prows.Next() {
		var uid string
		if err := prows.Scan(&uid); err != nil {
			prows.Close()
			return err
		}
		uids = append(uids, uid)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return err
	}
	out := []k8sServicePodJSON{}
	if len(cids) > 0 || len(uids) > 0 {
		if cids == nil {
			cids = []string{}
		}
		if uids == nil {
			uids = []string{}
		}
		q := podsQuery(sc, from, to).
			Where("has({sp_uids:Array(String)}, pod_uid) OR arrayExists(x -> position(p_cjson, x) > 0, {sp_cids:Array(String)})").
			Param("sp_uids", uids).Param("sp_cids", cids).
			OrderBy("p_cluster", "p_ns", "p_name").Limit(1000)
		rows, err := sc.Query(r.Context(), q)
		if err != nil {
			return err
		}
		pods, err := s.scanPods(rows)
		if err != nil {
			return err
		}
		for _, p := range pods {
			out = append(out, k8sServicePodJSON{ClusterUID: p.ClusterUID, ClusterName: p.ClusterName, Namespace: p.Namespace, PodName: p.PodName,
				PodUID: p.PodUID, WorkloadKind: p.WorkloadKind, WorkloadName: p.WorkloadName, NodeName: p.NodeName, Phase: p.Phase, Ready: p.Ready,
				Reporting: p.Reporting})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pods": out})
	return nil
}
