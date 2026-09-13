// Package discovery matches inventory data against a data-driven YAML rule
// catalog (docs/plan/05-infra-agent.md §4) and produces discovered_service
// inventory items. Supporting a new service only requires a new rule file.
package discovery

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule is one discovery rule.
type Rule struct {
	ID          string          `yaml:"id"`
	Name        string          `yaml:"name"`
	Category    string          `yaml:"category"`
	Match       []Matcher       `yaml:"match"`
	Version     []VersionSource `yaml:"version"`
	Endpoints   []Endpoint      `yaml:"endpoints"`
	Integration *Integration    `yaml:"integration"`
	APMHint     *APMHint        `yaml:"apm_hint"`
	Logs        []LogPath       `yaml:"logs"`

	// Source is the file the rule was loaded from.
	Source string `yaml:"-"`
}

// Matcher holds exactly one match condition. A rule is a candidate if any matcher matches.
type Matcher struct {
	Process       *ProcessMatcher   `yaml:"process"`
	SystemdUnit   *UnitMatcher      `yaml:"systemd_unit"`
	ListeningPort *PortMatcher      `yaml:"listening_port"`
	Package       *PackageMatcher   `yaml:"package"`
	Container     *ContainerMatcher `yaml:"container"`
}

// ProcessMatcher matches a process; all given fields must match.
type ProcessMatcher struct {
	ExeBasename      []string `yaml:"exe_basename"`
	ExeBasenameRegex string   `yaml:"exe_basename_regex"`
	CmdlineRegex     string   `yaml:"cmdline_regex"`

	exeRe, cmdRe *regexp.Regexp
}

// UnitMatcher matches a systemd unit by name.
type UnitMatcher struct {
	NameRegex string `yaml:"name_regex"`

	re *regexp.Regexp
}

// PortMatcher matches a listening port, optionally restricted to a process.
type PortMatcher struct {
	Port               int      `yaml:"port"`
	Protocol           string   `yaml:"protocol"`
	ProcessExeBasename []string `yaml:"process_exe_basename"`
}

// PackageMatcher matches installed packages by exact name or regex.
type PackageMatcher struct {
	Name      []string `yaml:"name"`
	NameRegex string   `yaml:"name_regex"`

	re *regexp.Regexp
}

// ContainerMatcher matches containers by image (container inventory arrives in M1).
type ContainerMatcher struct {
	ImageRegex string `yaml:"image_regex"`

	re *regexp.Regexp
}

// VersionSource is one way of determining the version; sources are tried in
// order. Binaries are never executed.
type VersionSource struct {
	Package      []string `yaml:"package"`
	PackageRegex string   `yaml:"package_regex"`
	CmdlineRegex string   `yaml:"cmdline_regex"`

	pkgRe, cmdRe *regexp.Regexp
}

// LogPath is a well-known log file glob of a service. Discovered services
// report it as log_paths; with logs.auto_from_discovery the files are tailed.
type LogPath struct {
	Path string `yaml:"path"`
}

// Endpoint declares where service endpoints come from.
type Endpoint struct {
	From string `yaml:"from"`
}

// Integration describes the metric integration for a discovered service.
type Integration struct {
	ID         string   `yaml:"id" json:"id"`
	AutoEnable bool     `yaml:"auto_enable" json:"-"`
	Requires   []string `yaml:"requires" json:"-"`
}

// APMHint suggests an APM agent for a runtime.
type APMHint struct {
	Language string `yaml:"language" json:"language"`
	Agent    string `yaml:"agent" json:"agent"`
	// Status is set by the agent, never by rules: for openlog-agent-php "active" when the PHP forwarder received
	// spans in the last 10 minutes, else "not_installed" (semantic-conventions §3.4).
	Status string `yaml:"-" json:"status,omitempty"`
}

var (
	idRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
	categoryRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Validate checks the rule and compiles its regular expressions.
func (r *Rule) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	compile := func(field, expr string) *regexp.Regexp {
		if expr == "" {
			return nil
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			add("%s: invalid regex %q: %v", field, expr, err)
		}
		return re
	}

	if !idRe.MatchString(r.ID) {
		add("id: must match %s (got %q)", idRe, r.ID)
	}
	if strings.TrimSpace(r.Name) == "" {
		add("name: required")
	}
	if !categoryRe.MatchString(r.Category) {
		add("category: must match %s (got %q)", categoryRe, r.Category)
	}
	if len(r.Match) == 0 {
		add("match: at least one matcher is required")
	}
	for i := range r.Match {
		m := &r.Match[i]
		p := fmt.Sprintf("match[%d]", i)
		n := 0
		if m.Process != nil {
			n++
			pm := m.Process
			if len(pm.ExeBasename) == 0 && pm.ExeBasenameRegex == "" && pm.CmdlineRegex == "" {
				add("%s.process: one of exe_basename, exe_basename_regex, cmdline_regex is required", p)
			}
			for _, b := range pm.ExeBasename {
				if b == "" || strings.Contains(b, "/") {
					add("%s.process.exe_basename: %q is not a basename", p, b)
				}
			}
			pm.exeRe = compile(p+".process.exe_basename_regex", pm.ExeBasenameRegex)
			pm.cmdRe = compile(p+".process.cmdline_regex", pm.CmdlineRegex)
		}
		if m.SystemdUnit != nil {
			n++
			if m.SystemdUnit.NameRegex == "" {
				add("%s.systemd_unit.name_regex: required", p)
			}
			m.SystemdUnit.re = compile(p+".systemd_unit.name_regex", m.SystemdUnit.NameRegex)
		}
		if m.ListeningPort != nil {
			n++
			if m.ListeningPort.Port < 1 || m.ListeningPort.Port > 65535 {
				add("%s.listening_port.port: must be 1..65535 (got %d)", p, m.ListeningPort.Port)
			}
			if pr := m.ListeningPort.Protocol; pr != "" && pr != "tcp" && pr != "udp" {
				add("%s.listening_port.protocol: must be tcp or udp (got %q)", p, pr)
			}
		}
		if m.Package != nil {
			n++
			if len(m.Package.Name) == 0 && m.Package.NameRegex == "" {
				add("%s.package: name or name_regex is required", p)
			}
			m.Package.re = compile(p+".package.name_regex", m.Package.NameRegex)
		}
		if m.Container != nil {
			n++
			if m.Container.ImageRegex == "" {
				add("%s.container.image_regex: required", p)
			}
			m.Container.re = compile(p+".container.image_regex", m.Container.ImageRegex)
		}
		if n != 1 {
			add("%s: exactly one of process, systemd_unit, listening_port, package, container is required (got %d)", p, n)
		}
	}
	for i := range r.Version {
		v := &r.Version[i]
		p := fmt.Sprintf("version[%d]", i)
		n := 0
		if len(v.Package) > 0 {
			n++
		}
		if v.PackageRegex != "" {
			n++
			v.pkgRe = compile(p+".package_regex", v.PackageRegex)
		}
		if v.CmdlineRegex != "" {
			n++
			v.cmdRe = compile(p+".cmdline_regex", v.CmdlineRegex)
		}
		if n != 1 {
			add("%s: exactly one of package, package_regex, cmdline_regex is required", p)
		}
	}
	for i, e := range r.Endpoints {
		if e.From != "listening_port" {
			add("endpoints[%d].from: only listening_port is supported (got %q)", i, e.From)
		}
	}
	if r.Integration != nil {
		if !idRe.MatchString(r.Integration.ID) {
			add("integration.id: must match %s (got %q)", idRe, r.Integration.ID)
		}
		for _, req := range r.Integration.Requires {
			if req == "" {
				add("integration.requires: empty entry")
			}
		}
	}
	for i, l := range r.Logs {
		if !strings.HasPrefix(l.Path, "/") {
			add("logs[%d].path: must be an absolute path or glob (got %q)", i, l.Path)
		} else if _, err := filepath.Match(l.Path, ""); err != nil {
			add("logs[%d].path: invalid glob %q", i, l.Path)
		}
	}
	if r.APMHint != nil && (r.APMHint.Language == "" || r.APMHint.Agent == "") {
		add("apm_hint: language and agent are required")
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("rule %q: %w", r.ID, errors.Join(errs...))
}

// Parse decodes one or more YAML documents (one rule each) and validates them.
func Parse(source string, data []byte) ([]*Rule, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var out []*Rule
	for i := 0; ; i++ {
		r := &Rule{}
		err := dec.Decode(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: document %d: %w", source, i, err)
		}
		if r.ID == "" && r.Name == "" && len(r.Match) == 0 {
			continue // empty document
		}
		r.Source = source
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// LoadFS loads every *.yaml / *.yml file of a file system root. Duplicate ids
// within one catalog are an error.
func LoadFS(fsys fs.FS, label string) ([]*Rule, error) {
	var names []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")) {
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []*Rule
	var errs []error
	seen := map[string]string{}
	for _, n := range names {
		data, err := fs.ReadFile(fsys, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rules, err := Parse(filepath.Join(label, n), data)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, r := range rules {
			if prev, dup := seen[r.ID]; dup {
				errs = append(errs, fmt.Errorf("%s: duplicate rule id %q (already defined in %s)", r.Source, r.ID, prev))
				continue
			}
			seen[r.ID] = r.Source
			out = append(out, r)
		}
	}
	return out, errors.Join(errs...)
}

// LoadDir loads rules from a directory; a missing directory yields no rules.
func LoadDir(dir string) ([]*Rule, error) {
	if dir == "" {
		return nil, nil
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return LoadFS(os.DirFS(dir), dir)
}

// Merge overlays rules by id: extra rules replace base rules with the same id.
func Merge(base, extra []*Rule) []*Rule {
	idx := map[string]int{}
	out := make([]*Rule, 0, len(base)+len(extra))
	for _, r := range base {
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	for _, r := range extra {
		if i, ok := idx[r.ID]; ok {
			out[i] = r
			continue
		}
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	return out
}
