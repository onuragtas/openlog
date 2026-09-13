package integrations

import (
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/inventory"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
)

// maxFallbackPIDs bounds the /proc/<pid>/fd directories read per container.
const maxFallbackPIDs = 16

// ProcessContainers completes the container list used for endpoint derivation
// when the container runtime API is unavailable (e.g. docker.sock permission
// denied): for every container id seen in process cgroups that the runtime did
// not describe, it reads the container's network namespace through
// /proc/<pid>/net (listening ports and local addresses) and maps published
// ports from docker-proxy command lines. Containers sharing the host network
// namespace are skipped: their ports already are host listening ports.
// The returned list starts with known.
func ProcessContainers(fs *hostfs.FS, instances []inventory.ProcessInstance, known []containers.Container) []containers.Container {
	if fs == nil {
		return known
	}
	described := map[string]bool{}
	for _, c := range known {
		if len(c.Ports) > 0 || len(c.IPs) > 0 {
			described[c.ID] = true
		}
	}
	pids := map[string][]int{}
	var proxies []dockerProxy
	for _, in := range instances {
		if in.ContainerID == "" {
			if p, ok := parseDockerProxy(in); ok {
				proxies = append(proxies, p)
			}
			continue
		}
		if !described[in.ContainerID] {
			pids[in.ContainerID] = append(pids[in.ContainerID], in.PID)
		}
	}
	if len(pids) == 0 {
		return known
	}
	hostNS, _ := fs.Readlink(fs.ProcSelf() + "/ns/net")
	hostInodes := map[uint64]bool{}
	for _, s := range netSockets(fs, fs.ProcSelf()) {
		hostInodes[s.Inode] = true
	}

	ids := make([]string, 0, len(pids))
	for id := range pids {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := slices.Clone(known)
	byIP := map[string]int{} // container IP → index in out
	var added []int
	for _, id := range ids {
		ps := pids[id]
		sort.Ints(ps)
		dir := "/proc/" + strconv.Itoa(ps[0])
		if ns, err := fs.Readlink(dir + "/ns/net"); err == nil && hostNS != "" && ns == hostNS {
			continue
		}
		socks := netSockets(fs, dir)
		shared := false
		for _, s := range socks {
			shared = shared || hostInodes[s.Inode]
		}
		if shared {
			continue // same namespace as the host (ns links unreadable)
		}
		owned := ownedInodes(fs, ps)
		c := containers.Container{ID: id}
		seen := map[int]bool{}
		for _, s := range socks {
			if s.Protocol != "tcp" || seen[s.Port] || (owned != nil && !owned[s.Inode]) {
				continue
			}
			if a, err := netip.ParseAddr(s.Address); err == nil && a.IsLoopback() {
				continue // not reachable from the host (e.g. Docker's embedded DNS on 127.0.0.11)
			}
			seen[s.Port] = true
			c.Ports = append(c.Ports, containers.Port{PrivatePort: s.Port, Protocol: "tcp"})
		}
		if b, err := fs.ReadFile(dir + "/net/fib_trie"); err == nil {
			c.IPs = LocalAddresses(string(b))
		}
		if len(c.Ports) == 0 && len(c.IPs) == 0 {
			continue
		}
		sort.Slice(c.Ports, func(i, j int) bool { return c.Ports[i].PrivatePort < c.Ports[j].PrivatePort })
		out = append(out, c)
		added = append(added, len(out)-1)
		for _, ip := range c.IPs {
			byIP[ip] = len(out) - 1
		}
	}

	// Published ports: docker-proxy -container-ip <ip> -container-port <p> -host-ip <h> -host-port <q>.
	for _, p := range proxies {
		idx, ok := byIP[p.containerIP]
		if !ok {
			// Without the container's addresses, attribute the port only when a
			// single fallback container listens on the container port.
			idx = -1
			for _, i := range added {
				if slices.ContainsFunc(out[i].Ports, func(cp containers.Port) bool { return cp.PrivatePort == p.containerPort }) {
					if idx >= 0 {
						idx = -2
						break
					}
					idx = i
				}
			}
			if idx < 0 {
				continue
			}
		}
		c := &out[idx]
		if slices.ContainsFunc(c.Ports, func(cp containers.Port) bool { return cp.PublicPort == p.hostPort }) {
			continue
		}
		c.Ports = append(c.Ports, containers.Port{IP: p.hostIP, PrivatePort: p.containerPort, PublicPort: p.hostPort, Protocol: "tcp"})
	}
	return out
}

func netSockets(fs *hostfs.FS, procDir string) []procfs.Socket {
	var out []procfs.Socket
	for _, f := range []struct {
		name   string
		family int
	}{{"tcp", 4}, {"tcp6", 6}} {
		if b, err := fs.ReadFile(procDir + "/net/" + f.name); err == nil {
			out = append(out, procfs.ParseNetSockets(b, "tcp", f.family)...)
		}
	}
	return out
}

// ownedInodes returns the socket inodes held by pids, or nil when no fd
// directory was readable (then every listening socket of the namespace counts).
func ownedInodes(fs *hostfs.FS, pids []int) map[uint64]bool {
	var owned map[uint64]bool
	for _, pid := range pids[:min(len(pids), maxFallbackPIDs)] {
		base := "/proc/" + strconv.Itoa(pid) + "/fd"
		fds, err := fs.ReadDir(base)
		if err != nil {
			continue
		}
		if owned == nil {
			owned = map[uint64]bool{}
		}
		for _, fd := range fds {
			if link, err := fs.Readlink(base + "/" + fd.Name()); err == nil {
				if ino, ok := procfs.SocketInode(link); ok {
					owned[ino] = true
				}
			}
		}
	}
	return owned
}

// LocalAddresses returns the non-loopback local IPv4 addresses listed in a
// /proc/<pid>/net/fib_trie ("|-- 172.17.0.2" followed by "/32 host LOCAL").
func LocalAddresses(fibTrie string) []string {
	var out []string
	last := ""
	for line := range strings.Lines(fibTrie) {
		t := strings.TrimSpace(line)
		if ip, ok := strings.CutPrefix(t, "|-- "); ok {
			last = ip
			continue
		}
		if last != "" && strings.HasPrefix(t, "/32 host LOCAL") {
			if a, err := netip.ParseAddr(last); err == nil && a.Is4() && !a.IsLoopback() && !slices.Contains(out, last) {
				out = append(out, last)
			}
		}
		if !strings.HasPrefix(t, "/") {
			last = ""
		}
	}
	return out
}

type dockerProxy struct {
	hostIP                  string
	hostPort, containerPort int
	containerIP             string
}

// parseDockerProxy reads the port mapping of a docker-proxy process.
func parseDockerProxy(in inventory.ProcessInstance) (dockerProxy, bool) {
	if in.ExeBasename() != "docker-proxy" {
		return dockerProxy{}, false
	}
	f := strings.Fields(in.Cmdline)
	arg := func(name string) string {
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "-"+name || f[i] == "--"+name {
				return f[i+1]
			}
		}
		return ""
	}
	if proto := arg("proto"); proto != "" && proto != "tcp" {
		return dockerProxy{}, false
	}
	p := dockerProxy{hostIP: arg("host-ip"), containerIP: arg("container-ip")}
	p.hostPort, _ = strconv.Atoi(arg("host-port"))
	p.containerPort, _ = strconv.Atoi(arg("container-port"))
	return p, p.hostPort > 0 && p.containerPort > 0
}
