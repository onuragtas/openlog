package osinfo

import (
	"runtime"
	"testing"
)

func TestParseSwVers(t *testing.T) {
	name, version, build := ParseSwVers("ProductName:\t\tmacOS\nProductVersion:\t\t15.5\nBuildVersion:\t\t24F74\n")
	if name != "macOS" || version != "15.5" || build != "24F74" {
		t.Errorf("got %q %q %q", name, version, build)
	}
	if got := Pretty(name, version, build); got != "macOS 15.5 (24F74)" {
		t.Errorf("Pretty = %q", got)
	}
	if got := Pretty("macOS", "", ""); got != "macOS" {
		t.Errorf("Pretty without version = %q", got)
	}
}

func TestGet(t *testing.T) {
	i := Get()
	switch runtime.GOOS {
	case "darwin":
		if i.ID != "macos" || i.Version == "" || i.KernelRelease == "" || i.BootTime.IsZero() {
			t.Errorf("darwin info = %+v", i)
		}
	case "windows":
		if i.ID != "windows" || i.Version == "" || i.BootTime.IsZero() {
			t.Errorf("windows info = %+v", i)
		}
	default:
		if i.ID != "" {
			t.Errorf("linux info = %+v", i)
		}
	}
}
