package updater

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
	"gopkg.in/yaml.v3"

	"github.com/onuragtas/openlog/internal/updatemsg"
	"github.com/onuragtas/openlog/internal/version"
)

// Notice is an operator hint shown with the updater status (Settings → Version and updates), e.g. compose files
// older than the running version. Message is the English text of Code (internal/updatemsg).
type Notice struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Params  map[string]string `json:"params,omitempty"`
}

func newNotice(code string, params updatemsg.Params) Notice {
	return Notice{Code: code, Message: updatemsg.Format(code, params), Params: params}
}

// Noticer is implemented by engines that report installation notices; the runner refreshes Status.Notices with it
// on every run.
type Noticer interface {
	Notices(ctx context.Context, running string, st *Status) []Notice
}

// composeVersionRe finds the version marker of deploy/compose/docker-compose.yml (stamped by `make release-prepare`).
var composeVersionRe = regexp.MustCompile(`(?m)^x-openlog-compose-version:\s*["']?([0-9A-Za-z.+-]+)["']?\s*(?:#.*)?$`)

const composeConfigHashLabel = "com.docker.compose.config-hash"

// Notices implements Noticer: compose files older than the running version, and compose changes of an installed
// bundle that still wait for `docker compose up -d` or a restart. Nothing is changed on disk.
func (e *ComposeEngine) Notices(ctx context.Context, running string, st *Status) []Notice {
	var out []Notice
	if n, ok := e.outdatedNotice(running); ok {
		out = append(out, n)
	}
	if n, ok := e.pendingNotice(ctx, st); ok {
		out = append(out, n)
	}
	return out
}

func (e *ComposeEngine) outdatedNotice(running string) (Notice, bool) {
	if e.Cfg.ComposeDir == "" {
		return Notice{}, false
	}
	runV, err := lib.ParseVersion(running)
	if err != nil || strings.Contains(running, "dev") {
		return Notice{}, false
	}
	bundle, err := e.installedBundleVersion()
	if err != nil {
		return Notice{}, false
	}
	filesVersion, code := bundle, updatemsg.ComposeOutdatedBundle
	if bundle == "" {
		b, err := os.ReadFile(filepath.Join(e.Cfg.ComposeDir, "docker-compose.yml"))
		if err != nil {
			e.Log.Debug("compose version check skipped: no docker-compose.yml in the compose directory", "dir", e.Cfg.ComposeDir, "err", err)
			return Notice{}, false
		}
		code = updatemsg.ComposeOutdated
		if m := composeVersionRe.FindSubmatch(b); m != nil {
			filesVersion = string(m[1])
		}
	}
	params := updatemsg.Params{"running_version": running}
	if filesVersion == "" {
		// No marker: the files predate version markers, i.e. the release of this updater.
		self := e.SelfVersion
		if self == "" {
			self = version.Version
		}
		selfV, err := lib.ParseVersion(self)
		if err != nil || strings.Contains(self, "dev") || lib.Compare(runV, selfV) < 0 {
			return Notice{}, false
		}
		params["files_version"] = "< " + self
	} else {
		filesV, err := lib.ParseVersion(filesVersion)
		if err != nil || lib.Compare(filesV, runV) >= 0 {
			return Notice{}, false
		}
		params["files_version"] = filesVersion
	}
	n := newNotice(code, params)
	e.Log.Warn(n.Message, "compose_dir", e.Cfg.ComposeDir)
	return n, true
}

// recordPending stores the changes of an installed bundle that the updater did not apply.
func (e *ComposeEngine) recordPending(ctx context.Context, st *Status, b *composeBundle) {
	if len(b.Pending) == 0 {
		return
	}
	cc := &ComposeChanges{Version: b.Version, InstalledAt: e.now()}
	for _, p := range b.Pending {
		if p.Reason == "definition" {
			p.ConfigHash = e.configHash(ctx, p.Service)
		}
		cc.Services = append(cc.Services, p)
	}
	// Changes of an earlier bundle that were not applied yet stay pending.
	if prev := st.ComposeChanges; prev != nil {
		for _, p := range prev.Services {
			if !slices.ContainsFunc(cc.Services, func(q PendingService) bool { return q.Service == p.Service }) {
				cc.Services = append(cc.Services, p)
			}
		}
	}
	st.ComposeChanges = cc
	e.Log.Warn("compose changes are not applied by the updater: re-run install-server.sh (or docker compose up -d)",
		"version", b.Version, "services", pendingNames(cc.Services))
}

// pendingNotice reports the pending services and forgets the ones that were recreated (definition: a different
// compose config hash) or restarted (files) since the bundle was installed.
func (e *ComposeEngine) pendingNotice(ctx context.Context, st *Status) (Notice, bool) {
	cc := st.ComposeChanges
	if cc == nil {
		return Notice{}, false
	}
	var keep []PendingService
	for _, p := range cc.Services {
		if e.applied(ctx, p, cc.InstalledAt) {
			e.Log.Info("pending compose change applied", "service", p.Service, "reason", p.Reason)
			continue
		}
		keep = append(keep, p)
	}
	if len(keep) == 0 {
		st.ComposeChanges = nil
		return Notice{}, false
	}
	cc.Services = keep
	return newNotice(updatemsg.ComposeChangesPending, updatemsg.Params{"version": cc.Version, "services": strings.Join(pendingNames(keep), ", ")}), true
}

func pendingNames(ps []PendingService) []string {
	var out []string
	for _, p := range ps {
		if !slices.Contains(out, p.Service) {
			out = append(out, p.Service)
		}
	}
	return out
}

func (e *ComposeEngine) configHash(ctx context.Context, service string) string {
	cs, err := e.Docker.ListContainers(ctx, map[string]string{labelProject: e.Cfg.Project, labelService: service})
	if err != nil {
		return ""
	}
	for _, c := range cs {
		if c.Labels[labelOneoff] != "True" && !strings.HasSuffix(c.Name(), preUpdateSuffix) {
			return c.Labels[composeConfigHashLabel]
		}
	}
	return ""
}

// applied: every container of the service already has what the compose file on disk asks for (definition), or
// was started after installedAt (files).
//
// A definition change is checked against the running container rather than only against its config hash. The
// hash alone cannot answer the question: `docker compose up -d` leaves the container untouched when it already
// matches the file -- which is exactly what a pending change that turned out to be no change looks like -- and
// the notice then had no way of ever clearing. An operator who re-runs install-server.sh, sees "Running", and
// is told again to re-run install-server.sh has been handed a loop. The hash stays as the fallback for
// whatever the compose file says that this cannot read.
func (e *ComposeEngine) applied(ctx context.Context, p PendingService, installedAt time.Time) bool {
	cs, err := e.Docker.ListContainers(ctx, map[string]string{labelProject: e.Cfg.Project, labelService: p.Service})
	if err != nil {
		return false
	}
	n := 0
	for _, c := range cs {
		if c.Labels[labelOneoff] == "True" || strings.HasSuffix(c.Name(), preUpdateSuffix) {
			continue
		}
		n++
		full, err := e.Docker.InspectContainer(ctx, c.ID)
		if err != nil {
			return false
		}
		if p.Reason == "definition" {
			if ok, verified := e.satisfies(p.Service, full); verified {
				if !ok {
					return false
				}
				continue
			}
			if p.ConfigHash == "" || c.Labels[composeConfigHashLabel] == p.ConfigHash {
				return false
			}
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, full.State.StartedAt)
		if err != nil || !started.After(installedAt) {
			return false
		}
	}
	return n > 0
}

// satisfies reports whether the container has every volume and every published port of the service as the
// compose file on disk spells them.
//
// verified is false when the answer cannot be trusted -- no compose file, no such service, or a line this does
// not understand (a port range, an unexpanded variable, a long-syntax entry without a target) -- and the caller
// then falls back to the config hash rather than guessing.
func (e *ComposeEngine) satisfies(service string, c *Container) (ok, verified bool) {
	svc, found := e.composeService(service)
	if !found {
		return false, false
	}
	expand := e.interpolator()
	vols, ok := anyList(svc["volumes"])
	if !ok {
		return false, false
	}
	for _, v := range vols {
		dst, src, understood := volumeTarget(v, expand)
		if !understood {
			return false, false
		}
		if !hasMount(c, dst, src, e.Cfg.ComposeDir) {
			return false, true
		}
	}
	ports, ok := anyList(svc["ports"])
	if !ok {
		return false, false
	}
	for _, p := range ports {
		port, understood := publishedPort(p, expand)
		if !understood {
			return false, false
		}
		if !hasPort(c, port) {
			return false, true
		}
	}
	return true, true
}

// composeService is one service of the compose file on disk.
func (e *ComposeEngine) composeService(service string) (map[string]any, bool) {
	if e.Cfg.ComposeDir == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(e.Cfg.ComposeDir, "docker-compose.yml"))
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	services, _ := m["services"].(map[string]any)
	svc, _ := services[service].(map[string]any)
	return svc, svc != nil
}

// interpolator expands compose variables the way the stack is actually started: against the .env file, with
// the defaults of ${VAR:-default}. Without it almost nothing in the shipped compose file can be read -- three
// of ClickHouse's eight volumes and both of its ports are variables.
func (e *ComposeEngine) interpolator() func(string) (string, bool) {
	var d *dotenv
	if e.Cfg.EnvFile != "" {
		if b, err := os.ReadFile(e.Cfg.EnvFile); err == nil {
			d, _ = parseDotenv(b)
		}
	}
	return func(s string) (string, bool) { return interpolate(s, d.lookup) }
}

// anyList accepts a missing key as an empty list; anything that is not a list is not understood.
func anyList(v any) ([]any, bool) {
	if v == nil {
		return nil, true
	}
	l, ok := v.([]any)
	return l, ok
}

// volumeTarget reads a volumes entry: the path inside the container, and the host path or volume name when the
// entry names one that can be resolved.
//
// A variable that is still unresolved leaves src empty rather than making the whole entry unreadable: which
// file ClickHouse's storage config comes from is the .env's business (and compose also reads the shell, which
// this process cannot see), while *that something is mounted there* is the part a pending change is about.
// Three of the eight ClickHouse volumes in the shipped file are such variables.
func volumeTarget(v any, expand func(string) (string, bool)) (dst, src string, understood bool) {
	switch x := v.(type) {
	case string:
		s := x
		if e, ok := expand(x); ok {
			s = e
		}
		parts := splitOutsideVars(s)
		switch len(parts) {
		case 1: // an anonymous volume: only the path inside the container
			dst = parts[0]
		case 2, 3:
			dst, src = parts[1], parts[0]
		default:
			return "", "", false
		}
		if hasVar(dst) || !strings.HasPrefix(dst, "/") {
			return "", "", false
		}
		if hasVar(src) {
			src = ""
		}
		return dst, src, true
	case map[string]any:
		t, _ := x["target"].(string)
		s, _ := x["source"].(string)
		if hasVar(t) {
			return "", "", false
		}
		if hasVar(s) {
			s = ""
		}
		return t, s, t != ""
	}
	return "", "", false
}

// maskVars blanks out every ${...} and $NAME, keeping the length, so that a colon inside a default value
// (`${VAR:-./a.xml}:/etc/a.xml`) is not mistaken for the separator of the entry.
func maskVars(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); {
		if b[i] != '$' {
			i++
			continue
		}
		j := i + 1
		if j < len(b) && b[j] == '{' {
			depth := 0
			for ; j < len(b); j++ {
				if b[j] == '{' {
					depth++
				} else if b[j] == '}' {
					depth--
					if depth == 0 {
						j++
						break
					}
				}
			}
		} else {
			for j < len(b) && isNameChar(b[j]) {
				j++
			}
		}
		for k := i; k < j; k++ {
			b[k] = 0
		}
		i = j
	}
	return string(b)
}

func hasVar(s string) bool { return strings.ContainsRune(maskVars(s), 0) }

// splitOutsideVars splits on the colons that are not inside a variable reference.
func splitOutsideVars(s string) []string {
	masked := maskVars(s)
	var out []string
	start := 0
	for i := range len(masked) {
		if masked[i] == ':' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// hasMount: the container has something mounted at dst, from src when the file names one. A relative source is
// resolved against the compose directory, which is how docker reads it.
func hasMount(c *Container, dst, src, composeDir string) bool {
	for _, m := range c.Mounts {
		if m.Destination != dst {
			continue
		}
		if src == "" {
			return true
		}
		if m.Type == "bind" {
			want := src
			if !filepath.IsAbs(want) {
				want = filepath.Join(composeDir, want)
			}
			return filepath.Clean(m.Source) == filepath.Clean(want)
		}
		// A named volume carries the project prefix in Name ("proj_data" for "data").
		return m.Name == src || strings.HasSuffix(m.Name, "_"+src)
	}
	return false
}

// publishedPort reads a ports entry as "<container port>/<proto>", the key docker binds it under.
func publishedPort(p any, expand func(string) (string, bool)) (string, bool) {
	var s string
	switch x := p.(type) {
	case string:
		s = x
		if e, ok := expand(x); ok {
			s = e
		}
	case int:
		s = strconv.Itoa(x)
	case map[string]any:
		switch t := x["target"].(type) {
		case int:
			s = strconv.Itoa(t)
		case string:
			s = t
		default:
			return "", false
		}
		proto, _ := x["protocol"].(string)
		if proto == "" {
			proto = "tcp"
		}
		return s + "/" + proto, s != ""
	default:
		return "", false
	}
	// The container port is the last field; the host port may still be a variable, and is not compared.
	parts := splitOutsideVars(s)
	container := parts[len(parts)-1]
	proto := "tcp"
	if i := strings.Index(container, "/"); i >= 0 {
		proto, container = container[i+1:], container[:i]
	}
	// A range is left to the config hash.
	if container == "" || hasVar(container) || strings.Contains(container, "-") {
		return "", false
	}
	return container + "/" + proto, true
}

// hasPort: the container publishes that port.
func hasPort(c *Container, port string) bool {
	bindings, _ := c.HostConfig["PortBindings"].(map[string]any)
	v, ok := bindings[port]
	if !ok {
		return false
	}
	l, _ := v.([]any)
	return len(l) > 0
}
