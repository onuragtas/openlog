//go:build darwin

package osinfo

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func read() Info {
	i := Info{ID: "macos", Name: "macOS"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/sw_vers").Output(); err == nil {
		name, version, build := ParseSwVers(string(out))
		if name != "" {
			i.Name = name
		}
		i.Version, i.Build = version, build
	}
	if i.Version == "" {
		i.Version, _ = unix.Sysctl("kern.osproductversion")
	}
	i.PrettyName = Pretty(i.Name, i.Version, i.Build)
	i.KernelRelease, _ = unix.Sysctl("kern.osrelease")
	v, _ := unix.Sysctl("kern.version")
	i.KernelVersion = strings.TrimSpace(v)
	return i
}

func bootTime() time.Time {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000).UTC()
}
