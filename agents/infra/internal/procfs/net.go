package procfs

import (
	"encoding/hex"
	"net/netip"
	"strconv"
	"strings"
)

// Socket is a bound socket from /proc/net/{tcp,tcp6,udp,udp6}.
type Socket struct {
	Protocol string // tcp, udp
	Family   int    // 4, 6
	Address  string
	Port     int
	Inode    uint64
}

// Key returns the inventory key <proto>:<address>:<port>.
func (s Socket) Key() string { return PortKey(s.Protocol, s.Address, s.Port) }

// PortKey builds a listening_port key; IPv6 addresses are bracketed
// (tcp:[::]:22) so that the key stays unambiguous (D-016).
func PortKey(protocol, address string, port int) string {
	if strings.Contains(address, ":") {
		address = "[" + address + "]"
	}
	return protocol + ":" + address + ":" + strconv.Itoa(port)
}

const tcpListen = "0A"

// ParseNetSockets returns listening TCP sockets or unconnected (bound) UDP
// sockets. Addresses in procfs are in host byte order; amd64 and arm64 are
// little-endian, which is what this parser assumes.
func ParseNetSockets(data []byte, protocol string, family int) []Socket {
	var out []Socket
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) < 10 || !strings.HasSuffix(f[0], ":") {
			continue
		}
		local, remote, state := f[1], f[2], f[3]
		switch protocol {
		case "tcp":
			if state != tcpListen {
				continue
			}
		case "udp":
			_, rport, ok := strings.Cut(remote, ":")
			if !ok || rport != "0000" || strings.Trim(strings.SplitN(remote, ":", 2)[0], "0") != "" {
				continue
			}
		}
		addrHex, portHex, ok := strings.Cut(local, ":")
		if !ok {
			continue
		}
		addr, ok := decodeAddr(addrHex)
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		inode, _ := strconv.ParseUint(f[9], 10, 64)
		out = append(out, Socket{Protocol: protocol, Family: family, Address: addr, Port: int(port), Inode: inode})
	}
	return out
}

func decodeAddr(h string) (string, bool) {
	b, err := hex.DecodeString(h)
	if err != nil {
		return "", false
	}
	switch len(b) {
	case 4:
		return netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]}).String(), true
	case 16:
		var a [16]byte
		for i := 0; i < 16; i += 4 {
			a[i], a[i+1], a[i+2], a[i+3] = b[i+3], b[i+2], b[i+1], b[i]
		}
		ip := netip.AddrFrom16(a)
		return ip.String(), true
	}
	return "", false
}

// SocketInode parses an fd link target like "socket:[12345]".
func SocketInode(link string) (uint64, bool) {
	rest, ok := strings.CutPrefix(link, "socket:[")
	if !ok || !strings.HasSuffix(rest, "]") {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSuffix(rest, "]"), 10, 64)
	return v, err == nil
}
