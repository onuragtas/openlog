//go:build windows

package osinfo

import (
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func read() Info {
	i := Info{ID: "windows", Name: "Windows"}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return i
	}
	defer k.Close()
	str := func(name string) string { v, _, _ := k.GetStringValue(name); return v }
	build := str("CurrentBuildNumber")
	ubr, _, _ := k.GetIntegerValue("UBR")
	major, _, errMajor := k.GetIntegerValue("CurrentMajorVersionNumber")
	minor, _, _ := k.GetIntegerValue("CurrentMinorVersionNumber")
	ver := "10.0"
	if errMajor == nil {
		ver = strconv.FormatUint(major, 10) + "." + strconv.FormatUint(minor, 10)
	}
	if name := str("ProductName"); name != "" {
		i.Name = name
	}
	// Windows 11 still reports "Windows 10" in ProductName; build 22000 is the first Windows 11 build.
	if n, _ := strconv.Atoi(build); n >= 22000 && strings.HasPrefix(i.Name, "Windows 10") {
		i.Name = "Windows 11" + strings.TrimPrefix(i.Name, "Windows 10")
	}
	if build != "" {
		i.Version = ver + "." + build
		i.Build = build
		if ubr > 0 {
			i.Build += "." + strconv.FormatUint(ubr, 10)
		}
		i.KernelRelease = ver + "." + i.Build
	}
	display := str("DisplayVersion")
	if display == "" {
		display = str("ReleaseId")
	}
	i.PrettyName = strings.TrimSpace(i.Name + " " + display)
	if i.Build != "" {
		i.PrettyName += " (build " + i.Build + ")"
	}
	i.KernelVersion = i.KernelRelease
	return i
}

func bootTime() time.Time {
	up := time.Duration(windows.DurationSinceBoot())
	if up <= 0 {
		return time.Time{}
	}
	return time.Now().Add(-up).UTC().Truncate(time.Second)
}
