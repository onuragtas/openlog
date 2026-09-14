//go:build darwin || windows

package metrics

// Host metrics of macOS and Windows hosts from native APIs through gopsutil (D-104). Metric names,
// units and attributes are those of the Linux collectors (semantic-conventions §2, platform notes):
// states that do not exist on a platform are reported as 0 so that every host has the same series.

import (
	"context"
	"errors"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// nativeCollectors builds the enabled host collectors of a macOS or Windows host.
func nativeCollectors(cfg *config.Config) ([]Collector, serviceLookupCollector) {
	c := cfg.Collectors
	var cs []Collector
	if c.CPU {
		cs = append(cs, &nativeCPU{})
	}
	if c.Load && runtime.GOOS != "windows" { // Windows has no load average
		cs = append(cs, &nativeLoad{})
	}
	if c.Memory {
		cs = append(cs, &nativeMemory{})
	}
	if c.Filesystem {
		cs = append(cs, &nativeFilesystem{})
	}
	if c.Disk {
		cs = append(cs, &nativeDisk{})
	}
	if c.Network {
		cs = append(cs, &nativeNetwork{})
	}
	if c.Uptime {
		cs = append(cs, &nativeUptime{})
	}
	if c.Processes {
		cs = append(cs, &nativeProcesses{})
	}
	var top serviceLookupCollector
	if cfg.ProcessMetrics.Enabled && (cfg.ProcessMetrics.TopNCPU > 0 || cfg.ProcessMetrics.TopNMemory > 0) {
		t := &nativeProcessTop{topCPU: cfg.ProcessMetrics.TopNCPU, topMemory: cfg.ProcessMetrics.TopNMemory}
		cs = append(cs, t)
		top = t
	}
	return cs, top
}

func nativeBootTime() time.Time {
	if s, err := host.BootTime(); err == nil && s > 0 {
		return time.Unix(int64(s), 0)
	}
	return time.Time{}
}

type nativeCPU struct {
	prev *procfs.CPUTimes
	boot time.Time
}

func (c *nativeCPU) Name() string { return "cpu" }

func (c *nativeCPU) Collect(now time.Time) ([]*metricspb.Metric, error) {
	ts, err := cpu.Times(false)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, errors.New("cpu: no CPU times")
	}
	if c.boot.IsZero() {
		c.boot = nativeBootTime()
	}
	t := ts[0]
	cur := procfs.CPUTimes{User: t.User, Nice: t.Nice, System: t.System, Idle: t.Idle, IOWait: t.Iowait,
		IRQ: t.Irq, SoftIRQ: t.Softirq, Steal: t.Steal}
	logical, _ := cpu.Counts(true)
	modes := cpuModes(cur)
	pts := make([]otlputil.Point, 0, len(modes))
	for _, m := range modes {
		pts = append(pts, otlputil.DoublePoint(m.v, otlputil.Str("cpu.mode", m.name)))
	}
	out := []*metricspb.Metric{
		otlputil.Sum("system.cpu.time", "s", true, c.boot, now, pts...),
		otlputil.Sum("system.cpu.logical.count", "{cpu}", false, time.Time{}, now, otlputil.IntPoint(int64(logical))),
	}
	if c.prev != nil {
		if u := CPUUtilization(*c.prev, cur); u != nil {
			out = append(out, otlputil.Gauge("system.cpu.utilization", "1", now, u...))
		}
	}
	c.prev = &cur
	return out, nil
}

type nativeLoad struct{}

func (nativeLoad) Name() string { return "load" }

func (nativeLoad) Collect(now time.Time) ([]*metricspb.Metric, error) {
	a, err := load.Avg()
	if err != nil {
		return nil, err
	}
	return []*metricspb.Metric{
		otlputil.Gauge("system.cpu.load_average.1m", "{thread}", now, otlputil.DoublePoint(a.Load1)),
		otlputil.Gauge("system.cpu.load_average.5m", "{thread}", now, otlputil.DoublePoint(a.Load5)),
		otlputil.Gauge("system.cpu.load_average.15m", "{thread}", now, otlputil.DoublePoint(a.Load15)),
	}, nil
}

type nativeMemory struct{}

func (nativeMemory) Name() string { return "memory" }

// NativeMemoryInfo maps native memory statistics to the meminfo keys MemoryMetrics uses.
// macOS: free = free pages, cached = inactive (file cache that can be reclaimed), used = the rest
// (active + wired + compressed). Windows: free = available (includes the standby list), cached = 0.
// buffers is 0 on both.
func NativeMemoryInfo(goos string, vm *mem.VirtualMemoryStat, sw *mem.SwapMemoryStat) map[string]uint64 {
	mi := map[string]uint64{"MemTotal": vm.Total}
	switch goos {
	case "darwin":
		mi["MemFree"], mi["Cached"] = vm.Free, vm.Inactive
	default:
		mi["MemFree"] = vm.Available
	}
	if sw != nil {
		mi["SwapTotal"], mi["SwapFree"] = sw.Total, sw.Free
	}
	return mi
}

func (nativeMemory) Collect(now time.Time) ([]*metricspb.Metric, error) {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}
	sw, _ := mem.SwapMemory()
	return MemoryMetrics(NativeMemoryInfo(runtime.GOOS, vm, sw), now), nil
}

// nativeExcludedFSTypes are skipped in addition to ExcludedFSTypes.
var nativeExcludedFSTypes = map[string]bool{"devfs": true, "autofs": true, "nullfs": true, "lifs": true, "fdesc": true}

// NativeFilesystemKept reports whether a mount of a macOS or Windows host is reported. On macOS the sealed
// system volume's helper mounts below /System/Volumes (VM, Preboot, Update, xarts, …) share the APFS container
// with / and are skipped, except /System/Volumes/Data, which holds user data.
func NativeFilesystemKept(mountpoint, fstype string) bool {
	if ExcludedFSTypes[fstype] || nativeExcludedFSTypes[fstype] {
		return false
	}
	if strings.HasPrefix(mountpoint, "/System/Volumes/") && mountpoint != "/System/Volumes/Data" {
		return false
	}
	return true
}

type nativeFilesystem struct{}

func (nativeFilesystem) Name() string { return "filesystem" }

func (nativeFilesystem) Collect(now time.Time) ([]*metricspb.Metric, error) {
	parts, err := disk.Partitions(false)
	if err != nil && len(parts) == 0 {
		return nil, err
	}
	seen := map[string]bool{}
	var usage, util []otlputil.Point
	var errs []error
	for _, p := range parts {
		if seen[p.Mountpoint] || !NativeFilesystemKept(p.Mountpoint, p.Fstype) {
			continue
		}
		seen[p.Mountpoint] = true
		u, err := usageWithTimeout(p.Mountpoint)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if u.Total == 0 {
			continue
		}
		reserved := uint64(0)
		if u.Total > u.Used+u.Free {
			reserved = u.Total - u.Used - u.Free
		}
		base := []otlputilKV{{"system.device", p.Device}, {"system.filesystem.mountpoint", p.Mountpoint}, {"system.filesystem.type", p.Fstype}}
		for _, s := range []struct {
			name string
			v    uint64
		}{{"used", u.Used}, {"free", u.Free}, {"reserved", reserved}} {
			usage = append(usage, otlputil.IntPoint(int64(s.v), attrs(base, otlputilKV{"system.filesystem.state", s.name})...))
		}
		if u.Used+u.Free > 0 {
			util = append(util, otlputil.DoublePoint(float64(u.Used)/float64(u.Used+u.Free), attrs(base)...))
		}
	}
	var out []*metricspb.Metric
	if len(usage) > 0 {
		out = append(out,
			otlputil.Sum("system.filesystem.usage", "By", false, time.Time{}, now, usage...),
			otlputil.Gauge("system.filesystem.utilization", "1", now, util...))
	}
	return out, joinErrs(errs)
}

func usageWithTimeout(mountpoint string) (*disk.UsageStat, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statfsTimeout)
	defer cancel()
	type res struct {
		u   *disk.UsageStat
		err error
	}
	ch := make(chan res, 1)
	go func() {
		u, err := disk.UsageWithContext(ctx, mountpoint)
		ch <- res{u, err}
	}()
	select {
	case r := <-ch:
		return r.u, r.err
	case <-ctx.Done():
		return nil, errors.New("usage " + mountpoint + ": timed out after " + statfsTimeout.String())
	}
}

type nativeDisk struct{ boot time.Time }

func (d *nativeDisk) Name() string { return "disk" }

func (d *nativeDisk) Collect(now time.Time) ([]*metricspb.Metric, error) {
	counters, err := disk.IOCounters()
	if err != nil {
		return nil, err
	}
	if d.boot.IsZero() {
		d.boot = nativeBootTime()
	}
	names := make([]string, 0, len(counters))
	for n := range counters {
		names = append(names, n)
	}
	sort.Strings(names)
	var io, ops []otlputil.Point
	for _, n := range names {
		c := counters[n]
		dev := otlputil.Str("system.device", n)
		read, write := otlputil.Str("disk.io.direction", "read"), otlputil.Str("disk.io.direction", "write")
		io = append(io, otlputil.IntPoint(int64(c.ReadBytes), dev, read), otlputil.IntPoint(int64(c.WriteBytes), dev, write))
		ops = append(ops, otlputil.IntPoint(int64(c.ReadCount), dev, read), otlputil.IntPoint(int64(c.WriteCount), dev, write))
	}
	if len(io) == 0 {
		return nil, nil
	}
	return []*metricspb.Metric{
		otlputil.Sum("system.disk.io", "By", true, d.boot, now, io...),
		otlputil.Sum("system.disk.operations", "{operation}", true, d.boot, now, ops...),
	}, nil
}

// NativeLoopback reports loopback interface names of macOS (lo0) and Windows ("Loopback Pseudo-Interface 1").
func NativeLoopback(name string) bool {
	return name == "lo" || name == "lo0" || strings.HasPrefix(strings.ToLower(name), "loopback")
}

type nativeNetwork struct{ boot time.Time }

func (n *nativeNetwork) Name() string { return "network" }

func (n *nativeNetwork) Collect(now time.Time) ([]*metricspb.Metric, error) {
	counters, err := gnet.IOCounters(true)
	if err != nil {
		return nil, err
	}
	if n.boot.IsZero() {
		n.boot = nativeBootTime()
	}
	stats := make([]procfs.NetDevStat, 0, len(counters))
	for _, c := range counters {
		if NativeLoopback(c.Name) {
			continue
		}
		stats = append(stats, procfs.NetDevStat{Interface: c.Name, RxBytes: c.BytesRecv, TxBytes: c.BytesSent,
			RxPackets: c.PacketsRecv, TxPackets: c.PacketsSent, RxErrors: c.Errin, TxErrors: c.Errout,
			RxDropped: c.Dropin, TxDropped: c.Dropout})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Interface < stats[j].Interface })
	return NetworkMetrics(stats, n.boot, now), nil
}

type nativeUptime struct{}

func (nativeUptime) Name() string { return "uptime" }

func (nativeUptime) Collect(now time.Time) ([]*metricspb.Metric, error) {
	up, err := host.Uptime()
	if err != nil {
		return nil, err
	}
	return []*metricspb.Metric{otlputil.Gauge("system.uptime", "s", now, otlputil.DoublePoint(float64(up)))}, nil
}

type nativeProcesses struct{}

func (nativeProcesses) Name() string { return "processes" }

func (nativeProcesses) Collect(now time.Time) ([]*metricspb.Metric, error) {
	counts, err := nativeProcessStatusCounts() // native_darwin.go, native_windows.go
	if err != nil {
		return nil, err
	}
	pts := make([]otlputil.Point, 0, len(ProcessStatuses))
	for _, s := range ProcessStatuses {
		pts = append(pts, otlputil.IntPoint(counts[s], otlputil.Str("process.status", s)))
	}
	return []*metricspb.Metric{otlputil.Sum("system.process.count", "{process}", false, time.Time{}, now, pts...)}, nil
}

// nativeProcessTop is ProcessTop for macOS and Windows.
type nativeProcessTop struct {
	topCPU, topMemory int

	mu     sync.Mutex
	lookup ServiceLookup

	prev     map[int32]nativeCPUSample
	prevTime time.Time
}

type nativeCPUSample struct {
	created int64
	seconds float64
}

func (p *nativeProcessTop) Name() string { return "process_metrics" }

func (p *nativeProcessTop) SetServiceLookup(l ServiceLookup) {
	p.mu.Lock()
	p.lookup = l
	p.mu.Unlock()
}

type nativeSample struct {
	proc    *process.Process
	util    float64
	hasUtil bool
	rss     uint64
	vms     uint64
}

func (p *nativeProcessTop) Collect(now time.Time) ([]*metricspb.Metric, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	ncpu, _ := cpu.Counts(true)
	if ncpu < 1 {
		ncpu = 1
	}
	elapsed := now.Sub(p.prevTime).Seconds()
	cur := make(map[int32]nativeCPUSample, len(procs))
	samples := make([]nativeSample, 0, len(procs))
	for _, pr := range procs {
		if pr.Pid == 0 {
			continue // kernel task / System Idle Process
		}
		s := nativeSample{proc: pr}
		if mi, err := pr.MemoryInfo(); err == nil {
			s.rss, s.vms = mi.RSS, mi.VMS
		}
		if t, err := pr.Times(); err == nil {
			created, _ := pr.CreateTime()
			secs := t.User + t.System
			cur[pr.Pid] = nativeCPUSample{created: created, seconds: secs}
			if prev, ok := p.prev[pr.Pid]; ok && prev.created == created && elapsed > 0 && secs >= prev.seconds {
				s.util = (secs - prev.seconds) / elapsed / float64(ncpu)
				s.hasUtil = true
			}
		}
		samples = append(samples, s)
	}
	p.prev, p.prevTime = cur, now

	top := selectNativeTop(samples, p.topCPU, p.topMemory)
	if len(top) == 0 {
		return nil, nil
	}
	p.mu.Lock()
	lookup := p.lookup
	p.mu.Unlock()
	var cpuPts, memPts, virtPts, threadPts, fdPts []otlputil.Point
	for _, s := range top {
		pid := int(s.proc.Pid)
		name, _ := s.proc.Name()
		exe, _ := s.proc.Exe()
		attrs := []*commonpb.KeyValue{otlputil.Int("process.pid", int64(pid)), otlputil.Str("process.executable.name", name)}
		if exe != "" {
			attrs = append(attrs, otlputil.Str("process.executable.path", exe))
		}
		if owner, err := s.proc.Username(); err == nil && owner != "" {
			attrs = append(attrs, otlputil.Str("process.owner", owner))
		}
		if lookup != nil {
			if id := lookup(pid, exe, ""); id != "" {
				attrs = append(attrs, otlputil.Str("openlog.discovery.id", id))
			}
		}
		if s.hasUtil {
			cpuPts = append(cpuPts, otlputil.DoublePoint(s.util, attrs...))
		}
		memPts = append(memPts, otlputil.IntPoint(int64(s.rss), attrs...))
		virtPts = append(virtPts, otlputil.IntPoint(int64(s.vms), attrs...))
		if n, err := s.proc.NumThreads(); err == nil {
			threadPts = append(threadPts, otlputil.IntPoint(int64(n), attrs...))
		}
		if n, err := s.proc.NumFDs(); err == nil { // Windows: handle count
			fdPts = append(fdPts, otlputil.IntPoint(int64(n), attrs...))
		}
	}
	var out []*metricspb.Metric
	if len(cpuPts) > 0 {
		out = append(out, otlputil.Gauge("process.cpu.utilization", "1", now, cpuPts...))
	}
	out = append(out,
		otlputil.Sum("process.memory.usage", "By", false, time.Time{}, now, memPts...),
		otlputil.Sum("process.memory.virtual", "By", false, time.Time{}, now, virtPts...))
	if len(threadPts) > 0 {
		out = append(out, otlputil.Sum("process.threads", "{thread}", false, time.Time{}, now, threadPts...))
	}
	if len(fdPts) > 0 {
		out = append(out, otlputil.Sum("process.open_file_descriptors", "{count}", false, time.Time{}, now, fdPts...))
	}
	return out, nil
}

// selectNativeTop is SelectTop for native samples: the union of the n highest CPU utilizations (> 0) and the
// m largest RSS values, ordered by PID.
func selectNativeTop(samples []nativeSample, n, m int) []nativeSample {
	chosen := map[int32]bool{}
	byCPU := append([]nativeSample(nil), samples...)
	sort.SliceStable(byCPU, func(i, j int) bool { return byCPU[i].util > byCPU[j].util })
	for i := 0; i < len(byCPU) && i < n && byCPU[i].util > 0; i++ {
		chosen[byCPU[i].proc.Pid] = true
	}
	byMem := append([]nativeSample(nil), samples...)
	sort.SliceStable(byMem, func(i, j int) bool { return byMem[i].rss > byMem[j].rss })
	for i := 0; i < len(byMem) && i < m && byMem[i].rss > 0; i++ {
		chosen[byMem[i].proc.Pid] = true
	}
	var out []nativeSample
	for _, s := range samples {
		if chosen[s.proc.Pid] {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].proc.Pid < out[j].proc.Pid })
	return out
}
