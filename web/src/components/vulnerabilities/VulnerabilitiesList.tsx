// The fleet's open vulnerability findings, grouped by advisory (docs/contracts/api.md "Vulnerabilities",
// D-142). Router-free: the page passes the callbacks.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { VulnGroup, VulnSeverity } from "@/api/vulnerabilities";
import { vulnerabilitiesQuery, vulnerabilityCatalogQuery } from "@/api/vulnerabilities";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { advisoryLabel, formatScore, packagesText, SEVERITIES, severityBadgeVariant, totalCount } from "@/lib/vulnerabilities";

export interface VulnerabilitiesListProps {
  severity?: VulnSeverity;
  onSeverity?: (severity: VulnSeverity | undefined) => void;
  onOpen: (group: VulnGroup) => void;
}

export function VulnerabilitiesList({ severity, onSeverity, onOpen }: VulnerabilitiesListProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(vulnerabilitiesQuery(severity));
  // The catalog status is what tells "nothing is vulnerable" apart from "the feed never synced"; a server
  // that does not offer it (static auth mode) simply shows neither.
  const catalog = useQuery({ ...vulnerabilityCatalogQuery(), throwOnError: false });

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const groups = q.data.vulnerabilities;
  const counts = q.data.severity_counts as Record<string, number>;
  const synced = catalog.data?.last_synced_at ?? null;

  return (
    <div className="flex flex-col gap-3">
      {/* The severity counters double as the filter: the number a person looks at is the thing they click. */}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" variant={severity ? "outline" : "default"} size="sm" onClick={() => onSeverity?.(undefined)}>
          {t("vulnerabilities.filters.all", { count: totalCount(counts) })}
        </Button>
        {SEVERITIES.map((s) => (
          <Button key={s} type="button" variant={severity === s ? "default" : "outline"} size="sm" onClick={() => onSeverity?.(s)}>
            {t(`vulnerabilities.severity.${s}`)}
            <span className="ml-1 font-mono tabular-nums">{counts[s] ?? 0}</span>
          </Button>
        ))}
        <span className="ml-auto text-xs text-muted-foreground">
          {synced ? t("vulnerabilities.synced", { at: new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(Date.parse(synced)) }) : t("vulnerabilities.neverSynced")}
        </span>
      </div>

      <div className="rounded-xl border bg-card">
        {groups.length === 0 ? (
          <EmptyState>
            <p>{synced ? t("vulnerabilities.empty") : t("vulnerabilities.emptyNoFeed")}</p>
            <p className="mt-1 text-xs">{synced ? t("vulnerabilities.emptyHint") : t("vulnerabilities.emptyNoFeedHint")}</p>
          </EmptyState>
        ) : (
          <Table mobile="stack" data-testid="vulnerabilities-list">
            <TableHeader>
              <TableRow>
                <TableHead>{t("vulnerabilities.columns.advisory")}</TableHead>
                <TableHead>{t("vulnerabilities.columns.severity")}</TableHead>
                <TableHead className="text-right">{t("vulnerabilities.columns.score")}</TableHead>
                <TableHead className="text-right">{t("vulnerabilities.columns.hosts")}</TableHead>
                <TableHead>{t("vulnerabilities.columns.packages")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {groups.map((g) => (
                <TableRow key={g.vuln_id} className="cursor-pointer" data-testid="vulnerability-row" onClick={() => onOpen(g)}>
                  <TableCell className="max-w-96">
                    <button type="button" className="block truncate text-left font-medium hover:underline" onClick={() => onOpen(g)}>
                      {advisoryLabel(g)}
                    </button>
                    {g.summary && <p className="truncate text-xs text-muted-foreground">{g.summary}</p>}
                  </TableCell>
                  <TableCell className="max-md:w-auto">
                    <Badge variant={severityBadgeVariant(g.severity)}>{t(`vulnerabilities.severity.${g.severity}`)}</Badge>
                  </TableCell>
                  <TableCell label={t("vulnerabilities.columns.score")} className="text-right font-mono tabular-nums">
                    {formatScore(g.score)}
                  </TableCell>
                  <TableCell label={t("vulnerabilities.columns.hosts")} className="text-right font-mono tabular-nums">
                    {g.hosts}
                  </TableCell>
                  <TableCell label={t("vulnerabilities.columns.packages")} className="max-w-72 truncate font-mono text-xs">
                    {packagesText(g.packages)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  );
}
