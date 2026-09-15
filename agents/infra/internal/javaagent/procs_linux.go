//go:build linux

package javaagent

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
)

const (
	maxFDs      = 8192
	maxOpenJars = 256
)

// ListProcs returns the JVM candidates of /proc: processes whose executable or argv[0] is java or whose command line
// has -javaagent:, plus their JVM option variables (environ), open jar files (fd), start time and container. Reading
// environ, fd and cwd of other users' processes needs CAP_SYS_PTRACE (the unit grants it); without it those fields
// stay empty and the loaded version falls back to the switch history.
func ListProcs() ([]Proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	boot := bootTime()
	var out []Proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		base := "/proc/" + e.Name()
		raw, err := os.ReadFile(base + "/cmdline")
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		exe, _ := os.Readlink(base + "/exe")
		exe = strings.TrimSuffix(exe, " (deleted)")
		java := isJavaName(exe) || isJavaName(args[0])
		if !java && !bytes.Contains(raw, []byte("-javaagent:")) {
			continue
		}
		p := Proc{PID: pid, Exe: exe, Args: args}
		if b, err := os.ReadFile(base + "/environ"); err == nil {
			p.Env = []string{}
			for _, kv := range strings.Split(string(b), "\x00") {
				if keepOptionVar(kv) {
					p.Env = append(p.Env, kv)
				}
			}
		}
		if b, err := os.ReadFile(base + "/stat"); err == nil && !boot.IsZero() {
			if st, err := procfs.ParsePIDStat(b); err == nil {
				p.Start = boot.Add(time.Duration(st.StartTime) * time.Second / procfs.ClockTicks)
			}
		}
		if b, err := os.ReadFile(base + "/cgroup"); err == nil {
			_, cid := procfs.ParseCgroup(b)
			p.Container = cid != ""
		}
		p.Cwd, _ = os.Readlink(base + "/cwd")
		p.OpenJars = openJars(base + "/fd")
		out = append(out, p)
	}
	return out, nil
}

func openJars(dir string) []string {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	names, err := f.Readdirnames(maxFDs)
	if err != nil && len(names) == 0 {
		return nil
	}
	jars := []string{}
	for _, n := range names {
		t, err := os.Readlink(filepath.Join(dir, n))
		if err != nil || !strings.HasSuffix(strings.TrimSuffix(t, " (deleted)"), ".jar") {
			continue
		}
		if !contains(jars, t) {
			jars = append(jars, t)
		}
		if len(jars) >= maxOpenJars {
			break
		}
	}
	return jars
}

func bootTime() time.Time {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			if s, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil {
				return time.Unix(s, 0)
			}
		}
	}
	return time.Time{}
}
