package hostfs

import "testing"

func TestPath(t *testing.T) {
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
