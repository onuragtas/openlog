package openlog

// Version is the openlog product version of this agent (D-025: every component
// shares one SemVer). It is reported as telemetry.distro.version.
//
// Go libraries are compiled by the user's build, so -ldflags injection is not
// possible: release tooling rewrites this line before tagging
// (make -C agents/go set-version VERSION=x.y.z).
const Version = "0.1.87"

// DistroName is the value of telemetry.distro.name.
const DistroName = "openlog"
