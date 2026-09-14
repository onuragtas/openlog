package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	type setup struct {
		dockerenv, containerenv bool
		cgroup                  string
		env                     map[string]string
		dpkgList                string
		noCurrent               bool
		exeOutside              bool
		readOnly                bool
		disabled, noKeys        bool
	}
	cases := []struct {
		name       string
		s          setup
		method     string
		capable    bool
		reason     string
		pkgVersion string
	}{
		{name: "tarball", method: MethodTarball, capable: true},
		{name: "container via /.dockerenv", s: setup{dockerenv: true}, method: MethodContainer, reason: "container"},
		{name: "container via podman", s: setup{containerenv: true}, method: MethodContainer},
		{name: "container via cgroup", s: setup{cgroup: "0::/kubepods.slice/kubepods-pod1.slice/cri-containerd-abc.scope\n"}, method: MethodContainer},
		{name: "container via env", s: setup{env: map[string]string{ContainerEnv: "1"}}, method: MethodContainer},
		{name: "container detection disabled by env", s: setup{dockerenv: true, env: map[string]string{ContainerEnv: "0"}}, method: MethodTarball, capable: true},
		{name: "plain cgroup v2 is not a container", s: setup{cgroup: "0::/system.slice/openlog-infra-agent.service\n"}, method: MethodTarball, capable: true},
		{name: "dev: binary outside versions", s: setup{exeOutside: true}, method: MethodDev, reason: "not under"},
		{name: "deb", s: setup{dpkgList: "/opt\n{root}\n{root}/versions/0.9.0/openlog-infra-agent\n"}, method: MethodDeb, capable: true, pkgVersion: "0.9.0-1"},
		{name: "dpkg list for other paths", s: setup{dpkgList: "/usr/bin/openlog-infra-agent\n"}, method: MethodTarball, capable: true},
		{name: "updates disabled", s: setup{disabled: true}, method: MethodTarball, reason: "disabled"},
		{name: "no trusted keys", s: setup{noKeys: true}, method: MethodTarball, reason: "no trusted release keys"},
		{name: "legacy layout without current", s: setup{noCurrent: true}, method: MethodTarball, reason: "no current symlink"},
		{name: "read-only install root", s: setup{readOnly: true}, method: MethodTarball, reason: "not writable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sys := t.TempDir()
			root := t.TempDir()
			installVersion(t, root, "0.9.0", "exit 0", nil)
			if !c.s.noCurrent {
				if err := SwitchCurrent(root, "0.9.0"); err != nil {
					t.Fatal(err)
				}
			}
			write := func(p, content string) {
				full := filepath.Join(sys, p)
				os.MkdirAll(filepath.Dir(full), 0o755)
				if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if c.s.dockerenv {
				write("/.dockerenv", "")
			}
			if c.s.containerenv {
				write("/run/.containerenv", "")
			}
			if c.s.cgroup != "" {
				write("/proc/self/cgroup", c.s.cgroup)
			}
			if c.s.dpkgList != "" {
				realRoot, _ := filepath.EvalSymlinks(root)
				write("/var/lib/dpkg/info/openlog-infra-agent.list", strings.ReplaceAll(c.s.dpkgList, "{root}", realRoot))
				write("/var/lib/dpkg/status", "Package: openlog-infra-agent\nStatus: install ok installed\nVersion: 0.9.0-1\nArchitecture: amd64\n\n")
			}
			if c.s.readOnly {
				if os.Geteuid() == 0 {
					t.Skip("root ignores permissions")
				}
				os.Chmod(root, 0o555)
				t.Cleanup(func() { os.Chmod(root, 0o755) })
			}
			exe := filepath.Join(root, "current", BinaryName) // resolved through the symlink
			if c.s.noCurrent {
				exe = filepath.Join(root, "versions", "0.9.0", BinaryName)
			}
			if c.s.exeOutside {
				exe = filepath.Join(sys, "usr/bin/openlog-infra-agent")
				write("/usr/bin/openlog-infra-agent", "")
			}
			in := Detect(Env{
				Root: sys, Getenv: func(k string) string { return c.s.env[k] }, Executable: exe, InstallRoot: root,
				UpdatesEnabled: !c.s.disabled, HaveTrustedKeys: !c.s.noKeys,
			})
			if in.Method != c.method || in.Capable != c.capable {
				t.Fatalf("got %+v, want method %s capable %v", in, c.method, c.capable)
			}
			if c.reason != "" && !strings.Contains(in.Reason, c.reason) {
				t.Errorf("reason %q, want %q", in.Reason, c.reason)
			}
			if c.capable && in.VersionDir != "0.9.0" {
				t.Errorf("version dir %q", in.VersionDir)
			}
			if in.PackageVersion != c.pkgVersion {
				t.Errorf("package version %q", in.PackageVersion)
			}
		})
	}
}

func TestVersionDirOf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	for _, c := range []struct{ exe, want string }{
		{"/opt/o/versions/0.9.0/openlog-infra-agent", "0.9.0"},
		{"/opt/o/versions/0.9.0/other", ""},
		{"/opt/o/versions/.0.9.0.extract/openlog-infra-agent", ""},
		{"/opt/o/versions/0.9.0/bin/openlog-infra-agent", ""},
		{"/opt/o/current/openlog-infra-agent", ""},
		{"/usr/bin/openlog-infra-agent", ""},
	} {
		if got := versionDirOf("/opt/o", c.exe); got != c.want {
			t.Errorf("versionDirOf(%s) = %q, want %q", c.exe, got, c.want)
		}
	}
}

func TestPackageUpstreamVersion(t *testing.T) {
	for in, want := range map[string]string{
		"0.4.0-1": "0.4.0", "1:0.4.0-1": "0.4.0", "0.5.0~beta.1-1": "0.5.0-beta.1", "0.4.0-1.el9": "0.4.0", "0.4.0": "0.4.0",
	} {
		if got := packageUpstreamVersion(in); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}
