package metrics

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// ServiceLookup maps a process to the rule id of the discovered service it
// belongs to ("" when none). pid, exe and containerID may be empty/zero.
type ServiceLookup func(pid int, exe, containerID string) string

// ProcessTop emits per-process metrics for the top processes by CPU and by
// memory (union). Only these processes produce series, which bounds cardinality;
// series still churn with PIDs.
type ProcessTop struct {
	FS         *hostfs.FS
	TopCPU     int
	TopMemory  int
	PageSize   int64
	passwdPath string

	mu     sync.Mutex
	lookup ServiceLookup

	prev     map[int]procCPU
	prevTime time.Time
	users    map[int]string
	usersMod time.Time
}

type procCPU struct {
	start uint64
	ticks uint64
}

func (p *ProcessTop) Name() string { return "process_metrics" }

// SetServiceLookup installs the discovery mapping used for openlog.discovery.id.
func (p *ProcessTop) SetServiceLookup(l ServiceLookup) {
	p.mu.Lock()
	p.lookup = l
	p.mu.Unlock()
}

type procSample struct {
	pid     int
	st      procfs.PIDStat
	util    float64
	hasUtil bool
}

// SelectTop returns the union of the n highest CPU utilizations (only
// processes with a utilization > 0) and the m largest RSS values, ordered by PID.
func SelectTop(samples []procSample, n, m int) []procSample {
	chosen := map[int]bool{}
	byCPU := append([]procSample(nil), samples...)
	sort.SliceStable(byCPU, func(i, j int) bool { return byCPU[i].util > byCPU[j].util })
	for i := 0; i < len(byCPU) && i < n; i++ {
		if byCPU[i].util <= 0 {
			break
		}
		chosen[byCPU[i].pid] = true
	}
	byMem := append([]procSample(nil), samples...)
	sort.SliceStable(byMem, func(i, j int) bool { return byMem[i].st.RSSPages > byMem[j].st.RSSPages })
	for i := 0; i < len(byMem) && i < m; i++ {
		if byMem[i].st.RSSPages <= 0 {
			break
		}
		chosen[byMem[i].pid] = true
	}
	var out []procSample
	for _, s := range samples {
		if chosen[s.pid] {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

func (p *ProcessTop) Collect(now time.Time) ([]*metricspb.Metric, error) {
	pids, err := procfs.ListPIDs(p.FS)
	if err != nil {
		return nil, err
	}
	ncpu := 1
	if b, err := p.FS.ReadFile("/proc/stat"); err == nil {
		if st, err := procfs.ParseStat(b); err == nil && st.LogicalCPUs > 0 {
			ncpu = st.LogicalCPUs
		}
	}
	elapsed := now.Sub(p.prevTime).Seconds()
	cur := make(map[int]procCPU, len(pids))
	samples := make([]procSample, 0, len(pids))
	for _, pid := range pids {
		b, err := p.FS.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			continue
		}
		st, err := procfs.ParsePIDStat(b)
		if err != nil || pid == 2 || st.PPID == 2 {
			continue // kernel threads
		}
		ticks := st.UTime + st.STime
		cur[pid] = procCPU{start: st.StartTime, ticks: ticks}
		s := procSample{pid: pid, st: st}
		if prev, ok := p.prev[pid]; ok && prev.start == st.StartTime && elapsed > 0 && ticks >= prev.ticks {
			s.util = float64(ticks-prev.ticks) / procfs.ClockTicks / elapsed / float64(ncpu)
			s.hasUtil = true
		}
		samples = append(samples, s)
	}
	p.prev, p.prevTime = cur, now

	top := SelectTop(samples, p.TopCPU, p.TopMemory)
	if len(top) == 0 {
		return nil, nil
	}
	p.mu.Lock()
	lookup := p.lookup
	p.mu.Unlock()
	pageSize := p.PageSize
	if pageSize <= 0 {
		pageSize = int64(os.Getpagesize())
	}
	users := p.userNames()

	var cpu, mem, virt, threads, fds []otlputil.Point
	for _, s := range top {
		base := "/proc/" + strconv.Itoa(s.pid)
		attrs := []*commonpb.KeyValue{otlputil.Int("process.pid", int64(s.pid)), otlputil.Str("process.executable.name", s.st.Comm)}
		exe, _ := p.FS.Readlink(base + "/exe")
		exe = strings.TrimSuffix(exe, " (deleted)")
		if exe != "" {
			attrs = append(attrs, otlputil.Str("process.executable.path", exe))
		}
		if b, err := p.FS.ReadFile(base + "/status"); err == nil {
			if uid, ok := procfs.ParseStatusUID(b); ok {
				owner := users[uid]
				if owner == "" {
					owner = strconv.Itoa(uid)
				}
				attrs = append(attrs, otlputil.Str("process.owner", owner))
			}
		}
		if lookup != nil {
			containerID := ""
			if b, err := p.FS.ReadFile(base + "/cgroup"); err == nil {
				_, containerID = procfs.ParseCgroup(b)
			}
			if id := lookup(s.pid, exe, containerID); id != "" {
				attrs = append(attrs, otlputil.Str("openlog.discovery.id", id))
			}
		}
		if s.hasUtil {
			cpu = append(cpu, otlputil.DoublePoint(s.util, attrs...))
		}
		mem = append(mem, otlputil.IntPoint(s.st.RSSPages*pageSize, attrs...))
		virt = append(virt, otlputil.IntPoint(int64(s.st.VSize), attrs...))
		threads = append(threads, otlputil.IntPoint(s.st.NumThreads, attrs...))
		if entries, err := p.FS.ReadDir(base + "/fd"); err == nil {
			fds = append(fds, otlputil.IntPoint(int64(len(entries)), attrs...))
		}
	}
	var out []*metricspb.Metric
	if len(cpu) > 0 {
		out = append(out, otlputil.Gauge("process.cpu.utilization", "1", now, cpu...))
	}
	out = append(out,
		otlputil.Sum("process.memory.usage", "By", false, time.Time{}, now, mem...),
		otlputil.Sum("process.memory.virtual", "By", false, time.Time{}, now, virt...),
		otlputil.Sum("process.threads", "{thread}", false, time.Time{}, now, threads...),
	)
	if len(fds) > 0 {
		out = append(out, otlputil.Sum("process.open_file_descriptors", "{count}", false, time.Time{}, now, fds...))
	}
	return out, nil
}

// userNames returns uid → name from /etc/passwd, re-read when the file changes.
func (p *ProcessTop) userNames() map[int]string {
	file := p.passwdPath
	if file == "" {
		file = "/etc/passwd"
	}
	fi, err := p.FS.Stat(file)
	if err != nil {
		return p.users
	}
	if p.users != nil && fi.ModTime().Equal(p.usersMod) {
		return p.users
	}
	b, err := p.FS.ReadFile(file)
	if err != nil {
		return p.users
	}
	users := map[int]string{}
	for line := range strings.Lines(string(b)) {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) < 3 || f[0] == "" || strings.HasPrefix(f[0], "#") {
			continue
		}
		if uid, err := strconv.Atoi(f[2]); err == nil {
			if _, dup := users[uid]; !dup {
				users[uid] = f[0]
			}
		}
	}
	p.users, p.usersMod = users, fi.ModTime()
	return users
}
