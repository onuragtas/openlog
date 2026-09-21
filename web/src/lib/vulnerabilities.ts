// Pure helpers for the Vulnerabilities screens (docs/contracts/api.md "Vulnerabilities", D-142).
import type { VulnFinding, VulnGroup, VulnSeverity } from "@/api/vulnerabilities";

/** Severities in the order the screens show them: most serious first. */
export const SEVERITIES: VulnSeverity[] = ["critical", "high", "medium", "low", "none"];

export function severityBadgeVariant(severity: string): "destructive" | "warning" | "muted" | "outline" {
  switch (severity) {
    case "critical":
    case "high":
      return "destructive";
    case "medium":
      return "warning";
    case "low":
      return "muted";
    default:
      return "outline";
  }
}

export function severityRank(severity: string): number {
  const i = SEVERITIES.indexOf(severity as VulnSeverity);
  return i < 0 ? SEVERITIES.length : i;
}

/** The CVSS score as it is usually written ("9.8"); "–" when the feed carries none. */
export function formatScore(score: number | null | undefined): string {
  if (score === null || score === undefined || !Number.isFinite(score) || score <= 0) return "–";
  return score.toFixed(1);
}

/** The advisory's own id and the CVE it is known by, without repeating one that is both. */
export function advisoryLabel(v: Pick<VulnGroup, "vuln_id" | "cve">): string {
  return !v.cve || v.cve === v.vuln_id ? v.vuln_id : `${v.vuln_id} · ${v.cve}`;
}

/** "1.2.3-4 → 1.2.3-5", or the installed version alone when the feed knows no fix. */
export function upgradeText(f: Pick<VulnFinding, "version" | "fixed_in">): string {
  return f.fixed_in ? `${f.version} → ${f.fixed_in}` : f.version;
}

/** The packages of a group, joined for the list cell; long lists are cut with a count. */
export function packagesText(packages: readonly string[], max = 3): string {
  if (packages.length <= max) return packages.join(", ");
  return `${packages.slice(0, max).join(", ")} +${packages.length - max}`;
}

/** Whether a finding still has no fixed version, which is the one a person cannot act on by upgrading. */
export function withoutFix(findings: readonly VulnFinding[]): VulnFinding[] {
  return findings.filter((f) => !f.fixed_in);
}

/** The total of the severity counters, for the "N findings" line. */
export function totalCount(counts: Record<string, number> | undefined): number {
  return Object.values(counts ?? {}).reduce((sum, n) => sum + n, 0);
}
