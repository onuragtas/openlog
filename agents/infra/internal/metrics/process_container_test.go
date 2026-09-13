package metrics

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
	"github.com/onuragtas/openlog/agents/infra/internal/testfixtures"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func intAttr(dp *metricspb.NumberDataPoint, k string) int64 {
	for _, a := range dp.Attributes {
		if a.Key == k {
			return a.Value.GetIntValue()
		}
	}
	return -1
}

func pidPoint(t *testing.T, m *metricspb.Metric, pid int64) *metricspb.NumberDataPoint {
	t.Helper()
	if m == nil {
		t.Fatalf("metric missing")
	}
	for _, dp := range points(m) {
		if intAttr(dp, "process.pid") == pid {
			return dp
		}
	}
	return nil
}

func TestSelectTop(t *testing.T) {
	s := func(pid int, util float64, rss int64) procSample {
		return procSample{pid: pid, util: util, st: procfs.PIDStat{RSSPages: rss}}
	}
	samples := []procSample{s(1, 0.5, 10), s(2, 0, 900), s(3, 0.9, 5), s(4, 0.1, 800), s(5, 0, 0)}
	got := SelectTop(samples, 2, 2)
	var pids []int
	for _, g := range got {
		pids = append(pids, g.pid)
	}
	if !slices.Equal(pids, []int{1, 2, 3, 4}) {
		t.Errorf("top = %v", pids)
	}
	if got := SelectTop(samples, 0, 0); len(got) != 0 {
		t.Errorf("n=0 → %v", got)
	}
	if got := SelectTop([]procSample{s(7, 0, 0)}, 5, 5); len(got) != 0 {
		t.Error("idle zero-RSS processes must not be selected")
	}
}

func setStat(t *testing.T, root string, pid int, utime int, rss int) {
	t.Helper()
	p := filepath.Join(root, "proc", strconv.Itoa(pid), "stat")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	// fields after "pid (comm)": utime is field 14, rss field 24 (1-based)
	f[13] = strconv.Itoa(utime)
	f[23] = strconv.Itoa(rss)
	if err := os.WriteFile(p, []byte(strings.Join(f, " ")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProcessTopOnFixture(t *testing.T) {
	files := maps.Clone(testfixtures.ServiceHost())
	fs := hostfstest.Build(t, files)
	root := fs.Root()
	p := &ProcessTop{FS: fs, TopCPU: 1, TopMemory: 2, PageSize: 4096}
	p.SetServiceLookup(func(pid int, exe, containerID string) string {
		if exe == "/usr/bin/redis-server" {
			return "redis"
		}
		return ""
	})
	now := time.Unix(1704153600, 0)
	setStat(t, root, 812, 150, 5000) // redis: largest RSS
	setStat(t, root, 680, 150, 3000) // chronyd: second largest
	first, err := p.Collect(now)
	if err != nil {
		t.Fatal(err)
	}
	if index(first)["process.cpu.utilization"] != nil {
		t.Error("no utilization on the first sample")
	}
	setStat(t, root, 900, 150+500, 100) // nginx: 5s CPU in 10s on 2 CPUs → 0.25
	ms := index(mustCollect(t, p, now.Add(10*time.Second)))

	// Every selected process with a previous sample reports utilization (idle ones as 0).
	util := ms["process.cpu.utilization"]
	if util == nil || len(points(util)) != 3 {
		t.Fatalf("cpu utilization = %v", util)
	}
	if dp := pidPoint(t, util, 900); dp == nil || dp.GetAsDouble() < 0.2499 || dp.GetAsDouble() > 0.2501 {
		t.Errorf("nginx utilization = %v", dp)
	}
	if dp := pidPoint(t, util, 812); dp == nil || dp.GetAsDouble() != 0 {
		t.Errorf("idle redis utilization = %v", dp)
	}
	mem := ms["process.memory.usage"]
	if len(points(mem)) != 3 {
		t.Errorf("memory points = %d, want union of top CPU (nginx) and top memory (redis, chronyd)", len(points(mem)))
	}
	redis := pidPoint(t, mem, 812)
	if redis == nil || redis.GetAsInt() != 5000*4096 || attr(redis, "process.executable.name") != "redis-server" ||
		attr(redis, "process.executable.path") != "/usr/bin/redis-server" || attr(redis, "openlog.discovery.id") != "redis" ||
		attr(redis, "process.owner") != "112" {
		t.Errorf("redis point = %v", redis)
	}
	if ch := pidPoint(t, mem, 680); ch == nil || attr(ch, "process.owner") != "_chrony" || attr(ch, "openlog.discovery.id") != "" {
		t.Errorf("chronyd point = %v", ch)
	}
	if v := pidPoint(t, ms["process.memory.virtual"], 812); v == nil || v.GetAsInt() != 1000 {
		t.Errorf("virtual = %v", v)
	}
	if th := pidPoint(t, ms["process.threads"], 812); th == nil || th.GetAsInt() != 1 {
		t.Errorf("threads = %v", th)
	}
	if fd := pidPoint(t, ms["process.open_file_descriptors"], 900); fd == nil || fd.GetAsInt() != 3 {
		t.Errorf("fds = %v", fd)
	}
	for _, m := range []string{"process.memory.usage", "process.threads"} {
		if ms[m].GetSum() == nil || ms[m].GetSum().IsMonotonic {
			t.Errorf("%s must be a non-monotonic sum", m)
		}
	}
}

func mustCollect(t *testing.T, c Collector, now time.Time) []*metricspb.Metric {
	t.Helper()
	ms, err := c.Collect(now)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestContainersCollector(t *testing.T) {
	id := strings.Repeat("d", 64)
	hostID := strings.Repeat("e", 64)
	cg := "/sys/fs/cgroup/system.slice/docker-" + id + ".scope"
	hcg := "/sys/fs/cgroup/docker/" + hostID
	files := map[string]string{
		"/proc/stat":                        "cpu  1 1 1 1 1 1 1 1\ncpu0 1 1 1 1 1 1 1 1\ncpu1 1 1 1 1 1 1 1 1\n",
		"/proc/meminfo":                     "MemTotal: 4000000 kB\n",
		"/proc/1/ns/net":                    "symlink:net:[4026531840]",
		"/sys/fs/cgroup/cgroup.controllers": "cpu io memory\n",
		cg + "/cpu.stat":                    "usage_usec 1000000\nuser_usec 600000\n",
		cg + "/memory.current":              "104857600\n",
		cg + "/memory.max":                  "max\n",
		cg + "/memory.stat":                 "anon 50000000\ninactive_file 4857600\n",
		cg + "/io.stat":                     "8:0 rbytes=100 wbytes=200 rios=1 rios_extra=9 wios=2\n253:0 rbytes=1 wbytes=2 rios=3 wios=4\n",
		cg + "/cgroup.procs":                "",
		cg + "/init.scope/cgroup.procs":     "4300\n",
		"/proc/4300/ns/net":                 "symlink:net:[4026532000]",
		"/proc/4300/net/dev":                "Inter-|\n face |\n    lo: 99 1 0 0 0 0 0 0 99 1 0 0 0 0 0 0\n  eth0: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0\n",
		hcg + "/cpu.stat":                   "usage_usec 5\n",
		hcg + "/memory.current":             "10\n",
		hcg + "/memory.max":                 "1048576\n",
		hcg + "/cgroup.procs":               "4400\n",
		"/proc/4400/ns/net":                 "symlink:net:[4026531840]",
	}
	fs := hostfstest.Build(t, files)
	c := &Containers{FS: fs}
	now := time.Unix(1704153600, 0)
	ms := index(mustCollect(t, c, now))
	if ms["container.cpu.utilization"] != nil {
		t.Error("no utilization on first sample")
	}
	if err := os.WriteFile(filepath.Join(fs.Root(), cg, "cpu.stat"), []byte("usage_usec 11000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ms = index(mustCollect(t, c, now.Add(10*time.Second)))
	if v := find(t, ms["container.cpu.utilization"], "container.id", id); v != 0.5 {
		t.Errorf("utilization = %v, want 10s/10s/2cpu", v)
	}
	if v := find(t, ms["container.cpu.time"], "container.id", id); v != 11 {
		t.Errorf("cpu time = %v", v)
	}
	if v := find(t, ms["container.memory.usage"], "container.id", id); v != 100000000 {
		t.Errorf("memory usage = %v (current − inactive_file)", v)
	}
	if v := find(t, ms["container.memory.limit"], "container.id", id); v != 4000000*1024 {
		t.Errorf("unlimited → host memory, got %v", v)
	}
	if v := find(t, ms["container.memory.limit"], "container.id", hostID); v != 1048576 {
		t.Errorf("limit = %v", v)
	}
	if v := find(t, ms["container.blockio.io"], "container.id", id, "disk.io.direction", "write"); v != 202 {
		t.Errorf("blockio write = %v", v)
	}
	if v := find(t, ms["container.blockio.operations"], "container.id", id, "disk.io.direction", "read"); v != 4 {
		t.Errorf("blockio read ops = %v", v)
	}
	if v := find(t, ms["container.network.io"], "container.id", id, "network.io.direction", "transmit"); v != 2000 {
		t.Errorf("network tx = %v", v)
	}
	for _, dp := range points(ms["container.network.io"]) {
		if attr(dp, "container.id") == hostID {
			t.Error("host-network container must not report network io")
		}
	}
	if dp := points(ms["container.cpu.time"])[0]; attr(dp, "container.runtime") == "" {
		t.Errorf("runtime attribute missing: %v", dp)
	}
	// Without Docker metadata every cgroup container is reported running.
	if v := find(t, ms["openlog.container.status"], "container.id", id, "openlog.container.state", "running"); v != 1 {
		t.Errorf("status = %v", v)
	}
	if ms["container.restarts"] != nil {
		t.Error("restarts need Docker metadata")
	}
}

func TestContainerStatusWithDockerMetadata(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	run, old, recent := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	meta := map[string]containers.Container{
		run: {ID: run, Name: "shop-orders-1", Runtime: "docker", Image: "openlog-apmdemo/orders:1", State: "running", Health: "healthy",
			Labels: map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.service": "orders"}},
		old:    {ID: old, Name: "old", Runtime: "docker", Image: "busybox", State: "exited", FinishedAt: "2026-09-01T00:00:00Z"},
		recent: {ID: recent, Name: "job", Runtime: "docker", Image: "busybox", State: "exited", FinishedAt: "2026-09-14T11:30:00Z"},
	}
	ct := meta[run]
	ct.Apply(containers.Details{StartedAt: time.Date(2026, 9, 14, 11, 0, 0, 0, time.UTC), RestartCount: 2})
	meta[run] = ct
	c := &Containers{cgroups: map[string]containers.Cgroup{run: {ID: run, Runtime: "docker"}}}
	status, restarts := c.statusPoints(meta, map[string]bool{run: true}, now)
	if len(status) != 2 || len(restarts) != 1 || restarts[0].Int != 2 {
		t.Fatalf("status %d points, restarts %+v", len(status), restarts)
	}
	got := map[string]map[string]string{}
	for _, p := range status {
		m := map[string]string{}
		for _, kv := range p.Attrs {
			m[kv.Key] = kv.Value.GetStringValue()
		}
		got[m["container.id"]] = m
	}
	r := got[run]
	if r["openlog.container.state"] != "running" || r["openlog.container.health"] != "healthy" || r["openlog.container.started_at"] != "2026-09-14T11:00:00Z" ||
		r["docker.compose.project"] != "shop" || r["docker.compose.service"] != "orders" || r["container.name"] != "shop-orders-1" {
		t.Errorf("running container attributes %v", r)
	}
	if got[recent]["openlog.container.state"] != "exited" || got[old] != nil {
		t.Errorf("stopped containers: recent %v, old %v", got[recent], got[old])
	}
}

func TestFilesystemExcludesFileMountsAndAgentDir(t *testing.T) {
	isDir := func(mp string) bool { return !strings.HasPrefix(mp, "/etc/") }
	mounts := FilesystemMounts([]procfs.Mount{
		{MountPoint: "/", FSType: "ext4", Device: "/dev/vda1", MajorMinor: "253:1"},
		{MountPoint: "/etc/hosts", FSType: "ext4", Device: "/dev/vda1", MajorMinor: "253:1"},
		{MountPoint: "/etc/resolv.conf", FSType: "ext4", Device: "/dev/vda1", MajorMinor: "253:1"},
		{MountPoint: "/data", FSType: "ext4", Device: "/dev/vda1", MajorMinor: "253:1"},
		{MountPoint: "/var/lib/openlog-infra-agent", FSType: "ext4", Device: "/dev/vda1", MajorMinor: "253:1"},
		{MountPoint: "/var/lib/other-agent", FSType: "xfs", Device: "/dev/vdb1", MajorMinor: "253:17"},
	}, isDir, "/var/lib/openlog-infra-agent/", "/var/lib/other-agent")
	var mps []string
	for _, m := range mounts {
		mps = append(mps, m.MountPoint)
	}
	if strings.Join(mps, " ") != "/ /data /var/lib/other-agent" {
		t.Errorf("mounts = %v", mps)
	}
}
