// Package testfixtures provides synthetic Linux host trees shared by tests of
// the metrics, inventory, discovery and agent packages. Values follow the
// conventions of hostfstest.Build ("symlink:<target>", "<dir>").
package testfixtures

import (
	"maps"
	"strconv"
	"strings"
)

// BootTime is the btime of the fixture host (2024-01-01T00:00:00Z).
const BootTime = 1704067200

func procStat(pid, ppid int, comm string, state byte, startTicks int) string {
	return strconv.Itoa(pid) + " (" + comm + ") " + string(state) + " " + strconv.Itoa(ppid) +
		" 1 1 0 -1 4194560 100 0 0 0 150 50 0 0 20 0 1 0 " + strconv.Itoa(startTicks) + " 1000 100 18446744073709551615\n"
}

// Proc describes a fixture process.
type Proc struct {
	PID, PPID int
	Comm      string
	State     byte
	Exe       string // empty: unreadable exe
	Argv      []string
	UID       int
	Start     int // ticks after boot
	Cgroup    string
	Sockets   []int // socket inodes held as fds
}

// AddProc writes a process into the tree.
func AddProc(files map[string]string, p Proc) {
	base := "/proc/" + strconv.Itoa(p.PID)
	state := p.State
	if state == 0 {
		state = 'S'
	}
	files[base+"/stat"] = procStat(p.PID, p.PPID, p.Comm, state, p.Start)
	files[base+"/comm"] = p.Comm + "\n"
	files[base+"/cmdline"] = strings.Join(p.Argv, "\x00")
	if len(p.Argv) > 0 {
		files[base+"/cmdline"] += "\x00"
	}
	uid := strconv.Itoa(p.UID)
	files[base+"/status"] = "Name:\t" + p.Comm + "\nUid:\t" + uid + "\t" + uid + "\t" + uid + "\t" + uid + "\n"
	cg := p.Cgroup
	if cg == "" {
		cg = "/"
	}
	files[base+"/cgroup"] = "0::" + cg + "\n"
	if p.Exe != "" {
		files[base+"/exe"] = "symlink:" + p.Exe
	}
	files[base+"/fd/0"] = "symlink:/dev/null"
	for i, ino := range p.Sockets {
		files[base+"/fd/"+strconv.Itoa(3+i)] = "symlink:socket:[" + strconv.Itoa(ino) + "]"
	}
}

func hexPort(p int) string {
	s := strings.ToUpper(strconv.FormatInt(int64(p), 16))
	return strings.Repeat("0", 4-len(s)) + s
}

// TCPListenLine renders a /proc/net/tcp LISTEN line for a little-endian IPv4 hex address.
func TCPListenLine(addrHex string, port, inode int) string {
	return "   0: " + addrHex + ":" + hexPort(port) + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 " + strconv.Itoa(inode) + "\n"
}

// TCP6ListenLine renders a /proc/net/tcp6 LISTEN line.
func TCP6ListenLine(addrHex string, port, inode int) string {
	return "   0: " + addrHex + ":" + hexPort(port) + " 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 " + strconv.Itoa(inode) + "\n"
}

// UDPLine renders an unconnected /proc/net/udp line.
func UDPLine(addrHex string, port, inode int) string {
	return "   0: " + addrHex + ":" + hexPort(port) + " 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 " + strconv.Itoa(inode) + " 2 0000000000000000 0\n"
}

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// PlainHost returns a typical Ubuntu server with only base services
// (systemd, sshd, cron, journald, chrony) and some client-only packages.
func PlainHost() map[string]string {
	f := map[string]string{
		"/etc/os-release":            "PRETTY_NAME=\"Ubuntu 24.04 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\nID_LIKE=debian\n",
		"/etc/machine-id":            "4c4c4544003510528058b4c04f4e3232\n",
		"/etc/hostname":              "plain-01\n",
		"/proc/sys/kernel/hostname":  "plain-01\n",
		"/proc/sys/kernel/osrelease": "6.8.0-31-generic\n",
		"/proc/sys/kernel/version":   "#31-Ubuntu SMP PREEMPT_DYNAMIC Sat Apr 20 00:40:06 UTC 2024\n",
		"/proc/sys/kernel/arch":      "x86_64\n",
		"/proc/stat": "cpu  10000 200 3000 80000 500 60 70 80 0 0\ncpu0 5000 100 1500 40000 250 30 35 40 0 0\ncpu1 5000 100 1500 40000 250 30 35 40 0 0\n" +
			"btime " + strconv.Itoa(BootTime) + "\nprocs_running 2\n",
		"/proc/loadavg": "0.15 0.10 0.05 1/180 4321\n",
		"/proc/uptime":  "86400.50 170000.00\n",
		"/proc/meminfo": "MemTotal:        4000000 kB\nMemFree:         1000000 kB\nMemAvailable:    2500000 kB\nBuffers:          100000 kB\nCached:          1200000 kB\n" +
			"SwapCached:            0 kB\nSReclaimable:     200000 kB\nSwapTotal:       2000000 kB\nSwapFree:        1500000 kB\n",
		"/proc/cpuinfo": "processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Xeon(R) Gold 6230 CPU @ 2.10GHz\ncpu MHz\t\t: 2100.000\nphysical id\t: 0\ncore id\t\t: 0\ncpu cores\t: 2\n\n" +
			"processor\t: 1\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Xeon(R) Gold 6230 CPU @ 2.10GHz\ncpu MHz\t\t: 2100.000\nphysical id\t: 0\ncore id\t\t: 1\ncpu cores\t: 2\n\n",
		"/proc/modules": "overlay 151552 0 - Live 0x0000000000000000\nnf_tables 376832 1 nft_compat, Live 0x0000000000000000\n",
		"/proc/diskstats": "   7       0 loop0 50 0 100 0 0 0 0 0 0 0 0\n   8       0 sda 1000 10 20000 300 2000 20 40000 400 0 700 700\n" +
			"   8       1 sda1 900 10 18000 300 1900 20 38000 400 0 700 700\n",
		"/proc/1/mountinfo": "22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro\n" +
			"23 22 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw\n" +
			"24 22 0:22 / /run rw,nosuid,nodev shared:5 - tmpfs tmpfs rw,size=400000k\n" +
			"25 22 8:15 / /boot/efi rw,relatime shared:3 - vfat /dev/sda15 rw,fmask=0077\n",
		"/proc/1/net/dev": "Inter-|   Receive                                                |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
			"    lo:  1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0\n" +
			"  eth0: 500000  4000    1    2    0     0          0         0   300000    3000    0    1    0     0       0          0\n",
		"/proc/1/net/tcp":  tcpHeader + TCPListenLine("00000000", 22, 2001) + TCPListenLine("3500007F", 53, 2002),
		"/proc/1/net/tcp6": tcpHeader + TCP6ListenLine("00000000000000000000000000000000", 22, 2003),
		"/proc/1/net/udp":  tcpHeader + UDPLine("0100007F", 323, 2004) + UDPLine("3500007F", 53, 2005),
		"/proc/1/net/udp6": tcpHeader,
		"/etc/passwd": "root:x:0:0:root:/root:/bin/bash\ndaemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n" +
			"_chrony:x:111:112:Chrony daemon,,,:/var/lib/chrony:/usr/sbin/nologin\nubuntu:x:1000:1000:Ubuntu:/home/ubuntu:/bin/bash\n",
		"/sys/class/net/lo/address":         "00:00:00:00:00:00\n",
		"/sys/class/net/lo/mtu":             "65536\n",
		"/sys/class/net/lo/operstate":       "unknown\n",
		"/sys/class/net/eth0/address":       "52:54:00:12:34:56\n",
		"/sys/class/net/eth0/mtu":           "1500\n",
		"/sys/class/net/eth0/operstate":     "up\n",
		"/sys/class/dmi/id/sys_vendor":      "QEMU\n",
		"/sys/class/dmi/id/product_name":    "Standard PC (Q35 + ICH9, 2009)\n",
		"/sys/class/dmi/id/product_version": "pc-q35-8.2\n",
		"/sys/class/dmi/id/bios_vendor":     "SeaBIOS\n",
		"/sys/class/dmi/id/bios_version":    "1.16.3-debian-1.16.3-2\n",
		"/sys/class/dmi/id/product_serial":  "SECRET-SERIAL\n",
		"/var/lib/dpkg/status": dpkg("base-files", "13ubuntu10", "amd64") + dpkg("bash", "5.2.21-2ubuntu4", "amd64") +
			dpkg("openssh-server", "1:9.6p1-3ubuntu13", "amd64") + dpkg("cron", "3.0pl1-184ubuntu2", "amd64") +
			dpkg("chrony", "4.5-1ubuntu4", "amd64") + dpkg("systemd", "255.4-1ubuntu8", "amd64") +
			dpkg("redis-tools", "5:7.0.15-1build2", "amd64") + dpkg("mysql-common", "5.8+1.1.0build1", "all") +
			dpkg("postgresql-client-common", "257build1", "all") + dpkg("python3", "3.12.3-0ubuntu1", "amd64") +
			"Package: removed-pkg\nStatus: deinstall ok config-files\nVersion: 1.0\nArchitecture: amd64\n\n",
		"/lib/systemd/system/ssh.service":                            unit("OpenBSD Secure Shell server", "/usr/sbin/sshd -D $SSHD_OPTS", "multi-user.target"),
		"/lib/systemd/system/cron.service":                           unit("Regular background program processing daemon", "/usr/sbin/cron -f -P", "multi-user.target"),
		"/lib/systemd/system/chrony.service":                         unit("chrony, an NTP client/server", "/usr/sbin/chronyd -F 1 $DAEMON_OPTS", "multi-user.target"),
		"/lib/systemd/system/systemd-journald.service":               unit("Journal Service", "/lib/systemd/systemd-journald", ""),
		"/lib/systemd/system/getty@.service":                         unit("Getty on %I", "-/sbin/agetty -o '-p -- \\\\u' --noclear - $TERM", "getty.target"),
		"/lib/systemd/system/backup.service":                         unit("Nightly backup", "/usr/local/bin/backup --password=hunter2 \\\n  --target s3://bucket", "multi-user.target"),
		"/lib/systemd/system/apt-daily.timer":                        "[Unit]\nDescription=Daily apt download activities\n\n[Timer]\nOnCalendar=*-*-* 6,18:00\n\n[Install]\nWantedBy=timers.target\n",
		"/lib/systemd/system/multi-user.target":                      "[Unit]\nDescription=Multi-User System\n",
		"/lib/systemd/system/snapd.service":                          unit("Snap Daemon", "/usr/lib/snapd/snapd", "multi-user.target"),
		"/etc/systemd/system/snapd.service":                          "symlink:/dev/null",
		"/etc/systemd/system/sshd.service":                           "symlink:/lib/systemd/system/ssh.service",
		"/etc/systemd/system/multi-user.target.wants/cron.service":   "symlink:/lib/systemd/system/cron.service",
		"/etc/systemd/system/multi-user.target.wants/chrony.service": "symlink:/lib/systemd/system/chrony.service",
		"/etc/systemd/system/getty.target.wants/getty@tty1.service":  "symlink:/lib/systemd/system/getty@.service",
		"/etc/systemd/system/timers.target.wants/apt-daily.timer":    "symlink:/lib/systemd/system/apt-daily.timer",
	}
	sys := "/init.scope"
	AddProc(f, Proc{PID: 1, Comm: "systemd", Exe: "/usr/lib/systemd/systemd", Argv: []string{"/sbin/init"}, Start: 1, Cgroup: sys})
	AddProc(f, Proc{PID: 2, Comm: "kthreadd", Start: 1})
	AddProc(f, Proc{PID: 15, PPID: 2, Comm: "kworker/0:1-events", State: 'I', Start: 2})
	AddProc(f, Proc{PID: 310, PPID: 1, Comm: "systemd-journal", Exe: "/usr/lib/systemd/systemd-journald", Argv: []string{"/lib/systemd/systemd-journald"}, Start: 300, Cgroup: "/system.slice/systemd-journald.service"})
	AddProc(f, Proc{PID: 650, PPID: 1, Comm: "cron", Exe: "/usr/sbin/cron", Argv: []string{"/usr/sbin/cron", "-f", "-P"}, Start: 900, Cgroup: "/system.slice/cron.service"})
	AddProc(f, Proc{PID: 680, PPID: 1, Comm: "chronyd", Exe: "/usr/sbin/chronyd", Argv: []string{"/usr/sbin/chronyd", "-F", "1"}, UID: 111, Start: 950, Cgroup: "/system.slice/chrony.service", Sockets: []int{2004}})
	AddProc(f, Proc{PID: 700, PPID: 1, Comm: "sshd", Exe: "/usr/sbin/sshd", Argv: []string{"sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups"}, Start: 1000, Cgroup: "/system.slice/ssh.service", Sockets: []int{2001, 2003}})
	AddProc(f, Proc{PID: 1200, PPID: 700, Comm: "sshd", Exe: "/usr/sbin/sshd", Argv: []string{"sshd: ubuntu [priv]"}, Start: 5000, Cgroup: "/system.slice/ssh.service"})
	AddProc(f, Proc{PID: 1300, PPID: 1250, Comm: "bash", Exe: "/usr/bin/bash", Argv: []string{"-bash"}, UID: 1000, Start: 5100, Cgroup: "/user.slice/user-1000.slice/session-1.scope"})
	AddProc(f, Proc{PID: 1400, PPID: 1300, Comm: "mysql", Exe: "/usr/bin/mysql", Argv: []string{"mysql", "-uroot", "-psupersecret", "--host", "db.internal"}, UID: 1000, Start: 5200, Cgroup: "/user.slice/user-1000.slice/session-1.scope"})
	AddProc(f, Proc{PID: 1500, PPID: 1300, Comm: "redis-cli", Exe: "/usr/bin/redis-cli", Argv: []string{"redis-cli", "-h", "cache", "-a", "x"}, UID: 1000, Start: 5300, Cgroup: "/user.slice/user-1000.slice/session-1.scope", State: 'T'})
	return f
}

func dpkg(name, version, arch string) string {
	return "Package: " + name + "\nStatus: install ok installed\nPriority: optional\nVersion: " + version + "\nArchitecture: " + arch + "\nDescription: " + name + "\n multi-line description\n\n"
}

func unit(desc, exec, wantedBy string) string {
	s := "[Unit]\nDescription=" + desc + "\nAfter=network.target\n\n[Service]\nExecStart=" + exec + "\n"
	if wantedBy != "" {
		s += "\n[Install]\nWantedBy=" + wantedBy + "\n"
	}
	return s
}

// ServiceHost extends PlainHost with nginx and redis managed by systemd.
func ServiceHost() map[string]string {
	f := maps.Clone(PlainHost())
	f["/var/lib/dpkg/status"] += dpkg("nginx", "1.24.0-2ubuntu7", "amd64") + dpkg("nginx-common", "1.24.0-2ubuntu7", "all") +
		dpkg("redis-server", "5:7.0.15-1build2", "amd64")
	f["/lib/systemd/system/nginx.service"] = unit("A high performance web server and a reverse proxy server", "/usr/sbin/nginx -g 'daemon on; master_process on;'", "multi-user.target")
	f["/lib/systemd/system/redis-server.service"] = unit("Advanced key-value store", "/usr/bin/redis-server /etc/redis/redis.conf --supervised systemd --daemonize no", "multi-user.target")
	f["/etc/systemd/system/multi-user.target.wants/nginx.service"] = "symlink:/lib/systemd/system/nginx.service"
	f["/etc/systemd/system/multi-user.target.wants/redis-server.service"] = "symlink:/lib/systemd/system/redis-server.service"
	f["/proc/1/net/tcp"] += TCPListenLine("00000000", 80, 3001) + TCPListenLine("0100007F", 6379, 3002)
	f["/proc/1/net/tcp6"] += TCP6ListenLine("00000000000000000000000000000000", 80, 3003)
	AddProc(f, Proc{PID: 900, PPID: 1, Comm: "nginx", Exe: "/usr/sbin/nginx", Argv: []string{"nginx: master process /usr/sbin/nginx -g daemon on; master_process on;"}, Start: 1100, Cgroup: "/system.slice/nginx.service", Sockets: []int{3001, 3003}})
	AddProc(f, Proc{PID: 901, PPID: 900, Comm: "nginx", Exe: "/usr/sbin/nginx", Argv: []string{"nginx: worker process"}, UID: 33, Start: 1101, Cgroup: "/system.slice/nginx.service", Sockets: []int{3001, 3003}})
	AddProc(f, Proc{PID: 812, PPID: 1, Comm: "redis-server", Exe: "/usr/bin/redis-server", Argv: []string{"/usr/bin/redis-server 127.0.0.1:6379"}, UID: 112, Start: 1050, Cgroup: "/system.slice/redis-server.service", Sockets: []int{3002}})
	return f
}
