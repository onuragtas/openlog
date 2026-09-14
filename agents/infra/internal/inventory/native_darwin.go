//go:build darwin

package inventory

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/onuragtas/openlog/agents/infra/internal/plist"
)

// Homebrew prefixes (Apple silicon, Intel).
var homebrewPrefixes = []string{"/opt/homebrew", "/usr/local"}

// maxReceipts and maxApps bound the package inventory.
const (
	maxReceipts = 5000
	maxApps     = 2000
	execTimeout = 20 * time.Second
)

func runTool(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}

func nativeCPU() *CPUInfo {
	logical, physical := nativeCores()
	c := &CPUInfo{LogicalCores: logical, PhysicalCores: physical, Sockets: 1}
	c.Model, _ = unix.Sysctl("machdep.cpu.brand_string")
	c.Vendor, _ = unix.Sysctl("machdep.cpu.vendor")
	if c.Vendor == "" && strings.HasPrefix(c.Model, "Apple") {
		c.Vendor = "Apple"
	}
	if n, err := unix.SysctlUint32("hw.packages"); err == nil && n > 0 {
		c.Sockets = int(n)
	}
	if hz, err := unix.SysctlUint64("hw.cpufrequency"); err == nil && hz > 0 {
		c.MHz = float64(hz) / 1e6
	}
	return c
}

func nativeDMI() *DMIInfo {
	model, err := unix.Sysctl("hw.model")
	if err != nil || model == "" {
		return nil
	}
	return &DMIInfo{SysVendor: "Apple Inc.", ProductName: model}
}

// nativePackages: installer receipts (pkgutil), Homebrew formulae and casks, application bundles in /Applications.
func nativePackages() []Package {
	var out []Package
	if files, err := filepath.Glob("/var/db/receipts/*.plist"); err == nil {
		for i, f := range files {
			if i >= maxReceipts {
				break
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			m, err := plist.Dict(b)
			if err != nil {
				continue
			}
			if id := plist.String(m, "PackageIdentifier"); id != "" {
				out = append(out, Package{Manager: "pkgutil", Name: id, Version: plist.String(m, "PackageVersion")})
			}
		}
	}
	out = append(out, HomebrewPackages(homebrewPrefixes)...)
	if apps, err := filepath.Glob("/Applications/*.app"); err == nil {
		for i, app := range apps {
			if i >= maxApps {
				break
			}
			b, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
			if err != nil {
				continue
			}
			m, err := plist.Dict(b)
			if err != nil {
				continue
			}
			v := plist.String(m, "CFBundleShortVersionString")
			if v == "" {
				v = plist.String(m, "CFBundleVersion")
			}
			out = append(out, Package{Manager: "app", Name: strings.TrimSuffix(filepath.Base(app), ".app"), Version: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// launchdDirs hold job definitions of the system domain.
var launchdDirs = []string{"/Library/LaunchDaemons", "/System/Library/LaunchDaemons"}

// nativeServices lists launchd jobs: the loaded jobs of the agent's domain (`launchctl list`; the system domain
// when running as root) plus not-loaded third-party daemons from /Library/LaunchDaemons, with program paths from
// their plists.
func nativeServices() ([]SystemService, error) {
	domain := "system"
	if os.Geteuid() != 0 {
		domain = "user"
	}
	type def struct{ path, program string }
	defs := map[string]def{}
	for _, dir := range launchdDirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.plist"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			m, err := plist.Dict(b)
			if err != nil {
				continue
			}
			label := plist.String(m, "Label")
			if label == "" {
				continue
			}
			prog := plist.String(m, "Program")
			if args, ok := m["ProgramArguments"].([]any); ok && prog == "" && len(args) > 0 {
				prog, _ = args[0].(string)
			}
			if _, dup := defs[label]; !dup {
				defs[label] = def{path: f, program: prog}
			}
		}
	}
	out, err := runTool("/bin/launchctl", "list")
	jobs := ParseLaunchctlList(out)
	loaded := map[string]bool{}
	var res []SystemService
	add := func(s LaunchdService) {
		if d, ok := defs[s.Label]; ok {
			s.Path, s.Program = d.path, d.program
		}
		s.Domain = domain
		pid := 0
		if s.PID != nil {
			pid = *s.PID
		}
		res = append(res, SystemService{Manager: ServiceManagerLaunchd, Name: s.Label, PID: pid, Body: s})
	}
	for _, j := range jobs {
		if strings.HasPrefix(j.Label, "application.") {
			continue // per-launch GUI application jobs (application.<bundle id>.<pid>…): churn, not services
		}
		loaded[j.Label] = true
		add(j)
	}
	for label, d := range defs {
		if !loaded[label] && strings.HasPrefix(d.path, "/Library/") {
			add(LaunchdService{Label: label})
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	if len(jobs) == 0 && err != nil {
		return res, err
	}
	return res, nil
}

func nativePorts(instances []ProcessInstance) ([]ListeningPort, error) {
	tcp, err := runTool("/usr/sbin/lsof", "-nP", "-w", "-F", "pcPnt", "-iTCP", "-sTCP:LISTEN")
	udp, _ := runTool("/usr/sbin/lsof", "-nP", "-w", "-F", "pcPnt", "-iUDP")
	ports := ParseLsof(tcp + "\n" + udp)
	if len(ports) == 0 && err != nil && tcp == "" {
		return nil, err
	}
	return portsWithProcesses(ports, instances), nil
}

func nativeUsers() ([]User, error) {
	out, err := runTool("/usr/bin/dscacheutil", "-q", "user")
	if err != nil && out == "" {
		return nil, err
	}
	return ParseDscacheutilUsers(out), nil
}

// NativeFingerprint is a cheap change fingerprint of a macOS host: receipts, applications, Homebrew and launchd
// definition directories.
func NativeFingerprint() uint64 {
	h := fnv.New64a()
	dirs := append([]string{"/var/db/receipts", "/Applications"}, launchdDirs...)
	for _, p := range homebrewPrefixes {
		dirs = append(dirs, filepath.Join(p, "Cellar"), filepath.Join(p, "Caskroom"))
	}
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil {
			fmt.Fprintf(h, "%s=%d;", d, fi.ModTime().UnixNano())
		}
	}
	return h.Sum64()
}
