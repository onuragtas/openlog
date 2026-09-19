import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { dbInstancesQuery, type DbInstance } from "@/api/db";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { dbSystemName, instanceLabel } from "@/lib/db";
import { formatNumber, formatRelative, formatValue } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import type { RangeSpec } from "@/lib/time";

/** Database instances with query monitoring data in the range, heaviest first (db-monitoring.md §5). */
export function DbInstances({ range, onOpen }: { range: RangeSpec; onOpen: (i: DbInstance) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = useNow();
  const q = useQuery(dbInstancesQuery(range));
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (q.data.length === 0) {
    return (
      <EmptyState>
        <span className="block">{t("db.empty")}</span>
        <code className="mt-2 block text-xs">integrations.postgresql.query_stats.enabled: true</code>
      </EmptyState>
    );
  }
  return (
    <div className="rounded-xl border bg-card" data-testid="db-instances">
      <Table mobile="stack">
        <TableHeader>
          <TableRow>
            <TableHead>{t("db.columns.instance")}</TableHead>
            <TableHead>{t("db.columns.host")}</TableHead>
            <TableHead className="text-right">{t("db.columns.throughput")}</TableHead>
            <TableHead className="text-right">{t("db.columns.avg")}</TableHead>
            <TableHead className="text-right">{t("db.columns.aas")}</TableHead>
            <TableHead>{t("db.columns.topWait")}</TableHead>
            <TableHead>{t("db.columns.lastSeen")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {q.data.map((d) => (
            <TableRow key={d.instance} className="cursor-pointer" onClick={() => onOpen(d)} data-testid="db-instance">
              <TableCell>
                <button type="button" className="text-left font-medium text-primary hover:underline" title={d.instance} onClick={() => onOpen(d)}>
                  {instanceLabel(d)}
                </button>
                <Badge variant="muted" className="ml-2">
                  {dbSystemName(d.db_system)}
                </Badge>
              </TableCell>
              <TableCell label={t("db.columns.host")}>{d.host_name || "–"}</TableCell>
              <TableCell label={t("db.columns.throughput")} className="text-right tabular-nums">
                {t("db.perSecond", { value: formatNumber(d.throughput, locale) })}
              </TableCell>
              <TableCell label={t("db.columns.avg")} className="text-right tabular-nums">
                {d.avg_ms === null ? "–" : formatValue(d.avg_ms, "ms", locale)}
              </TableCell>
              <TableCell label={t("db.columns.aas")} className="text-right tabular-nums">
                {d.avg_active_sessions === null ? "–" : formatNumber(d.avg_active_sessions, locale)}
              </TableCell>
              <TableCell label={t("db.columns.topWait")}>{d.top_wait || "–"}</TableCell>
              <TableCell label={t("db.columns.lastSeen")} className="tabular-nums">
                {formatRelative(Date.parse(d.last_seen), now, locale)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
