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
	if len(c.cgroups) == 0 {
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
		attrs := containerAttrs(cg, meta[id])
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
	return out, listErr
}

func containerAttrs(cg containers.Cgroup, meta containers.Container) []*commonpb.KeyValue {
	attrs := []*commonpb.KeyValue{otlputil.Str("container.id", cg.ID)}
	runtime := cg.Runtime
	if meta.ID != "" {
		runtime = meta.Runtime
		if meta.Name != "" {
			attrs = append(attrs, otlputil.Str("container.name", meta.Name))
		}
		if meta.Image != "" {
			name, tags := containers.ImageName(meta.Image)
			attrs = append(attrs, otlputil.Str("container.image.name", name))
			if len(tags) > 0 {
				attrs = append(attrs, otlputil.StrSlice("container.image.tags", tags))
			}
		}
	}
	if runtime != "" {
		attrs = append(attrs, otlputil.Str("container.runtime", runtime))
	}
	return attrs
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
