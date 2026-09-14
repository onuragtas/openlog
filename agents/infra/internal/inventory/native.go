package inventory

// Inventory of macOS and Windows hosts (D-104). The collectors live in native_darwin.go and
// native_windows.go; this file holds the platform-independent types and parsers so they are tested on
// every OS.

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
)

// Service manager categories of non-Linux hosts (semantic-conventions §3.3).
const (
	CategoryLaunchdService = "launchd_service"
	CategoryWindowsService = "windows_service"
)

// Service managers of SystemService.
const (
	ServiceManagerLaunchd = "launchd"
	ServiceManagerWindows = "windows"
)

// LaunchdService is the body of a "launchd_service" item (macOS).
type LaunchdService struct {
	Label          string `json:"label"`
	PID            *int   `json:"pid,omitempty"`
	LastExitStatus *int   `json:"last_exit_status,omitempty"`
	Program        string `json:"program,omitempty"`
	Path           string `json:"path,omitempty"`
	// Domain is "system" (LaunchDaemons, agent running as root) or "user".
	Domain string `json:"domain"`
}

// WindowsService is the body of a "windows_service" item.
type WindowsService struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	// State: stopped, start_pending, stop_pending, running, continue_pending, pause_pending, paused.
	State string `json:"state"`
	// StartType: boot, system, automatic, automatic_delayed, manual, disabled.
	StartType  string `json:"start_type"`
	PID        *int   `json:"pid,omitempty"`
	BinaryPath string `json:"binary_path,omitempty"`
	Account    string `json:"account,omitempty"`
}

// SystemService is a launchd job or Windows service; discovery matches it with the "service" matcher.
type SystemService struct {
	Manager string
	Name    string
	PID     int
	// Body is the inventory item body (LaunchdService or WindowsService).
	Body any
}

// Category returns the inventory category of the service.
func (s SystemService) Category() string {
	if s.Manager == ServiceManagerWindows {
		return CategoryWindowsService
	}
	return CategoryLaunchdService
}

// baseName returns the basename of an executable path or argv[0]. Windows replaces it with a version
// that splits at "\" and drops the ".exe" suffix, so that rules written for Linux names (nginx,
// redis-server, postgres, mysqld) match Windows processes; Linux keeps path.Base.
var baseName = path.Base

// WindowsBaseName is the Windows baseName: the last path element (either separator) without a
// case-insensitive ".exe" suffix and without surrounding quotes.
func WindowsBaseName(p string) string {
	p = strings.Trim(p, `"`)
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		p = p[i+1:]
	}
	if len(p) > 4 && strings.EqualFold(p[len(p)-4:], ".exe") {
		p = p[:len(p)-4]
	}
	if p == "" {
		return "."
	}
	return p
}

// ParseLsof parses `lsof -nP -F pcPnt` output into listening ports. Connected sockets ("a->b") are
// skipped; "*" becomes 0.0.0.0 or :: like on Linux.
func ParseLsof(out string) []ListeningPort {
	var res []ListeningPort
	pid, cmd, proto, family := 0, "", "", 4
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		v := line[1:]
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(v)
			cmd, proto, family = "", "", 4
		case 'c':
			cmd = v
		case 'f':
			proto, family = "", 4
		case 't':
			if v == "IPv6" {
				family = 6
			} else {
				family = 4
			}
		case 'P':
			proto = strings.ToLower(v)
		case 'n':
			if (proto != "tcp" && proto != "udp") || strings.Contains(v, "->") {
				continue
			}
			i := strings.LastIndexByte(v, ':')
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(v[i+1:])
			if err != nil || port <= 0 {
				continue
			}
			addr := strings.TrimSuffix(strings.TrimPrefix(v[:i], "["), "]")
			if j := strings.IndexByte(addr, '%'); j >= 0 {
				addr = addr[:j] // zone
			}
			if strings.Contains(addr, ":") {
				family = 6
			}
			if addr == "*" {
				addr = "0.0.0.0"
				if family == 6 {
					addr = "::"
				}
			}
			res = append(res, ListeningPort{Protocol: proto, Family: family, Address: addr, Port: port, PID: pid, ProcessName: cmd})
		}
	}
	return dedupePorts(res)
}

// dedupePorts keeps one port per key (lowest known PID) sorted by key.
func dedupePorts(ports []ListeningPort) []ListeningPort {
	items := map[string]ListeningPort{}
	for _, lp := range ports {
		k := lp.Key()
		if prev, ok := items[k]; ok && prev.PID != 0 && (lp.PID == 0 || prev.PID < lp.PID) {
			continue
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

// ParseLaunchctlList parses `launchctl list` ("PID\tStatus\tLabel", "-" for none).
func ParseLaunchctlList(out string) []LaunchdService {
	var res []LaunchdService
	for i, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 3 || (i == 0 && f[0] == "PID") || f[2] == "" {
			continue
		}
		s := LaunchdService{Label: f[2]}
		if n, err := strconv.Atoi(f[0]); err == nil && n > 0 {
			s.PID = &n
		}
		if n, err := strconv.Atoi(f[1]); err == nil {
			s.LastExitStatus = &n
		}
		res = append(res, s)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Label < res[j].Label })
	return slices.CompactFunc(res, func(a, b LaunchdService) bool { return a.Label == b.Label })
}

// ParseDscacheutilUsers parses `dscacheutil -q user` (blank-line separated "key: value" blocks).
func ParseDscacheutilUsers(out string) []User {
	var res []User
	cur := map[string]string{}
	flush := func() {
		if name := cur["name"]; name != "" {
			uid, err := strconv.Atoi(cur["uid"])
			if err == nil {
				gid, _ := strconv.Atoi(cur["gid"])
				res = append(res, User{Name: name, UID: uid, GID: gid, Home: cur["dir"], Shell: cur["shell"]})
			}
		}
		cur = map[string]string{}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	flush()
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return slices.CompactFunc(res, func(a, b User) bool { return a.Name == b.Name })
}

// HomebrewPackages lists formulae (<prefix>/Cellar/<name>/<version>) and casks (<prefix>/Caskroom/<name>/<version>)
// of the given Homebrew prefixes (/opt/homebrew, /usr/local). With several installed versions the highest
// (by procfs-free natural order) is reported.
func HomebrewPackages(prefixes []string) []Package {
	seen := map[string]Package{}
	for _, prefix := range prefixes {
		for _, sub := range []string{"Cellar", "Caskroom"} {
			names, err := os.ReadDir(filepath.Join(prefix, sub))
			if err != nil {
				continue
			}
			for _, n := range names {
				if !n.IsDir() || strings.HasPrefix(n.Name(), ".") {
					continue
				}
				versions, err := os.ReadDir(filepath.Join(prefix, sub, n.Name()))
				if err != nil {
					continue
				}
				best := ""
				for _, v := range versions {
					if v.IsDir() && !strings.HasPrefix(v.Name(), ".") && naturalLess(best, v.Name()) {
						best = v.Name()
					}
				}
				if best == "" {
					continue
				}
				if _, dup := seen[n.Name()]; !dup {
					seen[n.Name()] = Package{Manager: "homebrew", Name: n.Name(), Version: best}
				}
			}
		}
	}
	out := make([]Package, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// naturalLess compares version-like strings chunk by chunk (numbers numerically); "" is lowest.
func naturalLess(a, b string) bool {
	if a == "" {
		return b != ""
	}
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '_' || r == '-' })
	}
	pa, pb := split(a), split(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				return na < nb
			}
		case pa[i] != pb[i]:
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// nativePortsFallback is unused on Linux; keeps procfs imported for PortKey users in this file.
var _ = procfs.PortKey
