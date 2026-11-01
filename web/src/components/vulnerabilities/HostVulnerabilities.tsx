// The open findings of one host, shown as a tab of the host screen (D-142).
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { hostVulnerabilitiesQuery, type VulnFindingSort } from "@/api/vulnerabilities";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { SortableHead, Table, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { useListState } from "@/lib/list";
import { advisoryLabel, formatScore, severityBadgeVariant, upgradeText } from "@/lib/vulnerabilities";

export function HostVulnerabilities({ hostId, onOpen }: { hostId: string; onOpen?: (vulnId: string) => void }) {
  const { t } = useTranslation();
  const list = useListState<VulnFindingSort>("severity", "asc");
  const q = useQuery(hostVulnerabilitiesQuery(hostId, list.state));

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const findings = q.data.findings;
  if (findings.length === 0 && list.state.offset === 0) {
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
            <SortableHead column="advisory" defaultDir="asc" {...list.head}>{t("vulnerabilities.columns.advisory")}</SortableHead>
            <SortableHead column="severity" defaultDir="asc" {...list.head}>{t("vulnerabilities.columns.severity")}</SortableHead>
            <SortableHead column="score" className="text-right" {...list.head}>{t("vulnerabilities.columns.score")}</SortableHead>
            <SortableHead column="package" defaultDir="asc" {...list.head}>{t("vulnerabilities.columns.package")}</SortableHead>
            <SortableHead column="version" defaultDir="asc" {...list.head}>{t("vulnerabilities.columns.upgrade")}</SortableHead>
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
      <ListPager list={list} count={findings.length} hasMore={q.data.has_more ?? false} />
    </div>
  );
}
