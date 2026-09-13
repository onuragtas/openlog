package metrics

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
	"github.com/onuragtas/openlog/agents/infra/internal/testfixtures"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func index(ms []*metricspb.Metric) map[string]*metricspb.Metric {
	out := map[string]*metricspb.Metric{}
	for _, m := range ms {
		out[m.Name] = m
	}
	return out
}

func points(m *metricspb.Metric) []*metricspb.NumberDataPoint {
	if g := m.GetGauge(); g != nil {
		return g.DataPoints
	}
	return m.GetSum().GetDataPoints()
}

func attr(dp *metricspb.NumberDataPoint, k string) string {
	for _, a := range dp.Attributes {
		if a.Key == k {
			return a.Value.GetStringValue()
		}
	}
	return ""
}

func value(dp *metricspb.NumberDataPoint) float64 {
	if v, ok := dp.Value.(*metricspb.NumberDataPoint_AsInt); ok {
		return float64(v.AsInt)
	}
	return dp.GetAsDouble()
}

func find(t *testing.T, m *metricspb.Metric, kv ...string) float64 {
	t.Helper()
	if m == nil {
		t.Fatalf("metric missing (want point %v)", kv)
	}
outer:
	for _, dp := range points(m) {
		for i := 0; i < len(kv); i += 2 {
			if attr(dp, kv[i]) != kv[i+1] {
				continue outer
			}
		}
		return value(dp)
	}
	t.Fatalf("%s: no point with %v", m.Name, kv)
	return 0
}

func TestCPUUtilization(t *testing.T) {
	prev := procfs.CPUTimes{User: 10, Idle: 80, System: 10}
	cur := procfs.CPUTimes{User: 30, Idle: 150, System: 20}
	pts := CPUUtilization(prev, cur)
	want := map[string]float64{"user": 0.2, "idle": 0.7, "system": 0.1, "nice": 0}
	sum := 0.0
	for _, p := range pts {
		mode := p.Attrs[0].Value.GetStringValue()
		sum += p.Double
		if w, ok := want[mode]; ok && math.Abs(p.Double-w) > 1e-9 {
			t.Errorf("%s = %v want %v", mode, p.Double, w)
		}
	}
	if len(pts) != 8 || math.Abs(sum-1) > 1e-9 {
		t.Errorf("points=%d sum=%v", len(pts), sum)
	}
	if CPUUtilization(cur, cur) != nil {
		t.Error("zero delta must return nil")
	}
}

func TestComputeMemory(t *testing.T) {
	mi := map[string]uint64{"MemTotal": 1000, "MemFree": 200, "Buffers": 50, "Cached": 300, "SReclaimable": 50}
	s := ComputeMemory(mi)
	if s.Used != 400 || s.Cached != 350 || s.Free != 200 || s.Buffers != 50 {
		t.Errorf("memory = %+v", s)
	}
	if s.Used+s.Free+s.Cached+s.Buffers != s.Total {
		t.Error("states must sum to total")
	}
}

func TestFilesystemUsage(t *testing.T) {
	u, f, r := FilesystemUsage(hostfs.Statfs{BlockSize: 4096, Blocks: 1000, BlocksFree: 300, BlocksAvail: 250})
	if u != 700*4096 || f != 250*4096 || r != 50*4096 {
		t.Errorf("usage = %d %d %d", u, f, r)
	}
	mounts := FilesystemMounts([]procfs.Mount{
		{MountPoint: "/", FSType: "ext4", Device: "/dev/sda1"},
		{MountPoint: "/proc", FSType: "proc"},
		{MountPoint: "/data", FSType: "xfs", Device: "/dev/sdb1"},
		{MountPoint: "/data", FSType: "xfs", Device: "/dev/sdc1"},
		{MountPoint: "/run", FSType: "tmpfs"},
	}, nil)
	if len(mounts) != 2 || mounts[1].Device != "/dev/sdc1" {
		t.Errorf("mounts = %+v", mounts)
	}
}

func TestSetOnFixtureHost(t *testing.T) {
	fs := hostfstest.Build(t, testfixtures.PlainHost())
	stats := selfmon.New(time.Unix(1, 0))
	cfg := config.Default()
	cfg.ProcessMetrics.Enabled, cfg.Containers.Enabled = false, false
	set := NewSet(fs, cfg, stats, nil, nil)
	for _, c := range set.collectors {
		if f, ok := c.(*Filesystem); ok {
			f.Statfs = func(mp string) (hostfs.Statfs, error) {
				if mp == "/boot/efi" {
					return hostfs.Statfs{}, errors.New("boom")
				}
				return hostfs.Statfs{BlockSize: 4096, Blocks: 1000, BlocksFree: 400, BlocksAvail: 350}, nil
			}
		}
	}
	now := time.Unix(1704153600, 0)
	first := index(set.Collect(now))
	if _, ok := first["system.cpu.utilization"]; ok {
		t.Error("utilization must not be emitted on the first sample")
	}
	ms := index(set.Collect(now.Add(10 * time.Second)))

	required := []string{
		"system.cpu.time", "system.cpu.logical.count", "system.cpu.load_average.1m", "system.cpu.load_average.5m",
		"system.cpu.load_average.15m", "system.memory.usage", "system.memory.limit", "system.memory.utilization",
		"system.paging.usage", "system.filesystem.usage", "system.filesystem.utilization", "system.disk.io",
		"system.disk.operations", "system.network.io", "system.network.packets", "system.network.errors",
		"system.network.dropped", "system.uptime", "system.process.count", "openlog.agent.export.items",
		"openlog.agent.buffer.usage", "openlog.agent.collector.duration",
	}
	for _, name := range required {
		if ms[name] == nil {
			t.Errorf("missing metric %s", name)
		}
	}
	// Second identical /proc/stat → zero delta → still no utilization.
	if ms["system.cpu.utilization"] != nil {
		t.Error("identical samples must not produce utilization")
	}

	cpu := ms["system.cpu.time"]
	if !cpu.GetSum().IsMonotonic || cpu.Unit != "s" || find(t, cpu, "cpu.mode", "user") != 100 || find(t, cpu, "cpu.mode", "interrupt") != 0.6 {
		t.Errorf("cpu.time wrong: %v", cpu)
	}
	if points(cpu)[0].StartTimeUnixNano != uint64(time.Unix(testfixtures.BootTime, 0).UnixNano()) {
		t.Error("cpu.time start must be boot time")
	}
	if find(t, ms["system.cpu.logical.count"]) != 2 {
		t.Error("logical count")
	}
	if find(t, ms["system.memory.usage"], "system.memory.state", "used") != (4000000-1000000-100000-1200000-200000)*1024 {
		t.Error("memory used formula")
	}
	if find(t, ms["system.paging.usage"], "system.paging.state", "used") != 500000*1024 {
		t.Error("paging used")
	}
	fsu := ms["system.filesystem.usage"]
	if len(points(fsu)) != 3 || find(t, fsu, "system.filesystem.mountpoint", "/", "system.filesystem.state", "reserved") != 50*4096 {
		t.Errorf("filesystem usage points: %d", len(points(fsu)))
	}
	if find(t, ms["system.disk.io"], "system.device", "sda", "disk.io.direction", "write") != 40000*512 {
		t.Error("disk io")
	}
	for _, dp := range points(ms["system.disk.io"]) {
		if attr(dp, "system.device") == "loop0" {
			t.Error("loop devices must be excluded")
		}
	}
	if find(t, ms["system.network.dropped"], "network.interface.name", "eth0", "network.io.direction", "transmit") != 1 {
		t.Error("network dropped")
	}
	for _, dp := range points(ms["system.network.io"]) {
		if attr(dp, "network.interface.name") == "lo" {
			t.Error("lo must be excluded")
		}
	}
	pc := ms["system.process.count"]
	if find(t, pc, "process.status", "sleeping") != 9 || find(t, pc, "process.status", "idle") != 1 || find(t, pc, "process.status", "stopped") != 1 {
		t.Errorf("process count: %v", pc)
	}
	if find(t, ms["system.uptime"]) != 86400.5 {
		t.Error("uptime")
	}
	if find(t, ms["openlog.agent.collector.duration"], "collector", "cpu") < 0 {
		t.Error("collector duration")
	}
}

func TestPermissionDeniedMetric(t *testing.T) {
	stats := selfmon.New(time.Unix(1, 0))
	stats.PermissionDenied("processes")
	stats.PermissionDenied("processes")
	stats.SetCollectionInterval(20 * time.Second)
	ms := index(SelfTelemetry(stats, time.Unix(2, 0)))
	if iv := ms["openlog.agent.collection.interval"]; iv == nil || iv.Unit != "s" || iv.GetGauge() == nil || find(t, iv) != 20 || len(points(iv)[0].Attributes) != 0 {
		t.Errorf("collection.interval = %v", iv)
	}
	if find(t, ms["openlog.agent.permission_denied"], "collector", "processes") != 2 {
		t.Error("permission_denied")
	}
}

func TestSelfTelemetryUpdateMetrics(t *testing.T) {
	now := time.Unix(1700000000, 0)
	stats := selfmon.New(now)
	for _, m := range SelfTelemetry(stats, now) {
		if strings.HasPrefix(m.Name, "openlog.agent.update.") {
			t.Fatalf("update metrics without an update manager: %s", m.Name)
		}
	}
	stats.SetUpdateState("confirming")
	stats.AddUpdateAttempt("rolled_back")
	got := map[string]*metricspb.Metric{}
	for _, m := range SelfTelemetry(stats, now) {
		got[m.Name] = m
	}
	st := got["openlog.agent.update.state"]
	if st == nil || len(st.GetGauge().DataPoints) != 1 || st.GetGauge().DataPoints[0].GetAsInt() != 1 ||
		st.GetGauge().DataPoints[0].Attributes[0].Value.GetStringValue() != "confirming" {
		t.Fatalf("state metric: %v", st)
	}
	at := got["openlog.agent.update.attempts"]
	if at == nil || !at.GetSum().IsMonotonic || len(at.GetSum().DataPoints) != 3 {
		t.Fatalf("attempts metric: %v", at)
	}
	for _, p := range at.GetSum().DataPoints {
		want := int64(0)
		if p.Attributes[0].Value.GetStringValue() == "rolled_back" {
			want = 1
		}
		if p.GetAsInt() != want {
			t.Errorf("attempts %v", p)
		}
	}
}
