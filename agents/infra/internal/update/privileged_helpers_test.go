// POSIX ownership, modes, FIFOs and unix sockets; Windows has its own trust model (D-104).

//go:build !windows

package update

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSys is a Sys whose "root" is the test user and whose system files live below a temp dir.
type fakeSys struct {
	*Sys
	t    *testing.T
	mu   sync.Mutex
	runs []string
	env  map[string]string
	// tools available to LookPath.
	tools map[string]bool
	// fail makes Run fail for commands starting with this prefix.
	fail string
}

func newFakeSys(t *testing.T) *fakeSys {
	t.Helper()
	fs := &fakeSys{t: t, env: map[string]string{}, tools: map[string]bool{"usermod": true, "groupadd": true, "useradd": true, "systemctl": true}}
	root := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	fs.Sys = &Sys{
		Root: root, RootUID: uid, RootGID: gid,
		IsRoot: func() bool { return true },
		Lchown: func(p string, u, g int) error {
			if u == uid && g == gid {
				return os.Lchown(p, u, g)
			}
			return fmt.Errorf("fake lchown %s to %d:%d", p, u, g)
		},
		LookPath: func(name string) (string, error) {
			if fs.tools[name] {
				return "/usr/sbin/" + name, nil
			}
			return "", os.ErrNotExist
		},
		Getenv: func(k string) string { return fs.env[k] },
	}
	fs.Sys.Run = fs.run
	fs.write("/etc/passwd", fmt.Sprintf("root:x:0:0::/root:/bin/sh\n%s:x:%d:%d::/var/lib/openlog-infra-agent:/usr/sbin/nologin\n", AgentUser, uid, gid))
	fs.write("/etc/group", fmt.Sprintf("root:x:0:\n%s:x:%d:\ndocker:x:999:alice\n", AgentUser, gid))
	for _, d := range []string{"/etc/systemd/system", "/run/systemd/system", "/usr/bin"} {
		os.MkdirAll(fs.path(d), 0o755)
	}
	return fs
}

func (fs *fakeSys) write(p, content string) {
	fs.t.Helper()
	full := fs.path(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		fs.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		fs.t.Fatal(err)
	}
}

func (fs *fakeSys) read(p string) string {
	b, _ := os.ReadFile(fs.path(p))
	return string(b)
}

// run emulates the account and group tools on the fixture's /etc files.
func (fs *fakeSys) run(_ context.Context, name string, args ...string) error {
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	fs.mu.Lock()
	fs.runs = append(fs.runs, cmd)
	fs.mu.Unlock()
	if fs.fail != "" && strings.HasPrefix(cmd, fs.fail) {
		return fmt.Errorf("%s failed", cmd)
	}
	switch name {
	case "usermod": // -aG group user
		fs.addMember(args[1], args[2])
	case "groupadd":
		// A free gid, like groupadd: reusing the test process gid made fixture users whose primary gid happened to be
		// the same (e.g. 1001 for the CI runner) count as members of the new group.
		fs.write("/etc/group", fs.read("/etc/group")+fmt.Sprintf("%s:x:%d:\n", args[len(args)-1], fs.freeGID()))
	case "useradd":
		fs.write("/etc/passwd", fs.read("/etc/passwd")+fmt.Sprintf("%s:x:%d:%d::/x:/bin/false\n", args[len(args)-1], os.Getuid(), os.Getgid()))
	}
	return nil
}

// freeGID returns a gid used neither by a group nor as a primary gid in the fixture's /etc files.
func (fs *fakeSys) freeGID() int {
	used := map[string]bool{}
	for _, line := range strings.Split(fs.read("/etc/group"), "\n") {
		if f := strings.Split(line, ":"); len(f) > 2 {
			used[f[2]] = true
		}
	}
	for _, line := range strings.Split(fs.read("/etc/passwd"), "\n") {
		if f := strings.Split(line, ":"); len(f) > 3 {
			used[f[3]] = true
		}
	}
	gid := 60000
	for used[fmt.Sprint(gid)] {
		gid++
	}
	return gid
}

func (fs *fakeSys) addMember(group, user string) {
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(fs.read("/etc/group"), "\n"), "\n") {
		if strings.HasPrefix(line, group+":") {
			if strings.HasSuffix(line, ":") {
				line += user
			} else {
				line += "," + user
			}
		}
		out = append(out, line)
	}
	fs.write("/etc/group", strings.Join(out, "\n")+"\n")
}

func (fs *fakeSys) ran(prefix string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return slices.ContainsFunc(fs.runs, func(s string) bool { return strings.HasPrefix(s, prefix) })
}

// applyFixture is an install root with 0.9.0 current, a state dir and a signing key.
type applyFixture struct {
	t          *testing.T
	sys        *fakeSys
	key        testKey
	root       string
	stateDir   string
	now        time.Time
	starts     int
	selfTests  []string
	reconciles []string
	selfTestFn func(bin string) error
	enabled    bool
	noKeys     bool
}

func newApplyFixture(t *testing.T) *applyFixture {
	f := &applyFixture{
		t: t, sys: newFakeSys(t), key: newKey(t), root: t.TempDir(), stateDir: t.TempDir(),
		now: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC), enabled: true,
	}
	f.installTrusted("0.9.0", map[string]string{"rollback_floor": "0.8.0"})
	if err := SwitchCurrent(f.root, "0.9.0"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *applyFixture) installTrusted(v string, compat map[string]string) {
	installVersion(f.t, f.root, v, "exit 0", manifestFor(f.t, v, "https://example.com", []byte("x"), compat, nil))
}

// stageFiles is what the agent leaves in <state_dir>/updates/<v>/.
type stageFiles struct {
	dir      string // directory name below updates/
	archive  []byte
	manifest []byte
	sig      string
	state    State
}

// stage writes a correctly signed staged release, optionally modified by mutate, and the agent state.
func (f *applyFixture) stage(version string, compat map[string]string, mutate func(*stageFiles)) *stageFiles {
	f.t.Helper()
	archive := agentTarball(f.t, version, `[ "$1" = "-self-test" ] || exit 1`)
	m := manifestFor(f.t, version, "https://example.com", archive, compat, nil)
	sf := &stageFiles{
		dir: version, archive: archive, manifest: m, sig: sign(m, f.key),
		state: State{Staged: version, Candidate: version, Previous: "0.9.0", Action: ActionUpgrade, StagedAt: f.now, Status: StateRestarting},
	}
	if mutate != nil {
		mutate(sf)
	}
	dir := filepath.Join(f.stateDir, UpdatesDir, sf.dir)
	os.RemoveAll(filepath.Join(f.stateDir, UpdatesDir))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		f.t.Fatal(err)
	}
	for name, b := range map[string][]byte{ArchiveFile: sf.archive, ManifestFile: sf.manifest, SignatureFile: []byte(sf.sig)} {
		if b == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o640); err != nil {
			f.t.Fatal(err)
		}
	}
	f.saveAgent(sf.state)
	return sf
}

func (f *applyFixture) saveAgent(st State) {
	f.t.Helper()
	if err := SaveState(filepath.Join(f.stateDir, StateFile), st); err != nil {
		f.t.Fatal(err)
	}
}

func (f *applyFixture) agentState() State {
	st, _ := LoadState(filepath.Join(f.stateDir, StateFile))
	return st
}

// apply runs "-apply" as the binary of the current version.
func (f *applyFixture) apply() *ApplyStatus {
	f.t.Helper()
	f.starts++
	cur, _ := CurrentDir(f.root)
	var keys []ed25519.PublicKey
	if !f.noKeys {
		keys = []ed25519.PublicKey{f.key.pub}
	}
	return Apply(context.Background(), ApplyOptions{
		Sys: f.sys.Sys, Install: Install{Method: MethodTarball, InstallRoot: f.root, VersionDir: cur},
		StateDir: f.stateDir, Version: cur, Trusted: keys, UpdatesEnabled: f.enabled,
		InvocationID: fmt.Sprintf("invocation-%d", f.starts), OS: "linux", Arch: "amd64",
		Now: func() time.Time { return f.now },
		SelfTest: func(_ context.Context, bin string, uid, gid int) error {
			if uid != os.Getuid() || gid != os.Getgid() {
				f.t.Errorf("self-test as %d:%d", uid, gid)
			}
			f.selfTests = append(f.selfTests, bin)
			if f.selfTestFn != nil {
				return f.selfTestFn(bin)
			}
			return RunSelfTest(context.Background(), bin, "", 5*time.Second)
		},
		Reconcile: func(_ context.Context, dir string) error {
			f.reconciles = append(f.reconciles, dir)
			return nil
		},
	})
}

func (f *applyFixture) current() string {
	d, _ := CurrentDir(f.root)
	return d
}

func (f *applyFixture) versions() []string {
	entries, _ := os.ReadDir(filepath.Join(f.root, "versions"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
