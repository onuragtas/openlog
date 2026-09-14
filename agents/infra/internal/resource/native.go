//go:build darwin || windows

package resource

import (
	"os"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v4/host"

	"github.com/onuragtas/openlog/agents/infra/internal/osinfo"
)

// NativeHostID resolves host.id on macOS (IOPlatformUUID) and Windows (MachineGuid), falling back to the UUID
// persisted in stateDir. Cloned Windows images that were not generalized with sysprep share a MachineGuid.
func NativeHostID(stateDir string) (string, bool, error) {
	if id, err := host.HostID(); err == nil {
		id = strings.ToLower(strings.TrimSpace(id))
		if validID.MatchString(id) && strings.Trim(id, "0-") != "" {
			return id, true, nil
		}
	}
	return persistedHostID(stateDir)
}

// DetectNative builds the resource information of a macOS or Windows host.
func DetectNative(hostID, agentVersion string, extra map[string]string) Info {
	i := osinfo.Get()
	info := Info{HostID: hostID, AgentVersion: agentVersion, Extra: extra, Arch: NormalizeArch(runtime.GOARCH),
		OSType: runtime.GOOS, OSName: i.ID, OSVersion: i.Version, OSDescription: i.PrettyName, KernelRelease: i.KernelRelease}
	info.HostName, _ = os.Hostname()
	return info
}
