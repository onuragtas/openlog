// Package version holds the build version of the agent, injected at build time:
//
//	-ldflags "-X github.com/onuragtas/openlog/agents/infra/internal/version.Version=0.4.0
//	          -X github.com/onuragtas/openlog/agents/infra/internal/version.Commit=<sha>
//	          -X github.com/onuragtas/openlog/agents/infra/internal/version.Date=<RFC3339>"
//
// See docs/contracts/releases-updates.md §1.
package version

import "strings"

// DevVersion is the version of builds without an injected version.
const DevVersion = "0.0.0-dev"

// Build information; set with -ldflags -X.
var (
	Version = DevVersion
	Commit  = ""
	Date    = ""
)

// Current returns the SemVer version this binary reports. Dev builds report 0.0.0-dev+<commit>.
func Current() string {
	v := strings.TrimPrefix(Version, "v")
	if v == "" {
		v = DevVersion
	}
	if v == DevVersion {
		c := Commit
		if c == "" {
			c = "unknown"
		}
		return v + "+" + sanitize(c)
	}
	return v
}

// sanitize keeps a commit usable as SemVer build metadata.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return strings.Trim(b.String(), ".")
}
