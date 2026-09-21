package api

import (
	"context"
	"net/http"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// The usage summary of the hosts list (docs/contracts/api.md "Hosts"): how busy each host is right now,
// in one query for the whole page rather than one per row.
//
// A list of hosts without their load answers "which machines exist" and nothing else; the question people
// actually open it with is "which machine is in trouble". These are the four numbers that answer it — the
// same metrics the host's own charts draw, reduced to one figure each over a short window.

// hostUsageWindow is the window the figures are averaged over. Short enough to mean "now", long enough to
// survive a missed collection (the agent's default interval is 15s).
const hostUsageWindow = 5 * time.Minute

// Metric names the summary reads. They are parameters rather than literals because the query builder
// forbids the token "system" in a fragment (internal/api/query).
const (
	metricCPUUtil        = "system.cpu.utilization"
	metricMemUtil        = "system.memory.utilization"
	metricFilesystemUtil = "system.filesystem.utilization"
	metricLoad1          = "system.cpu.load_average.1m"
)

// hostUsageJSON is what one host reports. Every field is nullable: a host that has not sent a metric in the
// window has no number, and showing 0 % instead would be a lie about an agent that stopped reporting.
type hostUsageJSON struct {
	// CPU is the busy share (0–1): 1 − idle, over every mode the agent reports.
	CPU *float64 `json:"cpu"`
	// Memory is the used share (0–1) of the host's total memory.
	Memory *float64 `json:"memory"`
	// Disk is the *fullest* filesystem's used share (0–1). The mean over mountpoints would hide the one
	// partition that is about to fill up, which is the only one worth showing in a list.
	Disk *float64 `json:"disk"`
	// Load1 is the 1-minute load average, and LoadPerCPU it divided by the logical CPU count, which is what
	// makes a load of 8 readable without knowing the machine.
	Load1      *float64 `json:"load1"`
	LoadPerCPU *float64 `json:"load_per_cpu"`
}

// hostUsage reads the usage of every host that reported in the window. hostIDs limits the read to the page
// being rendered; an empty list reads every host of the tenant.
func (s *Server) hostUsage(ctx context.Context, sc *query.Scope, hostIDs []string) (map[string]hostUsageJSON, error) {
	now := s.now().UTC()
	from := now.Add(-hostUsageWindow)
	q := sc.From(query.Metrics).Columns(
		"host_id",
		// CPU: the idle share is what the agent reports per mode, so the busy share is one minus it.
		"avgIf(value, metric_name = {m_cpu:String} AND attributes[{a_cpu_mode:String}] = {v_idle:String}) AS u_idle",
		"countIf(metric_name = {m_cpu:String} AND attributes[{a_cpu_mode:String}] = {v_idle:String}) AS n_cpu",
		"avgIf(value, metric_name = {m_mem:String} AND attributes[{a_mem_state:String}] = {v_used:String}) AS u_mem",
		"countIf(metric_name = {m_mem:String} AND attributes[{a_mem_state:String}] = {v_used:String}) AS n_mem",
		// The fullest filesystem, not the average one.
		"maxIf(value, metric_name = {m_fs:String}) AS u_disk",
		"countIf(metric_name = {m_fs:String}) AS n_disk",
		"avgIf(value, metric_name = {m_load:String}) AS u_load",
		"countIf(metric_name = {m_load:String}) AS n_load",
		"maxIf(value, metric_name = {m_cpus:String}) AS u_cpus",
	).
		Where("timestamp >= fromUnixTimestamp64Milli({u_from:Int64}) AND timestamp <= fromUnixTimestamp64Milli({u_to:Int64})").
		Where("metric_name IN ({m_cpu:String}, {m_mem:String}, {m_fs:String}, {m_load:String}, {m_cpus:String})").
		Param("u_from", from.UnixMilli()).Param("u_to", now.UnixMilli()).
		Param("m_cpu", metricCPUUtil).Param("m_mem", metricMemUtil).Param("m_fs", metricFilesystemUtil).
		Param("m_load", metricLoad1).Param("m_cpus", "system.cpu.logical.count").
		Param("a_cpu_mode", "cpu.mode").Param("a_mem_state", "system.memory.state").
		Param("v_idle", "idle").Param("v_used", "used").
		GroupBy("host_id").Limit(maxUsageHosts)
	if len(hostIDs) > 0 {
		q.Where("host_id IN ({u_hosts:Array(String)})").Param("u_hosts", hostIDs)
	}
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]hostUsageJSON{}
	for rows.Next() {
		var (
			hostID                      string
			idle, mem, disk, load, cpus float64
			nCPU, nMem, nDisk, nLoad    uint64
		)
		if err := rows.Scan(&hostID, &idle, &nCPU, &mem, &nMem, &disk, &nDisk, &load, &nLoad, &cpus); err != nil {
			return nil, err
		}
		var u hostUsageJSON
		if nCPU > 0 {
			u.CPU = clampShare(1 - idle)
		}
		if nMem > 0 {
			u.Memory = clampShare(mem)
		}
		if nDisk > 0 {
			u.Disk = clampShare(disk)
		}
		if nLoad > 0 && finite(load) {
			v := load
			u.Load1 = &v
			if cpus >= 1 {
				per := load / cpus
				if finite(per) {
					u.LoadPerCPU = &per
				}
			}
		}
		out[hostID] = u
	}
	return out, rows.Err()
}

// maxUsageHosts bounds the summary query.
const maxUsageHosts = 10000

// clampShare clamps a value to [0, 1]: a rounding error must not render a bar past its track, and a negative
// value (an idle share above 1 on a machine whose clock jumped) is not a number worth showing.
func clampShare(v float64) *float64 {
	if !finite(v) {
		return nil
	}
	switch {
	case v < 0:
		v = 0
	case v > 1:
		v = 1
	}
	return &v
}

// withHostUsage attaches the usage summary to a page of hosts. A failing summary is not a failing page: the
// list still answers "which machines exist", which is what it did before the summary existed.
func (s *Server) withHostUsage(r *http.Request, sc *query.Scope, hosts []hostJSON) []hostJSON {
	if len(hosts) == 0 || r.URL.Query().Get("usage") == "false" {
		return hosts
	}
	ids := make([]string, 0, len(hosts))
	for _, h := range hosts {
		ids = append(ids, h.HostID)
	}
	usage, err := s.hostUsage(r.Context(), sc, ids)
	if err != nil {
		s.log.Warn("cannot read host usage for the list", "err", err)
		return hosts
	}
	for i := range hosts {
		if u, ok := usage[hosts[i].HostID]; ok {
			hosts[i].Usage = &u
		}
	}
	return hosts
}
