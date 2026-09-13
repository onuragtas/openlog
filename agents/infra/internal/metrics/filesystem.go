package metrics

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// ExcludedFSTypes are filesystem types skipped by default (semantic-conventions §2).
var ExcludedFSTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
	"cgroup": true, "cgroup2": true, "overlay": true, "squashfs": true, "securityfs": true,
	"debugfs": true, "tracefs": true, "pstore": true, "bpf": true, "mqueue": true,
	"hugetlbfs": true, "configfs": true, "fusectl": true, "autofs": true, "binfmt_misc": true,
	"nsfs": true, "rpc_pipefs": true, "ramfs": true,
}

// statfsTimeout bounds a statfs call; hung network mounts must not stall collection.
const statfsTimeout = 2 * time.Second

// Filesystem emits system.filesystem.usage and utilization.
type Filesystem struct {
	FS *hostfs.FS
	// Statfs is overridable in tests; defaults to FS.Statfs.
	Statfs func(mountpoint string) (hostfs.Statfs, error)
	// AgentDirs are the agent's own state/buffer directories. Such a mount is
	// skipped when it only duplicates a device that is already reported.
	AgentDirs []string
}

func (f *Filesystem) Name() string { return "filesystem" }

// FilesystemMounts filters mountinfo entries to the ones reported as metrics:
// excluded types are skipped and later mounts on the same point shadow earlier ones.
// isDir reports whether a mount point is a directory; bind-mounted files
// (Docker's /etc/hosts, /etc/hostname, /etc/resolv.conf) are not file systems.
// A nil isDir keeps every mount point. Mounts on agentDirs are dropped when
// another reported mount has the same device.
func FilesystemMounts(mounts []procfs.Mount, isDir func(string) bool, agentDirs ...string) []procfs.Mount {
	idx := map[string]int{}
	var out []procfs.Mount
	for _, m := range mounts {
		if ExcludedFSTypes[m.FSType] {
			continue
		}
		if i, ok := idx[m.MountPoint]; ok {
			out[i] = m
			continue
		}
		idx[m.MountPoint] = len(out)
		out = append(out, m)
	}
	kept := out[:0]
	for _, m := range out {
		if isDir != nil && !isDir(m.MountPoint) {
			continue
		}
		kept = append(kept, m)
	}
	out = kept
	isAgentDir := func(mp string) bool {
		for _, d := range agentDirs {
			if d != "" && filepath.Clean(d) == mp {
				return true
			}
		}
		return false
	}
	final := make([]procfs.Mount, 0, len(out))
	for _, m := range out {
		if isAgentDir(m.MountPoint) {
			dup := false
			for _, o := range out {
				if o.MountPoint != m.MountPoint && !isAgentDir(o.MountPoint) && o.Device == m.Device && o.MajorMinor == m.MajorMinor {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
		}
		final = append(final, m)
	}
	return final
}

func (f *Filesystem) Collect(now time.Time) ([]*metricspb.Metric, error) {
	b, err := f.FS.ReadFile(f.FS.ProcSelf() + "/mountinfo")
	if err != nil {
		return nil, err
	}
	statfs := f.Statfs
	if statfs == nil {
		statfs = f.FS.Statfs
	}
	var usage, util []otlputil.Point
	var errs []error
	isDir := func(mp string) bool {
		fi, err := f.FS.Stat(mp)
		return err != nil || fi.IsDir() // unknown (e.g. permission) → keep
	}
	for _, m := range FilesystemMounts(procfs.ParseMountinfo(b), isDir, f.AgentDirs...) {
		st, err := statfsWithTimeout(statfs, m.MountPoint)
		if err != nil {
			if err == hostfs.ErrUnsupported {
				return nil, err
			}
			errs = append(errs, fmt.Errorf("statfs %s: %w", m.MountPoint, err))
			continue
		}
		if st.Blocks == 0 {
			continue
		}
		u, fr, r := FilesystemUsage(st)
		base := []otlputilKV{
			{"system.device", m.Device}, {"system.filesystem.mountpoint", m.MountPoint}, {"system.filesystem.type", m.FSType},
		}
		for _, s := range []struct {
			name string
			v    uint64
		}{{"used", u}, {"free", fr}, {"reserved", r}} {
			usage = append(usage, otlputil.IntPoint(int64(s.v), attrs(base, otlputilKV{"system.filesystem.state", s.name})...))
		}
		if u+fr > 0 {
			util = append(util, otlputil.DoublePoint(float64(u)/float64(u+fr), attrs(base)...))
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

// FilesystemUsage splits statfs results into used, free (available to users)
// and reserved (free only for root) bytes. utilization = used/(used+free).
func FilesystemUsage(st hostfs.Statfs) (used, free, reserved uint64) {
	bs := st.BlockSize
	free = st.BlocksAvail * bs
	if st.BlocksFree > st.BlocksAvail {
		reserved = (st.BlocksFree - st.BlocksAvail) * bs
	}
	if st.Blocks > st.BlocksFree {
		used = (st.Blocks - st.BlocksFree) * bs
	}
	return used, free, reserved
}

type otlputilKV struct{ k, v string }

func attrs(base []otlputilKV, extra ...otlputilKV) []*commonKV {
	out := make([]*commonKV, 0, len(base)+len(extra))
	for _, kv := range append(append([]otlputilKV{}, base...), extra...) {
		out = append(out, otlputil.Str(kv.k, kv.v))
	}
	return out
}

func statfsWithTimeout(fn func(string) (hostfs.Statfs, error), mp string) (hostfs.Statfs, error) {
	type res struct {
		st  hostfs.Statfs
		err error
	}
	ch := make(chan res, 1)
	go func() {
		st, err := fn(mp)
		ch <- res{st, err}
	}()
	select {
	case r := <-ch:
		return r.st, r.err
	case <-time.After(statfsTimeout):
		return hostfs.Statfs{}, fmt.Errorf("timed out after %s", statfsTimeout)
	}
}
