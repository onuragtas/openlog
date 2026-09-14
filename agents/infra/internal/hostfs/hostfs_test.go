package hostfs

import (
	"runtime"
	"testing"
)

func TestPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	cases := []struct{ root, in, want string }{
		{"/", "/proc/stat", "/proc/stat"},
		{"", "proc/stat", "/proc/stat"},
		{"/host", "/proc/stat", "/host/proc/stat"},
		{"/host/", "/../../etc/passwd", "/host/etc/passwd"},
	}
	for _, c := range cases {
		if got := New(c.root).Path(c.in); got != c.want {
			t.Errorf("New(%q).Path(%q) = %q, want %q", c.root, c.in, got, c.want)
		}
	}
	if New("/").ProcSelf() != "/proc/self" || New("/host").ProcSelf() != "/proc/1" {
		t.Error("ProcSelf mismatch")
	}
}

// Runs on every OS: on Windows filepath.Clean("/") is `\`, which once made NativeOS false and sent every
// collector to procfs.
func TestHostRootOnEveryOS(t *testing.T) {
	for _, root := range []string{"", "/", "//", "/./"} {
		if f := New(root); !f.IsHostRoot() || f.Root() != "/" || f.NativeOS() != (runtime.GOOS != "linux") {
			t.Errorf("New(%q): root %q, IsHostRoot %v, NativeOS %v", root, f.Root(), f.IsHostRoot(), f.NativeOS())
		}
	}
	if New("/host").IsHostRoot() || New("/host").NativeOS() {
		t.Error(`New("/host") must not be the host root`)
	}
}
