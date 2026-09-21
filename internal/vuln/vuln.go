// Package vuln matches the packages the infra agent already reports against a vulnerability feed (D-142).
//
// The inventory has known every installed package and its version since M1; what was missing was the other
// half of the sentence — which of those versions are known to be vulnerable. That half is a feed, and the
// feed is the only new thing here: the catalog is stored per installation (not per tenant, it is public
// data), the matching is a version comparison per ecosystem (version.go), and the result is written where
// every other per-host fact is written.
//
// The feed is OSV (osv.dev), because it is the one format the distributions publish into: Debian, Ubuntu,
// Alpine, Rocky and SUSE all have OSV exports, and a CVE in them carries the *distribution's* fixed version
// rather than the upstream one — which is what makes "1.2.3-4ubuntu0.3 is patched" answerable at all.
//
// Layout: vuln.go (model), version.go (per-ecosystem comparison), osv.go (the feed format), feed.go (the
// sync), match.go (inventory × catalog), pgstore.go (the catalog), matcher.go (the leader task).
package vuln

import (
	"context"
	"strings"
	"time"
)

// Severity buckets, from the CVSS base score. The bucket is what a person filters and alerts on; the score
// is kept next to it for the ones who want the number.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
	SeverityNone     = "none"
)

// Severities in descending order of seriousness.
var Severities = []string{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityNone}

// SeverityFor buckets a CVSS base score the way the CVSS specification does.
func SeverityFor(score float64) string {
	switch {
	case score >= 9.0:
		return SeverityCritical
	case score >= 7.0:
		return SeverityHigh
	case score >= 4.0:
		return SeverityMedium
	case score > 0:
		return SeverityLow
	}
	return SeverityNone
}

// SeverityRank orders the buckets for sorting (critical first).
func SeverityRank(s string) int {
	for i, v := range Severities {
		if v == s {
			return i
		}
	}
	return len(Severities)
}

// Ecosystems openlog syncs, keyed by the OSV ecosystem name. The value is the comparison scheme its
// versions use, which is a property of the distribution's package manager rather than of OSV.
var Ecosystems = map[string]string{
	"Debian":         SchemeDpkg,
	"Ubuntu":         SchemeDpkg,
	"Alpine":         SchemeAPK,
	"Rocky Linux":    SchemeRPM,
	"AlmaLinux":      SchemeRPM,
	"Red Hat":        SchemeRPM,
	"SUSE":           SchemeRPM,
	"openSUSE":       SchemeRPM,
	"Mageia":         SchemeRPM,
	"Chainguard":     SchemeAPK,
	"Wolfi":          SchemeAPK,
	"Alpaquita":      SchemeAPK,
	"MinimOS":        SchemeAPK,
	"Photon OS":      SchemeRPM,
	"Bitnami":        SchemeSemVer,
	"CRAN":           SchemeSemVer,
	"npm":            SchemeSemVer,
	"PyPI":           SchemeSemVer,
	"Go":             SchemeSemVer,
	"Maven":          SchemeSemVer,
	"RubyGems":       SchemeSemVer,
	"crates.io":      SchemeSemVer,
	"Packagist":      SchemeSemVer,
	"NuGet":          SchemeSemVer,
	"Hex":            SchemeSemVer,
	"Pub":            SchemeSemVer,
	"Hackage":        SchemeSemVer,
	"SwiftURL":       SchemeSemVer,
	"Android":        SchemeSemVer,
	"GitHub Actions": SchemeSemVer,
}

// SchemeForEcosystem returns the comparison scheme of an OSV ecosystem. An ecosystem with a release suffix
// ("Debian:12", "Ubuntu:22.04:LTS") is the same scheme as its base.
func SchemeForEcosystem(ecosystem string) string {
	base := ecosystem
	if at := strings.Index(base, ":"); at >= 0 {
		base = base[:at]
	}
	if scheme, ok := Ecosystems[base]; ok {
		return scheme
	}
	return SchemeSemVer
}

// EcosystemFor maps what the inventory reports about a host — its package manager and the operating system
// release the OS item names — to the OSV ecosystem its packages are described in.
//
// The release matters: a Debian 12 package and a Debian 11 package of the same name are patched at
// different versions, and OSV says so by publishing them as different ecosystems.
func EcosystemFor(manager, osID, osVersionID string) string {
	id := strings.ToLower(strings.TrimSpace(osID))
	version := strings.TrimSpace(osVersionID)
	switch id {
	case "debian":
		return withRelease("Debian", majorOf(version))
	case "ubuntu":
		return withRelease("Ubuntu", version)
	case "alpine":
		return withRelease("Alpine", "v"+minorOf(version))
	case "rocky":
		return withRelease("Rocky Linux", majorOf(version))
	case "almalinux", "alma":
		return withRelease("AlmaLinux", majorOf(version))
	case "rhel", "redhat", "red hat", "centos":
		return withRelease("Red Hat", majorOf(version))
	case "opensuse", "opensuse-leap", "opensuse-tumbleweed":
		return withRelease("openSUSE", version)
	case "sles", "suse":
		return withRelease("SUSE", version)
	case "photon":
		return withRelease("Photon OS", majorOf(version))
	}
	// Without a recognized operating system the package manager still narrows it down, which is better than
	// matching nothing: a dpkg host with an unknown id is described by Debian's feed.
	switch SchemeFor(manager) {
	case SchemeDpkg:
		return "Debian"
	case SchemeRPM:
		return "Red Hat"
	case SchemeAPK:
		return "Alpine"
	}
	return ""
}

func withRelease(name, release string) string {
	if release == "" {
		return name
	}
	return name + ":" + release
}

func majorOf(version string) string {
	if at := strings.Index(version, "."); at >= 0 {
		return version[:at]
	}
	return version
}

// minorOf keeps major.minor ("3.19.1" → "3.19"), which is how Alpine names its branches.
func minorOf(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
}

// Vulnerability is one advisory of the catalog.
type Vulnerability struct {
	// ID is the OSV id (CVE-2024-1234, DSA-5600-1, GHSA-…).
	ID string
	// Aliases are the other ids the same advisory is known by; the CVE id is usually among them.
	Aliases []string
	Summary string
	Details string
	// Severity is the bucket of Score; Score is the CVSS v3/v4 base score (0 when the feed carries none).
	Severity string
	Score    float64
	// Vector is the CVSS vector string, kept so the score can be explained rather than just asserted.
	Vector    string
	Published time.Time
	Modified  time.Time
	// References are the advisory's own links (the distribution's advisory, the upstream fix).
	References []string
	// Affected is what this advisory says about packages.
	Affected []Affected
	// Withdrawn marks an advisory the feed retracted; it is kept but never matched.
	Withdrawn bool
}

// Affected is one package range of an advisory: a package in an ecosystem is vulnerable from Introduced
// (inclusive) until Fixed (exclusive).
type Affected struct {
	Ecosystem string
	Package   string
	// Introduced is "0" when the package was vulnerable from its first version.
	Introduced string
	// Fixed is empty when the feed knows no fixed version yet — the package is vulnerable from Introduced on.
	Fixed string
	// LastAffected is the alternative the feed uses when it names the last vulnerable version instead of the
	// first fixed one; the range is then inclusive of it.
	LastAffected string
}

// CVE returns the CVE id of the advisory: its own id when it is one, else the first alias that is.
func (v Vulnerability) CVE() string {
	if strings.HasPrefix(v.ID, "CVE-") {
		return v.ID
	}
	for _, a := range v.Aliases {
		if strings.HasPrefix(a, "CVE-") {
			return a
		}
	}
	return ""
}

// Vulnerable reports whether version of a package in this range is affected.
func (a Affected) Vulnerable(scheme, version string) bool {
	if version == "" {
		return false
	}
	if a.Introduced != "" && a.Introduced != "0" && Compare(scheme, version, a.Introduced) < 0 {
		return false
	}
	switch {
	case a.Fixed != "":
		return Compare(scheme, version, a.Fixed) < 0
	case a.LastAffected != "":
		return Compare(scheme, version, a.LastAffected) <= 0
	}
	// No fixed version: everything from Introduced on is affected, which is what an advisory without a fix
	// means and is exactly the case a person most needs to see.
	return true
}

// Match is one vulnerable package on one host.
type Match struct {
	TenantID  string
	HostID    string
	HostName  string
	VulnID    string
	CVE       string
	Severity  string
	Score     float64
	Ecosystem string
	Package   string
	// Version is what the host has installed.
	Version string
	// FixedIn is the version that resolves it ("" when the feed knows none).
	FixedIn string
	Summary string
	// DetectedAt is the moment the matcher concluded it.
	DetectedAt time.Time
}

// Catalog stores the advisories of the installation (PostgreSQL: PGStore). It is not tenant-scoped: the
// feed is public data and every organization is matched against the same catalog.
type Catalog interface {
	// Upsert stores a batch of advisories, replacing what was stored under the same ids.
	Upsert(ctx context.Context, source string, vulns []Vulnerability) error
	// Affected returns every affected range of the ecosystems, for the matcher to hold in memory.
	Affected(ctx context.Context, ecosystems []string) ([]AffectedRow, error)
	// Stats reports what the catalog holds, for the sync status the UI shows.
	Stats(ctx context.Context) (CatalogStats, error)
	// MarkSync records the outcome of a feed sync.
	MarkSync(ctx context.Context, source, ecosystem string, at time.Time, count int, err error) error
	// SyncStatus returns the last sync per ecosystem.
	SyncStatus(ctx context.Context) ([]SyncState, error)
}

// AffectedRow is one affected range joined with the advisory's own fields, which is what matching needs.
type AffectedRow struct {
	Affected
	VulnID   string
	CVE      string
	Severity string
	Score    float64
	Summary  string
}

// CatalogStats is what the catalog holds.
type CatalogStats struct {
	Vulnerabilities int
	AffectedRanges  int
	Ecosystems      int
	LastSyncedAt    *time.Time
}

// SyncState is the last sync of one ecosystem.
type SyncState struct {
	Source    string
	Ecosystem string
	SyncedAt  time.Time
	Count     int
	Error     string
}
