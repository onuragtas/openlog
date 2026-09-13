package inventory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
	"github.com/onuragtas/openlog/agents/infra/internal/testfixtures"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func collectFixture(t *testing.T, files map[string]string) *Data {
	t.Helper()
	fs := hostfstest.Build(t, files)
	c := &Collector{FS: fs, Stats: selfmon.New(time.Now()), InterfaceAddrs: func(name string) []string {
		if name == "eth0" {
			return []string{"10.0.0.5/24", "fe80::5054:ff:fe12:3456/64"}
		}
		return nil
	}}
	return c.Collect()
}

func TestCollectPlainHost(t *testing.T) {
	d := collectFixture(t, testfixtures.PlainHost())

	wantOS := &OSInfo{ID: "ubuntu", Name: "Ubuntu", VersionID: "24.04", PrettyName: "Ubuntu 24.04 LTS", KernelRelease: "6.8.0-31-generic",
		KernelVersion: "#31-Ubuntu SMP PREEMPT_DYNAMIC Sat Apr 20 00:40:06 UTC 2024", Arch: "amd64", Hostname: "plain-01", BootTime: "2024-01-01T00:00:00Z"}
	if !reflect.DeepEqual(d.OS, wantOS) {
		t.Errorf("os = %+v", d.OS)
	}
	if d.CPU == nil || *d.CPU != (CPUInfo{Vendor: "GenuineIntel", Model: "Intel(R) Xeon(R) Gold 6230 CPU @ 2.10GHz", LogicalCores: 2, PhysicalCores: 2, Sockets: 1, MHz: 2100}) {
		t.Errorf("cpu = %+v", d.CPU)
	}
	if d.Memory == nil || d.Memory.TotalBytes != 4000000*1024 || d.Memory.SwapTotalBytes != 2000000*1024 {
		t.Errorf("memory = %+v", d.Memory)
	}
	if d.DMI == nil || d.DMI.SysVendor != "QEMU" || d.DMI.BIOSVersion != "1.16.3-debian-1.16.3-2" {
		t.Errorf("dmi = %+v", d.DMI)
	}
	if b, _ := json.Marshal(d.DMI); strings.Contains(string(b), "SECRET") {
		t.Error("dmi must not include serial numbers")
	}
	if len(d.Modules) != 2 || d.Modules[1] != (KernelModule{"nf_tables", 376832, "live"}) {
		t.Errorf("modules = %+v", d.Modules)
	}

	// Packages: installed only, sorted by key.
	pk := map[string]Package{}
	for _, p := range d.Packages {
		pk[p.Key()] = p
	}
	if len(d.Packages) != 10 || pk["dpkg:openssh-server"].Version != "1:9.6p1-3ubuntu13" || pk["dpkg:mysql-common"].Arch != "all" {
		t.Errorf("packages = %+v", d.Packages)
	}
	if _, ok := pk["dpkg:removed-pkg"]; ok {
		t.Error("deinstalled package must be skipped")
	}

	units := map[string]SystemdUnit{}
	for _, u := range d.Units {
		units[u.Name] = u
	}
	checks := map[string]string{
		"cron.service": "enabled", "chrony.service": "enabled", "ssh.service": "enabled", // enabled via sshd.service alias
		"systemd-journald.service": "static", "getty@.service": "enabled", "backup.service": "disabled",
		"snapd.service": "masked", "apt-daily.timer": "enabled", "multi-user.target": "static",
	}
	for name, state := range checks {
		if units[name].EnabledState != state {
			t.Errorf("unit %s enabled_state = %q, want %q", name, units[name].EnabledState, state)
		}
	}
	if _, ok := units["sshd.service"]; ok {
		t.Error("alias symlink must not become its own unit")
	}
	if u := units["backup.service"]; u.ExecStart != "/usr/local/bin/backup --password=*** --target s3://bucket" || u.Type != "service" || u.Path != "/lib/systemd/system/backup.service" {
		t.Errorf("backup unit = %+v", u)
	}
	if units["cron.service"].Description != "Regular background program processing daemon" || units["apt-daily.timer"].Type != "timer" {
		t.Error("unit description/type")
	}

	ports := map[string]ListeningPort{}
	for _, p := range d.Ports {
		ports[p.Key()] = p
	}
	if p := ports["tcp:0.0.0.0:22"]; p.PID != 700 || p.ProcessName != "sshd" || p.ProcessExe != "/usr/sbin/sshd" || p.Family != 4 {
		t.Errorf("port 22 = %+v", p)
	}
	if p := ports["tcp:[::]:22"]; p.PID != 700 || p.Family != 6 {
		t.Errorf("port 22 v6 = %+v", p)
	}
	if p := ports["udp:127.0.0.1:323"]; p.PID != 680 || p.Protocol != "udp" {
		t.Errorf("udp 323 = %+v", p)
	}
	if p, ok := ports["tcp:127.0.0.53:53"]; !ok || p.PID != 0 {
		t.Errorf("unowned port = %+v %v", p, ok)
	}

	procs := map[string]Process{}
	for _, p := range d.Processes {
		procs[p.Key()] = p
	}
	sshd := procs["/usr/sbin/sshd"]
	if sshd.Count != 2 || !reflect.DeepEqual(sshd.PIDs, []int{700, 1200}) || sshd.SystemdUnit != "ssh.service" ||
		sshd.StartTime != "2024-01-01T00:00:10Z" || !reflect.DeepEqual(sshd.UIDs, []int{0}) || sshd.Name != "sshd" {
		t.Errorf("sshd = %+v", sshd)
	}
	if mysql := procs["/usr/bin/mysql"]; mysql.Cmdline != "mysql -uroot -p*** --host db.internal" {
		t.Errorf("mysql cmdline not masked: %q", mysql.Cmdline)
	}
	for k := range procs {
		if strings.Contains(k, "kworker") || strings.Contains(k, "kthreadd") {
			t.Errorf("kernel thread reported: %s", k)
		}
	}
	if len(d.Instances) != 9 {
		t.Errorf("instances = %d", len(d.Instances))
	}

	if len(d.Users) != 4 || d.Users[2] != (User{"_chrony", 111, 112, "/var/lib/chrony", "/usr/sbin/nologin"}) {
		t.Errorf("users = %+v", d.Users)
	}
	if len(d.Interfaces) != 2 || d.Interfaces[0].Name != "eth0" || d.Interfaces[0].MTU != 1500 || d.Interfaces[0].MAC != "52:54:00:12:34:56" ||
		len(d.Interfaces[0].Addresses) != 2 || d.Interfaces[1].Addresses == nil {
		t.Errorf("interfaces = %+v", d.Interfaces)
	}
	if len(d.Mounts) != 4 || d.Mounts[0] != (Mount{"/", "/dev/sda1", "ext4", "rw,relatime,rw,errors=remount-ro"}) {
		t.Errorf("mounts = %+v", d.Mounts)
	}
}

func TestSnapshotLogRecords(t *testing.T) {
	d := collectFixture(t, testfixtures.PlainHost())
	items := Normalize(append(d.Items(), Item{CategoryOS, "os", d.OS}))
	snap := &Snapshot{ID: "0190-snap", Time: time.Unix(1704067200, 5), Items: items}
	recs, err := snap.LogRecords(time.Unix(1704067201, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(items)+1 {
		t.Fatalf("records = %d items = %d", len(recs), len(items))
	}
	seen := map[string]bool{}
	for i, r := range recs[:len(recs)-1] {
		a := attrs(r.Attributes)
		if a[AttrEventName] != EventItem || a[AttrSnapshotID] != "0190-snap" || r.TimeUnixNano != uint64(snap.Time.UnixNano()) {
			t.Fatalf("record %d attrs = %v", i, a)
		}
		k := a[AttrCategory] + "|" + a[AttrKey]
		if seen[k] {
			t.Errorf("duplicate key %s", k)
		}
		seen[k] = true
		var body map[string]any
		if err := json.Unmarshal([]byte(r.Body.GetStringValue()), &body); err != nil {
			t.Errorf("body of %s is not a JSON object: %v", k, err)
		}
	}
	last := recs[len(recs)-1]
	la := attrs(last.Attributes)
	if la[AttrEventName] != EventSnapshot || la[AttrSnapshotID] != "0190-snap" || last.Body != nil {
		t.Errorf("snapshot record = %v", la)
	}
	for _, kv := range last.Attributes {
		if kv.Key == AttrItemCount && kv.Value.GetIntValue() != int64(len(items)) {
			t.Errorf("item_count = %d", kv.Value.GetIntValue())
		}
	}
	for _, cat := range []string{CategoryOS, CategoryHardware, CategoryKernelModule, CategoryPackage, CategorySystemdUnit,
		CategoryListeningPort, CategoryProcess, CategoryUser, CategoryNetworkInterface, CategoryMount} {
		found := false
		for k := range seen {
			if strings.HasPrefix(k, cat+"|") {
				found = true
			}
		}
		if !found {
			t.Errorf("category %s missing from snapshot", cat)
		}
	}
}

func attrs(kvs []*commonKeyValue) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs {
		out[kv.Key] = kv.Value.GetStringValue()
	}
	return out
}

func TestParsers(t *testing.T) {
	apk := ParseApkInstalled([]byte("C:Q1abc=\nP:musl\nV:1.2.4-r2\nA:x86_64\nS:383152\n\nP:redis\nV:7.2.4-r0\nA:aarch64\n\n"))
	if len(apk) != 2 || apk[1] != (Package{"apk", "redis", "7.2.4-r0", "aarch64"}) {
		t.Errorf("apk = %+v", apk)
	}
	arm := ParseCPUInfo([]byte("processor\t: 0\nBogoMIPS\t: 50.00\nCPU implementer\t: 0x41\nCPU part\t: 0xd0c\n\nprocessor\t: 1\nCPU implementer\t: 0x41\n"))
	if arm.Vendor != "ARM" || arm.LogicalCores != 2 || arm.PhysicalCores != 2 || arm.Sockets != 1 {
		t.Errorf("arm cpu = %+v", arm)
	}
	uf := ParseUnitFile([]byte("[Unit]\nDescription=  Foo   bar\n# ExecStart=/nope\n[Service]\nExecStart=/a\nExecStart=\nExecStart=/b \\\n  --x\n[Install]\nWantedBy=\n"))
	if uf.Description != "Foo bar" || !reflect.DeepEqual(uf.ExecStart, []string{"/b --x"}) || uf.HasInstall {
		t.Errorf("unit file = %+v", uf)
	}
	if templateName("getty@tty1.service") != "getty@.service" || templateName("foo.service") != "" || templateName("foo@.service") != "" {
		t.Error("templateName")
	}
	if len(ParsePasswd([]byte("+nis\n#c\nroot:x:0:0::/root:/bin/sh\n"))) != 1 {
		t.Error("passwd")
	}
}

func TestGroupProcessesLimits(t *testing.T) {
	var ins []ProcessInstance
	for i := 30; i > 0; i-- {
		ins = append(ins, ProcessInstance{PID: i, Exe: "/usr/bin/php-fpm8.2", Comm: "php-fpm8.2", UID: i % 2, HasUID: true,
			StartTime: time.Unix(int64(1000+i), 0)})
	}
	ins = append(ins, ProcessInstance{PID: 99, Comm: "mystery", Cmdline: "mystery"})
	g := GroupProcesses(ins)
	if len(g) != 2 {
		t.Fatalf("groups = %d", len(g))
	}
	php := g[0]
	if php.Key() != "/usr/bin/php-fpm8.2" || php.Count != 30 || len(php.PIDs) != 20 || php.PIDs[0] != 1 ||
		!reflect.DeepEqual(php.UIDs, []int{0, 1}) || php.StartTime != time.Unix(1001, 0).UTC().Format(time.RFC3339) {
		t.Errorf("php = %+v", php)
	}
	if g[1].Key() != "comm:mystery" || g[1].Exe != "" {
		t.Errorf("comm group = %+v", g[1])
	}
	in := ProcessInstance{Comm: "nginx", Cmdline: "nginx: master process /usr/sbin/nginx"}
	if in.ExeBasename() != "nginx" {
		t.Errorf("basename fallback = %q", in.ExeBasename())
	}
}

type commonKeyValue = commonpb.KeyValue
