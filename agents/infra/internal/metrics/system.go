package metrics

import (
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// CPU emits system.cpu.time, system.cpu.utilization and system.cpu.logical.count.
type CPU struct {
	FS       *hostfs.FS
	BootTime func() time.Time

	prev    *procfs.CPUTimes
	boot    time.Time
	hasBoot bool
}

func (c *CPU) Name() string { return "cpu" }

func (c *CPU) Collect(now time.Time) ([]*metricspb.Metric, error) {
	b, err := c.FS.ReadFile("/proc/stat")
	if err != nil {
		return nil, err
	}
	st, err := procfs.ParseStat(b)
	if err != nil {
		return nil, err
	}
	if !c.hasBoot {
		c.boot, c.hasBoot = st.BootTime, true
	}
	modes := cpuModes(st.CPU)
	timePts := make([]otlputil.Point, 0, len(modes))
	for _, m := range modes {
		timePts = append(timePts, otlputil.DoublePoint(m.v, otlputil.Str("cpu.mode", m.name)))
	}
	out := []*metricspb.Metric{
		otlputil.Sum("system.cpu.time", "s", true, c.boot, now, timePts...),
		otlputil.Sum("system.cpu.logical.count", "{cpu}", false, time.Time{}, now, otlputil.IntPoint(int64(st.LogicalCPUs))),
	}
	if c.prev != nil {
		if pts := CPUUtilization(*c.prev, st.CPU); pts != nil {
			out = append(out, otlputil.Gauge("system.cpu.utilization", "1", now, pts...))
		}
	}
	cur := st.CPU
	c.prev = &cur
	return out, nil
}

type modeValue struct {
	name string
	v    float64
}

func cpuModes(t procfs.CPUTimes) []modeValue {
	return []modeValue{
		{"user", t.User}, {"nice", t.Nice}, {"system", t.System}, {"idle", t.Idle},
		{"iowait", t.IOWait}, {"interrupt", t.IRQ}, {"softirq", t.SoftIRQ}, {"steal", t.Steal},
	}
}

// CPUUtilization computes per-mode utilization (0..1) between two samples.
// It returns nil when no time elapsed or counters went backwards.
func CPUUtilization(prev, cur procfs.CPUTimes) []otlputil.Point {
	total := cur.Total() - prev.Total()
	if total <= 0 {
		return nil
	}
	p, c := cpuModes(prev), cpuModes(cur)
	pts := make([]otlputil.Point, 0, len(c))
	for i := range c {
		d := c[i].v - p[i].v
		if d < 0 {
			d = 0
		}
		pts = append(pts, otlputil.DoublePoint(d/total, otlputil.Str("cpu.mode", c[i].name)))
	}
	return pts
}

// Load emits system.cpu.load_average.{1m,5m,15m}.
type Load struct{ FS *hostfs.FS }

func (l *Load) Name() string { return "load" }

func (l *Load) Collect(now time.Time) ([]*metricspb.Metric, error) {
	b, err := l.FS.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, err
	}
	la, err := procfs.ParseLoadAvg(b)
	if err != nil {
		return nil, err
	}
	return []*metricspb.Metric{
		otlputil.Gauge("system.cpu.load_average.1m", "{thread}", now, otlputil.DoublePoint(la.Load1)),
		otlputil.Gauge("system.cpu.load_average.5m", "{thread}", now, otlputil.DoublePoint(la.Load5)),
		otlputil.Gauge("system.cpu.load_average.15m", "{thread}", now, otlputil.DoublePoint(la.Load15)),
	}, nil
}

// Memory emits system.memory.* and system.paging.usage.
type Memory struct{ FS *hostfs.FS }

func (m *Memory) Name() string { return "memory" }

func (m *Memory) Collect(now time.Time) ([]*metricspb.Metric, error) {
	b, err := m.FS.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	return MemoryMetrics(procfs.ParseMeminfo(b), now), nil
}

// MemoryState is the split of MemTotal into the contract's states.
type MemoryState struct {
	Total, Used, Free, Cached, Buffers uint64
}

// ComputeMemory applies the contract formula:
// used = MemTotal − MemFree − Buffers − Cached − SReclaimable. cached includes
// SReclaimable so that the four states sum to MemTotal.
func ComputeMemory(mi map[string]uint64) MemoryState {
	s := MemoryState{
		Total:   mi["MemTotal"],
		Free:    mi["MemFree"],
		Buffers: mi["Buffers"],
		Cached:  mi["Cached"] + mi["SReclaimable"],
	}
	if sub := s.Free + s.Buffers + s.Cached; s.Total > sub {
		s.Used = s.Total - sub
	}
	return s
}

// MemoryMetrics builds memory and paging metrics from parsed meminfo.
func MemoryMetrics(mi map[string]uint64, now time.Time) []*metricspb.Metric {
	s := ComputeMemory(mi)
	states := []struct {
		name string
		v    uint64
	}{{"used", s.Used}, {"free", s.Free}, {"cached", s.Cached}, {"buffers", s.Buffers}}
	var usage, util []otlputil.Point
	for _, st := range states {
		attr := otlputil.Str("system.memory.state", st.name)
		usage = append(usage, otlputil.IntPoint(int64(st.v), attr))
		if s.Total > 0 {
			util = append(util, otlputil.DoublePoint(float64(st.v)/float64(s.Total), attr))
		}
	}
	out := []*metricspb.Metric{
		otlputil.Sum("system.memory.usage", "By", false, time.Time{}, now, usage...),
		otlputil.Sum("system.memory.limit", "By", false, time.Time{}, now, otlputil.IntPoint(int64(s.Total))),
	}
	if len(util) > 0 {
		out = append(out, otlputil.Gauge("system.memory.utilization", "1", now, util...))
	}
	swapTotal, swapFree := mi["SwapTotal"], mi["SwapFree"]
	swapUsed := uint64(0)
	if swapTotal > swapFree {
		swapUsed = swapTotal - swapFree
	}
	out = append(out, otlputil.Sum("system.paging.usage", "By", false, time.Time{}, now,
		otlputil.IntPoint(int64(swapUsed), otlputil.Str("system.paging.state", "used")),
		otlputil.IntPoint(int64(swapFree), otlputil.Str("system.paging.state", "free")),
	))
	return out
}

// Disk emits system.disk.io and system.disk.operations.
type Disk struct {
	FS       *hostfs.FS
	BootTime func() time.Time
}

func (d *Disk) Name() string { return "disk" }

// ExcludedDisk reports whether a block device is excluded by default.
func ExcludedDisk(name string) bool {
	return strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram")
}

func (d *Disk) Collect(now time.Time) ([]*metricspb.Metric, error) {
	b, err := d.FS.ReadFile("/proc/diskstats")
	if err != nil {
		return nil, err
	}
	return DiskMetrics(procfs.ParseDiskstats(b), d.BootTime(), now), nil
}

// DiskMetrics builds disk metrics from parsed diskstats.
func DiskMetrics(stats []procfs.DiskStat, start, now time.Time) []*metricspb.Metric {
	var io, ops []otlputil.Point
	for _, s := range stats {
		if ExcludedDisk(s.Device) {
			continue
		}
		dev := otlputil.Str("system.device", s.Device)
		read, write := otlputil.Str("disk.io.direction", "read"), otlputil.Str("disk.io.direction", "write")
		io = append(io,
			otlputil.IntPoint(int64(s.SectorsRead*procfs.SectorSize), dev, read),
			otlputil.IntPoint(int64(s.SectorsWritten*procfs.SectorSize), dev, write))
		ops = append(ops,
			otlputil.IntPoint(int64(s.ReadsCompleted), dev, read),
			otlputil.IntPoint(int64(s.WritesCompleted), dev, write))
	}
	if len(io) == 0 {
		return nil
	}
	return []*metricspb.Metric{
		otlputil.Sum("system.disk.io", "By", true, start, now, io...),
		otlputil.Sum("system.disk.operations", "{operation}", true, start, now, ops...),
	}
}

// Network emits system.network.{io,packets,errors,dropped}.
type Network struct {
	FS       *hostfs.FS
	BootTime func() time.Time
}

func (n *Network) Name() string { return "network" }

func (n *Network) Collect(now time.Time) ([]*metricspb.Metric, error) {
	b, err := n.FS.ReadFile(n.FS.ProcSelf() + "/net/dev")
	if err != nil {
		return nil, err
	}
	return NetworkMetrics(procfs.ParseNetDev(b), n.BootTime(), now), nil
}

// NetworkMetrics builds network metrics from parsed /proc/net/dev.
func NetworkMetrics(stats []procfs.NetDevStat, start, now time.Time) []*metricspb.Metric {
	var io, pkts, errs, drops []otlputil.Point
	for _, s := range stats {
		if s.Interface == "lo" {
			continue
		}
		ifc := otlputil.Str("network.interface.name", s.Interface)
		rx, tx := otlputil.Str("network.io.direction", "receive"), otlputil.Str("network.io.direction", "transmit")
		io = append(io, otlputil.IntPoint(int64(s.RxBytes), ifc, rx), otlputil.IntPoint(int64(s.TxBytes), ifc, tx))
		pkts = append(pkts, otlputil.IntPoint(int64(s.RxPackets), ifc, rx), otlputil.IntPoint(int64(s.TxPackets), ifc, tx))
		errs = append(errs, otlputil.IntPoint(int64(s.RxErrors), ifc, rx), otlputil.IntPoint(int64(s.TxErrors), ifc, tx))
		drops = append(drops, otlputil.IntPoint(int64(s.RxDropped), ifc, rx), otlputil.IntPoint(int64(s.TxDropped), ifc, tx))
	}
	if len(io) == 0 {
		return nil
	}
	return []*metricspb.Metric{
		otlputil.Sum("system.network.io", "By", true, start, now, io...),
		otlputil.Sum("system.network.packets", "{packet}", true, start, now, pkts...),
		otlputil.Sum("system.network.errors", "{error}", true, start, now, errs...),
		otlputil.Sum("system.network.dropped", "{packet}", true, start, now, drops...),
	}
}

// Uptime emits system.uptime.
type Uptime struct{ FS *hostfs.FS }

func (u *Uptime) Name() string { return "uptime" }

func (u *Uptime) Collect(now time.Time) ([]*metricspb.Metric, error) {
	b, err := u.FS.ReadFile("/proc/uptime")
	if err != nil {
		return nil, err
	}
	v, err := procfs.ParseUptime(b)
	if err != nil {
		return nil, err
	}
	return []*metricspb.Metric{otlputil.Gauge("system.uptime", "s", now, otlputil.DoublePoint(v))}, nil
}

// Processes emits system.process.count by status.
type Processes struct{ FS *hostfs.FS }

func (p *Processes) Name() string { return "processes" }

// ProcessStatuses lists every process.status value in output order.
var ProcessStatuses = []string{"running", "sleeping", "disk_sleep", "stopped", "zombie", "idle", "other"}

func (p *Processes) Collect(now time.Time) ([]*metricspb.Metric, error) {
	pids, err := procfs.ListPIDs(p.FS)
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, pid := range pids {
		b, err := p.FS.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			continue // process exited
		}
		st, err := procfs.ParsePIDStat(b)
		if err != nil {
			continue
		}
		counts[procfs.ProcessStatus(st.State)]++
	}
	pts := make([]otlputil.Point, 0, len(ProcessStatuses))
	for _, s := range ProcessStatuses {
		pts = append(pts, otlputil.IntPoint(counts[s], otlputil.Str("process.status", s)))
	}
	return []*metricspb.Metric{otlputil.Sum("system.process.count", "{process}", false, time.Time{}, now, pts...)}, nil
}
