package vuln

import (
	"archive/zip"
	"bytes"
	"testing"
)

// A Debian advisory as the feed publishes it: the fixed version is the distribution's, which is the whole
// reason this feed is used rather than an upstream one.
const debianRecord = `{
  "id": "DSA-5600-1",
  "aliases": ["CVE-2024-0001"],
  "summary": "buffer overflow in libexample",
  "modified": "2026-01-02T00:00:00Z",
  "published": "2026-01-01T00:00:00Z",
  "severity": [{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}],
  "references": [{"type": "ADVISORY", "url": "https://security-tracker.debian.org/tracker/DSA-5600-1"}],
  "affected": [{
    "package": {"ecosystem": "Debian:12", "name": "libexample"},
    "ranges": [{"type": "ECOSYSTEM", "events": [{"introduced": "0"}, {"fixed": "1.2.3-4+deb12u2"}]}]
  }]
}`

func TestParseOSV(t *testing.T) {
	v, ok := ParseOSV([]byte(debianRecord))
	if !ok {
		t.Fatal("the record must parse")
	}
	if v.ID != "DSA-5600-1" || v.CVE() != "CVE-2024-0001" {
		t.Errorf("id = %q, cve = %q", v.ID, v.CVE())
	}
	// AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H is the canonical 9.8.
	if v.Score != 9.8 || v.Severity != SeverityCritical {
		t.Errorf("score = %v, severity = %q; want 9.8 critical", v.Score, v.Severity)
	}
	if len(v.Affected) != 1 || v.Affected[0].Fixed != "1.2.3-4+deb12u2" {
		t.Fatalf("affected = %+v", v.Affected)
	}
	scheme := SchemeForEcosystem(v.Affected[0].Ecosystem)
	if scheme != SchemeDpkg {
		t.Fatalf("scheme = %q", scheme)
	}
	// The point of the whole feature: the installed version decides, not the package name.
	if !v.Affected[0].Vulnerable(scheme, "1.2.3-4+deb12u1") {
		t.Error("the unpatched version must match")
	}
	if v.Affected[0].Vulnerable(scheme, "1.2.3-4+deb12u2") {
		t.Error("the fixed version must not match")
	}
	if v.Affected[0].Vulnerable(scheme, "1.2.4-1") {
		t.Error("a newer version must not match")
	}
}

func TestParseOSVWithoutAFix(t *testing.T) {
	const rec = `{"id":"CVE-2026-9","affected":[{"package":{"ecosystem":"Alpine:v3.19","name":"curl"},
	  "ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"8.5.0-r0"}]}]}]}`
	v, ok := ParseOSV([]byte(rec))
	if !ok || len(v.Affected) != 1 {
		t.Fatalf("v = %+v, ok = %v", v, ok)
	}
	a := v.Affected[0]
	scheme := SchemeForEcosystem(a.Ecosystem)
	// No fixed version: everything from the introduced version on is affected, and the one before is not.
	if !a.Vulnerable(scheme, "8.5.0-r1") || !a.Vulnerable(scheme, "8.6.0-r0") {
		t.Error("versions from the introduced one on must match")
	}
	if a.Vulnerable(scheme, "8.4.0-r0") {
		t.Error("an older version must not match")
	}
	if v.Severity != SeverityNone {
		t.Errorf("severity = %q; an advisory without a score must not be given one", v.Severity)
	}
}

func TestParseOSVVersionList(t *testing.T) {
	const rec = `{"id":"GHSA-x","affected":[{"package":{"ecosystem":"PyPI","name":"requests"},
	  "versions":["2.31.0","2.31.1"]}]}`
	v, ok := ParseOSV([]byte(rec))
	if !ok || len(v.Affected) != 2 {
		t.Fatalf("affected = %+v", v.Affected)
	}
	if !v.Affected[0].Vulnerable(SchemeSemVer, "2.31.0") || v.Affected[0].Vulnerable(SchemeSemVer, "2.32.0") {
		t.Error("a listed version must match exactly")
	}
}

func TestParseOSVSkipsRecordsWithoutPackages(t *testing.T) {
	if _, ok := ParseOSV([]byte(`{"id":"CVE-2026-1","summary":"no packages"}`)); ok {
		t.Error("an advisory naming no package cannot be matched and must be skipped")
	}
	if _, ok := ParseOSV([]byte("not json")); ok {
		t.Error("a malformed document must be skipped")
	}
}

func TestParseOSVZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"DSA-5600-1.json", "broken.json", "README.txt"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		body := debianRecord
		if name == "broken.json" {
			body = "{"
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	vulns, skipped, err := ParseOSVZip(buf.Bytes(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// One advisory, one skipped malformed document, and the non-JSON entry ignored: a single bad record
	// must not cost the rest of the export.
	if len(vulns) != 1 || skipped != 1 {
		t.Fatalf("vulns = %d, skipped = %d", len(vulns), skipped)
	}
}

func TestEcosystemFor(t *testing.T) {
	cases := []struct {
		manager, osID, osVersion, want string
	}{
		{"dpkg", "debian", "12.4", "Debian:12"},
		{"dpkg", "ubuntu", "22.04", "Ubuntu:22.04"},
		{"apk", "alpine", "3.19.1", "Alpine:v3.19"},
		{"rpm", "rocky", "9.3", "Rocky Linux:9"},
		{"rpm", "rhel", "9.3", "Red Hat:9"},
		{"dpkg", "", "", "Debian"},
		{"rpm", "", "", "Red Hat"},
		{"npm", "", "", ""},
	}
	for _, tc := range cases {
		if got := EcosystemFor(tc.manager, tc.osID, tc.osVersion); got != tc.want {
			t.Errorf("EcosystemFor(%q, %q, %q) = %q, want %q", tc.manager, tc.osID, tc.osVersion, got, tc.want)
		}
	}
}

func TestSeverityFor(t *testing.T) {
	cases := map[float64]string{9.8: SeverityCritical, 9.0: SeverityCritical, 7.5: SeverityHigh,
		5.3: SeverityMedium, 3.1: SeverityLow, 0: SeverityNone}
	for score, want := range cases {
		if got := SeverityFor(score); got != want {
			t.Errorf("SeverityFor(%v) = %q, want %q", score, got, want)
		}
	}
}
