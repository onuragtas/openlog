package openlog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

// hostFS reads host files below a root prefix (tests use a temp dir; containers may use
// OPENLOG_HOST_ROOT=/host like the infra agent's host.root_path).
type hostFS struct{ root string }

func (f hostFS) path(p string) string {
	if f.root == "" || f.root == "/" {
		return filepath.Clean("/" + p)
	}
	return filepath.Join(f.root, filepath.Clean("/"+p))
}

func (f hostFS) read(p string) ([]byte, error) { return os.ReadFile(f.path(p)) }

func (f hostFS) readString(p string) string {
	b, err := f.read(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// validHostID is the infra agent's validity rule (agents/infra/internal/resource).
var validHostID = regexp.MustCompile(`^[0-9A-Za-z-]{8,}$`)

// hostIDFiles is the infra agent's resolution chain (semantic-conventions.md §1). Keep in sync.
var hostIDFiles = []string{"/etc/machine-id", "/var/lib/dbus/machine-id", "/sys/class/dmi/id/product_uuid"}

// Host id sources reported at debug level.
const (
	hostIDSourceConfig    = "config"
	hostIDSourceInfra     = "infra-agent-state"
	hostIDSourcePlatform  = "platform"
	hostIDSourceGenerated = "generated"
)

// resolveHostID implements the same chain as the infra agent so both report the same
// host.id on one machine:
//
//  1. /etc/machine-id → /var/lib/dbus/machine-id → /sys/class/dmi/id/product_uuid
//     (non-empty, [0-9A-Za-z-]{8,}, not all zeros; lower-cased)
//  2. the UUID the infra agent generated and persisted in its state dir (<state_dir>/host-id)
//  3. non-Linux: the platform machine id (macOS IOPlatformUUID, Windows MachineGuid)
//  4. a UUID generated and persisted by this agent (stateDir, default user cache dir)
//
// Step 4 cannot match an infra agent that generates its own id later; the caller logs it.
func resolveHostID(ctx context.Context, fs hostFS, infraStateDir, stateDir string) (id, source string) {
	for _, p := range hostIDFiles {
		if v := fs.readString(p); validHostID.MatchString(v) && strings.Trim(v, "0-") != "" {
			return strings.ToLower(v), p
		}
	}
	if infraStateDir != "" {
		if v := fs.readString(filepath.Join(infraStateDir, "host-id")); validHostID.MatchString(v) {
			return v, hostIDSourceInfra
		}
	}
	if v := platformHostID(ctx); v != "" {
		return v, hostIDSourcePlatform
	}
	if stateDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return "", ""
		}
		stateDir = filepath.Join(dir, "openlog")
	}
	file := filepath.Join(stateDir, "host-id")
	if b, err := os.ReadFile(file); err == nil {
		if v := strings.TrimSpace(string(b)); validHostID.MatchString(v) {
			return v, hostIDSourceGenerated
		}
	}
	gen := newUUIDv4()
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return "", ""
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(gen+"\n"), 0o640); err != nil {
		return "", ""
	}
	if err := os.Rename(tmp, file); err != nil {
		return "", ""
	}
	return gen, hostIDSourceGenerated
}

// platformHostID returns the OS machine id on non-Linux systems (the infra agent is
// Linux-only, so there is nothing to match). A variable so tests can disable it.
var platformHostID = func(ctx context.Context) string {
	if runtime.GOOS == "linux" {
		return ""
	}
	r, err := resource.New(ctx, resource.WithHostID())
	if err != nil {
		return ""
	}
	if v, ok := r.Set().Value("host.id"); ok {
		return strings.ToLower(v.AsString())
	}
	return ""
}

func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

var (
	containerIDInCgroup    = regexp.MustCompile(`([0-9a-f]{64})(?:\.scope)?$`)
	containerIDInMountinfo = regexp.MustCompile(`containers/([0-9a-f]{64})/`)
)

// containerID returns the id of the container this process runs in: the last 64-hex
// segment of /proc/self/cgroup (cgroup v1, or v2 without a cgroup namespace), otherwise
// the Docker/Podman container directory seen in /proc/self/mountinfo (cgroup v2 with a
// private cgroup namespace, where /proc/self/cgroup is just "0::/").
func containerID(fs hostFS) string {
	if b, err := fs.read("/proc/self/cgroup"); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			parts := strings.SplitN(sc.Text(), ":", 3)
			if len(parts) != 3 {
				continue
			}
			segs := strings.Split(parts[2], "/")
			for i := len(segs) - 1; i >= 0; i-- {
				if m := containerIDInCgroup.FindStringSubmatch(segs[i]); m != nil {
					return m[1]
				}
			}
		}
	}
	if b, err := fs.read("/proc/self/mountinfo"); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, "/sandboxes/") {
				continue // containerd pod sandbox, not this container
			}
			if m := containerIDInMountinfo.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// normalizeArch maps kernel/Go architecture names to OTel host.arch values (same as the infra agent).
func normalizeArch(a string) string {
	switch a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "i386", "i686", "386":
		return "x86"
	case "armv7l", "armv6l", "arm":
		return "arm32"
	case "ppc64le", "ppc64":
		return "ppc64"
	case "s390x":
		return "s390x"
	}
	return a
}

func hostName(fs hostFS) string {
	if v := fs.readString("/proc/sys/kernel/hostname"); v != "" {
		return v
	}
	if v := fs.readString("/etc/hostname"); v != "" {
		return v
	}
	h, _ := os.Hostname()
	return h
}

func parseOSRelease(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `"'`)
		}
		out[k] = v
	}
	return out
}

// k8sAttributes reads Kubernetes metadata exposed through the downward API as
// environment variables (K8S_POD_NAME, …); inside a pod the namespace falls back to the
// service account namespace file and the pod name to HOSTNAME.
func k8sAttributes(fs hostFS, lookup lookupFunc) map[string]string {
	out := map[string]string{}
	env := func(names ...string) string {
		for _, n := range names {
			if v, ok := lookup(n); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	set := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	set("k8s.pod.name", env("K8S_POD_NAME", "POD_NAME"))
	set("k8s.pod.uid", env("K8S_POD_UID", "POD_UID"))
	set("k8s.namespace.name", env("K8S_NAMESPACE_NAME", "K8S_NAMESPACE", "POD_NAMESPACE"))
	set("k8s.node.name", env("K8S_NODE_NAME", "NODE_NAME"))
	set("k8s.container.name", env("K8S_CONTAINER_NAME", "CONTAINER_NAME"))
	set("k8s.deployment.name", env("K8S_DEPLOYMENT_NAME"))
	set("k8s.cluster.name", env("K8S_CLUSTER_NAME"))
	if _, inPod := lookup("KUBERNETES_SERVICE_HOST"); inPod {
		if out["k8s.namespace.name"] == "" {
			set("k8s.namespace.name", fs.readString("/var/run/secrets/kubernetes.io/serviceaccount/namespace"))
		}
		if out["k8s.pod.name"] == "" {
			set("k8s.pod.name", env("HOSTNAME"))
		}
	}
	return out
}

func osType() string {
	switch runtime.GOOS {
	case "dragonfly":
		return "dragonflybsd"
	case "zos":
		return "z_os"
	}
	return runtime.GOOS
}

// detectAttributes gathers host, OS, process, container and Kubernetes attributes.
func detectAttributes(ctx context.Context, cfg *Config, lookup lookupFunc) (map[string]string, string) {
	fs := hostFS{root: cfg.HostRoot}
	a := map[string]string{
		"host.name":                   hostName(fs),
		"host.arch":                   normalizeArch(runtime.GOARCH),
		"os.type":                     osType(),
		"process.pid":                 strconv.Itoa(os.Getpid()),
		"process.runtime.name":        "go",
		"process.runtime.version":     runtime.Version(),
		"process.runtime.description": "go version " + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH,
	}
	if runtime.GOOS == "linux" {
		if v := fs.readString("/proc/sys/kernel/arch"); v != "" {
			a["host.arch"] = normalizeArch(v)
		}
		for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
			if b, err := fs.read(p); err == nil {
				osr := parseOSRelease(b)
				setIf(a, "os.name", osr["ID"])
				setIf(a, "os.version", osr["VERSION_ID"])
				setIf(a, "os.description", osr["PRETTY_NAME"])
				break
			}
		}
		setIf(a, "openlog.os.kernel_release", fs.readString("/proc/sys/kernel/osrelease"))
		setIf(a, "container.id", containerID(fs))
	}
	if exe, err := os.Executable(); err == nil {
		a["process.executable.path"] = exe
		a["process.executable.name"] = filepath.Base(exe)
	}
	if u, err := user.Current(); err == nil {
		a["process.owner"] = u.Username
	}
	for k, v := range k8sAttributes(fs, lookup) {
		a[k] = v
	}
	source := hostIDSourceConfig
	if cfg.HostID == "" {
		var id string
		id, source = resolveHostID(ctx, fs, cfg.InfraStateDir, cfg.StateDir)
		setIf(a, "host.id", id)
	}
	return a, source
}

func setIf(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// buildResource merges detected attributes < user resource attributes < explicit settings.
func buildResource(ctx context.Context, cfg *Config, lookup lookupFunc) (*resource.Resource, string, error) {
	attrs, source := detectAttributes(ctx, cfg, lookup)
	for k, v := range cfg.ResourceAttributes {
		attrs[k] = v
	}
	attrs["service.name"] = cfg.ServiceName
	setIf(attrs, "service.version", cfg.ServiceVersion)
	setIf(attrs, "service.namespace", cfg.ServiceNamespace)
	setIf(attrs, "deployment.environment.name", cfg.Environment)
	setIf(attrs, "host.id", cfg.HostID)
	attrs["telemetry.distro.name"] = DistroName
	attrs["telemetry.distro.version"] = Version

	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kvs := make([]attribute.KeyValue, 0, len(keys))
	for _, k := range keys {
		if k == "process.pid" {
			if pid, err := strconv.Atoi(attrs[k]); err == nil {
				kvs = append(kvs, attribute.Int(k, pid))
				continue
			}
		}
		kvs = append(kvs, attribute.String(k, attrs[k]))
	}
	r, err := resource.New(ctx, resource.WithTelemetrySDK(), resource.WithAttributes(kvs...))
	return r, source, err
}
