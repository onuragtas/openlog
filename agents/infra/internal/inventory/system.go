package inventory

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
	"github.com/onuragtas/openlog/agents/infra/internal/resource"
)

// OSInfo is the body of the "os" item.
type OSInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	VersionID     string `json:"version_id"`
	PrettyName    string `json:"pretty_name"`
	KernelRelease string `json:"kernel_release"`
	KernelVersion string `json:"kernel_version"`
	Arch          string `json:"arch"`
	Hostname      string `json:"hostname"`
	BootTime      string `json:"boot_time,omitempty"`
}

func collectOS(fs *hostfs.FS) *OSInfo {
	o := &OSInfo{Arch: resource.Arch(fs), Hostname: resource.Hostname(fs)}
	if osr := resource.OSRelease(fs); osr != nil {
		o.ID, o.Name, o.VersionID, o.PrettyName = osr["ID"], osr["NAME"], osr["VERSION_ID"], osr["PRETTY_NAME"]
	}
	o.KernelRelease, _ = fs.ReadString("/proc/sys/kernel/osrelease")
	o.KernelVersion, _ = fs.ReadString("/proc/sys/kernel/version")
	if b, err := fs.ReadFile("/proc/stat"); err == nil {
		if st, err := procfs.ParseStat(b); err == nil && !st.BootTime.IsZero() {
			o.BootTime = st.BootTime.UTC().Format(time.RFC3339)
		}
	}
	return o
}

// CPUInfo is the body of the "hardware/cpu" item.
type CPUInfo struct {
	Vendor        string  `json:"vendor"`
	Model         string  `json:"model"`
	LogicalCores  int     `json:"logical_cores"`
	PhysicalCores int     `json:"physical_cores"`
	Sockets       int     `json:"sockets"`
	MHz           float64 `json:"mhz,omitempty"`
}

// MemoryInfo is the body of the "hardware/memory" item.
type MemoryInfo struct {
	TotalBytes     uint64 `json:"total_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
}

// DMIInfo is the body of the "hardware/dmi" item. Serial numbers are never read.
type DMIInfo struct {
	SysVendor      string `json:"sys_vendor"`
	ProductName    string `json:"product_name"`
	ProductVersion string `json:"product_version"`
	BIOSVendor     string `json:"bios_vendor"`
	BIOSVersion    string `json:"bios_version"`
}

var armImplementers = map[string]string{
	"0x41": "ARM", "0x42": "Broadcom", "0x43": "Cavium", "0x46": "Fujitsu", "0x48": "HiSilicon",
	"0x4e": "NVIDIA", "0x50": "APM", "0x51": "Qualcomm", "0x61": "Apple", "0x6d": "Microsoft", "0xc0": "Ampere",
}

// ParseCPUInfo parses /proc/cpuinfo (x86 and arm64 layouts).
func ParseCPUInfo(data []byte) *CPUInfo {
	c := &CPUInfo{}
	sockets := map[string]bool{}
	cores := map[string]bool{}
	var physID string
	for line := range strings.Lines(string(data)) {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			c.LogicalCores++
			physID = ""
		case "vendor_id":
			c.Vendor = v
		case "CPU implementer":
			if c.Vendor == "" {
				if n, ok := armImplementers[strings.ToLower(v)]; ok {
					c.Vendor = n
				} else {
					c.Vendor = v
				}
			}
		case "model name":
			c.Model = v
		case "cpu MHz":
			if c.MHz == 0 {
				c.MHz, _ = strconv.ParseFloat(v, 64)
			}
		case "physical id":
			physID = v
			sockets[v] = true
		case "core id":
			cores[physID+"/"+v] = true
		}
	}
	c.Sockets = len(sockets)
	c.PhysicalCores = len(cores)
	if c.LogicalCores > 0 {
		if c.Sockets == 0 {
			c.Sockets = 1
		}
		if c.PhysicalCores == 0 {
			c.PhysicalCores = c.LogicalCores
		}
	}
	return c
}

func collectHardware(fs *hostfs.FS) (*CPUInfo, *MemoryInfo, *DMIInfo) {
	var cpu *CPUInfo
	if b, err := fs.ReadFile("/proc/cpuinfo"); err == nil {
		cpu = ParseCPUInfo(b)
		if cpu.Model == "" {
			cpu.Model, _ = fs.ReadString("/sys/firmware/devicetree/base/model")
			cpu.Model = strings.TrimRight(cpu.Model, "\x00")
		}
	}
	var mem *MemoryInfo
	if b, err := fs.ReadFile("/proc/meminfo"); err == nil {
		mi := procfs.ParseMeminfo(b)
		mem = &MemoryInfo{TotalBytes: mi["MemTotal"], SwapTotalBytes: mi["SwapTotal"]}
	}
	read := func(n string) string { v, _ := fs.ReadString("/sys/class/dmi/id/" + n); return v }
	dmi := &DMIInfo{
		SysVendor: read("sys_vendor"), ProductName: read("product_name"), ProductVersion: read("product_version"),
		BIOSVendor: read("bios_vendor"), BIOSVersion: read("bios_version"),
	}
	if *dmi == (DMIInfo{}) {
		dmi = nil
	}
	return cpu, mem, dmi
}

// KernelModule is the body of a "kernel_module" item.
type KernelModule struct {
	Name      string `json:"name"`
	SizeBytes uint64 `json:"size_bytes"`
	State     string `json:"state"`
}

// ParseModules parses /proc/modules.
func ParseModules(data []byte) []KernelModule {
	var out []KernelModule
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		size, _ := strconv.ParseUint(f[1], 10, 64)
		out = append(out, KernelModule{Name: f[0], SizeBytes: size, State: strings.ToLower(f[4])})
	}
	return out
}

func collectModules(fs *hostfs.FS) ([]KernelModule, error) {
	b, err := fs.ReadFile("/proc/modules")
	if err != nil {
		return nil, err
	}
	return ParseModules(b), nil
}

// User is the body of a "user" item (no GECOS, no password fields).
type User struct {
	Name  string `json:"name"`
	UID   int    `json:"uid"`
	GID   int    `json:"gid"`
	Home  string `json:"home"`
	Shell string `json:"shell"`
}

// ParsePasswd parses /etc/passwd.
func ParsePasswd(data []byte) []User {
	var out []User
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 7 || f[0] == "" || strings.HasPrefix(f[0], "+") || strings.HasPrefix(f[0], "-") {
			continue
		}
		out = append(out, User{Name: f[0], UID: atoi(f[2]), GID: atoi(f[3]), Home: f[5], Shell: f[6]})
	}
	return out
}

func collectUsers(fs *hostfs.FS) ([]User, error) {
	b, err := fs.ReadFile("/etc/passwd")
	if err != nil {
		return nil, err
	}
	return ParsePasswd(b), nil
}

// NetworkInterface is the body of a "network_interface" item.
type NetworkInterface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	MTU       int      `json:"mtu"`
	OperState string   `json:"operstate"`
	Addresses []string `json:"addresses"`
}

func collectInterfaces(fs *hostfs.FS, addrs func(string) []string) ([]NetworkInterface, error) {
	entries, err := fs.ReadDir("/sys/class/net")
	if err != nil {
		return nil, err
	}
	if addrs == nil {
		addrs = netAddrs
	}
	var out []NetworkInterface
	for _, e := range entries {
		name := e.Name()
		base := "/sys/class/net/" + name
		if _, err := fs.ReadString(base + "/operstate"); err != nil {
			continue // not an interface directory (e.g. bonding_masters)
		}
		n := NetworkInterface{Name: name, Addresses: addrs(name)}
		n.MAC, _ = fs.ReadString(base + "/address")
		mtu, _ := fs.ReadString(base + "/mtu")
		n.MTU = atoi(mtu)
		n.OperState, _ = fs.ReadString(base + "/operstate")
		if n.Addresses == nil {
			n.Addresses = []string{}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// netAddrs resolves interface addresses via netlink (net package). sysfs does
// not expose IP addresses; this requires the host network namespace.
func netAddrs(name string) []string {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	as, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.String())
	}
	return out
}

// Mount is the body of a "mount" item.
type Mount struct {
	MountPoint string `json:"mountpoint"`
	Device     string `json:"device"`
	FSType     string `json:"fs_type"`
	Options    string `json:"options"`
}

func collectMounts(fs *hostfs.FS) ([]Mount, error) {
	b, err := fs.ReadFile(fs.ProcSelf() + "/mountinfo")
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	var out []Mount
	for _, m := range procfs.ParseMountinfo(b) {
		item := Mount{MountPoint: m.MountPoint, Device: m.Device, FSType: m.FSType, Options: m.Options}
		if i, ok := idx[m.MountPoint]; ok {
			out[i] = item // stacked mount: the later one is visible
			continue
		}
		idx[m.MountPoint] = len(out)
		out = append(out, item)
	}
	return out, nil
}
