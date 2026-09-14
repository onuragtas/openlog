package discovery

import (
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/inventory"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"
)

// Matched-by source names, in output order.
const (
	SourceProcess       = "process"
	SourceSystemdUnit   = "systemd_unit"
	SourceService       = "service"
	SourceListeningPort = "listening_port"
	SourceContainer     = "container"
	SourcePackage       = "package"
)

var sourceOrder = []string{SourceProcess, SourceSystemdUnit, SourceService, SourceListeningPort, SourceContainer, SourcePackage}

// Integration status values (semantic-conventions §3.4).
const (
	StatusEnabled            = "enabled"
	StatusNeedsConfiguration = "needs_configuration"
	StatusError              = "error"
	StatusNotAvailable       = "not_available"
)

// PortRef is a port of a discovered service.
type PortRef struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
}

// IntegrationStatus is the integration part of a discovered service.
type IntegrationStatus struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
	// Error is the sanitized reason of status "error" (also set for
	// needs_configuration when the server rejected the connection).
	Error string `json:"error,omitempty"`
	// Hint is a short configuration hint (a config.yaml snippet) for
	// needs_configuration and not_available.
	Hint string `json:"hint,omitempty"`
	// Endpoint is the endpoint the integration collects from (no credentials).
	Endpoint string `json:"endpoint,omitempty"`
}

// Service is the body of a discovered_service item (semantic-conventions §3.4).
type Service struct {
	RuleID   string `json:"rule_id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Instance string `json:"instance"`
	Command  string `json:"command,omitempty"`
	// DisplayInstance is the invoked path of a multi-call or symlinked
	// executable (/usr/bin/redis-server when Instance is /usr/bin/redis-check-rdb);
	// display only, Instance stays the grouping key.
	DisplayInstance string    `json:"display_instance,omitempty"`
	Version         string    `json:"version"`
	MatchedBy       []string  `json:"matched_by"`
	PIDs            []int     `json:"pids"`
	Ports           []PortRef `json:"ports"`
	SystemdUnits    []string  `json:"systemd_units"`
	// Services are launchd job labels or Windows service names (macOS, Windows; D-104).
	Services     []string          `json:"services,omitempty"`
	Packages     []string          `json:"packages"`
	ContainerIDs []string          `json:"container_ids"`
	Integration  IntegrationStatus `json:"integration"`
	APMHint      *APMHint          `json:"apm_hint"`
	LogPaths     []string          `json:"log_paths"`
}

// ServiceIndex maps processes to the rule id of their discovered service.
type ServiceIndex struct {
	byPID       map[int]string
	byExe       map[string]string
	byContainer map[string]string
}

// NewServiceIndex indexes services by PID, executable instance and container
// id. When several services claim a process, a non-runtime service wins (e.g.
// kafka over jvm), then the lower rule id.
func NewServiceIndex(services []Service) *ServiceIndex {
	ss := append([]Service(nil), services...)
	sort.SliceStable(ss, func(i, j int) bool {
		ri, rj := ss[i].Category == "runtime", ss[j].Category == "runtime"
		if ri != rj {
			return !ri
		}
		return ss[i].RuleID < ss[j].RuleID
	})
	ix := &ServiceIndex{byPID: map[int]string{}, byExe: map[string]string{}, byContainer: map[string]string{}}
	setIfEmpty := func(m map[string]string, k, v string) {
		if _, ok := m[k]; !ok && k != "" {
			m[k] = v
		}
	}
	for _, s := range ss {
		for _, pid := range s.PIDs {
			if _, ok := ix.byPID[pid]; !ok {
				ix.byPID[pid] = s.RuleID
			}
		}
		if strings.HasPrefix(s.Instance, "/") {
			setIfEmpty(ix.byExe, s.Instance, s.RuleID)
		}
		for _, c := range s.ContainerIDs {
			setIfEmpty(ix.byContainer, c, s.RuleID)
		}
	}
	return ix
}

// Lookup returns the rule id for a process, or "".
func (ix *ServiceIndex) Lookup(pid int, exe, containerID string) string {
	if ix == nil {
		return ""
	}
	if id, ok := ix.byPID[pid]; ok {
		return id
	}
	if id, ok := ix.byExe[exe]; ok && exe != "" {
		return id
	}
	if containerID != "" {
		return ix.byContainer[containerID]
	}
	return ""
}

// mainProcess returns the service's main process: the lowest PID whose parent
// is not part of the service.
func mainProcess(pids []int, ix *index) (inventory.ProcessInstance, bool) {
	set := map[int]bool{}
	for _, p := range pids {
		set[p] = true
	}
	for _, p := range pids { // pids are sorted
		if in, ok := ix.byPID[p]; ok && !set[in.PPID] {
			return in, true
		}
	}
	if len(pids) > 0 {
		in, ok := ix.byPID[pids[0]]
		return in, ok
	}
	return inventory.ProcessInstance{}, false
}

// displayInstance returns the invoked path of an executable instance started
// through another name of the same binary (Debian's /usr/bin/redis-server →
// redis-check-rdb, busybox applets): the absolute argv[0] when its basename
// differs from the resolved executable and comm confirms it is the exec'd name
// (comm is set from the executed file name, truncated to 15 bytes; a
// setproctitle title does not change it). Otherwise "".
func displayInstance(instance string, main inventory.ProcessInstance) string {
	if !strings.HasPrefix(instance, "/") || main.ContainerID != "" {
		return ""
	}
	argv0, _, _ := strings.Cut(strings.TrimSpace(main.Cmdline), " ")
	if !strings.HasPrefix(argv0, "/") || strings.HasSuffix(argv0, ":") {
		return ""
	}
	argv0 = path.Clean(argv0)
	name := path.Base(argv0)
	if argv0 == instance || name == path.Base(instance) {
		return ""
	}
	comm := name
	if len(comm) > 15 {
		comm = comm[:15]
	}
	if main.Comm != comm {
		return ""
	}
	return argv0
}

// Key returns <rule_id>:<instance>.
func (s Service) Key() string { return s.RuleID + ":" + s.Instance }

// Engine evaluates a rule catalog.
type Engine struct {
	rules []*Rule
}

// NewEngine returns an engine for validated rules.
func NewEngine(rules []*Rule) *Engine { return &Engine{rules: rules} }

// Rules returns the engine's rules.
func (e *Engine) Rules() []*Rule { return e.rules }

// Discover evaluates all rules against the inventory.
func (e *Engine) Discover(d *inventory.Data) []Service {
	ix := newIndex(d)
	var out []Service
	for _, r := range e.rules {
		out = append(out, evaluate(r, ix)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// Items converts services to inventory items.
func Items(services []Service) []inventory.Item {
	items := make([]inventory.Item, 0, len(services))
	for _, s := range services {
		items = append(items, inventory.Item{Category: inventory.CategoryDiscoveredService, Key: s.Key(), Body: s})
	}
	return items
}

type index struct {
	d        *inventory.Data
	byPID    map[int]inventory.ProcessInstance
	unitPIDs map[string][]int
	pidPorts map[int][]PortRef
}

func newIndex(d *inventory.Data) *index {
	ix := &index{d: d, byPID: map[int]inventory.ProcessInstance{}, unitPIDs: map[string][]int{}, pidPorts: map[int][]PortRef{}}
	for _, in := range d.Instances {
		ix.byPID[in.PID] = in
		if in.SystemdUnit != "" {
			ix.unitPIDs[in.SystemdUnit] = append(ix.unitPIDs[in.SystemdUnit], in.PID)
		}
	}
	for _, p := range d.Ports {
		if p.PID != 0 {
			ix.pidPorts[p.PID] = append(ix.pidPorts[p.PID], PortRef{p.Protocol, p.Address, p.Port})
		}
	}
	return ix
}

// candidate is one service instance being assembled.
type candidate struct {
	instance   string
	pids       map[int]bool
	units      map[string]bool
	services   map[string]bool
	containers map[string]bool
	ports      map[string]PortRef
	matchedBy  map[string]bool
}

func newCandidate(instance string) *candidate {
	return &candidate{
		instance: instance, pids: map[int]bool{}, units: map[string]bool{}, services: map[string]bool{}, containers: map[string]bool{},
		ports: map[string]PortRef{}, matchedBy: map[string]bool{},
	}
}

func (c *candidate) addProcess(in inventory.ProcessInstance) {
	c.pids[in.PID] = true
	if in.SystemdUnit != "" {
		c.units[in.SystemdUnit] = true
	}
	if in.ContainerID != "" {
		c.containers[in.ContainerID] = true
	}
}

type candidates struct {
	byKey map[string]*candidate
	order []string
}

func (cs *candidates) get(key, instance string) *candidate {
	if c, ok := cs.byKey[key]; ok {
		return c
	}
	c := newCandidate(instance)
	cs.byKey[key] = c
	cs.order = append(cs.order, key)
	return c
}

func (cs *candidates) find(pred func(*candidate) bool) []*candidate {
	var out []*candidate
	for _, k := range cs.order {
		if c := cs.byKey[k]; pred(c) {
			out = append(out, c)
		}
	}
	return out
}

func matchProcess(m *ProcessMatcher, in inventory.ProcessInstance) bool {
	names := in.Names()
	if len(m.ExeBasename) > 0 && !slices.ContainsFunc(names, func(n string) bool { return slices.Contains(m.ExeBasename, n) }) {
		return false
	}
	if m.exeRe != nil && !slices.ContainsFunc(names, m.exeRe.MatchString) {
		return false
	}
	if m.cmdRe != nil && !m.cmdRe.MatchString(in.Cmdline) {
		return false
	}
	return true
}

// executablePath returns the exe link target, or an absolute argv[0] when the
// link is unreadable (processes of other users without CAP_SYS_PTRACE).
func executablePath(in inventory.ProcessInstance) string {
	if in.Exe != "" {
		return in.Exe
	}
	if argv0, _, _ := strings.Cut(in.Cmdline, " "); strings.HasPrefix(argv0, "/") && !strings.HasSuffix(argv0, ":") {
		return argv0
	}
	return ""
}

func matchPort(m *PortMatcher, p inventory.ListeningPort, ix *index) bool {
	if p.Port != m.Port || (m.Protocol != "" && m.Protocol != p.Protocol) {
		return false
	}
	if len(m.ProcessExeBasename) == 0 {
		return true
	}
	if in, ok := ix.byPID[p.PID]; ok {
		return slices.ContainsFunc(in.Names(), func(n string) bool { return slices.Contains(m.ProcessExeBasename, n) })
	}
	if p.ProcessExe != "" {
		return slices.Contains(m.ProcessExeBasename, path.Base(p.ProcessExe))
	}
	return false
}

func matchName(names []string, re *regexp.Regexp, name string) bool {
	return slices.Contains(names, name) || (re != nil && re.MatchString(name))
}

func evaluate(r *Rule, ix *index) []Service {
	cs := &candidates{byKey: map[string]*candidate{}}
	matchesAnyProcess := func(in inventory.ProcessInstance) bool {
		for _, m := range r.Match {
			if m.Process != nil && matchProcess(m.Process, in) {
				return true
			}
		}
		return false
	}
	// anchor maps a process whose exe link is unreadable (e.g. a worker running
	// under another uid without CAP_SYS_PTRACE) to its closest matching ancestor
	// with a readable exe, so that workers join their master's instance.
	anchor := func(in inventory.ProcessInstance) inventory.ProcessInstance {
		for depth := 0; executablePath(in) == "" && depth < 8; depth++ {
			parent, ok := ix.byPID[in.PPID]
			if !ok || parent.PID == in.PID || !matchesAnyProcess(parent) {
				break
			}
			in = parent
		}
		return in
	}
	procCandidate := func(in inventory.ProcessInstance) *candidate {
		a := anchor(in)
		if a.ContainerID != "" {
			// Containers share executable paths (/usr/local/bin/redis-server in
			// every redis image): each container is its own instance.
			c := cs.get("container\x00"+a.ContainerID, a.ContainerID)
			c.addProcess(in)
			return c
		}
		instance := executablePath(a)
		if instance == "" {
			instance = a.Key()
		}
		c := cs.get("exe\x00"+instance, instance)
		c.addProcess(in)
		return c
	}

	// 1. Processes: instances are grouped by executable.
	for _, in := range ix.d.Instances {
		if matchesAnyProcess(in) {
			procCandidate(in).matchedBy[SourceProcess] = true
		}
	}

	// 2. Listening ports: attach to the owning process's instance. Ports
	// without a known owner are resolved last (lowest instance precedence).
	var ownerless []inventory.ListeningPort
	for _, p := range ix.d.Ports {
		for _, m := range r.Match {
			if m.ListeningPort == nil || !matchPort(m.ListeningPort, p, ix) {
				continue
			}
			if in, ok := ix.byPID[p.PID]; ok {
				c := procCandidate(in)
				c.matchedBy[SourceListeningPort] = true
			} else {
				ownerless = append(ownerless, p)
			}
			break
		}
	}

	// 3. systemd units: merge into instances running inside the unit. Template
	// unit files (foo@.service) only describe instances and are not services.
	for _, u := range ix.d.Units {
		if u.EnabledState == "masked" || strings.Contains(u.Name, "@.") {
			continue
		}
		for _, m := range r.Match {
			if m.SystemdUnit == nil || !m.SystemdUnit.re.MatchString(u.Name) {
				continue
			}
			owners := cs.find(func(c *candidate) bool { return c.units[u.Name] })
			if len(owners) == 0 && len(ix.unitPIDs[u.Name]) == 0 && len(cs.order) > 0 {
				// The unit runs nothing we can see (inactive, or processes not
				// started by systemd, e.g. in a container): it belongs to the
				// already discovered instances rather than being a new one.
				owners = cs.find(func(*candidate) bool { return true })
				for _, c := range owners {
					c.units[u.Name] = true
				}
			}
			if len(owners) == 0 {
				c := cs.get("unit\x00"+u.Name, u.Name)
				c.units[u.Name] = true
				for _, pid := range ix.unitPIDs[u.Name] {
					c.addProcess(ix.byPID[pid])
				}
				owners = []*candidate{c}
			}
			for _, c := range owners {
				c.matchedBy[SourceSystemdUnit] = true
			}
			break
		}
	}

	// 3b. launchd jobs and Windows services: merge into the instance of their process, like systemd units.
	for _, sv := range ix.d.Services {
		for _, m := range r.Match {
			if m.Service == nil || (m.Service.Manager != "" && m.Service.Manager != sv.Manager) || !m.Service.re.MatchString(sv.Name) {
				continue
			}
			owners := cs.find(func(c *candidate) bool { return c.services[sv.Name] || (sv.PID != 0 && c.pids[sv.PID]) })
			if len(owners) == 0 && len(cs.order) > 0 {
				// Not running, or running in a process no matcher claims (e.g. IIS's W3SVC inside a shared
				// svchost.exe while the w3wp.exe workers matched): it belongs to the discovered instances.
				owners = cs.find(func(*candidate) bool { return true })
			}
			if len(owners) == 0 {
				c := cs.get("service\x00"+sv.Name, sv.Name)
				if in, ok := ix.byPID[sv.PID]; ok && sv.PID != 0 && !strings.EqualFold(in.Comm, "svchost") {
					c.addProcess(in) // a shared service host process is not the service's own process
				}
				owners = []*candidate{c}
			}
			for _, c := range owners {
				c.services[sv.Name] = true
				c.matchedBy[SourceService] = true
			}
			break
		}
	}

	// 4. Containers (inventory arrives in M1).
	for _, ct := range ix.d.Containers {
		for _, m := range r.Match {
			if m.Container == nil || !m.Container.re.MatchString(ct.Image) {
				continue
			}
			owners := cs.find(func(c *candidate) bool { return c.containers[ct.ID] })
			if len(owners) == 0 {
				c := cs.get("container\x00"+ct.ID, ct.ID)
				c.containers[ct.ID] = true
				owners = []*candidate{c}
			}
			for _, c := range owners {
				c.matchedBy[SourceContainer] = true
			}
			break
		}
	}

	// 5. Packages: matched packages mark every instance; with no running
	// instance, an installed server package still yields a service.
	var matchedPkgs, linkedPkgs []string
	for _, p := range ix.d.Packages {
		matched := false
		for _, m := range r.Match {
			if m.Package != nil && matchName(m.Package.Name, m.Package.re, p.Name) {
				matched = true
				break
			}
		}
		linked := matched
		for _, v := range r.Version {
			if matchName(v.Package, v.pkgRe, p.Name) {
				linked = true
			}
		}
		if matched {
			matchedPkgs = append(matchedPkgs, p.Key())
		}
		if linked {
			linkedPkgs = append(linkedPkgs, p.Key())
		}
	}
	if len(cs.order) == 0 && len(matchedPkgs) > 0 {
		cs.get("package\x00"+matchedPkgs[0], matchedPkgs[0])
	}

	// 6. Ownerless ports: join the existing instances (exe → unit → container
	// → package precedence already applied); only otherwise the port key is
	// the instance.
	if len(ownerless) > 0 {
		if owners := cs.find(func(*candidate) bool { return true }); len(owners) > 0 {
			for _, c := range owners {
				for _, p := range ownerless {
					c.ports[p.Key()] = PortRef{p.Protocol, p.Address, p.Port}
				}
				c.matchedBy[SourceListeningPort] = true
			}
		} else {
			for _, p := range ownerless {
				c := cs.get("port\x00"+p.Key(), p.Key())
				c.ports[p.Key()] = PortRef{p.Protocol, p.Address, p.Port}
				c.matchedBy[SourceListeningPort] = true
			}
		}
	}

	out := make([]Service, 0, len(cs.order))
	for _, k := range cs.order {
		c := cs.byKey[k]
		if len(matchedPkgs) > 0 {
			c.matchedBy[SourcePackage] = true
		}
		for pid := range c.pids {
			for _, pr := range ix.pidPorts[pid] {
				c.ports[procfs.PortKey(pr.Protocol, pr.Address, pr.Port)] = pr
			}
		}
		s := Service{
			RuleID: r.ID, Name: r.Name, Category: r.Category, Instance: c.instance,
			MatchedBy: []string{}, PIDs: sortedInts(c.pids), Ports: sortedPorts(c.ports),
			SystemdUnits: sortedStrings(c.units), Packages: append([]string{}, linkedPkgs...),
			Services:     sortedStringsOmitEmpty(c.services),
			ContainerIDs: sortedStrings(c.containers), APMHint: r.APMHint, LogPaths: []string{},
		}
		if main, ok := mainProcess(s.PIDs, ix); ok {
			s.Command = main.Command()
			s.DisplayInstance = displayInstance(s.Instance, main)
		}
		for _, l := range r.Logs {
			if l.AppliesTo(ix.d.Platform) {
				s.LogPaths = append(s.LogPaths, l.Path)
			}
		}
		for _, src := range sourceOrder {
			if c.matchedBy[src] {
				s.MatchedBy = append(s.MatchedBy, src)
			}
		}
		s.Version = resolveVersion(r, s.PIDs, ix)
		s.Integration = integrationStatus(r.Integration)
		out = append(out, s)
	}
	return out
}

func resolveVersion(r *Rule, pids []int, ix *index) string {
	for _, v := range r.Version {
		switch {
		case len(v.Package) > 0:
			for _, name := range v.Package { // honour the listed preference order
				for _, p := range ix.d.Packages {
					if p.Name == name && p.Version != "" {
						return NormalizePackageVersion(p.Version)
					}
				}
			}
		case v.pkgRe != nil:
			for _, p := range ix.d.Packages {
				if v.pkgRe.MatchString(p.Name) && p.Version != "" {
					return NormalizePackageVersion(p.Version)
				}
			}
		case v.cmdRe != nil:
			for _, pid := range pids {
				m := v.cmdRe.FindStringSubmatch(ix.byPID[pid].Cmdline)
				if m == nil {
					continue
				}
				if len(m) > 1 && m[1] != "" {
					return m[1]
				}
				return m[0]
			}
		}
	}
	return ""
}

var epochRe = regexp.MustCompile(`^[0-9]+:`)

// NormalizePackageVersion turns distro package versions into upstream
// versions: "5:7.0.15-1build2" → "7.0.15", "7.2.4-r0" → "7.2.4", "16+257" → "16".
func NormalizePackageVersion(v string) string {
	v = epochRe.ReplaceAllString(v, "")
	if i := strings.LastIndexByte(v, '-'); i > 0 {
		v = v[:i]
	}
	if i := strings.IndexAny(v, "+~"); i > 0 {
		v = v[:i]
	}
	return v
}

// integrationStatus follows semantic-conventions §3.4. M0 cannot satisfy any
// requirement (credentials etc.), so a non-empty requires list is unmet.
func integrationStatus(in *Integration) IntegrationStatus {
	switch {
	case in == nil:
		return IntegrationStatus{Status: StatusNotAvailable}
	case len(in.Requires) > 0:
		return IntegrationStatus{ID: in.ID, Status: StatusNeedsConfiguration}
	case in.AutoEnable:
		return IntegrationStatus{ID: in.ID, Status: StatusEnabled}
	default:
		return IntegrationStatus{ID: in.ID, Status: StatusNeedsConfiguration}
	}
}

func sortedInts(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// sortedStringsOmitEmpty is sortedStrings, but nil for an empty set (omitted from JSON).
func sortedStringsOmitEmpty(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	return sortedStrings(m)
}

func sortedStrings(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedPorts(m map[string]PortRef) []PortRef {
	out := make([]PortRef, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		if out[i].Protocol != out[j].Protocol {
			return out[i].Protocol < out[j].Protocol
		}
		return out[i].Address < out[j].Address
	})
	return out
}
