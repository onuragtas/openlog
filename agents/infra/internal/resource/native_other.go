//go:build !darwin && !windows

package resource

// NativeHostID is only used on macOS and Windows (hostfs.FS.NativeOS).
func NativeHostID(stateDir string) (string, bool, error) { return persistedHostID(stateDir) }

// DetectNative is only used on macOS and Windows (hostfs.FS.NativeOS).
func DetectNative(hostID, agentVersion string, extra map[string]string) Info {
	return Info{HostID: hostID, AgentVersion: agentVersion, Extra: extra}
}
