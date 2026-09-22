// Package version holds the build version of the profiler, injected at build time:
//
//	-ldflags "-X github.com/onuragtas/openlog/agents/ebpf/internal/version.Version=0.4.0
//	          -X github.com/onuragtas/openlog/agents/ebpf/internal/version.Commit=<sha>
//	          -X github.com/onuragtas/openlog/agents/ebpf/internal/version.Date=<RFC3339>"
//
// Same shape as the infra agent's, so one release process sets both.
package version

import "strings"

// DevVersion is the version of builds without an injected version.
const DevVersion = "dev"

var (
	Version = DevVersion
	Commit  = ""
	Date    = ""
)

// Current returns a SemVer string. A build with no injected version is not called 0.0.0: it says it is a
// development build and carries the commit, so a profile's telemetry.sdk.version can never be mistaken for
// a release that was never cut.
func Current() string {
	v := strings.TrimPrefix(strings.TrimSpace(Version), "v")
	if v != "" && v != DevVersion {
		return v
	}
	c := sanitize(Commit)
	if c == "" {
		c = "unknown"
	}
	return "0.0.0-dev+" + c
}

// sanitize keeps the characters SemVer build metadata allows, so Current() is always parseable.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}
