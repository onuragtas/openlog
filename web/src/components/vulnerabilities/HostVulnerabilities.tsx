// The open findings of one host, shown as a tab of the host screen (D-142).
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { hostVulnerabilitiesQuery } from "@/api/vulnerabilities";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { advisoryLabel, formatScore, severityBadgeVariant, upgradeText } from "@/lib/vulnerabilities";

export function HostVulnerabilities({ hostId, onOpen }: { hostId: string; onOpen?: (vulnId: string) => void }) {
  const { t } = useTranslation();
  const q = useQuery(hostVulnerabilitiesQuery(hostId));

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const findings = q.data.findings;
  if (findings.length === 0) {
    return (
      <EmptyState>
        <p>{t("vulnerabilities.host.empty")}</p>
        <p className="mt-1 text-xs">{t("vulnerabilities.host.emptyHint")}</p>
      </EmptyState>
    );
  }

  return (
    <div className="rounded-xl border bg-card">
      <Table mobile="stack" data-testid="host-vulnerabilities">
        <TableHeader>
          <TableRow>
            <TableHead>{t("vulnerabilities.columns.advisory")}</TableHead>
            <TableHead>{t("vulnerabilities.columns.severity")}</TableHead>
            <TableHead className="text-right">{t("vulnerabilities.columns.score")}</TableHead>
            <TableHead>{t("vulnerabilities.columns.package")}</TableHead>
            <TableHead>{t("vulnerabilities.columns.upgrade")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {findings.map((f) => (
            <TableRow key={`${f.vuln_id}/${f.package}`}>
              <TableCell className="max-w-80">
                <button type="button" className="block truncate text-left font-medium hover:underline" onClick={() => onOpen?.(f.vuln_id)}>
                  {advisoryLabel(f)}
                </button>
                {f.summary && <p className="truncate text-xs text-muted-foreground">{f.summary}</p>}
              </TableCell>
              <TableCell className="max-md:w-auto">
                <Badge variant={severityBadgeVariant(f.severity)}>{t(`vulnerabilities.severity.${f.severity}`)}</Badge>
              </TableCell>
              <TableCell label={t("vulnerabilities.columns.score")} className="text-right font-mono tabular-nums">
                {formatScore(f.score)}
              </TableCell>
              <TableCell label={t("vulnerabilities.columns.package")} className="font-mono text-xs">
                {f.package}
              </TableCell>
              <TableCell label={t("vulnerabilities.columns.upgrade")} className="font-mono text-xs">
                {upgradeText(f)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
