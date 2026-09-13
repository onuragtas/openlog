// Package version holds the openlog product version of this build (docs/contracts/releases-updates.md §1).
// Release builds set the variables with -ldflags "-X github.com/onuragtas/openlog/internal/version.Version=…".
package version

import "strings"

var (
	// Version is the SemVer product version without a leading "v".
	Version = "0.0.0-dev"
	// Commit is the git commit the binary was built from.
	Commit = "unknown"
	// Date is the build time in RFC 3339.
	Date = ""
)

// String returns the version with the commit as build metadata for dev builds, e.g.
// "0.0.0-dev+abc123", and the plain version otherwise.
func String() string {
	if strings.HasSuffix(Version, "-dev") && Commit != "unknown" && Commit != "" {
		return Version + "+" + Commit
	}
	return Version
}
