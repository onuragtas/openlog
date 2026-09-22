// Package attribute decides which name a sample is stored under (docs/contracts/ebpf-profiler.md §4).
//
// This is not cosmetics. profiles_local shards on cityHash64(tenant_id, service_name) and the read API
// requires a service, so a sample with no name is a sample nobody can open — and one host-wide name would
// put the whole fleet on a single Kafka partition. Every sample therefore gets the most specific name the
// machine can prove: the discovery rule the infra agent matched, else the binary, else `kernel`.
package attribute

import (
	"bufio"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// KernelName is the single name every kernel thread is attributed to. One pseudo-service per kernel thread
// would be hundreds of names nobody asked for.
const KernelName = "kernel"

// ServicesFile is published by the infra agent in its runtime directory, beside host-id.
const ServicesFile = "services"

// HostIDFile is published by the infra agent beside the service map; the language agents read the same
// file, so a profile lands on the host whose metrics are already there.
const HostIDFile = "host-id"

// ReadHostID returns the host id the infra agent published, or "" when it is not there. A missing id is
// not an error: the profile is still correct, it simply is not linked to a host.
func ReadHostID(fs FS, runtimeDir string) string {
	if runtimeDir == "" {
		return ""
	}
	b, err := fs.ReadFile(path.Join(runtimeDir, HostIDFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// FS is the process-information the resolver reads. Injected so tests never need a real /proc.
type FS interface {
	// ReadFile reads a regular file, e.g. /proc/<pid>/comm.
	ReadFile(name string) ([]byte, error)
	// Readlink resolves a symlink, e.g. /proc/<pid>/exe. A kernel thread has no target.
	Readlink(name string) (string, error)
}

// OS reads the real file system, rooted at Root ("" means "/", or the host root when the profiler runs in a
// container with the host mounted).
type OS struct{ Root string }

func (o OS) path(name string) string {
	if o.Root == "" {
		return name
	}
	return filepath.Join(o.Root, name)
}

func (o OS) ReadFile(name string) ([]byte, error) { return os.ReadFile(o.path(name)) }
func (o OS) Readlink(name string) (string, error) { return os.Readlink(o.path(name)) }

// Resolver names processes. The zero value is usable: with no published services every process falls back
// to its binary name, which is what happens when the infra agent is not installed.
type Resolver struct {
	fs          FS
	byExe       map[string]string
	byContainer map[string]string
}

// New builds a resolver, loading the infra agent's published services from runtimeDir when it is there.
// A missing or unreadable file is not an error: it costs better names, not profiling.
func New(fs FS, runtimeDir string) *Resolver {
	r := &Resolver{fs: fs, byExe: map[string]string{}, byContainer: map[string]string{}}
	if runtimeDir == "" {
		return r
	}
	b, err := fs.ReadFile(path.Join(runtimeDir, ServicesFile))
	if err != nil {
		return r
	}
	r.parse(string(b))
	return r
}

// parse reads the published table. An unrecognised kind or a malformed line is skipped rather than
// refused: an infra agent that learns to publish something new must not break a profiler that has not
// learned to read it.
func (r *Resolver) parse(s string) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[1] == "" || f[2] == "" {
			continue
		}
		switch f[0] {
		case "exe":
			r.byExe[f[1]] = f[2]
		case "container":
			r.byContainer[f[1]] = f[2]
		}
	}
}

// Name returns the service name for a pid.
func (r *Resolver) Name(pid int) string {
	proc := "/proc/" + itoa(pid)

	exe, err := r.fs.Readlink(proc + "/exe")
	if err != nil || exe == "" {
		// A kernel thread has no executable. So does a process that vanished between the sample and this
		// lookup, and so would one we could not read — but this component runs with the privileges to read
		// /proc, so the remaining case is overwhelmingly the kernel one.
		if r.exists(proc + "/comm") {
			return KernelName
		}
		return ""
	}
	// Deleted binaries keep their path with a suffix; the service is the same one.
	exe = strings.TrimSuffix(exe, " (deleted)")

	if id, ok := r.byExe[exe]; ok {
		return id
	}
	if c := r.containerID(proc); c != "" {
		if id, ok := r.byContainer[c]; ok {
			return id
		}
	}
	return path.Base(exe)
}

func (r *Resolver) exists(name string) bool {
	_, err := r.fs.ReadFile(name)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// containerID reads the container this pid belongs to from its cgroup, for both cgroup v1 and v2 layouts.
// The id is the last path segment that looks like one; anything else is not a container.
func (r *Resolver) containerID(proc string) string {
	b, err := r.fs.ReadFile(proc + "/cgroup")
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		for _, seg := range strings.Split(sc.Text(), "/") {
			seg = strings.TrimSuffix(seg, ".scope")
			seg = strings.TrimPrefix(seg, "docker-")
			seg = strings.TrimPrefix(seg, "crio-")
			seg = strings.TrimPrefix(seg, "cri-containerd-")
			if isHex64(seg) {
				return seg
			}
		}
	}
	return ""
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
