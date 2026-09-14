package inventory

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"sort"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// SystemdBusSocket is the D-Bus system bus socket (resolved under the host root).
const SystemdBusSocket = "/run/dbus/system_bus_socket"

// D-Bus limits: one connection per inventory collection.
const (
	busTimeout = 5 * time.Second
	// maxBusUnits bounds the units whose properties are read (4 calls each).
	maxBusUnits = 512
)

const (
	systemdDest     = "org.freedesktop.systemd1"
	systemdPath     = "/org/freedesktop/systemd1"
	ifaceManager    = "org.freedesktop.systemd1.Manager"
	ifaceUnit       = "org.freedesktop.systemd1.Unit"
	ifaceService    = "org.freedesktop.systemd1.Service"
	ifaceProperties = "org.freedesktop.DBus.Properties"
)

// busUnit is one entry of Manager.ListUnits (signature a(ssssssouso)).
type busUnit struct {
	Name, Description, LoadState, ActiveState, SubState, Followed string
	Path                                                          dbus.ObjectPath
	JobID                                                         uint32
	JobType                                                       string
	JobPath                                                       dbus.ObjectPath
}

// unitBus is the part of systemd's D-Bus API the collector uses (a fake in tests).
type unitBus interface {
	ListUnits(ctx context.Context) ([]busUnit, error)
	Property(ctx context.Context, path dbus.ObjectPath, iface, name string) (any, error)
	Close() error
}

// unitState is the runtime state of a loaded unit.
type unitState struct {
	Name, Description, LoadState, ActiveState, SubState string
	// FileState is Unit.UnitFileState; only read for units that have no unit file on disk.
	FileState   string
	ActiveSince time.Time
	Restarts    *uint32
	MemoryBytes *uint64
	CPUUsageNs  *uint64
}

type godbusBus struct{ conn *dbus.Conn }

// dialSystemBus connects to the system bus socket (EXTERNAL authentication by uid).
func dialSystemBus(ctx context.Context, socket string) (unitBus, error) {
	conn, err := dbus.Dial("unix:path="+socket, dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		conn.Close()
		return nil, err
	}
	return godbusBus{conn}, nil
}

func (b godbusBus) ListUnits(ctx context.Context) ([]busUnit, error) {
	var out []busUnit
	err := b.conn.Object(systemdDest, systemdPath).CallWithContext(ctx, ifaceManager+".ListUnits", 0).Store(&out)
	return out, err
}

func (b godbusBus) Property(ctx context.Context, path dbus.ObjectPath, iface, name string) (any, error) {
	var v dbus.Variant
	if err := b.conn.Object(systemdDest, path).CallWithContext(ctx, ifaceProperties+".Get", 0, iface, name).Store(&v); err != nil {
		return nil, err
	}
	return v.Value(), nil
}

func (b godbusBus) Close() error { return b.conn.Close() }

// readUnitStates lists loaded units and reads the runtime properties of units that are (or
// were) running: ActiveEnterTimestamp, and for services NRestarts, MemoryCurrent and
// CPUUsageNSec (omitted when accounting is off). known reports whether a unit file was found
// on disk; for other units the unit file state is read. A failing property only leaves that
// field empty.
func readUnitStates(ctx context.Context, bus unitBus, known func(string) bool) ([]unitState, error) {
	list, err := bus.ListUnits(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := make([]unitState, 0, len(list))
	budget := maxBusUnits
	for _, u := range list {
		typ := UnitType(u.Name)
		if typ == "" {
			continue
		}
		st := unitState{Name: u.Name, Description: u.Description, LoadState: u.LoadState, ActiveState: u.ActiveState, SubState: u.SubState}
		running := u.ActiveState == "active" || u.ActiveState == "reloading" || u.ActiveState == "activating" ||
			u.ActiveState == "deactivating" || u.ActiveState == "failed"
		wantFile := !known(u.Name) && u.LoadState == "loaded" && typ == "service"
		if (running || wantFile) && budget > 0 && ctx.Err() == nil {
			budget--
			if running {
				if usec, ok := uint64Prop(ctx, bus, u.Path, ifaceUnit, "ActiveEnterTimestamp"); ok && usec > 0 && u.ActiveState != "failed" {
					st.ActiveSince = time.UnixMicro(int64(usec)).UTC()
				}
				if typ == "service" {
					if v, err := bus.Property(ctx, u.Path, ifaceService, "NRestarts"); err == nil {
						if n, ok := v.(uint32); ok {
							st.Restarts = &n
						}
					}
					st.MemoryBytes = accounted(uint64Prop(ctx, bus, u.Path, ifaceService, "MemoryCurrent"))
					st.CPUUsageNs = accounted(uint64Prop(ctx, bus, u.Path, ifaceService, "CPUUsageNSec"))
				}
			}
			if wantFile {
				if v, err := bus.Property(ctx, u.Path, ifaceUnit, "UnitFileState"); err == nil {
					st.FileState, _ = v.(string)
				}
			}
		}
		out = append(out, st)
	}
	return out, ctx.Err()
}

func uint64Prop(ctx context.Context, bus unitBus, path dbus.ObjectPath, iface, name string) (uint64, bool) {
	v, err := bus.Property(ctx, path, iface, name)
	if err != nil {
		return 0, false
	}
	n, ok := v.(uint64)
	return n, ok
}

// accounted drops systemd's "not set" value (UINT64_MAX: accounting disabled or no cgroup).
func accounted(v uint64, ok bool) *uint64 {
	if !ok || v == math.MaxUint64 {
		return nil
	}
	return &v
}

// fileStateToEnabled maps Unit.UnitFileState to the enabled_state values of §3.3.
func fileStateToEnabled(s string) string {
	switch s {
	case "enabled", "enabled-runtime", "linked", "linked-runtime", "alias":
		return "enabled"
	case "disabled":
		return "disabled"
	case "static", "indirect":
		return "static"
	case "masked", "masked-runtime":
		return "masked"
	case "generated", "transient":
		return s
	}
	return "unknown"
}

// mergeUnitStates adds the runtime state to the units found on disk. Loaded services
// without a unit file of their own (template instances such as postgresql@16-main.service,
// generated and transient services) are added; other loaded units without a file (devices,
// scopes, mounts) are not. Units that systemd does not know (not loaded) keep file data only.
func mergeUnitStates(units []SystemdUnit, states []unitState) []SystemdUnit {
	idx := make(map[string]int, len(units))
	for i, u := range units {
		idx[u.Name] = i
	}
	for _, st := range states {
		i, ok := idx[st.Name]
		if !ok {
			if st.LoadState != "loaded" || UnitType(st.Name) != "service" {
				continue
			}
			u := SystemdUnit{Name: st.Name, Type: "service", Description: st.Description, EnabledState: fileStateToEnabled(st.FileState)}
			if t := templateName(st.Name); t != "" {
				if j, ok := idx[t]; ok {
					u.Path = units[j].Path
					u.ExecStart = units[j].ExecStart
					if u.Description == "" {
						u.Description = units[j].Description
					}
				}
			}
			units = append(units, u)
			i = len(units) - 1
			idx[st.Name] = i
		}
		u := &units[i]
		if st.LoadState == "not-found" {
			continue // referenced by another unit only; the file data (if any) stands
		}
		u.LoadState, u.ActiveState, u.SubState = st.LoadState, st.ActiveState, st.SubState
		if !st.ActiveSince.IsZero() && st.ActiveState != "inactive" {
			u.ActiveSince = st.ActiveSince.Format(time.RFC3339)
		}
		u.Restarts, u.MemoryBytes, u.CPUUsageNs = st.Restarts, st.MemoryBytes, st.CPUUsageNs
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
	return units
}

// errNoBus means the system bus socket does not exist (no systemd/D-Bus, or not mounted).
var errNoBus = errors.New("systemd: no D-Bus system bus socket")

// collectUnitStates enriches units over D-Bus; on any connection error the units are
// returned unchanged (file-based inventory).
func collectUnitStates(ctx context.Context, socket string, units []SystemdUnit, dial func(context.Context, string) (unitBus, error)) ([]SystemdUnit, error) {
	if socket == "" {
		return units, nil
	}
	ctx, cancel := context.WithTimeout(ctx, busTimeout)
	defer cancel()
	bus, err := dial(ctx, socket)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
			return units, errNoBus
		}
		return units, err
	}
	defer bus.Close()
	known := make(map[string]bool, len(units))
	for _, u := range units {
		known[u.Name] = true
	}
	states, err := readUnitStates(ctx, bus, func(n string) bool { return known[n] })
	if err != nil && len(states) == 0 {
		return units, err
	}
	return mergeUnitStates(units, states), err
}
