package vuln

import (
	"testing"
	"time"
)

func rowsFor() []AffectedRow {
	return []AffectedRow{
		{VulnID: "DSA-1", CVE: "CVE-2026-1", Severity: SeverityCritical, Score: 9.8, Summary: "overflow",
			Affected: Affected{Ecosystem: "Debian:12", Package: "libexample", Introduced: "0", Fixed: "1.2.3-4+deb12u2"}},
		{VulnID: "DSA-2", CVE: "CVE-2026-2", Severity: SeverityMedium, Score: 5.3, Summary: "info leak",
			Affected: Affected{Ecosystem: "Debian:12", Package: "curl", Introduced: "7.88.0-1", Fixed: "7.88.1-10+deb12u5"}},
		{VulnID: "DSA-3", CVE: "CVE-2026-3", Severity: SeverityHigh, Score: 7.5, Summary: "no fix yet",
			Affected: Affected{Ecosystem: "Debian:12", Package: "openssl", Introduced: "3.0.0"}},
		// The same package on another release is patched at another version, which is why the ecosystem
		// carries the release.
		{VulnID: "DSA-4", CVE: "CVE-2026-1", Severity: SeverityCritical, Score: 9.8, Summary: "overflow",
			Affected: Affected{Ecosystem: "Debian:11", Package: "libexample", Introduced: "0", Fixed: "1.1.0-1+deb11u3"}},
	}
}

func TestMatch(t *testing.T) {
	idx := NewIndex(rowsFor())
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	host := Host{TenantID: "t1", HostID: "h1", HostName: "web-1", Ecosystem: "Debian:12", Packages: []InstalledPackage{
		{Manager: "dpkg", Name: "libexample", Version: "1.2.3-4+deb12u1"}, // vulnerable
		{Manager: "dpkg", Name: "curl", Version: "7.88.1-10+deb12u5"},     // patched
		{Manager: "dpkg", Name: "openssl", Version: "3.0.11-1"},           // vulnerable, no fix
		{Manager: "dpkg", Name: "bash", Version: "5.2-15"},                // not in the catalog
	}}
	got := idx.Match(host, now)
	if len(got) != 2 {
		t.Fatalf("matches = %d: %+v", len(got), got)
	}
	// Critical first: the order is what the list shows.
	if got[0].VulnID != "DSA-1" || got[0].Severity != SeverityCritical {
		t.Errorf("first match = %+v", got[0])
	}
	if got[0].FixedIn != "1.2.3-4+deb12u2" || got[0].Version != "1.2.3-4+deb12u1" {
		t.Errorf("the finding must carry both versions: %+v", got[0])
	}
	if got[1].VulnID != "DSA-3" || got[1].FixedIn != "" {
		t.Errorf("second match = %+v", got[1])
	}
	for _, m := range got {
		if m.TenantID != "t1" || m.HostID != "h1" || m.HostName != "web-1" {
			t.Errorf("a finding must name its host: %+v", m)
		}
	}
}

// A host of another release must not be matched against the release it is not running.
func TestMatchIsPerRelease(t *testing.T) {
	idx := NewIndex(rowsFor())
	now := time.Now()
	deb11 := Host{TenantID: "t", HostID: "h", Ecosystem: "Debian:11",
		Packages: []InstalledPackage{{Manager: "dpkg", Name: "libexample", Version: "1.2.3-4+deb12u1"}}}
	got := idx.Match(deb11, now)
	// 1.2.3 is past Debian 11's fixed 1.1.0-1+deb11u3, so nothing is found — and DSA-1 (Debian 12) is not
	// consulted at all.
	if len(got) != 0 {
		t.Fatalf("matches = %+v", got)
	}
}

func TestMatchDeduplicatesAnAdvisory(t *testing.T) {
	rows := []AffectedRow{
		{VulnID: "DSA-1", Severity: SeverityHigh, Affected: Affected{Ecosystem: "Debian:12", Package: "x", Introduced: "0", Fixed: "2.0"}},
		{VulnID: "DSA-1", Severity: SeverityHigh, Affected: Affected{Ecosystem: "Debian:12", Package: "x", Introduced: "1.0", Fixed: "1.9"}},
	}
	idx := NewIndex(rows)
	host := Host{TenantID: "t", HostID: "h", Ecosystem: "Debian:12",
		Packages: []InstalledPackage{{Manager: "dpkg", Name: "x", Version: "1.5"}}}
	// Both ranges match, but the person upgrades the package once.
	if got := idx.Match(host, time.Now()); len(got) != 1 {
		t.Fatalf("matches = %+v", got)
	}
}

func TestMatchIgnoresCaseOfThePackageName(t *testing.T) {
	idx := NewIndex([]AffectedRow{{VulnID: "V", Severity: SeverityLow,
		Affected: Affected{Ecosystem: "Debian:12", Package: "LibExample", Introduced: "0", Fixed: "2.0"}}})
	host := Host{TenantID: "t", HostID: "h", Ecosystem: "Debian:12",
		Packages: []InstalledPackage{{Manager: "dpkg", Name: "libexample", Version: "1.0"}}}
	if got := idx.Match(host, time.Now()); len(got) != 1 {
		t.Fatalf("a feed and an inventory disagreeing on case must still match: %+v", got)
	}
}

func TestMatchWithoutAnEcosystem(t *testing.T) {
	idx := NewIndex(rowsFor())
	host := Host{TenantID: "t", HostID: "h", Packages: []InstalledPackage{{Name: "libexample", Version: "1.0"}}}
	if got := idx.Match(host, time.Now()); got != nil {
		t.Fatalf("a host whose ecosystem is unknown must produce nothing, got %+v", got)
	}
}

func TestHostEcosystems(t *testing.T) {
	hosts := []Host{{Ecosystem: "Ubuntu:22.04"}, {Ecosystem: "Debian:12"}, {Ecosystem: "Ubuntu:22.04"}, {Ecosystem: ""}}
	got := HostEcosystems(hosts)
	if len(got) != 2 || got[0] != "Debian:12" || got[1] != "Ubuntu:22.04" {
		t.Fatalf("ecosystems = %v", got)
	}
}
