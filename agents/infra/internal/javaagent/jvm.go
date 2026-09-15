package javaagent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/mask"
)

// Proc is a process that may be a JVM (procs_linux.go, procs_other.go; tests substitute a fixture).
type Proc struct {
	PID  int
	Exe  string
	Args []string
	// Env holds only the JVM option variables (JAVA_TOOL_OPTIONS, JDK_JAVA_OPTIONS, _JAVA_OPTIONS); nil when unreadable.
	Env   []string
	Start time.Time
	// OpenJars are the .jar files the process has open (Linux /proc/<pid>/fd, " (deleted)" kept); nil when unreadable.
	OpenJars  []string
	Cwd       string
	Container bool
}

// jvmOptionVars are the environment variables the JVM reads options from.
var jvmOptionVars = []string{"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS"}

func keepOptionVar(kv string) bool {
	for _, v := range jvmOptionVars {
		if strings.HasPrefix(kv, v+"=") {
			return true
		}
	}
	return false
}

// isJavaName reports whether an executable base name is a Java launcher.
func isJavaName(name string) bool {
	n := strings.ToLower(filepath.Base(strings.ReplaceAll(name, `\`, "/")))
	n = strings.TrimSuffix(n, ".exe")
	return n == "java" || n == "javaw"
}

// AgentPaths returns the -javaagent: paths of a command line and the JVM option variables, in order, de-duplicated.
func AgentPaths(args, env []string) []string {
	var out []string
	add := func(tok string) {
		rest, ok := strings.CutPrefix(tok, "-javaagent:")
		if !ok {
			return
		}
		p := rest
		// options follow "=", but a Windows drive letter uses ":" only, so cut at the first "=".
		if i := strings.IndexByte(rest, '='); i > 0 {
			p = rest[:i]
		}
		p = strings.Trim(p, `"'`)
		if p != "" && !contains(out, p) {
			out = append(out, p)
		}
	}
	for _, a := range args {
		add(a)
	}
	for _, kv := range env {
		if _, v, ok := strings.Cut(kv, "="); ok && keepOptionVar(kv) {
			for _, tok := range splitOptions(v) {
				add(tok)
			}
		}
	}
	return out
}

// splitOptions splits JAVA_TOOL_OPTIONS on whitespace; double or single quotes group a token.
func splitOptions(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	in := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, in = r, true
		case quote == 0 && (r == ' ' || r == '\t' || r == '\n'):
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// inspector turns processes into JVM reports for one configuration and install state.
type inspector struct {
	goos string
	root string // install_root
	link string // link_path
	// foreignLink: link_path is a file the infra agent did not create (JVMs using it are not managed).
	foreignLink bool
	current     string // current version ("" = not installed)
	status      *Status
	cache       map[string]jarInfo
}

type jarInfo struct {
	size    int64
	mod     time.Time
	version string
}

func (in *inspector) clean(p string) string {
	if in.goos == "windows" {
		p = strings.ReplaceAll(p, "/", `\`)
		if len(p) >= 2 && p[1] == ':' {
			return strings.ToLower(filepath.Clean(p))
		}
		return strings.ToLower(p)
	}
	return filepath.Clean(p)
}

func (in *inspector) sep() string {
	if in.goos == "windows" {
		return `\`
	}
	return "/"
}

// under reports whether p is dir or below it.
func (in *inspector) under(p, dir string) bool {
	p, dir = in.clean(p), in.clean(dir)
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, in.sep())+in.sep())
}

// versionOf returns v when p is <root>/versions/<v>/<JarName>.
func (in *inspector) versionOf(p string) string {
	p = strings.TrimSuffix(p, " (deleted)")
	versions := in.clean(in.root) + in.sep() + "versions" + in.sep()
	rest, ok := strings.CutPrefix(in.clean(p), versions)
	if !ok {
		return ""
	}
	v, file, ok := strings.Cut(rest, in.sep())
	if !ok || !strings.EqualFold(file, JarName) || !validVersion(v) {
		return ""
	}
	return v
}

func (in *inspector) isLink(p string) bool { return in.clean(p) == in.clean(in.link) }

func (in *inspector) isCurrent(p string) bool {
	return in.clean(p) == in.clean(in.root+in.sep()+"current"+in.sep()+JarName)
}

// openlogJar reports whether an agent path is an openlog Java agent (managed paths or openlog-javaagent*.jar).
func (in *inspector) openlogJar(p string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(p, `\`, "/")))
	return in.isLink(p) || in.under(p, in.root) || (strings.HasPrefix(base, "openlog-javaagent") && strings.HasSuffix(base, ".jar"))
}

func (in *inspector) absolute(p, cwd string) string {
	if in.goos == "windows" {
		if len(p) >= 2 && p[1] == ':' || strings.HasPrefix(p, `\\`) || cwd == "" {
			return p
		}
		return cwd + `\` + p
	}
	if filepath.IsAbs(p) || cwd == "" {
		return p
	}
	return filepath.Join(cwd, p)
}

// versionAt returns the version of history that was in place at t (newest first; false when t precedes it).
func versionAt(history []Switch, t time.Time) (string, bool) {
	for _, s := range history {
		if !s.At.After(t) {
			return s.Version, true
		}
	}
	return "", false
}

// jarVersionAt is the openlog version of a jar file that has not changed since the process started.
func (in *inspector) jarVersionAt(p string, start time.Time) string {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || (!start.IsZero() && fi.ModTime().After(start)) {
		return ""
	}
	if c, ok := in.cache[p]; ok && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
		return c.version
	}
	v := JarVersion(p)
	if in.cache != nil {
		in.cache[p] = jarInfo{size: fi.Size(), mod: fi.ModTime(), version: v}
	}
	return v
}

// jvm builds the report of one agent path of a process. The loaded version comes from (1) the jar files the JVM
// has open (Linux), (2) the version directory named by the path, (3) the switch history of current/link_path
// compared with the process start time, or (4) the jar's manifest when the file did not change since the start.
func (in *inspector) jvm(p Proc, agentPath string) JVM {
	name := ""
	if p.Exe != "" {
		name = filepath.Base(strings.ReplaceAll(p.Exe, `\`, "/"))
	} else if len(p.Args) > 0 {
		name = filepath.Base(strings.ReplaceAll(p.Args[0], `\`, "/"))
	}
	j := JVM{PID: p.PID, Name: mask.Truncate(name, 128), AgentPath: mask.Truncate(agentPath, 512), Container: p.Container,
		Command: mask.Truncate(mask.Cmdline(p.Exe, strings.Join(p.Args, " ")), 512)}
	if !p.Start.IsZero() {
		j.StartedAt = p.Start.UTC().Format(time.RFC3339)
	}
	if p.Container {
		return j // the path belongs to the container's file system
	}
	abs := in.absolute(agentPath, p.Cwd)
	j.Managed = (in.isLink(abs) && !in.foreignLink) || in.under(abs, in.root)

	// (1) open files
	for _, f := range p.OpenJars {
		if v := in.versionOf(f); v != "" {
			j.LoadedVersion = v
			break
		}
	}
	if j.LoadedVersion == "" && p.OpenJars != nil && !j.Managed {
		for _, f := range p.OpenJars {
			if in.clean(strings.TrimSuffix(f, " (deleted)")) == in.clean(abs) && !strings.HasSuffix(f, " (deleted)") {
				j.LoadedVersion = in.jarVersionAt(abs, p.Start)
				break
			}
		}
	}
	var switchedAt time.Time // when the path started pointing at the current version
	if j.Managed && in.status != nil {
		hist := in.status.Switches
		if in.isLink(abs) {
			hist = in.status.LinkSwitches
		}
		if len(hist) > 0 && hist[0].Version == in.current {
			switchedAt = hist[0].At
		}
		if j.LoadedVersion == "" {
			switch v := in.versionOf(abs); {
			case v != "": // (2)
				j.LoadedVersion = v
			case !p.Start.IsZero(): // (3)
				j.LoadedVersion, _ = versionAt(hist, p.Start)
			}
		}
	}
	if j.LoadedVersion == "" && !j.Managed { // (4)
		j.LoadedVersion = in.jarVersionAt(abs, p.Start)
	}
	if j.Managed && in.current != "" && in.versionOf(abs) == "" {
		switch {
		case j.LoadedVersion != "":
			j.RestartPending = j.LoadedVersion != in.current
		case !switchedAt.IsZero() && !p.Start.IsZero():
			j.RestartPending = p.Start.Before(switchedAt)
		}
	}
	return j
}

// JVMs returns the JVMs of procs that load an openlog Java agent, sorted by pid (at most MaxJVMs).
func (in *inspector) JVMs(procs []Proc) []JVM {
	out := []JVM{}
	for _, p := range procs {
		for _, ap := range AgentPaths(p.Args, p.Env) {
			if in.openlogJar(in.absolute(ap, p.Cwd)) {
				out = append(out, in.jvm(p, ap))
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	if len(out) > MaxJVMs {
		out = out[:MaxJVMs]
	}
	return out
}
