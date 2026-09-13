package metrics

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// cgroupRescan bounds how long a new container can go unnoticed by the cgroup walk.
const cgroupRescan = 30 * time.Second

// Containers emits cgroup v2 metrics for every container found in the cgroup
// hierarchy, enriched with Docker metadata when the Docker socket is available.
type Containers struct {
	FS     *hostfs.FS
	Source *containers.Source
	// MaxAge of the Docker listing reused from a previous call.
	MaxAge time.Duration

	cgroups  map[string]containers.Cgroup
	walkedAt time.Time
	prevCPU  map[string]cpuSample
}

type cpuSample struct {
	usec uint64
	at   time.Time
}

func (c *Containers) Name() string { return "containers" }

// CgroupStats are the values read from one container cgroup.
type CgroupStats struct {
	CPUUsageUsec    uint64
	HasCPU          bool
	MemoryCurrent   uint64
	InactiveFile    uint64
	HasMemory       bool
	MemoryMax       uint64 // 0 = unlimited ("max")
	ReadBytes       uint64
	WriteBytes      uint64
	ReadOps         uint64
	WriteOps        uint64
	HasIO           bool
	NetRx, NetTx    uint64
	HasNetwork      bool
	FirstPID        int
	SharesHostNetNS bool
}

// ParseCPUStat returns usage_usec from cgroup v2 cpu.stat.
func ParseCPUStat(b []byte) (uint64, bool) {
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// ParseIOStat sums rbytes, wbytes, rios and wios over all devices of cgroup v2 io.stat.
func ParseIOStat(b []byte) (rbytes, wbytes, rios, wios uint64) {
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		for _, kv := range f[min(1, len(f)):] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			n, _ := strconv.ParseUint(v, 10, 64)
			switch k {
			case "rbytes":
				rbytes += n
			case "wbytes":
				wbytes += n
			case "rios":
				rios += n
			case "wios":
				wios += n
			}
		}
	}
	return
}

func readUint(fsys *hostfs.FS, p string) (uint64, bool) {
	s, err := fsys.ReadString(p)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// ReadCgroup reads the stats of one container cgroup directory.
func ReadCgroup(fsys *hostfs.FS, dir string) CgroupStats {
	var s CgroupStats
	if b, err := fsys.ReadFile(dir + "/cpu.stat"); err == nil {
		s.CPUUsageUsec, s.HasCPU = ParseCPUStat(b)
	}
	if v, ok := readUint(fsys, dir+"/memory.current"); ok {
		s.MemoryCurrent, s.HasMemory = v, true
		if b, err := fsys.ReadFile(dir + "/memory.stat"); err == nil {
			for line := range strings.Lines(string(b)) {
				if rest, ok := strings.CutPrefix(line, "inactive_file "); ok {
					s.InactiveFile, _ = strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
				}
			}
		}
	}
	if v, ok := readUint(fsys, dir+"/memory.max"); ok {
		s.MemoryMax = v
	}
	if b, err := fsys.ReadFile(dir + "/io.stat"); err == nil {
		s.ReadBytes, s.WriteBytes, s.ReadOps, s.WriteOps = ParseIOStat(b)
		s.HasIO = true
	}
	s.FirstPID = firstPID(fsys, dir, 0)
	return s
}

// firstPID returns a process of the cgroup, looking into child cgroups when
// the container runs its own hierarchy (e.g. systemd inside the container).
func firstPID(fsys *hostfs.FS, dir string, depth int) int {
	if b, err := fsys.ReadFile(dir + "/cgroup.procs"); err == nil {
		for line := range strings.Lines(string(b)) {
			if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
				return pid
			}
		}
	}
	if depth >= 3 {
		return 0
	}
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() {
			if pid := firstPID(fsys, dir+"/"+e.Name(), depth+1); pid > 0 {
				return pid
			}
		}
	}
	return 0
}

// readNetwork fills network counters from the network namespace of pid,
// unless it is the host's namespace (host-network containers would duplicate
// host counters).
func readNetwork(fsys *hostfs.FS, s *CgroupStats) {
	if s.FirstPID <= 0 {
		return
	}
	base := "/proc/" + strconv.Itoa(s.FirstPID)
	ns, err := fsys.Readlink(base + "/ns/net")
	if err != nil {
		return
	}
	hostNS, err := fsys.Readlink("/proc/1/ns/net")
	if err != nil {
		return
	}
	if ns == hostNS {
		s.SharesHostNetNS = true
		return
	}
	b, err := fsys.ReadFile(base + "/net/dev")
	if err != nil {
		return
	}
	for _, d := range procfs.ParseNetDev(b) {
		if d.Interface == "lo" {
			continue
		}
		s.NetRx += d.RxBytes
		s.NetTx += d.TxBytes
	}
	s.HasNetwork = true
}

func (c *Containers) Collect(now time.Time) ([]*metricspb.Metric, error) {
	var meta map[string]containers.Container
	var listErr error
	if c.Source != nil {
		list, err := c.Source.List(context.Background(), c.MaxAge)
		if err != nil && !errors.Is(err, containers.ErrNoRuntime) {
			listErr = err
		}
		meta = make(map[string]containers.Container, len(list))
		for _, ct := range list {
			meta[ct.ID] = ct
		}
	}
	rescan := c.cgroups == nil || now.Sub(c.walkedAt) >= cgroupRescan
	for id, ct := range meta {
		if _, ok := c.cgroups[id]; !ok && ct.State == "running" {
			rescan = true
		}
	}
	if rescan {
		c.cgroups = containers.FindCgroups(c.FS)
		c.walkedAt = now
	}
	if len(c.cgroups) == 0 && len(meta) == 0 {
		return nil, listErr
	}

	ncpu := 1
	var hostMem uint64
	if b, err := c.FS.ReadFile("/proc/stat"); err == nil {
		if st, err := procfs.ParseStat(b); err == nil && st.LogicalCPUs > 0 {
			ncpu = st.LogicalCPUs
		}
	}
	if b, err := c.FS.ReadFile("/proc/meminfo"); err == nil {
		hostMem = procfs.ParseMeminfo(b)["MemTotal"]
	}
	if c.prevCPU == nil {
		c.prevCPU = map[string]cpuSample{}
	}
	ids := make([]string, 0, len(c.cgroups))
	for id := range c.cgroups {
		ids = append(ids, id)
	}
	sortStrings(ids)

	var cpuTime, cpuUtil, memUsage, memLimit, bio, bops, netio []otlputil.Point
	seen := map[string]bool{}
	for _, id := range ids {
		cg := c.cgroups[id]
		if _, err := c.FS.Stat(cg.Path); err != nil {
			continue // container stopped since the last walk
		}
		st := ReadCgroup(c.FS, cg.Path)
		readNetwork(c.FS, &st)
		var m *containers.Container
		if ct, ok := meta[id]; ok {
			m = &ct
		}
		attrs := containers.Attributes(id, cg.Runtime, m)
		seen[id] = true
		if st.HasCPU {
			cpuTime = append(cpuTime, otlputil.DoublePoint(float64(st.CPUUsageUsec)/1e6, attrs...))
			if prev, ok := c.prevCPU[id]; ok && st.CPUUsageUsec >= prev.usec {
				if el := now.Sub(prev.at).Seconds(); el > 0 {
					cpuUtil = append(cpuUtil, otlputil.DoublePoint(float64(st.CPUUsageUsec-prev.usec)/1e6/el/float64(ncpu), attrs...))
				}
			}
			c.prevCPU[id] = cpuSample{usec: st.CPUUsageUsec, at: now}
		}
		if st.HasMemory {
			usage := st.MemoryCurrent
			if st.InactiveFile < usage {
				usage -= st.InactiveFile
			} else {
				usage = 0
			}
			memUsage = append(memUsage, otlputil.IntPoint(int64(usage), attrs...))
			limit := st.MemoryMax
			if limit == 0 || (hostMem > 0 && limit > hostMem) {
				limit = hostMem
			}
			if limit > 0 {
				memLimit = append(memLimit, otlputil.IntPoint(int64(limit), attrs...))
			}
		}
		if st.HasIO {
			bio = append(bio,
				otlputil.IntPoint(int64(st.ReadBytes), withAttr(attrs, "disk.io.direction", "read")...),
				otlputil.IntPoint(int64(st.WriteBytes), withAttr(attrs, "disk.io.direction", "write")...))
			bops = append(bops,
				otlputil.IntPoint(int64(st.ReadOps), withAttr(attrs, "disk.io.direction", "read")...),
				otlputil.IntPoint(int64(st.WriteOps), withAttr(attrs, "disk.io.direction", "write")...))
		}
		if st.HasNetwork {
			netio = append(netio,
				otlputil.IntPoint(int64(st.NetRx), withAttr(attrs, "network.io.direction", "receive")...),
				otlputil.IntPoint(int64(st.NetTx), withAttr(attrs, "network.io.direction", "transmit")...))
		}
	}
	for id := range c.prevCPU {
		if !seen[id] {
			delete(c.prevCPU, id)
		}
	}
	status, restarts := c.statusPoints(meta, seen, now)
	var out []*metricspb.Metric
	add := func(m *metricspb.Metric, n int) {
		if n > 0 {
			out = append(out, m)
		}
	}
	// Cumulative counters start at an unknown point in the container's life.
	add(otlputil.Sum("container.cpu.time", "s", true, time.Time{}, now, cpuTime...), len(cpuTime))
	add(otlputil.Gauge("container.cpu.utilization", "1", now, cpuUtil...), len(cpuUtil))
	add(otlputil.Sum("container.memory.usage", "By", false, time.Time{}, now, memUsage...), len(memUsage))
	add(otlputil.Sum("container.memory.limit", "By", false, time.Time{}, now, memLimit...), len(memLimit))
	add(otlputil.Sum("container.blockio.io", "By", true, time.Time{}, now, bio...), len(bio))
	add(otlputil.Sum("container.blockio.operations", "{operation}", true, time.Time{}, now, bops...), len(bops))
	add(otlputil.Sum("container.network.io", "By", true, time.Time{}, now, netio...), len(netio))
	add(otlputil.Gauge("openlog.container.status", "1", now, status...), len(status))
	add(otlputil.Sum("container.restarts", "{restart}", true, time.Time{}, now, restarts...), len(restarts))
	return out, listErr
}

// Container status attributes (semantic-conventions §2 container metrics).
const (
	attrContainerState     = "openlog.container.state"
	attrContainerHealth    = "openlog.container.health"
	attrContainerStartedAt = "openlog.container.started_at"
)

// Status reporting limits: stopped containers are reported while they stopped (or were
// created) within statusRecent; at most maxStatusContainers containers per sample.
const (
	statusRecent        = 24 * time.Hour
	maxStatusContainers = 500
)

// recentlyActive reports whether a non-running container finished, started or was created within statusRecent.
func recentlyActive(ct containers.Container, now time.Time) bool {
	for _, v := range []string{ct.FinishedAt, ct.StartedAt, ct.Created} {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return now.Sub(t) < statusRecent
		}
	}
	return false
}

// statusPoints builds openlog.container.status (value 1 per container, state attributes) and
// container.restarts for listed containers and for cgroup containers without Docker metadata.
func (c *Containers) statusPoints(meta map[string]containers.Container, live map[string]bool, now time.Time) (status, restarts []otlputil.Point) {
	ids := make([]string, 0, len(meta)+len(live))
	for id := range meta {
		ids = append(ids, id)
	}
	for id := range live {
		if _, ok := meta[id]; !ok {
			ids = append(ids, id)
		}
	}
	sortStrings(ids)
	// Running containers first, so the cap never hides them behind old stopped ones.
	running := func(id string) bool { ct, ok := meta[id]; return (ok && ct.State == "running") || (!ok && live[id]) }
	ordered := make([]string, 0, len(ids))
	for _, pass := range []bool{true, false} {
		for _, id := range ids {
			if running(id) == pass {
				ordered = append(ordered, id)
			}
		}
	}
	for _, id := range ordered {
		if len(status) >= maxStatusContainers {
			break
		}
		ct, hasMeta := meta[id]
		state := "running" // a container cgroup exists
		if hasMeta {
			state = ct.State
			if state != "running" && state != "paused" && state != "restarting" && !live[id] && !recentlyActive(ct, now) {
				continue
			}
		}
		var m *containers.Container
		if hasMeta {
			m = &ct
		}
		attrs := containers.Attributes(id, c.cgroups[id].Runtime, m)
		sattrs := withAttr(attrs, attrContainerState, state)
		if hasMeta && ct.Health != "" {
			sattrs = append(sattrs, otlputil.Str(attrContainerHealth, ct.Health))
		}
		if hasMeta && ct.StartedAt != "" {
			sattrs = append(sattrs, otlputil.Str(attrContainerStartedAt, ct.StartedAt))
		}
		status = append(status, otlputil.IntPoint(1, sattrs...))
		if hasMeta && ct.Inspected() {
			restarts = append(restarts, otlputil.IntPoint(int64(ct.RestartCount), attrs...))
		}
	}
	return status, restarts
}

func withAttr(base []*commonpb.KeyValue, k, v string) []*commonpb.KeyValue {
	out := make([]*commonpb.KeyValue, 0, len(base)+1)
	return append(append(out, base...), otlputil.Str(k, v))
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
