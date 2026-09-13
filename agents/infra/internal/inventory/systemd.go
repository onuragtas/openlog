package inventory

import (
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/mask"
)

// SystemdUnit is the body of a "systemd_unit" item.
type SystemdUnit struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Path         string `json:"path"`
	EnabledState string `json:"enabled_state"`
	Description  string `json:"description"`
	ExecStart    string `json:"exec_start,omitempty"`
}

// UnitDirs are scanned in systemd precedence order (first match wins).
var UnitDirs = []string{
	"/etc/systemd/system",
	"/run/systemd/system",
	"/usr/local/lib/systemd/system",
	"/usr/lib/systemd/system",
	"/lib/systemd/system",
}

// enablementDirs hold the *.wants / *.requires symlinks created by "systemctl enable".
var enablementDirs = []string{"/etc/systemd/system", "/run/systemd/system"}

var unitTypes = map[string]bool{
	"service": true, "socket": true, "target": true, "timer": true, "mount": true, "automount": true,
	"swap": true, "path": true, "slice": true, "scope": true, "device": true,
}

// UnitType returns the unit type suffix, or "" if name is not a unit file name.
func UnitType(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return ""
	}
	if t := name[i+1:]; unitTypes[t] {
		return t
	}
	return ""
}

// UnitFile is the parsed subset of a unit file.
type UnitFile struct {
	Description string
	ExecStart   []string
	HasInstall  bool
}

// ParseUnitFile parses an INI-style systemd unit file.
func ParseUnitFile(data []byte) UnitFile {
	var u UnitFile
	section := ""
	var logical []string
	var cur strings.Builder
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(line)
		if cur.Len() == 0 && (strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";")) {
			continue
		}
		if strings.HasSuffix(trimmed, `\`) {
			cur.WriteString(strings.TrimSuffix(trimmed, `\`))
			cur.WriteByte(' ')
			continue
		}
		cur.WriteString(trimmed)
		logical = append(logical, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		logical = append(logical, cur.String())
	}
	for _, l := range logical {
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			section = l[1 : len(l)-1]
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Join(strings.Fields(v), " ")
		switch {
		case section == "Unit" && k == "Description":
			u.Description = v
		case section == "Service" && k == "ExecStart":
			if v == "" {
				u.ExecStart = nil // an empty assignment resets the list
			} else {
				u.ExecStart = append(u.ExecStart, v)
			}
		case section == "Install" && (k == "WantedBy" || k == "RequiredBy" || k == "Alias" || k == "Also" || k == "UpheldBy"):
			if v != "" {
				u.HasInstall = true
			}
		}
	}
	return u
}

// templateName maps "foo@bar.service" to "foo@.service".
func templateName(name string) string {
	at := strings.IndexByte(name, '@')
	dot := strings.LastIndexByte(name, '.')
	if at < 0 || dot < at || at+1 == dot {
		return ""
	}
	return name[:at+1] + name[dot:]
}

func collectUnits(hfs *hostfs.FS) []SystemdUnit {
	enabled := map[string]bool{}
	for _, dir := range enablementDirs {
		entries, err := hfs.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() || !(strings.HasSuffix(n, ".wants") || strings.HasSuffix(n, ".requires") || strings.HasSuffix(n, ".upholds")) {
				continue
			}
			links, _ := hfs.ReadDir(path.Join(dir, n))
			for _, l := range links {
				enabled[l.Name()] = true
				if t := templateName(l.Name()); t != "" {
					enabled[t] = true
				}
			}
		}
	}

	type found struct {
		unit   SystemdUnit
		masked bool
		file   string // host path of the unit content
	}
	units := map[string]*found{}
	for _, dir := range UnitDirs {
		entries, err := hfs.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			typ := UnitType(name)
			if typ == "" || e.IsDir() {
				continue
			}
			if _, seen := units[name]; seen {
				continue
			}
			p := path.Join(dir, name)
			f := &found{unit: SystemdUnit{Name: name, Type: typ, Path: p}, file: p}
			if e.Type()&fs.ModeSymlink != 0 {
				target, err := hfs.Readlink(p)
				if err != nil {
					continue
				}
				if !path.IsAbs(target) {
					target = path.Join(dir, target)
				}
				if target == "/dev/null" {
					f.masked = true
				} else if path.Base(target) != name {
					// Alias symlink (Alias= in [Install]): it exists only when
					// the target is enabled; it is not a unit of its own.
					enabled[path.Base(target)] = true
					continue
				} else {
					f.file = target
					f.unit.Path = target
				}
			}
			units[name] = f
		}
	}

	out := make([]SystemdUnit, 0, len(units))
	for name, f := range units {
		u := f.unit
		switch {
		case f.masked:
			u.EnabledState = "masked"
		default:
			data, err := hfs.ReadFile(f.file)
			if err != nil {
				u.EnabledState = "unknown"
				break
			}
			if len(strings.TrimSpace(string(data))) == 0 && f.file != "" {
				// Empty unit files in /etc are also a masking technique.
				u.EnabledState = "masked"
				break
			}
			uf := ParseUnitFile(data)
			u.Description = uf.Description
			if len(uf.ExecStart) > 0 {
				masked := make([]string, len(uf.ExecStart))
				for i, cmd := range uf.ExecStart {
					argv0, _, _ := strings.Cut(cmd, " ")
					masked[i] = mask.Cmdline(argv0, cmd)
				}
				u.ExecStart = mask.Truncate(strings.Join(masked, " ; "), maxCmdline)
			}
			switch {
			case enabled[name]:
				u.EnabledState = "enabled"
			case !uf.HasInstall:
				u.EnabledState = "static"
			default:
				u.EnabledState = "disabled"
			}
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
