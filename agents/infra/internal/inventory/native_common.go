//go:build darwin || windows

package inventory

import (
	"errors"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/mask"
	"github.com/onuragtas/openlog/agents/infra/internal/osinfo"
)

// collectNative is Collect for macOS and Windows hosts. kernel_module and systemd_unit do not exist
// there; launchd jobs and Windows services are reported as SystemService instead.
func (c *Collector) collectNative() *Data {
	d := &Data{Platform: runtime.GOOS}
	c.timed(CategoryOS, func(*hostfs.FS) error { d.OS = nativeOS(); return nil })
	c.timed(CategoryHardware, func(*hostfs.FS) error {
		d.CPU, d.DMI = nativeCPU(), nativeDMI()
		d.Memory = nativeMemoryInfo()
		return nil
	})
	c.timed(CategoryPackage, func(*hostfs.FS) error { d.Packages = nativePackages(); return nil })
	c.timed("service", func(*hostfs.FS) (err error) { d.Services, err = nativeServices(); return })
	c.timed(CategoryProcess, func(*hostfs.FS) (err error) {
		d.Instances, err = nativeInstances()
		d.Processes = GroupProcesses(d.Instances)
		return
	})
	c.timed(CategoryListeningPort, func(*hostfs.FS) (err error) { d.Ports, err = nativePorts(d.Instances); return })
	c.timed(CategoryUser, func(*hostfs.FS) (err error) { d.Users, err = nativeUsers(); return })
	c.timed(CategoryNetworkInterface, func(*hostfs.FS) (err error) { d.Interfaces, err = nativeInterfaces(c.InterfaceAddrs); return })
	c.timed(CategoryMount, func(*hostfs.FS) (err error) { d.Mounts, err = nativeMounts(); return })
	if c.Containers != nil {
		c.timed(CategoryContainer, func(*hostfs.FS) (err error) { d.Containers, err = c.Containers(); return })
	}
	return d
}

func nativeOS() *OSInfo {
	i := osinfo.Get()
	o := &OSInfo{ID: i.ID, Name: i.Name, VersionID: i.Version, PrettyName: i.PrettyName,
		KernelRelease: i.KernelRelease, KernelVersion: i.KernelVersion, Arch: runtime.GOARCH}
	o.Hostname, _ = os.Hostname()
	if !i.BootTime.IsZero() {
		o.BootTime = i.BootTime.UTC().Format(time.RFC3339)
	}
	return o
}

func nativeMemoryInfo() *MemoryInfo {
	m := &MemoryInfo{}
	if vm, err := mem.VirtualMemory(); err == nil {
		m.TotalBytes = vm.Total
	}
	if sw, err := mem.SwapMemory(); err == nil {
		m.SwapTotalBytes = sw.Total
	}
	return m
}

func nativeCores() (logical, physical int) {
	logical, _ = cpu.Counts(true)
	physical, _ = cpu.Counts(false)
	return logical, physical
}

// nativeInstances lists processes. Windows processes have no uid (HasUID false).
func nativeInstances() ([]ProcessInstance, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	out := make([]ProcessInstance, 0, len(procs))
	for _, p := range procs {
		if p.Pid == 0 {
			continue
		}
		in := ProcessInstance{PID: int(p.Pid)}
		ppid, _ := p.Ppid()
		in.PPID = int(ppid)
		in.Exe, _ = p.Exe()
		name, _ := p.Name()
		in.Comm = baseName(name)
		if cmd, err := p.Cmdline(); err == nil {
			in.Cmdline = mask.Truncate(mask.Cmdline(in.Exe, cmd), maxCmdline)
		}
		if in.Exe == "" && in.Cmdline == "" {
			continue // kernel tasks, protected system processes
		}
		if in.Comm == "" || in.Comm == "." {
			in.Comm = baseName(in.Exe)
		}
		if uids, err := p.Uids(); err == nil && len(uids) > 0 {
			in.UID, in.HasUID = int(uids[0]), true
		}
		if ms, err := p.CreateTime(); err == nil && ms > 0 {
			in.StartTime = time.UnixMilli(ms).UTC()
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// portsWithProcesses fills process names and executables from the instances.
func portsWithProcesses(ports []ListeningPort, instances []ProcessInstance) []ListeningPort {
	byPID := make(map[int]ProcessInstance, len(instances))
	for _, in := range instances {
		byPID[in.PID] = in
	}
	for i := range ports {
		if in, ok := byPID[ports[i].PID]; ok {
			ports[i].ProcessName, ports[i].ProcessExe = in.Comm, in.Exe
		}
	}
	return ports
}

func nativeInterfaces(addrs func(string) []string) ([]NetworkInterface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	if addrs == nil {
		addrs = netAddrs
	}
	out := make([]NetworkInterface, 0, len(ifs))
	for _, ifc := range ifs {
		n := NetworkInterface{Name: ifc.Name, MAC: ifc.HardwareAddr.String(), MTU: ifc.MTU, OperState: "down", Addresses: addrs(ifc.Name)}
		if ifc.Flags&net.FlagUp != 0 {
			n.OperState = "up"
		}
		if n.Addresses == nil {
			n.Addresses = []string{}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func nativeMounts() ([]Mount, error) {
	parts, err := disk.Partitions(true)
	if err != nil && len(parts) == 0 {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]Mount, 0, len(parts))
	for _, p := range parts {
		if p.Mountpoint == "" || seen[p.Mountpoint] {
			continue
		}
		seen[p.Mountpoint] = true
		out = append(out, Mount{MountPoint: p.Mountpoint, Device: p.Device, FSType: p.Fstype, Options: strings.Join(p.Opts, ",")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountPoint < out[j].MountPoint })
	if len(out) == 0 {
		return nil, errors.New("no mounts")
	}
	return out, nil
}
