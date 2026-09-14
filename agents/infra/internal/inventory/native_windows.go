//go:build windows

package inventory

import (
	"fmt"
	"hash/fnv"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	gnet "github.com/shirou/gopsutil/v4/net"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/onuragtas/openlog/agents/infra/internal/mask"
)

func init() { baseName = WindowsBaseName }

func regString(k registry.Key, name string) string {
	v, _, _ := k.GetStringValue(name)
	return v
}

func nativeCPU() *CPUInfo {
	logical, physical := nativeCores()
	c := &CPUInfo{LogicalCores: logical, PhysicalCores: physical}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return c
	}
	defer k.Close()
	c.Model = strings.TrimSpace(regString(k, "ProcessorNameString"))
	c.Vendor = regString(k, "VendorIdentifier")
	if mhz, _, err := k.GetIntegerValue("~MHz"); err == nil {
		c.MHz = float64(mhz)
	}
	return c
}

func nativeDMI() *DMIInfo {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	return &DMIInfo{
		SysVendor: regString(k, "SystemManufacturer"), ProductName: regString(k, "SystemProductName"),
		ProductVersion: regString(k, "SystemVersion"), BIOSVendor: regString(k, "BIOSVendor"), BIOSVersion: regString(k, "BIOSVersion"),
	}
}

const uninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`

// nativePackages lists installed programs from the 64-bit and 32-bit Uninstall registry keys ("Programs and
// Features"); system components and updates are skipped.
func nativePackages() []Package {
	seen := map[string]bool{}
	var out []Package
	for _, view := range []struct {
		access uint32
		arch   string
	}{{registry.WOW64_64KEY, runtime.GOARCH}, {registry.WOW64_32KEY, "386"}} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallKey, registry.ENUMERATE_SUB_KEYS|view.access)
		if err != nil {
			continue
		}
		subs, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, sub := range subs {
			sk, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallKey+`\`+sub, registry.QUERY_VALUE|view.access)
			if err != nil {
				continue
			}
			name := strings.TrimSpace(regString(sk, "DisplayName"))
			sysComp, _, _ := sk.GetIntegerValue("SystemComponent")
			parent := regString(sk, "ParentKeyName")
			release := regString(sk, "ReleaseType")
			version := regString(sk, "DisplayVersion")
			sk.Close()
			if name == "" || sysComp == 1 || parent != "" || release == "Update" || release == "Hotfix" || release == "Security Update" {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, Package{Manager: "windows", Name: name, Version: version, Arch: view.arch})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

var serviceStates = map[svc.State]string{
	svc.Stopped: "stopped", svc.StartPending: "start_pending", svc.StopPending: "stop_pending", svc.Running: "running",
	svc.ContinuePending: "continue_pending", svc.PausePending: "pause_pending", svc.Paused: "paused",
}

func startType(c mgr.Config) string {
	switch c.StartType {
	case windows.SERVICE_BOOT_START:
		return "boot"
	case windows.SERVICE_SYSTEM_START:
		return "system"
	case mgr.StartAutomatic:
		if c.DelayedAutoStart {
			return "automatic_delayed"
		}
		return "automatic"
	case mgr.StartManual:
		return "manual"
	case mgr.StartDisabled:
		return "disabled"
	}
	return strconv.FormatUint(uint64(c.StartType), 10)
}

// nativeServices lists Win32 services through the Service Control Manager with read-only access rights.
func nativeServices() ([]SystemService, error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT|windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return nil, fmt.Errorf("open service control manager: %w", err)
	}
	m := &mgr.Mgr{Handle: h}
	defer m.Disconnect()
	names, err := m.ListServices()
	if err != nil {
		return nil, err
	}
	out := make([]SystemService, 0, len(names))
	for _, name := range names {
		p, err := windows.UTF16PtrFromString(name)
		if err != nil {
			continue
		}
		sh, err := windows.OpenService(h, p, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
		if err != nil {
			continue
		}
		s := &mgr.Service{Name: name, Handle: sh}
		cfg, cerr := s.Config()
		st, serr := s.Query()
		s.Close()
		ws := WindowsService{Name: name}
		if cerr == nil {
			ws.DisplayName, ws.StartType, ws.Account = cfg.DisplayName, startType(cfg), cfg.ServiceStartName
			ws.BinaryPath = mask.Truncate(mask.Cmdline("", cfg.BinaryPathName), maxCmdline)
		}
		pid := 0
		if serr == nil {
			ws.State = serviceStates[st.State]
			if st.ProcessId != 0 {
				pid = int(st.ProcessId)
				ws.PID = &pid
			}
		}
		out = append(out, SystemService{Manager: ServiceManagerWindows, Name: name, PID: pid, Body: ws})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// nativePorts lists listening TCP sockets and bound UDP sockets with their owning PIDs (GetExtendedTcpTable).
func nativePorts(instances []ProcessInstance) ([]ListeningPort, error) {
	conns, err := gnet.Connections("inet")
	if err != nil {
		return nil, err
	}
	var ports []ListeningPort
	for _, c := range conns {
		proto := ""
		switch c.Type {
		case syscall.SOCK_STREAM:
			if c.Status != "LISTEN" {
				continue
			}
			proto = "tcp"
		case syscall.SOCK_DGRAM:
			proto = "udp"
		default:
			continue
		}
		if c.Laddr.Port == 0 {
			continue
		}
		family := 4
		if c.Family == syscall.AF_INET6 || strings.Contains(c.Laddr.IP, ":") {
			family = 6
		}
		ports = append(ports, ListeningPort{Protocol: proto, Family: family, Address: c.Laddr.IP, Port: int(c.Laddr.Port), PID: int(c.Pid)})
	}
	return portsWithProcesses(dedupePorts(ports), instances), nil
}

const profileListKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList`

// nativeUsers lists accounts with a user profile (ProfileList); uid is the account's relative identifier.
func nativeUsers() ([]User, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	sids, err := k.ReadSubKeyNames(-1)
	k.Close()
	if err != nil {
		return nil, err
	}
	var out []User
	for _, s := range sids {
		sid, err := windows.StringToSid(s)
		if err != nil {
			continue
		}
		account, domain, _, err := sid.LookupAccount("")
		if err != nil || account == "" {
			continue
		}
		name := account
		if domain != "" && !strings.EqualFold(domain, "NT AUTHORITY") && !strings.EqualFold(domain, "BUILTIN") {
			name = domain + `\` + account
		}
		u := User{Name: name}
		if i := strings.LastIndexByte(s, '-'); i >= 0 {
			u.UID, _ = strconv.Atoi(s[i+1:])
		}
		if pk, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKey+`\`+s, registry.QUERY_VALUE); err == nil {
			u.Home, _, _ = pk.GetStringValue("ProfileImagePath")
			pk.Close()
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// NativeFingerprint is a cheap change fingerprint of a Windows host: last write times of the Uninstall keys and
// of the service configuration key.
func NativeFingerprint() uint64 {
	h := fnv.New64a()
	for _, key := range []struct {
		path   string
		access uint32
	}{
		{uninstallKey, registry.WOW64_64KEY}, {uninstallKey, registry.WOW64_32KEY},
		{`SYSTEM\CurrentControlSet\Services`, 0},
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, key.path, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS|key.access)
		if err != nil {
			continue
		}
		if st, err := k.Stat(); err == nil {
			fmt.Fprintf(h, "%s/%d=%d/%d;", key.path, key.access, st.ModTime().UnixNano(), st.SubKeyCount)
		}
		k.Close()
	}
	return h.Sum64()
}
