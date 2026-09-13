package inventory

import (
	"hash/fnv"
	"sort"
	"strconv"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
)

// ListeningPort is the body of a "listening_port" item.
type ListeningPort struct {
	Protocol    string `json:"protocol"`
	Family      int    `json:"family"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	PID         int    `json:"pid,omitempty"`
	ProcessName string `json:"process_name,omitempty"`
	ProcessExe  string `json:"process_exe,omitempty"`
}

// Key returns <proto>:<address>:<port>.
func (p ListeningPort) Key() string {
	return procfs.PortKey(p.Protocol, p.Address, p.Port)
}

var socketFiles = []struct {
	file     string
	protocol string
	family   int
}{{"tcp", "tcp", 4}, {"tcp6", "tcp", 6}, {"udp", "udp", 4}, {"udp6", "udp", 6}}

// ReadSockets reads all listening/bound sockets of the host network namespace.
func ReadSockets(fs *hostfs.FS) []procfs.Socket {
	var out []procfs.Socket
	for _, sf := range socketFiles {
		b, err := fs.ReadFile(fs.ProcSelf() + "/net/" + sf.file)
		if err != nil {
			continue
		}
		out = append(out, procfs.ParseNetSockets(b, sf.protocol, sf.family)...)
	}
	return out
}

// PortSetHash is a cheap fingerprint of the listening socket set.
func PortSetHash(fs *hostfs.FS) uint64 {
	keys := map[string]bool{}
	for _, s := range ReadSockets(fs) {
		keys[s.Key()] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	h := fnv.New64a()
	for _, k := range sorted {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

func collectPorts(fs *hostfs.FS, instances []ProcessInstance) []ListeningPort {
	sockets := ReadSockets(fs)
	if len(sockets) == 0 {
		return nil
	}
	wanted := make(map[uint64]bool, len(sockets))
	for _, s := range sockets {
		wanted[s.Inode] = true
	}
	// Map socket inode → lowest PID holding it (instances are PID-ordered).
	owner := map[uint64]int{}
	byPID := map[int]ProcessInstance{}
	for _, in := range instances {
		byPID[in.PID] = in
		base := "/proc/" + strconv.Itoa(in.PID) + "/fd"
		fds, err := fs.ReadDir(base)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := fs.Readlink(base + "/" + fd.Name())
			if err != nil {
				continue
			}
			if ino, ok := procfs.SocketInode(link); ok && wanted[ino] {
				if cur, seen := owner[ino]; !seen || in.PID < cur {
					owner[ino] = in.PID
				}
			}
		}
	}
	items := map[string]ListeningPort{}
	for _, s := range sockets {
		lp := ListeningPort{Protocol: s.Protocol, Family: s.Family, Address: s.Address, Port: s.Port}
		if pid, ok := owner[s.Inode]; ok {
			in := byPID[pid]
			lp.PID, lp.ProcessName, lp.ProcessExe = pid, in.Comm, in.Exe
		}
		k := lp.Key()
		if prev, ok := items[k]; ok && prev.PID != 0 && (lp.PID == 0 || prev.PID < lp.PID) {
			continue // SO_REUSEPORT duplicates: keep the lowest known PID
		}
		items[k] = lp
	}
	out := make([]ListeningPort, 0, len(items))
	for _, lp := range items {
		out = append(out, lp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}
