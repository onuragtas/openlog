package vuln

import (
	"sort"
	"strings"
	"time"
)

// Matching: the installed packages of one host against the catalog's affected ranges.
//
// The index is keyed by (ecosystem, package name) because that is the only key both sides share; the
// version is then compared in the ecosystem's own scheme, which is what separates a real finding from a
// name collision. A package that is not in the catalog produces nothing, and so does a package whose
// installed version is at or past the fixed one.

// Host is what the matcher knows about one host: its identity, which ecosystem its packages belong to, and
// the packages themselves.
type Host struct {
	TenantID  string
	HostID    string
	HostName  string
	Ecosystem string
	Packages  []InstalledPackage
}

// InstalledPackage is one row of the host's inventory.
type InstalledPackage struct {
	Manager string
	Name    string
	Version string
}

// Index is the catalog arranged for matching: the affected ranges of one ecosystem by package name.
type Index struct {
	byKey map[string][]AffectedRow
}

// NewIndex builds the index from the catalog's rows.
func NewIndex(rows []AffectedRow) *Index {
	idx := &Index{byKey: make(map[string][]AffectedRow, len(rows)/2+1)}
	for _, r := range rows {
		key := indexKey(r.Ecosystem, r.Package)
		idx.byKey[key] = append(idx.byKey[key], r)
	}
	return idx
}

// Size is how many ranges the index holds.
func (i *Index) Size() int {
	n := 0
	for _, rows := range i.byKey {
		n += len(rows)
	}
	return n
}

// indexKey lower-cases the package name: Debian and Alpine are case-sensitive in practice, but a feed and
// an inventory disagreeing on case would silently produce no findings at all, which is the worse failure.
func indexKey(ecosystem, pkg string) string {
	return ecosystem + "\x00" + strings.ToLower(pkg)
}

// Match returns the findings of one host. The same advisory can affect several packages on the host, and
// each of those is its own finding, because each is fixed by its own upgrade.
func (i *Index) Match(h Host, now time.Time) []Match {
	if h.Ecosystem == "" || len(h.Packages) == 0 {
		return nil
	}
	scheme := SchemeForEcosystem(h.Ecosystem)
	var out []Match
	for _, p := range h.Packages {
		if p.Name == "" || p.Version == "" {
			continue
		}
		rows := i.byKey[indexKey(h.Ecosystem, p.Name)]
		if len(rows) == 0 {
			continue
		}
		// One finding per advisory, even when the advisory names several overlapping ranges for the same
		// package: the person upgrades the package once.
		seen := map[string]bool{}
		for _, r := range rows {
			if seen[r.VulnID] || !r.Affected.Vulnerable(scheme, p.Version) {
				continue
			}
			seen[r.VulnID] = true
			out = append(out, Match{
				TenantID: h.TenantID, HostID: h.HostID, HostName: h.HostName,
				VulnID: r.VulnID, CVE: r.CVE, Severity: r.Severity, Score: r.Score,
				Ecosystem: h.Ecosystem, Package: p.Name, Version: p.Version, FixedIn: r.Fixed,
				Summary: r.Summary, DetectedAt: now,
			})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if ra, rb := SeverityRank(out[a].Severity), SeverityRank(out[b].Severity); ra != rb {
			return ra < rb
		}
		if out[a].Package != out[b].Package {
			return out[a].Package < out[b].Package
		}
		return out[a].VulnID < out[b].VulnID
	})
	return out
}

// HostEcosystems returns the distinct ecosystems of the hosts, which is what the catalog has to be loaded
// for: an installation of Debian hosts never pays for Alpine's advisories.
func HostEcosystems(hosts []Host) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, h := range hosts {
		if h.Ecosystem != "" && !seen[h.Ecosystem] {
			seen[h.Ecosystem] = true
			out = append(out, h.Ecosystem)
		}
	}
	sort.Strings(out)
	return out
}
