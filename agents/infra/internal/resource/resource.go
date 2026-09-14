// Package resource resolves the host identity and builds the OTLP resource
// attached to every payload (semantic-conventions §1).
package resource

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/ids"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/procfs"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// AgentName is the value of openlog.agent.name.
const AgentName = "openlog-infra-agent"

// HostIDFile is the file name of the generated host id in the state dir.
const HostIDFile = "host-id"

var validID = regexp.MustCompile(`^[0-9A-Za-z-]{8,}$`)

// HostID resolves host.id: /etc/machine-id → /var/lib/dbus/machine-id →
// /sys/class/dmi/id/product_uuid → UUID persisted in stateDir. The returned
// bool is false when a generated id could not be persisted.
func HostID(fs *hostfs.FS, stateDir string) (string, bool, error) {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id", "/sys/class/dmi/id/product_uuid"} {
		if v, err := fs.ReadString(p); err == nil && validID.MatchString(v) && strings.Trim(v, "0-") != "" {
			return strings.ToLower(v), true, nil
		}
	}
	return persistedHostID(stateDir)
}

// persistedHostID reads the UUID persisted in stateDir, generating and persisting one when missing.
func persistedHostID(stateDir string) (string, bool, error) {
	file := filepath.Join(stateDir, HostIDFile)
	if b, err := os.ReadFile(file); err == nil {
		if v := strings.TrimSpace(string(b)); validID.MatchString(v) {
			return v, true, nil
		}
	}
	id := ids.NewV4()
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return id, false, fmt.Errorf("resource: persist host id: %w", err)
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o640); err != nil {
		return id, false, fmt.Errorf("resource: persist host id: %w", err)
	}
	if err := os.Rename(tmp, file); err != nil {
		return id, false, fmt.Errorf("resource: persist host id: %w", err)
	}
	return id, true, nil
}

// RuntimeDir is the agent's runtime directory (systemd RuntimeDirectory=openlog-infra-agent,
// mode 0755). The running agent publishes its host.id there for APM agents on the same host,
// including applications in containers that mount the directory read-only.
const RuntimeDir = "/run/openlog-infra-agent"

// PublishHostID writes id to <dir>/host-id (0644, atomic). A missing dir is not created
// (the agent does not run under systemd and nothing mounted it): it returns nil.
func PublishHostID(dir, id string) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil
	}
	file := filepath.Join(dir, HostIDFile)
	if b, err := os.ReadFile(file); err == nil && strings.TrimSpace(string(b)) == id {
		return nil
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o644); err != nil {
		return fmt.Errorf("resource: publish host id: %w", err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil { // umask
		return fmt.Errorf("resource: publish host id: %w", err)
	}
	if err := os.Rename(tmp, file); err != nil {
		return fmt.Errorf("resource: publish host id: %w", err)
	}
	return nil
}

// Info holds the resolved resource attributes.
type Info struct {
	HostID        string
	HostName      string
	Arch          string
	OSName        string
	OSVersion     string
	OSDescription string
	KernelRelease string
	AgentVersion  string
	// OSType is os.type: "linux" (default when empty), "darwin" or "windows".
	OSType string
	Extra  map[string]string
}

// Detect gathers resource information from the host file system.
func Detect(fs *hostfs.FS, hostID, agentVersion string, extra map[string]string) Info {
	info := Info{HostID: hostID, AgentVersion: agentVersion, Extra: extra}
	info.HostName = Hostname(fs)
	info.Arch = Arch(fs)
	if osr := OSRelease(fs); osr != nil {
		info.OSName = osr["ID"]
		info.OSVersion = osr["VERSION_ID"]
		info.OSDescription = osr["PRETTY_NAME"]
	}
	info.KernelRelease, _ = fs.ReadString("/proc/sys/kernel/osrelease")
	return info
}

// Hostname returns the kernel hostname.
func Hostname(fs *hostfs.FS) string {
	if v, err := fs.ReadString("/proc/sys/kernel/hostname"); err == nil && v != "" {
		return v
	}
	if v, err := fs.ReadString("/etc/hostname"); err == nil && v != "" {
		return v
	}
	h, _ := os.Hostname()
	return h
}

// Arch returns the OTel host.arch value.
func Arch(fs *hostfs.FS) string {
	if v, err := fs.ReadString("/proc/sys/kernel/arch"); err == nil && v != "" {
		return NormalizeArch(v)
	}
	return NormalizeArch(runtime.GOARCH)
}

// NormalizeArch maps kernel/Go architecture names to OTel host.arch values.
func NormalizeArch(a string) string {
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

// OSRelease reads /etc/os-release with /usr/lib/os-release fallback.
func OSRelease(fs *hostfs.FS) map[string]string {
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if b, err := fs.ReadFile(p); err == nil {
			return procfs.ParseKeyValueFile(b)
		}
	}
	return nil
}

// Proto builds the OTLP resource.
func (i Info) Proto() *resourcepb.Resource {
	attrs := []*commonpb.KeyValue{
		otlputil.Str("host.id", i.HostID),
		otlputil.Str("host.name", i.HostName),
		otlputil.Str("host.arch", i.Arch),
		otlputil.Str("os.type", cmp.Or(i.OSType, "linux")),
	}
	opt := func(k, v string) {
		if v != "" {
			attrs = append(attrs, otlputil.Str(k, v))
		}
	}
	opt("os.name", i.OSName)
	opt("os.version", i.OSVersion)
	opt("os.description", i.OSDescription)
	opt("openlog.os.kernel_release", i.KernelRelease)
	attrs = append(attrs,
		otlputil.Str("openlog.entity.type", "host"),
		otlputil.Str("openlog.agent.name", AgentName),
		otlputil.Str("openlog.agent.version", i.AgentVersion),
	)
	reserved := map[string]bool{}
	for _, a := range attrs {
		reserved[a.Key] = true
	}
	keys := make([]string, 0, len(i.Extra))
	for k := range i.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !reserved[k] { // user attributes must not override identity attributes
			attrs = append(attrs, otlputil.Str(k, i.Extra[k]))
		}
	}
	return &resourcepb.Resource{Attributes: attrs}
}

// ErrNoHostID is returned when no host id can be determined.
var ErrNoHostID = errors.New("resource: no host id")
