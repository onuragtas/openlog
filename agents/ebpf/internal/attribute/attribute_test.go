package attribute

import (
	"os"
	"testing"
)

type fakeFS struct {
	files map[string]string
	links map[string]string
}

func (f fakeFS) ReadFile(name string) ([]byte, error) {
	if v, ok := f.files[name]; ok {
		return []byte(v), nil
	}
	return nil, os.ErrNotExist
}

func (f fakeFS) Readlink(name string) (string, error) {
	if v, ok := f.links[name]; ok {
		return v, nil
	}
	return "", os.ErrNotExist
}

const published = "# openlog discovered services\n" +
	"exe\t/usr/bin/redis-server\tredis\n" +
	"container\t" + cid + "\tpostgresql\n"

const cid = "3f2a1b9c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"

func TestDiscoveredServiceWinsOverTheBinaryName(t *testing.T) {
	fs := fakeFS{
		files: map[string]string{"/run/openlog-infra-agent/services": published},
		links: map[string]string{"/proc/42/exe": "/usr/bin/redis-server"},
	}
	if got := New(fs, "/run/openlog-infra-agent").Name(42); got != "redis" {
		t.Errorf("name = %q, want redis", got)
	}
}

func TestContainerMatchWhenTheBinaryIsUnknown(t *testing.T) {
	fs := fakeFS{
		files: map[string]string{
			"/run/openlog-infra-agent/services": published,
			"/proc/7/cgroup":                    "0::/system.slice/docker-" + cid + ".scope\n",
		},
		links: map[string]string{"/proc/7/exe": "/usr/lib/postgresql/16/bin/postgres"},
	}
	if got := New(fs, "/run/openlog-infra-agent").Name(7); got != "postgresql" {
		t.Errorf("name = %q, want postgresql", got)
	}
}

// Nothing discovered: the binary is still an answer, and a far better one than an empty flame graph.
func TestUnknownProcessFallsBackToItsBinary(t *testing.T) {
	fs := fakeFS{links: map[string]string{"/proc/9/exe": "/usr/sbin/sshd"}}
	if got := New(fs, "/run/openlog-infra-agent").Name(9); got != "sshd" {
		t.Errorf("name = %q, want sshd", got)
	}
}

// The infra agent may not be installed at all. That must cost names, not profiling.
func TestMissingServicesFileStillNames(t *testing.T) {
	fs := fakeFS{links: map[string]string{"/proc/9/exe": "/usr/sbin/nginx"}}
	r := New(fs, "")
	if got := r.Name(9); got != "nginx" {
		t.Errorf("name = %q, want nginx", got)
	}
}

// A kernel thread has no executable. One pseudo-service per kernel thread would be hundreds of names.
func TestKernelThreadsShareOneName(t *testing.T) {
	fs := fakeFS{files: map[string]string{"/proc/3/comm": "rcu_preempt\n"}}
	if got := New(fs, "").Name(3); got != KernelName {
		t.Errorf("name = %q, want %q", got, KernelName)
	}
}

// A process that vanished between the sample and the lookup has neither exe nor comm: it is named nothing
// rather than being guessed at.
func TestVanishedProcessIsNotNamed(t *testing.T) {
	if got := New(fakeFS{}, "").Name(1234); got != "" {
		t.Errorf("name = %q, want empty", got)
	}
}

// An upgraded binary keeps its path with a suffix; it is still the same service.
func TestDeletedBinaryKeepsItsService(t *testing.T) {
	fs := fakeFS{
		files: map[string]string{"/run/openlog-infra-agent/services": published},
		links: map[string]string{"/proc/42/exe": "/usr/bin/redis-server (deleted)"},
	}
	if got := New(fs, "/run/openlog-infra-agent").Name(42); got != "redis" {
		t.Errorf("name = %q, want redis", got)
	}
}

// Forward compatibility: an infra agent that publishes a kind this profiler does not know must not break
// the lines it does know.
func TestUnknownKindsAndJunkAreSkipped(t *testing.T) {
	fs := fakeFS{
		files: map[string]string{"/run/openlog-infra-agent/services": "" +
			"cgroup\t/some/future/thing\tmystery\n" +
			"exe\t/usr/bin/redis-server\tredis\n" +
			"exe\tmissing-a-field\n" +
			"\t\t\n" +
			"exe\t\tempty-key\n"},
		links: map[string]string{"/proc/42/exe": "/usr/bin/redis-server"},
	}
	r := New(fs, "/run/openlog-infra-agent")
	if got := r.Name(42); got != "redis" {
		t.Errorf("name = %q, want redis", got)
	}
	if len(r.byExe) != 1 {
		t.Errorf("parsed %d exe entries, want 1", len(r.byExe))
	}
}

func TestReadHostID(t *testing.T) {
	fs := fakeFS{files: map[string]string{"/run/openlog-infra-agent/host-id": "abc-123\n"}}
	if got := ReadHostID(fs, "/run/openlog-infra-agent"); got != "abc-123" {
		t.Errorf("host id = %q, want abc-123", got)
	}
	// Not an error: the profile is still correct, it is simply not linked to a host.
	if got := ReadHostID(fakeFS{}, "/run/openlog-infra-agent"); got != "" {
		t.Errorf("missing file returned %q", got)
	}
	if got := ReadHostID(fakeFS{}, ""); got != "" {
		t.Errorf("empty dir returned %q", got)
	}
}
