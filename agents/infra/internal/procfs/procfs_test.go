package procfs

import (
	"reflect"
	"testing"
	"time"
)

func TestParseStat(t *testing.T) {
	data := []byte(`cpu  1000 200 300 4000 50 6 7 8 0 0
cpu0 500 100 150 2000 25 3 3 4 0 0
cpu1 500 100 150 2000 25 3 4 4 0 0
intr 12345
ctxt 999
btime 1700000000
processes 4242
procs_running 3
procs_blocked 0
`)
	st, err := ParseStat(data)
	if err != nil {
		t.Fatal(err)
	}
	want := CPUTimes{10, 2, 3, 40, 0.5, 0.06, 0.07, 0.08}
	if st.CPU != want {
		t.Errorf("cpu = %+v, want %+v", st.CPU, want)
	}
	if st.LogicalCPUs != 2 || st.ProcsRunning != 3 || !st.BootTime.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("stat = %+v", st)
	}
	if _, err := ParseStat([]byte("btime 1\n")); err == nil {
		t.Error("expected error without cpu line")
	}
}

func TestParseLoadAvgMeminfoUptime(t *testing.T) {
	la, err := ParseLoadAvg([]byte("0.50 1.25 2.00 2/345 6789\n"))
	if err != nil || la != (LoadAvg{0.5, 1.25, 2}) {
		t.Errorf("loadavg = %+v %v", la, err)
	}
	mi := ParseMeminfo([]byte("MemTotal:       16000 kB\nMemFree:  4000 kB\nHugePages_Total:       0\n"))
	if mi["MemTotal"] != 16000*1024 || mi["MemFree"] != 4000*1024 || mi["HugePages_Total"] != 0 {
		t.Errorf("meminfo = %v", mi)
	}
	up, err := ParseUptime([]byte("12345.67 999.00\n"))
	if err != nil || up != 12345.67 {
		t.Errorf("uptime = %v %v", up, err)
	}
}

func TestParseMountinfo(t *testing.T) {
	data := []byte(`22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro
23 22 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw
24 22 8:2 / /mnt/my\040disk rw,relatime - xfs /dev/sdb1 rw
`)
	got := ParseMountinfo(data)
	want := []Mount{
		{MountPoint: "/", Device: "/dev/sda1", FSType: "ext4", Options: "rw,relatime,rw,errors=remount-ro", Root: "/", MajorMinor: "8:1"},
		{MountPoint: "/proc", Device: "proc", FSType: "proc", Options: "rw,nosuid,nodev,noexec,relatime,rw", Root: "/", MajorMinor: "0:21"},
		{MountPoint: "/mnt/my disk", Device: "/dev/sdb1", FSType: "xfs", Options: "rw,relatime,rw", Root: "/", MajorMinor: "8:2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mountinfo:\n got  %+v\n want %+v", got, want)
	}
}

func TestParseDiskstatsNetDev(t *testing.T) {
	ds := ParseDiskstats([]byte("   8       0 sda 100 5 2000 30 200 6 4000 40 0 50 70\n   7       0 loop0 1 0 2 0 0 0 0 0 0 0 0\n"))
	if len(ds) != 2 || ds[0] != (DiskStat{"sda", 100, 2000, 200, 4000}) {
		t.Errorf("diskstats = %+v", ds)
	}
	nd := ParseNetDev([]byte(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0
  eth0: 5000      50    1    2    0     0          0         0     6000      60    3    4    0     0       0          0
`))
	if len(nd) != 2 || nd[1] != (NetDevStat{"eth0", 5000, 50, 1, 2, 6000, 60, 3, 4}) {
		t.Errorf("netdev = %+v", nd)
	}
}

func TestParsePIDStat(t *testing.T) {
	st, err := ParsePIDStat([]byte("812 (redis-server) S 1 812 812 0 -1 4194560 1 0 0 0 150 50 0 0 20 0 5 0 4200 1000 100 18446744073709551615\n"))
	if err != nil {
		t.Fatal(err)
	}
	if st.PID != 812 || st.Comm != "redis-server" || st.State != 'S' || st.PPID != 1 || st.UTime != 150 || st.STime != 50 || st.StartTime != 4200 {
		t.Errorf("stat = %+v", st)
	}
	st, err = ParsePIDStat([]byte("5 (weird ) name) R 2 0 0 0 -1 0 0 0 0 0 1 2 0 0 20 0 1 0 99 0 0\n"))
	if err != nil || st.Comm != "weird ) name" || st.State != 'R' || st.StartTime != 99 {
		t.Errorf("stat = %+v %v", st, err)
	}
	if ProcessStatus('D') != "disk_sleep" || ProcessStatus('X') != "other" || ProcessStatus('I') != "idle" {
		t.Error("status mapping")
	}
}

func TestParseStatusCmdline(t *testing.T) {
	uid, ok := ParseStatusUID([]byte("Name:\tnginx\nUid:\t33\t33\t33\t33\nGid:\t33\n"))
	if !ok || uid != 33 {
		t.Errorf("uid = %d %v", uid, ok)
	}
	if got := ParseCmdline([]byte("/usr/bin/redis-server\x00127.0.0.1:6379\x00\x00")); got != "/usr/bin/redis-server 127.0.0.1:6379" {
		t.Errorf("cmdline = %q", got)
	}
}

func TestParseCgroup(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		name, in, unit, cid string
	}{
		{"v2 service", "0::/system.slice/nginx.service\n", "nginx.service", ""},
		{"v2 user nested", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-foo.scope\n", "app-foo.scope", ""},
		{"v2 docker scope", "0::/system.slice/docker-" + id + ".scope\n", "", id},
		{"v1 cgroupfs docker", "12:memory:/docker/" + id + "\n1:name=systemd:/docker/" + id + "\n", "", id},
		{"kubepods", "0::/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod1.slice/cri-containerd-" + id + ".scope\n", "", id},
		{"root", "0::/\n", "", ""},
		{"v1 systemd", "11:cpu:/\n1:name=systemd:/system.slice/ssh.service\n", "ssh.service", ""},
	}
	for _, c := range cases {
		u, cid := ParseCgroup([]byte(c.in))
		if u != c.unit || cid != c.cid {
			t.Errorf("%s: got (%q,%q) want (%q,%q)", c.name, u, cid, c.unit, c.cid)
		}
	}
}

func TestParseNetSockets(t *testing.T) {
	tcp := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:18EB 00000000:0000 0A 00000000:00000000 00:00000000 00000000   101        0 18923
   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 17854
   2: 0100007F:18EB 0100007F:D2A4 01 00000000:00000000 00:00000000 00000000   101        0 55555
`)
	got := ParseNetSockets(tcp, "tcp", 4)
	want := []Socket{
		{"tcp", 4, "127.0.0.1", 6379, 18923},
		{"tcp", 4, "0.0.0.0", 22, 17854},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tcp = %+v", got)
	}
	tcp6 := []byte(`  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 17856
   1: 00000000000000000000000001000000:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 17857
`)
	got = ParseNetSockets(tcp6, "tcp", 6)
	if len(got) != 2 || got[0].Address != "::" || got[0].Port != 22 || got[1].Address != "::1" || got[1].Port != 80 {
		t.Errorf("tcp6 = %+v", got)
	}
	udp := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  123: 3500007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   101        0 18922 2 0000000000000000 0
  124: 0F02000A:B1C2 08080808:0035 07 00000000:00000000 00:00000000 00000000   101        0 18999 2 0000000000000000 0
`)
	got = ParseNetSockets(udp, "udp", 4)
	if len(got) != 1 || got[0].Address != "127.0.0.53" || got[0].Port != 53 || got[0].Key() != "udp:127.0.0.53:53" || PortKey("tcp", "::1", 80) != "tcp:[::1]:80" {
		t.Errorf("udp = %+v", got)
	}
	if ino, ok := SocketInode("socket:[18923]"); !ok || ino != 18923 {
		t.Error("SocketInode")
	}
	if _, ok := SocketInode("pipe:[1]"); ok {
		t.Error("pipe should not parse")
	}
}

func TestParseKeyValueFile(t *testing.T) {
	kv := ParseKeyValueFile([]byte("# comment\nID=ubuntu\nVERSION_ID=\"24.04\"\nPRETTY_NAME='Ubuntu 24.04 LTS'\n"))
	if kv["ID"] != "ubuntu" || kv["VERSION_ID"] != "24.04" || kv["PRETTY_NAME"] != "Ubuntu 24.04 LTS" {
		t.Errorf("kv = %v", kv)
	}
}
