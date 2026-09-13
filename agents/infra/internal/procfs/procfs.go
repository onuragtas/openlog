// Package procfs contains pure parsers for Linux procfs files plus small
// helpers that read them through a hostfs.FS. Parsers take bytes so that they
// are testable on any OS.
package procfs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
)

// ClockTicks is USER_HZ. It is 100 on every mainstream Linux architecture.
const ClockTicks = 100

// CPUTimes are cumulative CPU seconds summed over all CPUs.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal float64
}

// Total returns the sum of all modes.
func (c CPUTimes) Total() float64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

// Stat is the parsed content of /proc/stat.
type Stat struct {
	CPU          CPUTimes
	LogicalCPUs  int
	BootTime     time.Time
	ProcsRunning int
}

// ParseStat parses /proc/stat.
func ParseStat(data []byte) (Stat, error) {
	var st Stat
	found := false
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "cpu":
			vals := make([]float64, 8)
			for i := 0; i < 8 && i+1 < len(f); i++ {
				v, err := strconv.ParseUint(f[i+1], 10, 64)
				if err != nil {
					return st, fmt.Errorf("procfs: bad cpu line: %w", err)
				}
				vals[i] = float64(v) / ClockTicks
			}
			st.CPU = CPUTimes{vals[0], vals[1], vals[2], vals[3], vals[4], vals[5], vals[6], vals[7]}
			found = true
		case strings.HasPrefix(f[0], "cpu"):
			st.LogicalCPUs++
		case f[0] == "btime" && len(f) > 1:
			if v, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				st.BootTime = time.Unix(v, 0)
			}
		case f[0] == "procs_running" && len(f) > 1:
			st.ProcsRunning, _ = strconv.Atoi(f[1])
		}
	}
	if !found {
		return st, errors.New("procfs: no cpu line in stat")
	}
	return st, nil
}

// LoadAvg holds the 1/5/15 minute load averages.
type LoadAvg struct{ Load1, Load5, Load15 float64 }

// ParseLoadAvg parses /proc/loadavg.
func ParseLoadAvg(data []byte) (LoadAvg, error) {
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return LoadAvg{}, errors.New("procfs: short loadavg")
	}
	var v [3]float64
	for i := range v {
		x, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return LoadAvg{}, fmt.Errorf("procfs: bad loadavg: %w", err)
		}
		v[i] = x
	}
	return LoadAvg{v[0], v[1], v[2]}, nil
}

// ParseMeminfo parses /proc/meminfo into a map; "kB" values are converted to bytes.
func ParseMeminfo(data []byte) map[string]uint64 {
	out := map[string]uint64{}
	for line := range strings.Lines(string(data)) {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			v *= 1024
		}
		out[strings.TrimSpace(name)] = v
	}
	return out
}

// ParseUptime parses /proc/uptime and returns seconds since boot.
func ParseUptime(data []byte) (float64, error) {
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0, errors.New("procfs: empty uptime")
	}
	return strconv.ParseFloat(f[0], 64)
}

// Mount is one line of mountinfo.
type Mount struct {
	MountPoint string
	Device     string
	FSType     string
	Options    string
	Root       string
	MajorMinor string // st_dev of the file system, e.g. "8:1"
}

// ParseMountinfo parses /proc/<pid>/mountinfo.
func ParseMountinfo(data []byte) []Mount {
	var out []Mount
	for line := range strings.Lines(string(data)) {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		a := strings.Fields(pre)
		b := strings.Fields(post)
		if len(a) < 6 || len(b) < 2 {
			continue
		}
		m := Mount{
			MajorMinor: a[2],
			Root:       unescapeOctal(a[3]),
			MountPoint: unescapeOctal(a[4]),
			Options:    a[5],
			FSType:     b[0],
			Device:     unescapeOctal(b[1]),
		}
		if len(b) > 2 && b[2] != "" {
			m.Options += "," + b[2]
		}
		out = append(out, m)
	}
	return out
}

// unescapeOctal decodes \040-style escapes used by the kernel for spaces etc.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// DiskStat is the subset of /proc/diskstats the agent reports.
type DiskStat struct {
	Device                          string
	ReadsCompleted, SectorsRead     uint64
	WritesCompleted, SectorsWritten uint64
}

// SectorSize is the fixed unit of /proc/diskstats sector counters.
const SectorSize = 512

// ParseDiskstats parses /proc/diskstats.
func ParseDiskstats(data []byte) []DiskStat {
	var out []DiskStat
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		d := DiskStat{Device: f[2]}
		d.ReadsCompleted, _ = strconv.ParseUint(f[3], 10, 64)
		d.SectorsRead, _ = strconv.ParseUint(f[5], 10, 64)
		d.WritesCompleted, _ = strconv.ParseUint(f[7], 10, 64)
		d.SectorsWritten, _ = strconv.ParseUint(f[9], 10, 64)
		out = append(out, d)
	}
	return out
}

// NetDevStat holds per-interface counters from /proc/net/dev.
type NetDevStat struct {
	Interface                               string
	RxBytes, RxPackets, RxErrors, RxDropped uint64
	TxBytes, TxPackets, TxErrors, TxDropped uint64
}

// ParseNetDev parses /proc/net/dev.
func ParseNetDev(data []byte) []NetDevStat {
	var out []NetDevStat
	for line := range strings.Lines(string(data)) {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		u := func(i int) uint64 { v, _ := strconv.ParseUint(f[i], 10, 64); return v }
		out = append(out, NetDevStat{
			Interface: strings.TrimSpace(name),
			RxBytes:   u(0), RxPackets: u(1), RxErrors: u(2), RxDropped: u(3),
			TxBytes: u(8), TxPackets: u(9), TxErrors: u(10), TxDropped: u(11),
		})
	}
	return out
}

// PIDStat is the subset of /proc/<pid>/stat the agent needs.
type PIDStat struct {
	PID        int
	Comm       string
	State      byte
	PPID       int
	UTime      uint64 // clock ticks
	STime      uint64 // clock ticks
	NumThreads int64
	StartTime  uint64 // clock ticks after boot
	VSize      uint64 // bytes
	RSSPages   int64  // resident set size in pages
}

// ParsePIDStat parses /proc/<pid>/stat. The comm field may contain spaces and
// parentheses, so parsing is anchored on the last ')'.
func ParsePIDStat(data []byte) (PIDStat, error) {
	s := string(data)
	open := strings.IndexByte(s, '(')
	closeIdx := strings.LastIndexByte(s, ')')
	if open < 0 || closeIdx < open {
		return PIDStat{}, errors.New("procfs: malformed pid stat")
	}
	var st PIDStat
	st.PID, _ = strconv.Atoi(strings.TrimSpace(s[:open]))
	st.Comm = s[open+1 : closeIdx]
	f := strings.Fields(s[closeIdx+1:])
	if len(f) < 20 {
		return PIDStat{}, errors.New("procfs: short pid stat")
	}
	st.State = f[0][0]
	st.PPID, _ = strconv.Atoi(f[1])
	st.UTime, _ = strconv.ParseUint(f[11], 10, 64)
	st.STime, _ = strconv.ParseUint(f[12], 10, 64)
	st.NumThreads, _ = strconv.ParseInt(f[17], 10, 64)
	st.StartTime, _ = strconv.ParseUint(f[19], 10, 64)
	if len(f) >= 22 {
		st.VSize, _ = strconv.ParseUint(f[20], 10, 64)
		st.RSSPages, _ = strconv.ParseInt(f[21], 10, 64)
	}
	return st, nil
}

// ProcessStatus maps a stat state character to the process.status attribute value.
func ProcessStatus(state byte) string {
	switch state {
	case 'R':
		return "running"
	case 'S':
		return "sleeping"
	case 'D':
		return "disk_sleep"
	case 'T', 't':
		return "stopped"
	case 'Z':
		return "zombie"
	case 'I':
		return "idle"
	default:
		return "other"
	}
}

// ParseStatusUID returns the real UID from /proc/<pid>/status.
func ParseStatusUID(data []byte) (int, bool) {
	for line := range strings.Lines(string(data)) {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				if v, err := strconv.Atoi(f[0]); err == nil {
					return v, true
				}
			}
		}
	}
	return 0, false
}

// ParseCmdline converts NUL-separated argv into a space-joined string.
func ParseCmdline(data []byte) string {
	data = bytes.TrimRight(data, "\x00")
	return strings.TrimSpace(string(bytes.ReplaceAll(data, []byte{0}, []byte{' '})))
}

// ListPIDs returns the numeric entries of /proc in ascending order.
func ListPIDs(fs *hostfs.FS) ([]int, error) {
	entries, err := fs.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}

// ParseKeyValueFile parses os-release style KEY=VALUE files (quotes removed).
func ParseKeyValueFile(data []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}
