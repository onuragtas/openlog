// MSW handlers for the vulnerability endpoints (internal/api/vulnerabilities.go, docs/contracts/api.md
// "Vulnerabilities"): three advisories over the mock fleet, one of them without a fix, plus the catalog
// status that tells an empty list apart from a feed that never synced.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { VulnFinding, VulnGroup } from "@/api/vulnerabilities";
import { authenticate } from "./account";
import { formatTs, HOST_IDS } from "./fixtures";

const API = "*/api/v1";

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

const now = Date.now();

const FINDINGS: VulnFinding[] = [
  {
    host_id: HOST_IDS.web,
    host_name: "web-1.shop.internal",
    vuln_id: "DSA-5600-1",
    cve: "CVE-2026-0001",
    severity: "critical",
    score: 9.8,
    ecosystem: "Debian:12",
    package: "libexample",
    version: "1.2.3-4+deb12u1",
    fixed_in: "1.2.3-4+deb12u2",
    summary: "buffer overflow in libexample",
    first_seen: formatTs(now - 6 * 86_400_000),
    last_seen: formatTs(now - 3_600_000),
  },
  {
    host_id: HOST_IDS.db,
    host_name: "db-1.shop.internal",
    vuln_id: "DSA-5600-1",
    cve: "CVE-2026-0001",
    severity: "critical",
    score: 9.8,
    ecosystem: "Debian:12",
    package: "libexample",
    version: "1.2.3-4+deb12u1",
    fixed_in: "1.2.3-4+deb12u2",
    summary: "buffer overflow in libexample",
    first_seen: formatTs(now - 6 * 86_400_000),
    last_seen: formatTs(now - 3_600_000),
  },
  {
    host_id: HOST_IDS.web,
    host_name: "web-1.shop.internal",
    vuln_id: "CVE-2026-0042",
    cve: "CVE-2026-0042",
    severity: "high",
    score: 7.5,
    ecosystem: "Debian:12",
    package: "openssl",
    version: "3.0.11-1~deb12u2",
    // No fixed version yet: the case a person cannot resolve by upgrading.
    fixed_in: "",
    summary: "denial of service in the TLS handshake",
    first_seen: formatTs(now - 2 * 86_400_000),
    last_seen: formatTs(now - 3_600_000),
  },
  {
    host_id: HOST_IDS.web,
    host_name: "web-1.shop.internal",
    vuln_id: "CVE-2026-0100",
    cve: "CVE-2026-0100",
    severity: "medium",
    score: 5.3,
    ecosystem: "Debian:12",
    package: "curl",
    version: "7.88.1-10+deb12u4",
    fixed_in: "7.88.1-10+deb12u5",
    summary: "information disclosure in cookie handling",
    first_seen: formatTs(now - 86_400_000),
    last_seen: formatTs(now - 3_600_000),
  },
];

function groups(severity?: string): VulnGroup[] {
  const byVuln = new Map<string, VulnGroup>();
  for (const f of FINDINGS) {
    if (severity && f.severity !== severity) continue;
    const g = byVuln.get(f.vuln_id);
    if (g) {
      g.hosts += 1;
      if (!g.packages.includes(f.package)) g.packages.push(f.package);
      continue;
    }
    byVuln.set(f.vuln_id, {
      vuln_id: f.vuln_id,
      cve: f.cve,
      severity: f.severity,
      score: f.score,
      summary: f.summary,
      hosts: 1,
      packages: [f.package],
      first_seen: f.first_seen,
      last_seen: f.last_seen,
    });
  }
  return [...byVuln.values()];
}

export const vulnerabilityHandlers = [
  http.get(`${API}/vulnerabilities`, authed(({ request }) => {
    const url = new URL(request.url);
    const severity = url.searchParams.get("severity") ?? undefined;
    const list = groups(severity);
    const counts: Record<string, number> = { critical: 0, high: 0, medium: 0, low: 0, none: 0 };
    for (const g of groups()) counts[g.severity] = (counts[g.severity] ?? 0) + 1;
    return HttpResponse.json({ vulnerabilities: list, severity_counts: counts });
  })),
  http.get(`${API}/vulnerabilities/catalog/status`, authed(() =>
    HttpResponse.json({
      vulnerabilities: 61_234,
      affected_ranges: 98_211,
      ecosystems: 2,
      last_synced_at: formatTs(now - 4 * 3_600_000),
      sources: [
        { source: "osv", ecosystem: "Debian:12", synced_at: formatTs(now - 4 * 3_600_000), count: 41_022, error: "" },
        { source: "osv", ecosystem: "Ubuntu:22.04", synced_at: formatTs(now - 4 * 3_600_000), count: 20_212, error: "" },
      ],
    }),
  )),
  http.get(`${API}/vulnerabilities/:id`, authed(({ params }) => {
    const findings = FINDINGS.filter((f) => f.vuln_id === params.id);
    if (findings.length === 0) {
      return HttpResponse.json({ error: { code: "not_found", message: "no open finding for this advisory" } }, { status: 404 });
    }
    return HttpResponse.json({ vuln_id: params.id, findings });
  })),
  http.get(`${API}/hosts/:hostId/vulnerabilities`, authed(({ params }) =>
    HttpResponse.json({ host_id: params.hostId, findings: FINDINGS.filter((f) => f.host_id === params.hostId) }),
  )),
];
