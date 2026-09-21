package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// ---- hosts ----

type hostJSON struct {
	HostID             string            `json:"host_id"`
	HostName           string            `json:"host_name"`
	OSDescription      string            `json:"os_description"`
	Arch               string            `json:"arch"`
	AgentVersion       string            `json:"agent_version"`
	LastSeen           string            `json:"last_seen"`
	ResourceAttributes map[string]string `json:"resource_attributes"`
	// Usage is how busy the host is right now (host_usage.go); nil when it reported no metric in the
	// window, or when the caller asked for the list without it (usage=false).
	Usage *hostUsageJSON `json:"usage,omitempty"`
}

func hostsQuery(sc *query.Scope) *query.Select {
	return sc.From(query.Hosts).Columns(
		"host_id",
		"argMax(host_name, last_seen) AS h_name",
		"argMax(os_description, last_seen) AS h_os",
		"argMax(arch, last_seen) AS h_arch",
		"argMax(agent_version, last_seen) AS h_agent",
		"max(last_seen) AS h_last",
		"argMax(resource_attributes, last_seen) AS h_attrs",
	).GroupBy("host_id")
}

func scanHosts(rows query.Rows) ([]hostJSON, error) {
	defer rows.Close()
	out := []hostJSON{}
	for rows.Next() {
		var h hostJSON
		var last time.Time
		if err := rows.Scan(&h.HostID, &h.HostName, &h.OSDescription, &h.Arch, &h.AgentVersion, &last, &h.ResourceAttributes); err != nil {
			return nil, err
		}
		h.LastSeen = formatTime(last)
		h.ResourceAttributes = nonNilMap(h.ResourceAttributes)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	q := hostsQuery(sc).
		Where("last_seen >= fromUnixTimestamp64Milli({since:Int64})").Param("since", s.now().Add(-24*time.Hour).UnixMilli()).
		OrderBy("h_name", "host_id").Limit(limit)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	hosts, err := scanHosts(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": s.withHostUsage(r, sc, hosts)})
	return nil
}

func (s *Server) getHost(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	q := hostsQuery(sc).Where("host_id = {host_id:String}").Param("host_id", r.PathValue("host_id"))
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	hosts, err := scanHosts(rows)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return notFound("host not found")
	}
	writeJSON(w, http.StatusOK, s.withHostUsage(r, sc, hosts)[0])
	return nil
}

// ---- metric names ----

func (s *Server) metricNames(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	q := sc.From(query.Metrics).
		Columns("metric_name", "toString(any(metric_type)) AS mtype", "any(unit) AS munit").
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).
		GroupBy("metric_name").OrderBy("metric_name").Limit(s.cfg.MaxRows)
	if h := r.URL.Query().Get("host_id"); h != "" {
		q.Where("host_id = {host_id:String}").Param("host_id", h)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type nameJSON struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Unit string `json:"unit"`
	}
	names := []nameJSON{}
	for rows.Next() {
		var n nameJSON
		if err := rows.Scan(&n.Name, &n.Type, &n.Unit); err != nil {
			return err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": names})
	return nil
}

// ---- inventory ----

type itemJSON struct {
	HostID   string          `json:"host_id,omitempty"`
	HostName string          `json:"host_name,omitempty"`
	Category string          `json:"category"`
	Key      string          `json:"key"`
	Data     json.RawMessage `json:"data"`
}

// latestSnapshot returns the host's latest complete snapshot, or "" if none.
func latestSnapshot(r *http.Request, sc *query.Scope, hostID string) (string, time.Time, error) {
	q := sc.From(query.InventorySnapshots).Columns("snapshot_id", "snapshot_time").
		Where("host_id = {host_id:String}").Param("host_id", hostID).
		OrderBy("snapshot_time DESC").Limit(1)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return "", time.Time{}, err
	}
	defer rows.Close()
	var id string
	var ts time.Time
	if rows.Next() {
		if err := rows.Scan(&id, &ts); err != nil {
			return "", time.Time{}, err
		}
	}
	return id, ts, rows.Err()
}

func (s *Server) inventory(w http.ResponseWriter, r *http.Request, sc *query.Scope, category string) error {
	hostID := r.PathValue("host_id")
	if err := requireHost(r, sc, hostID); err != nil {
		return err
	}
	snapID, snapTime, err := latestSnapshot(r, sc, hostID)
	if err != nil {
		return err
	}
	resp := map[string]any{"snapshot_id": snapID, "snapshot_time": nil, "items": []itemJSON{}}
	if snapID == "" {
		writeJSON(w, http.StatusOK, resp)
		return nil
	}
	resp["snapshot_time"] = formatTime(snapTime)
	q := sc.From(query.InventoryItems).Columns("category", "item_key", "any(data)").
		Where("host_id = {host_id:String}").Param("host_id", hostID).
		Where("snapshot_id = {snapshot_id:String}").Param("snapshot_id", snapID).
		GroupBy("category", "item_key").OrderBy("category", "item_key").Limit(s.cfg.MaxRows)
	if category != "" {
		q.Where("category = {category:String}").Param("category", category)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []itemJSON{}
	for rows.Next() {
		var it itemJSON
		var data string
		if err := rows.Scan(&it.Category, &it.Key, &data); err != nil {
			return err
		}
		it.Data = jsonData(data)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	resp["items"] = items
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) hostInventory(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	return s.inventory(w, r, sc, r.URL.Query().Get("category"))
}

func (s *Server) hostServices(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	return s.inventory(w, r, sc, "discovered_service")
}

func (s *Server) inventorySearch(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	category := r.URL.Query().Get("category")
	if category == "" {
		return badRequest("category is required")
	}
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	latest := sc.From(query.InventorySnapshots).
		Columns("host_id", "argMax(snapshot_id, snapshot_time)").GroupBy("host_id")
	q := sc.From(query.InventoryItems).Columns("host_id", "category", "item_key", "any(data)").
		Where("category = {category:String}").Param("category", category).
		WhereIn("(host_id, snapshot_id)", latest).
		GroupBy("host_id", "category", "item_key").OrderBy("item_key", "host_id").Limit(limit)
	if qs := r.URL.Query().Get("q"); qs != "" {
		q.Where("positionCaseInsensitiveUTF8(item_key, {q:String}) > 0").Param("q", qs)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	items := []itemJSON{}
	hostSet := map[string]bool{}
	for rows.Next() {
		var it itemJSON
		var data string
		if err := rows.Scan(&it.HostID, &it.Category, &it.Key, &data); err != nil {
			rows.Close()
			return err
		}
		it.Data = jsonData(data)
		hostSet[it.HostID] = true
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(hostSet) > 0 {
		ids := make([]string, 0, len(hostSet))
		for id := range hostSet {
			ids = append(ids, id)
		}
		hq := sc.From(query.Hosts).Columns("host_id", "argMax(host_name, last_seen)").
			Where("has({host_ids:Array(String)}, host_id)").Param("host_ids", ids).GroupBy("host_id")
		hrows, err := sc.Query(r.Context(), hq)
		if err != nil {
			return err
		}
		names := map[string]string{}
		for hrows.Next() {
			var id, name string
			if err := hrows.Scan(&id, &name); err != nil {
				hrows.Close()
				return err
			}
			names[id] = name
		}
		hrows.Close()
		for i := range items {
			items[i].HostName = names[items[i].HostID]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

// ---- logs ----

var severityNames = map[string]int{"TRACE": 1, "DEBUG": 5, "INFO": 9, "WARN": 13, "WARNING": 13, "ERROR": 17, "FATAL": 21}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.limit(r)
	if err != nil {
		return err
	}
	qp := r.URL.Query()
	var cursor *logPos
	skip := 0
	if v := qp.Get("cursor"); v != "" {
		pos, n, err := decodeLogCursor(v)
		if err != nil {
			return err
		}
		cursor, skip = &pos, n
	}
	q := sc.From(query.Logs).Columns("timestamp", "severity_text", "severity_number", "body", "host_id", "service_name",
		"trace_id", "span_id", "attributes", "resource_attributes", logRowKey).
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).
		Where("NOT startsWith(event_name, {inventory_prefix:String})").Param("inventory_prefix", "openlog.inventory.").
		OrderBy("timestamp DESC", "l_key DESC").Limit(limit + skip + 1)
	if cursor != nil {
		q.Where("timestamp <= fromUnixTimestamp64Nano({c_ts:Int64})").
			Where("(timestamp < fromUnixTimestamp64Nano({c_ts:Int64}) OR l_key <= {c_key:UInt64})").
			Param("c_ts", cursor.ts).Param("c_key", cursor.key)
	}
	if v := qp.Get("span_id"); v != "" {
		q.Where("span_id = {span_id:String}").Param("span_id", strings.ToLower(v))
	}
	// Logs correlated with the traces of one transaction (entry spans in the range, at most 10000 traces).
	if traces, err := transactionTraces(sc, qp.Get("transaction"), qp.Get("transaction_service"), from, to); err != nil {
		return err
	} else if traces != nil {
		q.WhereIn("trace_id", traces)
	}
	if v := qp.Get("host_id"); v != "" {
		q.Where("host_id = {host_id:String}").Param("host_id", v)
	}
	if v := qp.Get("service"); v != "" {
		q.Where("service_name = {service:String}").Param("service", v)
	}
	if v := qp.Get("q"); v != "" {
		q.Where("positionCaseInsensitiveUTF8(body, {q:String}) > 0").Param("q", v)
	}
	if v := qp.Get("trace_id"); v != "" {
		q.Where("trace_id = {trace_id:String}").Param("trace_id", strings.ToLower(v))
	}
	// Container logs (semantic-conventions §4): resource attributes with skip indexes (0009_containers).
	if v := qp.Get("container_id"); v != "" {
		q.Where("resource_attributes['container.id'] = {container_id:String}").Param("container_id", strings.ToLower(v))
	}
	if v := qp.Get("compose_service"); v != "" {
		q.Where("resource_attributes['docker.compose.service'] = {compose_service:String}").Param("compose_service", v)
	}
	if v := qp.Get("compose_project"); v != "" {
		q.Where("resource_attributes['docker.compose.project'] = {compose_project:String}").Param("compose_project", v)
	}
	// Kubernetes pod logs (semantic-conventions §7.2): resource attribute with a skip index (0043_k8s_logs_indexes).
	if v := qp.Get("k8s_pod_uid"); v != "" {
		q.Where("resource_attributes['k8s.pod.uid'] = {k8s_pod_uid:String}").Param("k8s_pod_uid", v)
	}
	if v := qp.Get("severity_min"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			var ok bool
			if n, ok = severityNames[strings.ToUpper(v)]; !ok {
				return badRequest("severity_min must be a number 1-24 or a severity name")
			}
		}
		q.Where("severity_number >= {severity_min:UInt8}").Param("severity_min", min(max(n, 0), 255))
	}
	if err := addLogAttrFilters(q, qp); err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type logJSON struct {
		Timestamp          string            `json:"timestamp"`
		SeverityText       string            `json:"severity_text"`
		SeverityNumber     uint8             `json:"severity_number"`
		Body               string            `json:"body"`
		HostID             string            `json:"host_id"`
		ServiceName        string            `json:"service_name"`
		TraceID            string            `json:"trace_id"`
		SpanID             string            `json:"span_id"`
		Attributes         map[string]string `json:"attributes"`
		ResourceAttributes map[string]string `json:"resource_attributes"`
	}
	logs := []logJSON{}
	positions := []logPos{}
	for rows.Next() {
		var l logJSON
		var ts time.Time
		var key uint64
		if err := rows.Scan(&ts, &l.SeverityText, &l.SeverityNumber, &l.Body, &l.HostID, &l.ServiceName, &l.TraceID, &l.SpanID, &l.Attributes, &l.ResourceAttributes, &key); err != nil {
			return err
		}
		l.Timestamp = formatTime(ts)
		l.Attributes, l.ResourceAttributes = nonNilMap(l.Attributes), nonNilMap(l.ResourceAttributes)
		logs = append(logs, l)
		positions = append(positions, logPos{ts: ts.UnixNano(), key: key})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	first, end, next := logPage(positions, cursor, skip, limit)
	var nextCursor *string
	if next != "" {
		nextCursor = &next
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs[first:end], "next_cursor": nextCursor})
	return nil
}

// ---- traces ----

func (s *Server) getTrace(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	traceID := strings.ToLower(r.PathValue("trace_id"))
	if !isHex(traceID, 32) {
		return badRequest("trace_id must be 32 hex characters")
	}
	idx := sc.From(query.TraceIndex).Columns("min(start)", "max(`end`)", "sum(span_count)").
		Where("trace_id = {trace_id:String}").Param("trace_id", traceID)
	irows, err := sc.Query(r.Context(), idx)
	if err != nil {
		return err
	}
	var start, end time.Time
	var count uint64
	if irows.Next() {
		if err := irows.Scan(&start, &end, &count); err != nil {
			irows.Close()
			return err
		}
	}
	irows.Close()
	if count == 0 {
		return notFound("trace not found")
	}
	q := sc.From(query.Spans).Columns("span_id", "parent_span_id", "name", "toString(kind)", "service_name", "timestamp",
		"duration_ns", "toString(status_code)", "status_message", "attributes", "resource_attributes",
		"events_timestamp", "events_name", "events_attributes").
		Where("trace_id = {trace_id:String}").Param("trace_id", traceID).
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", start.UnixNano()).Param("t_to", end.UnixNano()).
		OrderBy("timestamp", "span_id").LimitBy(1, "span_id").Limit(s.cfg.MaxRows)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type eventJSON struct {
		Timestamp  string            `json:"timestamp"`
		Name       string            `json:"name"`
		Attributes map[string]string `json:"attributes"`
	}
	type spanJSON struct {
		SpanID             string            `json:"span_id"`
		ParentSpanID       string            `json:"parent_span_id"`
		Name               string            `json:"name"`
		Kind               string            `json:"kind"`
		ServiceName        string            `json:"service_name"`
		Start              string            `json:"start"`
		DurationNs         uint64            `json:"duration_ns"`
		StatusCode         string            `json:"status_code"`
		StatusMessage      string            `json:"status_message"`
		Attributes         map[string]string `json:"attributes"`
		ResourceAttributes map[string]string `json:"resource_attributes"`
		Events             []eventJSON       `json:"events"`
	}
	spans := []spanJSON{}
	for rows.Next() {
		var sp spanJSON
		var ts time.Time
		var evTS []time.Time
		var evNames []string
		var evAttrs []map[string]string
		if err := rows.Scan(&sp.SpanID, &sp.ParentSpanID, &sp.Name, &sp.Kind, &sp.ServiceName, &ts, &sp.DurationNs,
			&sp.StatusCode, &sp.StatusMessage, &sp.Attributes, &sp.ResourceAttributes, &evTS, &evNames, &evAttrs); err != nil {
			return err
		}
		sp.Start = formatTime(ts)
		sp.Attributes, sp.ResourceAttributes = nonNilMap(sp.Attributes), nonNilMap(sp.ResourceAttributes)
		sp.Events = []eventJSON{}
		for i := range evNames {
			ev := eventJSON{Name: evNames[i], Attributes: map[string]string{}}
			if i < len(evTS) {
				ev.Timestamp = formatTime(evTS[i])
			}
			if i < len(evAttrs) {
				ev.Attributes = nonNilMap(evAttrs[i])
			}
			sp.Events = append(sp.Events, ev)
		}
		spans = append(spans, sp)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(spans) == 0 {
		return notFound("trace not found")
	}
	writeJSON(w, http.StatusOK, map[string]any{"trace_id": traceID, "spans": spans})
	return nil
}
