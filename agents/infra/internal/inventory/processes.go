package inventory

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/mask"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
)

const (
	maxCmdline = 1024
	maxPIDs    = 20
)

// ProcessInstance is a single process as seen by the collector.
type ProcessInstance struct {
	PID         int
	PPID        int
	Exe         string // empty when unreadable
	Comm        string
	Cmdline     string // masked and truncated
	UID         int
	HasUID      bool
	StartTime   time.Time
	SystemdUnit string
	ContainerID string
}

// Key returns the process inventory key: the exe path, or comm:<name>.
func (p ProcessInstance) Key() string {
	if p.Exe != "" {
		return p.Exe
	}
	return "comm:" + p.Comm
}

// ExeBasename returns the basename of the executable, falling back to argv[0]
// and comm when the exe link is unreadable.
func (p ProcessInstance) ExeBasename() string {
	if p.Exe != "" {
		return baseName(p.Exe)
	}
	if argv0, _, _ := strings.Cut(p.Cmdline, " "); argv0 != "" && !strings.HasSuffix(argv0, ":") {
		return baseName(argv0)
	}
	return p.Comm
}

// Names returns every name the process is known by: the exe basename, the
// argv[0] basename and comm. Multi-call binaries and symlinked executables
// (e.g. Debian's redis-server → redis-check-rdb) need all of them.
func (p ProcessInstance) Names() []string {
	names := make([]string, 0, 3)
	add := func(n string) {
		if n != "" && n != "." && n != "/" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if p.Exe != "" {
		add(baseName(p.Exe))
	}
	if argv0, _, _ := strings.Cut(p.Cmdline, " "); argv0 != "" && !strings.HasSuffix(argv0, ":") {
		add(baseName(argv0))
	}
	add(p.Comm)
	return names
}

// Command returns the basename of argv[0] (the name a process was started as,
// e.g. "redis-server" when the executable is a symlink to redis-check-rdb).
// A trailing ':' of setproctitle-style titles ("nginx: master process") and a
// login shell's leading '-' are removed; comm is the fallback.
func (p ProcessInstance) Command() string {
	argv0, _, _ := strings.Cut(strings.TrimSpace(p.Cmdline), " ")
	argv0 = strings.TrimLeft(strings.TrimSuffix(argv0, ":"), "-")
	if b := baseName(argv0); argv0 != "" && b != "." && b != "/" {
		return b
	}
	return p.Comm
}

// Process is the body of a "process" item: all instances of one executable.
type Process struct {
	Exe         string `json:"exe"`
	Name        string `json:"name"`
	Command     string `json:"command,omitempty"`
	Cmdline     string `json:"cmdline"`
	Count       int    `json:"count"`
	PIDs        []int  `json:"pids"`
	UIDs        []int  `json:"uids"`
	StartTime   string `json:"start_time,omitempty"`
	SystemdUnit string `json:"systemd_unit,omitempty"`
	ContainerID string `json:"container_id,omitempty"`

	key string
}

// Key returns the inventory key.
func (p Process) Key() string { return p.key }

func collectInstances(fs *hostfs.FS, boot time.Time) ([]ProcessInstance, error) {
	pids, err := procfs.ListPIDs(fs)
	if err != nil {
		return nil, err
	}
	out := make([]ProcessInstance, 0, len(pids))
	for _, pid := range pids {
		base := "/proc/" + strconv.Itoa(pid)
		b, err := fs.ReadFile(base + "/stat")
		if err != nil {
			continue
		}
		st, err := procfs.ParsePIDStat(b)
		if err != nil || st.PID == 2 || st.PPID == 2 {
			continue // kernel threads
		}
		inst := ProcessInstance{PID: pid, PPID: st.PPID, Comm: st.Comm}
		if exe, err := fs.Readlink(base + "/exe"); err == nil {
			inst.Exe = strings.TrimSuffix(exe, " (deleted)")
		}
		if b, err := fs.ReadFile(base + "/cmdline"); err == nil {
			inst.Cmdline = mask.Truncate(mask.Cmdline(inst.Exe, procfs.ParseCmdline(b)), maxCmdline)
		}
		if inst.Exe == "" && inst.Cmdline == "" {
			continue // zombie or kernel thread without a user-space image
		}
		if b, err := fs.ReadFile(base + "/status"); err == nil {
			inst.UID, inst.HasUID = procfs.ParseStatusUID(b)
		}
		if !boot.IsZero() {
			inst.StartTime = boot.Add(time.Duration(st.StartTime) * time.Second / procfs.ClockTicks)
		}
		if b, err := fs.ReadFile(base + "/cgroup"); err == nil {
			inst.SystemdUnit, inst.ContainerID = procfs.ParseCgroup(b)
		}
		out = append(out, inst)
	}
	return out, nil
}

// GroupProcesses groups instances by executable into process items.
func GroupProcesses(instances []ProcessInstance) []Process {
	groups := map[string]*Process{}
	uids := map[string]map[int]bool{}
	var starts = map[string]time.Time{}
	var order []string
	sorted := append([]ProcessInstance(nil), instances...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PID < sorted[j].PID })
	for _, in := range sorted {
		k := in.Key()
		g, ok := groups[k]
		if !ok {
			g = &Process{key: k, Exe: in.Exe, Name: in.Comm, Command: in.Command(), Cmdline: in.Cmdline, PIDs: []int{}, UIDs: []int{}}
			groups[k] = g
			uids[k] = map[int]bool{}
			order = append(order, k)
		}
		g.Count++
		if len(g.PIDs) < maxPIDs {
			g.PIDs = append(g.PIDs, in.PID)
		}
		if in.HasUID && !uids[k][in.UID] {
			uids[k][in.UID] = true
			g.UIDs = append(g.UIDs, in.UID)
		}
		if !in.StartTime.IsZero() && (starts[k].IsZero() || in.StartTime.Before(starts[k])) {
			starts[k] = in.StartTime
		}
		if g.SystemdUnit == "" {
			g.SystemdUnit = in.SystemdUnit
		}
		if g.ContainerID == "" {
			g.ContainerID = in.ContainerID
		}
	}
	out := make([]Process, 0, len(order))
	for _, k := range order {
		g := groups[k]
		sort.Ints(g.UIDs)
		if t := starts[k]; !t.IsZero() {
			g.StartTime = t.UTC().Format(time.RFC3339)
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}
