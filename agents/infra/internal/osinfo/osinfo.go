// Package osinfo describes the operating system of a macOS or Windows host from native sources
// (sw_vers and sysctl on macOS, the registry on Windows). Linux hosts use /etc/os-release and procfs
// in the resource and inventory packages instead.
package osinfo

import (
	"strings"
	"sync"
	"time"
)

// Info is the operating system of the host.
type Info struct {
	// ID is the lowercase OS id used as os.name: "macos" or "windows".
	ID string
	// Name is the product name, e.g. "macOS" or "Windows Server 2022 Datacenter".
	Name string
	// Version is os.version, e.g. "15.5" or "10.0.20348".
	Version string
	// Build is the build identifier, e.g. "24F74" or "20348.2340".
	Build string
	// PrettyName is os.description, e.g. "macOS 15.5 (24F74)".
	PrettyName    string
	KernelRelease string
	KernelVersion string
	// BootTime is zero when unknown.
	BootTime time.Time
}

var (
	once   sync.Once
	cached Info
)

// Get returns the host's OS information; the product/version part is read once per process.
func Get() Info {
	once.Do(func() { cached = read() })
	i := cached
	i.BootTime = bootTime()
	return i
}

// ParseSwVers parses the output of sw_vers ("ProductName:\tmacOS" lines).
func ParseSwVers(out string) (name, version, build string) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "ProductName":
			name = v
		case "ProductVersion":
			version = v
		case "BuildVersion":
			build = v
		}
	}
	return name, version, build
}

// Pretty joins name, version and build: "macOS 15.5 (24F74)".
func Pretty(name, version, build string) string {
	s := strings.TrimSpace(name + " " + version)
	if build != "" {
		s += " (" + build + ")"
	}
	return s
}
