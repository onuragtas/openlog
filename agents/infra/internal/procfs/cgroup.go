package procfs

import (
	"regexp"
	"strings"
)

var containerIDRe = regexp.MustCompile(`^(?:docker-|cri-containerd-|crio-|libpod-|containerd-)?([0-9a-f]{64})(?:\.scope)?$`)

// ParseCgroup extracts the owning systemd unit and container id from
// /proc/<pid>/cgroup (cgroup v1 and v2). Container scopes are not reported as units.
func ParseCgroup(data []byte) (unit, containerID string) {
	var paths []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		// hierarchy-ID:controller-list:cgroup-path
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		// Prefer the unified (v2) or name=systemd hierarchy; fall back to any.
		if parts[0] == "0" || parts[1] == "name=systemd" {
			paths = append([]string{parts[2]}, paths...)
		} else {
			paths = append(paths, parts[2])
		}
	}
	for _, p := range paths {
		segs := strings.Split(p, "/")
		for i := len(segs) - 1; i >= 0; i-- {
			seg := segs[i]
			if seg == "" {
				continue
			}
			if containerID == "" {
				if m := containerIDRe.FindStringSubmatch(seg); m != nil {
					containerID = m[1]
					continue
				}
			}
			if unit == "" && (strings.HasSuffix(seg, ".service") || strings.HasSuffix(seg, ".scope")) {
				unit = seg
			}
		}
		if unit != "" || containerID != "" {
			break
		}
	}
	return unit, containerID
}
