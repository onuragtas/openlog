//go:build !linux

package javaagent

import (
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// ListProcs returns the Java launcher processes (java, javaw) with their command line, start time and, best effort,
// JVM option variables and working directory (macOS: sysctl/libproc as root; Windows: the process environment block,
// readable by LocalSystem). Open files are not read: the loaded version comes from the switch history or the jar.
func ListProcs() ([]Proc, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, pr := range procs {
		if pr.Pid <= 0 {
			continue
		}
		name, err := pr.Name()
		if err != nil || !isJavaName(name) {
			continue
		}
		args, err := pr.CmdlineSlice()
		if err != nil || len(args) == 0 {
			continue
		}
		p := Proc{PID: int(pr.Pid), Args: args}
		p.Exe, _ = pr.Exe()
		if env, err := pr.Environ(); err == nil {
			p.Env = []string{}
			for _, kv := range env {
				if keepOptionVar(kv) {
					p.Env = append(p.Env, kv)
				}
			}
		}
		if ms, err := pr.CreateTime(); err == nil && ms > 0 {
			p.Start = time.UnixMilli(ms)
		}
		if cwd, err := pr.Cwd(); err == nil {
			p.Cwd = strings.TrimRight(cwd, "\x00")
		}
		out = append(out, p)
	}
	return out, nil
}
