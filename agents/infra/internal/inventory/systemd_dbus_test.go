package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type fakeBus struct {
	units  []busUnit
	props  map[string]any // "<path> <iface>.<name>"
	calls  int
	closed bool
}

func (b *fakeBus) ListUnits(context.Context) ([]busUnit, error) { return b.units, nil }

func (b *fakeBus) Property(_ context.Context, path dbus.ObjectPath, iface, name string) (any, error) {
	b.calls++
	if v, ok := b.props[string(path)+" "+iface+"."+name]; ok {
		return v, nil
	}
	return nil, errors.New("org.freedesktop.DBus.Error.UnknownProperty")
}

func (b *fakeBus) Close() error { b.closed = true; return nil }

func TestSystemdUnitStatesOverDBus(t *testing.T) {
	since := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	svc := func(n string) dbus.ObjectPath {
		return dbus.ObjectPath("/org/freedesktop/systemd1/unit/" + strings.NewReplacer("-", "_2d", ".", "_2e", "@", "_40").Replace(n))
	}
	bus := &fakeBus{
		units: []busUnit{
			{Name: "ssh.service", Description: "OpenBSD Secure Shell server", LoadState: "loaded", ActiveState: "active", SubState: "running", Path: svc("ssh.service")},
			{Name: "cron.service", LoadState: "loaded", ActiveState: "failed", SubState: "failed", Path: svc("cron.service")},
			{Name: "backup.service", LoadState: "loaded", ActiveState: "inactive", SubState: "dead", Path: svc("backup.service")},
			{Name: "getty@tty1.service", Description: "Getty on tty1", LoadState: "loaded", ActiveState: "active", SubState: "running", Path: svc("getty@tty1.service")},
			{Name: "run-u42.service", Description: "/usr/bin/sleep 60", LoadState: "loaded", ActiveState: "active", SubState: "running", Path: svc("run-u42.service")},
			{Name: "dev-sda.device", LoadState: "loaded", ActiveState: "active", SubState: "plugged", Path: svc("dev-sda.device")},
			{Name: "session-1.scope", LoadState: "loaded", ActiveState: "active", SubState: "running", Path: svc("session-1.scope")},
			{Name: "nfs-server.service", LoadState: "not-found", ActiveState: "inactive", SubState: "dead", Path: svc("nfs-server.service")},
			{Name: "apt-daily.timer", LoadState: "loaded", ActiveState: "active", SubState: "waiting", Path: svc("apt-daily.timer")},
		},
		props: map[string]any{
			string(svc("ssh.service")) + " " + ifaceUnit + ".ActiveEnterTimestamp":     uint64(since.UnixMicro()),
			string(svc("ssh.service")) + " " + ifaceService + ".NRestarts":             uint32(2),
			string(svc("ssh.service")) + " " + ifaceService + ".MemoryCurrent":         uint64(5 << 20),
			string(svc("ssh.service")) + " " + ifaceService + ".CPUUsageNSec":          uint64(1500000000),
			string(svc("cron.service")) + " " + ifaceUnit + ".ActiveEnterTimestamp":    uint64(since.UnixMicro()),
			string(svc("cron.service")) + " " + ifaceService + ".NRestarts":            uint32(5),
			string(svc("cron.service")) + " " + ifaceService + ".MemoryCurrent":        uint64(math.MaxUint64), // accounting off
			string(svc("getty@tty1.service")) + " " + ifaceUnit + ".UnitFileState":     "enabled",
			string(svc("getty@tty1.service")) + " " + ifaceService + ".NRestarts":      uint32(0),
			string(svc("run-u42.service")) + " " + ifaceUnit + ".UnitFileState":        "transient",
			string(svc("apt-daily.timer")) + " " + ifaceUnit + ".ActiveEnterTimestamp": uint64(since.UnixMicro()),
		},
	}
	files := collectFixtureUnits(t)
	dial := func(context.Context, string) (unitBus, error) { return bus, nil }
	units, err := collectUnitStates(context.Background(), "/run/dbus/system_bus_socket", files, dial)
	if err != nil {
		t.Fatal(err)
	}
	if !bus.closed {
		t.Error("bus not closed")
	}
	by := map[string]SystemdUnit{}
	for _, u := range units {
		by[u.Name] = u
	}

	ssh := by["ssh.service"]
	if ssh.ActiveState != "active" || ssh.SubState != "running" || ssh.LoadState != "loaded" || ssh.ActiveSince != "2026-09-01T08:30:00Z" ||
		ssh.Restarts == nil || *ssh.Restarts != 2 || ssh.MemoryBytes == nil || *ssh.MemoryBytes != 5<<20 || ssh.CPUUsageNs == nil || *ssh.CPUUsageNs != 1500000000 {
		t.Errorf("ssh = %+v", ssh)
	}
	if ssh.EnabledState != "enabled" || ssh.Path != "/lib/systemd/system/ssh.service" || ssh.Description != "OpenBSD Secure Shell server" {
		t.Errorf("ssh file data lost: %+v", ssh)
	}
	cron := by["cron.service"]
	if cron.ActiveState != "failed" || cron.ActiveSince != "" || cron.Restarts == nil || *cron.Restarts != 5 || cron.MemoryBytes != nil || cron.CPUUsageNs != nil {
		t.Errorf("cron = %+v", cron)
	}
	if b := by["backup.service"]; b.ActiveState != "inactive" || b.Restarts != nil || b.ActiveSince != "" {
		t.Errorf("backup = %+v", b)
	}
	// Loaded template instances and transient services without a file of their own are added.
	getty, ok := by["getty@tty1.service"]
	if !ok || getty.EnabledState != "enabled" || getty.Path != "/lib/systemd/system/getty@.service" || getty.Description != "Getty on tty1" ||
		getty.ExecStart == "" || getty.Restarts == nil || *getty.Restarts != 0 {
		t.Errorf("getty@tty1 = %+v (ok=%v)", getty, ok)
	}
	if r := by["run-u42.service"]; r.EnabledState != "transient" || r.Path != "" || r.ActiveState != "active" {
		t.Errorf("transient = %+v", r)
	}
	for _, n := range []string{"dev-sda.device", "session-1.scope", "nfs-server.service"} {
		if _, ok := by[n]; ok {
			t.Errorf("%s must not be added", n)
		}
	}
	if tm := by["apt-daily.timer"]; tm.ActiveState != "active" || tm.ActiveSince == "" || tm.Restarts != nil {
		t.Errorf("timer = %+v", tm)
	}
	for i := 1; i < len(units); i++ {
		if units[i-1].Name >= units[i].Name {
			t.Fatalf("units not sorted: %s, %s", units[i-1].Name, units[i].Name)
		}
	}

	// JSON: runtime fields only when known; restarts 0 is kept.
	b, _ := json.Marshal(by["getty@tty1.service"])
	if !strings.Contains(string(b), `"restarts":0`) || strings.Contains(string(b), "memory_bytes") {
		t.Errorf("getty json %s", b)
	}
	b, _ = json.Marshal(by["systemd-journald.service"])
	if strings.Contains(string(b), "active_state") {
		t.Errorf("unit unknown to systemd must not carry runtime fields: %s", b)
	}
}

func collectFixtureUnits(t *testing.T) []SystemdUnit {
	t.Helper()
	d := collectFixture(t, map[string]string{
		"/lib/systemd/system/ssh.service":                         "[Unit]\nDescription=OpenBSD Secure Shell server\n[Service]\nExecStart=/usr/sbin/sshd -D\n[Install]\nWantedBy=multi-user.target\n",
		"/etc/systemd/system/multi-user.target.wants/ssh.service": "",
		"/lib/systemd/system/cron.service":                        "[Unit]\nDescription=cron\n[Service]\nExecStart=/usr/sbin/cron -f\n[Install]\nWantedBy=multi-user.target\n",
		"/lib/systemd/system/backup.service":                      "[Unit]\nDescription=backup\n[Service]\nExecStart=/usr/local/bin/backup\n",
		"/lib/systemd/system/getty@.service":                      "[Unit]\nDescription=Getty on %I\n[Service]\nExecStart=-/sbin/agetty --noclear %I\n[Install]\nWantedBy=getty.target\n",
		"/lib/systemd/system/systemd-journald.service":            "[Unit]\nDescription=Journal Service\n[Service]\nExecStart=/lib/systemd/systemd-journald\n",
		"/lib/systemd/system/apt-daily.timer":                     "[Unit]\nDescription=Daily apt\n[Timer]\nOnCalendar=daily\n",
	})
	return d.Units
}

func TestSystemdDBusFallback(t *testing.T) {
	units := []SystemdUnit{{Name: "a.service", EnabledState: "enabled"}}
	// Missing socket: file data, errNoBus (not logged).
	got, err := collectUnitStates(context.Background(), filepath.Join(t.TempDir(), "absent.sock"), units, dialSystemBus)
	if !errors.Is(err, errNoBus) || len(got) != 1 || got[0].ActiveState != "" {
		t.Fatalf("missing socket: %v %+v", err, got)
	}
	// A socket that is not a D-Bus server (e.g. blocked by policy or wrong daemon): error, file data kept.
	dir, err := os.MkdirTemp("", "dbus")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "bus.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("REJECTED\r\n"))
			c.Close()
		}
	}()
	got, err = collectUnitStates(context.Background(), sock, units, dialSystemBus)
	if err == nil || errors.Is(err, errNoBus) || len(got) != 1 || got[0].ActiveState != "" {
		t.Fatalf("rejecting server: %v %+v", err, got)
	}
	if got, err := collectUnitStates(context.Background(), "", units, dialSystemBus); err != nil || len(got) != 1 {
		t.Fatalf("disabled: %v", err)
	}
}

// TestSystemdDBusLive reads a real system bus when one is reachable (Linux hosts with
// systemd; skipped elsewhere).
func TestSystemdDBusLive(t *testing.T) {
	if _, err := os.Stat(SystemdBusSocket); err != nil {
		t.Skip("no system bus")
	}
	got, err := collectUnitStates(context.Background(), SystemdBusSocket, nil, dialSystemBus)
	if err != nil {
		t.Skipf("system bus not usable: %v", err)
	}
	active := 0
	for _, u := range got {
		if u.ActiveState == "active" {
			active++
		}
	}
	t.Logf("%d services added from D-Bus, %d active", len(got), active)
	if active == 0 {
		t.Error("no active service")
	}
}
