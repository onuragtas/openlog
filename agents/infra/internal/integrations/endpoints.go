package integrations

import (
	"net"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
)

// MaxEndpointCandidates bounds the endpoints tried per instance.
const MaxEndpointCandidates = 8

// DeriveEndpoints lists endpoint candidates for a target, in order:
//  1. TCP listening ports of the service (default port first; wildcard
//     addresses are reached over loopback),
//  2. well-known unix sockets that exist under host.root_path,
//  3. published container ports on the host loopback,
//  4. container IP addresses with the container's private ports,
//  5. only when nothing else is known, the default port on 127.0.0.1.
func DeriveEndpoints(t Target, spec EndpointSpec, fs *hostfs.FS) []Endpoint {
	var out []Endpoint
	seen := map[string]bool{}
	add := func(e Endpoint) {
		if !seen[e.Address] && len(out) < MaxEndpointCandidates {
			seen[e.Address] = true
			out = append(out, e)
		}
	}
	skip := func(p int) bool { return p <= 0 || (spec.SkipPort != nil && spec.SkipPort(p)) }
	portRank := func(p int) int {
		if p == spec.DefaultPort {
			return -1
		}
		return p
	}

	type hp struct {
		host string
		port int
	}
	var tcp []hp
	for _, p := range t.Ports {
		if p.Protocol != "tcp" || skip(p.Port) {
			continue
		}
		tcp = append(tcp, hp{reachableHost(p.Address), p.Port})
	}
	sort.SliceStable(tcp, func(i, j int) bool {
		if tcp[i].port != tcp[j].port {
			return portRank(tcp[i].port) < portRank(tcp[j].port)
		}
		return tcp[i].host < tcp[j].host
	})
	for _, e := range tcp {
		add(TCP(e.host, e.port))
	}

	for _, s := range spec.UnixSockets {
		if fs == nil {
			break
		}
		if fi, err := fs.Stat(s); err == nil && fi.Mode()&os.ModeSocket != 0 {
			add(Endpoint{Network: "unix", Address: fs.Path(s), Display: "unix:" + s})
		}
	}

	for _, c := range t.Containers {
		type cp struct{ priv, pub int }
		var ports []cp
		privs := map[int]bool{}
		for _, p := range c.Ports {
			if p.Protocol != "" && p.Protocol != "tcp" || skip(p.PrivatePort) {
				continue
			}
			privs[p.PrivatePort] = true
			if p.PublicPort > 0 && (p.IP == "" || p.IP == "0.0.0.0" || p.IP == "::" || p.IP == "127.0.0.1" || p.IP == "::1") {
				ports = append(ports, cp{p.PrivatePort, p.PublicPort})
			}
		}
		sort.SliceStable(ports, func(i, j int) bool { return portRank(ports[i].priv) < portRank(ports[j].priv) })
		for _, p := range ports {
			add(TCP("127.0.0.1", p.pub))
		}
		if spec.DefaultPort > 0 && !skip(spec.DefaultPort) {
			privs[spec.DefaultPort] = true
		}
		var pl []int
		for p := range privs {
			pl = append(pl, p)
		}
		sort.Slice(pl, func(i, j int) bool { return portRank(pl[i]) < portRank(pl[j]) })
		for _, ip := range c.IPs {
			for _, p := range pl {
				add(TCP(ip, p))
			}
		}
	}

	// 5. Nothing known about the service's sockets (a container whose runtime
	// API and network namespace are unreadable): its default port on loopback,
	// where a published container port usually is.
	if len(out) == 0 && spec.DefaultPort > 0 && !skip(spec.DefaultPort) {
		add(TCP("127.0.0.1", spec.DefaultPort))
	}
	return out
}

// ExplicitEndpoint parses a configured endpoint (host:port or unix:/path).
func ExplicitEndpoint(s string, fs *hostfs.FS) Endpoint {
	if p, ok := strings.CutPrefix(s, "unix:"); ok {
		addr := p
		if fs != nil {
			addr = fs.Path(p)
		}
		return Endpoint{Network: "unix", Address: addr, Display: "unix:" + p}
	}
	return Endpoint{Network: "tcp", Address: s, Display: s}
}

// reachableHost maps a listening address to an address the agent can dial.
func reachableHost(addr string) string {
	a, err := netip.ParseAddr(addr)
	switch {
	case err != nil, a.IsUnspecified():
		if err == nil && a.Is6() {
			return "::1"
		}
		return "127.0.0.1"
	}
	return a.String()
}

// MatchInstance returns the index of the first instance override matching the
// target and candidates, or -1.
func MatchInstance(instances []config.InstanceConfig, t Target, candidates []Endpoint) int {
	for i, in := range instances {
		if matches(in.Match, t, candidates) {
			return i
		}
	}
	return -1
}

func matches(m config.InstanceMatch, t Target, candidates []Endpoint) bool {
	if m.IsZero() {
		return false
	}
	if m.Port != 0 {
		ok := false
		for _, p := range t.Ports {
			ok = ok || p.Port == m.Port
		}
		for _, c := range t.Containers {
			for _, p := range c.Ports {
				ok = ok || p.PrivatePort == m.Port || p.PublicPort == m.Port
			}
		}
		if !ok {
			return false
		}
	}
	if m.Endpoint != "" {
		ok := false
		for _, e := range candidates {
			ok = ok || e.Display == m.Endpoint || e.Address == m.Endpoint
		}
		if !ok {
			return false
		}
	}
	if m.Unit != "" {
		ok := false
		for _, u := range t.Units {
			ok = ok || u == m.Unit
		}
		if !ok {
			return false
		}
	}
	if m.Container != "" {
		ok := false
		for _, c := range t.Containers {
			ok = ok || c.Name == m.Container || (len(m.Container) >= 12 && strings.HasPrefix(c.ID, m.Container))
		}
		if !ok {
			return false
		}
	}
	if m.Instance != "" && m.Instance != t.Instance {
		return false
	}
	return true
}

// ServiceInstanceID returns host:port for TCP endpoints with loopback hosts
// replaced by host.name (like the OTel postgresql receiver), and
// <host.name>:<socket path> for unix sockets.
func ServiceInstanceID(e Endpoint, hostName string) string {
	if e.Network == "unix" {
		return hostName + ":" + strings.TrimPrefix(e.Display, "unix:")
	}
	h, p, err := net.SplitHostPort(e.Address)
	if err != nil {
		return e.Display
	}
	if ip := net.ParseIP(h); (ip != nil && ip.IsLoopback()) || strings.EqualFold(h, "localhost") {
		if hostName != "" {
			h = hostName
		}
	}
	return net.JoinHostPort(h, p)
}

// PortString formats a port.
func PortString(p int) string { return strconv.Itoa(p) }

// URLEndpoint returns the TCP endpoint of an http(s) URL (default ports 80/443).
func URLEndpoint(raw string) (Endpoint, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Endpoint{}, false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	p, _ := strconv.Atoi(port)
	return TCP(u.Hostname(), p), true
}
