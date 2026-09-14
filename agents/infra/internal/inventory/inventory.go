// Package inventory collects "what is on this machine" and encodes it as
// snapshot log records (semantic-conventions §3).
package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Category names.
const (
	CategoryOS                = "os"
	CategoryHardware          = "hardware"
	CategoryKernelModule      = "kernel_module"
	CategoryPackage           = "package"
	CategorySystemdUnit       = "systemd_unit"
	CategoryListeningPort     = "listening_port"
	CategoryProcess           = "process"
	CategoryUser              = "user"
	CategoryNetworkInterface  = "network_interface"
	CategoryMount             = "mount"
	CategoryDiscoveredService = "discovered_service"
)

// Event names and attribute keys of inventory log records.
const (
	EventItem      = "openlog.inventory.item"
	EventSnapshot  = "openlog.inventory.snapshot"
	AttrEventName  = "event.name"
	AttrSnapshotID = "openlog.inventory.snapshot_id"
	AttrCategory   = "openlog.inventory.category"
	AttrKey        = "openlog.inventory.key"
	AttrItemCount  = "openlog.inventory.item_count"
)

// Item is one inventory item; Body is marshalled to a JSON string.
type Item struct {
	Category string
	Key      string
	Body     any
}

// Data is the typed result of an inventory collection. Discovery consumes it.
type Data struct {
	OS         *OSInfo
	CPU        *CPUInfo
	Memory     *MemoryInfo
	DMI        *DMIInfo
	Modules    []KernelModule
	Packages   []Package
	Units      []SystemdUnit
	Ports      []ListeningPort
	Processes  []Process
	Instances  []ProcessInstance // per-PID view used by discovery; not emitted
	Containers []Container       // Docker Engine API; empty without a runtime socket
	Users      []User
	Interfaces []NetworkInterface
	Mounts     []Mount
}

// Collector gathers the inventory from a host file system.
type Collector struct {
	FS    *hostfs.FS
	Stats *selfmon.Stats
	Log   *slog.Logger
	// InterfaceAddrs returns addresses for a network interface; defaults to
	// the net package (the agent must share the host network namespace).
	InterfaceAddrs func(name string) []string
	// Containers lists containers (nil: no container inventory).
	Containers func() ([]Container, error)
	// SystemdBus is the path of the D-Bus system bus socket used to add unit runtime state
	// (active state, restarts, memory/CPU); "" or an unreachable bus keeps file data only.
	SystemdBus string
}

func (c *Collector) fs(name string) *hostfs.FS {
	fs := c.FS
	if c.Stats != nil {
		fs = fs.WithRecorder(c.Stats)
	}
	return fs.ForCollector("inventory." + name)
}

func (c *Collector) timed(name string, fn func(fs *hostfs.FS) error) {
	start := time.Now()
	err := fn(c.fs(name))
	if c.Stats != nil {
		c.Stats.SetCollectorDuration("inventory."+name, time.Since(start))
	}
	if err != nil && c.Log != nil {
		c.Log.Debug("inventory collector error", "collector", name, "error", err)
	}
}

// Collect runs all inventory collectors. Individual failures only leave
// the affected category empty.
func (c *Collector) Collect() *Data {
	d := &Data{}
	c.timed(CategoryOS, func(fs *hostfs.FS) error { d.OS = collectOS(fs); return nil })
	c.timed(CategoryHardware, func(fs *hostfs.FS) error {
		d.CPU, d.Memory, d.DMI = collectHardware(fs)
		return nil
	})
	c.timed(CategoryKernelModule, func(fs *hostfs.FS) (err error) { d.Modules, err = collectModules(fs); return })
	c.timed(CategoryPackage, func(fs *hostfs.FS) error { d.Packages = collectPackages(fs); return nil })
	c.timed(CategorySystemdUnit, func(fs *hostfs.FS) error { d.Units = collectUnits(fs); return nil })
	if c.SystemdBus != "" {
		c.timed("systemd_dbus", func(*hostfs.FS) (err error) {
			d.Units, err = collectUnitStates(context.Background(), c.SystemdBus, d.Units, dialSystemBus)
			if errors.Is(err, errNoBus) {
				return nil
			}
			return err
		})
	}
	c.timed(CategoryProcess, func(fs *hostfs.FS) (err error) {
		d.Instances, err = collectInstances(fs, bootTimeOf(d.OS))
		d.Processes = GroupProcesses(d.Instances)
		return
	})
	c.timed(CategoryListeningPort, func(fs *hostfs.FS) error { d.Ports = collectPorts(fs, d.Instances); return nil })
	c.timed(CategoryUser, func(fs *hostfs.FS) (err error) { d.Users, err = collectUsers(fs); return })
	c.timed(CategoryNetworkInterface, func(fs *hostfs.FS) (err error) {
		d.Interfaces, err = collectInterfaces(fs, c.InterfaceAddrs)
		return
	})
	c.timed(CategoryMount, func(fs *hostfs.FS) (err error) { d.Mounts, err = collectMounts(fs); return })
	if c.Containers != nil {
		c.timed(CategoryContainer, func(*hostfs.FS) (err error) { d.Containers, err = c.Containers(); return })
	}
	return d
}

// Items converts the collected data to inventory items (sorted, unique keys).
func (d *Data) Items() []Item {
	var items []Item
	add := func(cat, key string, body any) { items = append(items, Item{cat, key, body}) }
	if d.OS != nil {
		add(CategoryOS, "os", d.OS)
	}
	if d.CPU != nil {
		add(CategoryHardware, "cpu", d.CPU)
	}
	if d.Memory != nil {
		add(CategoryHardware, "memory", d.Memory)
	}
	if d.DMI != nil {
		add(CategoryHardware, "dmi", d.DMI)
	}
	for _, m := range d.Modules {
		add(CategoryKernelModule, m.Name, m)
	}
	for _, p := range d.Packages {
		add(CategoryPackage, p.Key(), p)
	}
	for _, u := range d.Units {
		add(CategorySystemdUnit, u.Name, u)
	}
	for _, p := range d.Ports {
		add(CategoryListeningPort, p.Key(), p)
	}
	for _, p := range d.Processes {
		add(CategoryProcess, p.Key(), p)
	}
	for _, u := range d.Users {
		add(CategoryUser, u.Name, u)
	}
	for _, n := range d.Interfaces {
		add(CategoryNetworkInterface, n.Name, n)
	}
	for _, m := range d.Mounts {
		add(CategoryMount, m.MountPoint, m)
	}
	for _, c := range d.Containers {
		add(CategoryContainer, c.ID, c)
	}
	return items
}

// Normalize sorts items by (category, key) and removes duplicate keys, keeping the last.
func Normalize(items []Item) []Item {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Category != items[j].Category {
			return items[i].Category < items[j].Category
		}
		return items[i].Key < items[j].Key
	})
	out := items[:0]
	for i, it := range items {
		if i+1 < len(items) && items[i+1].Category == it.Category && items[i+1].Key == it.Key {
			continue
		}
		out = append(out, it)
	}
	return out
}

// Snapshot is a complete inventory at one point in time.
type Snapshot struct {
	ID    string
	Time  time.Time
	Items []Item
}

// LogRecords encodes the snapshot: one item record per item followed by the
// snapshot-complete record, which is always last.
func (s *Snapshot) LogRecords(observed time.Time) ([]*logspb.LogRecord, error) {
	ts := uint64(s.Time.UnixNano())
	obs := uint64(observed.UnixNano())
	out := make([]*logspb.LogRecord, 0, len(s.Items)+1)
	for _, it := range s.Items {
		body, err := json.Marshal(it.Body)
		if err != nil {
			return nil, err
		}
		out = append(out, &logspb.LogRecord{
			TimeUnixNano:         ts,
			ObservedTimeUnixNano: obs,
			Attributes: []*commonpb.KeyValue{
				otlputil.Str(AttrEventName, EventItem),
				otlputil.Str(AttrSnapshotID, s.ID),
				otlputil.Str(AttrCategory, it.Category),
				otlputil.Str(AttrKey, it.Key),
			},
			Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: string(body)}},
		})
	}
	out = append(out, &logspb.LogRecord{
		TimeUnixNano:         ts,
		ObservedTimeUnixNano: obs,
		Attributes: []*commonpb.KeyValue{
			otlputil.Str(AttrEventName, EventSnapshot),
			otlputil.Str(AttrSnapshotID, s.ID),
			otlputil.Int(AttrItemCount, int64(len(s.Items))),
		},
	})
	return out, nil
}

func bootTimeOf(o *OSInfo) time.Time {
	if o == nil || o.BootTime == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, o.BootTime)
	return t
}

func atoi(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}
