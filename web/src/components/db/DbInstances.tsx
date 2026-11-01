import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { dbInstancesPageQuery, type DbInstance, type DbInstanceSort } from "@/api/db";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { SortableHead, Table, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { dbSystemName, instanceLabel } from "@/lib/db";
import { formatNumber, formatRelative, formatValue } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { useListState } from "@/lib/list";
import type { RangeSpec } from "@/lib/time";

/** Database instances with query monitoring data in the range, heaviest first (db-monitoring.md §5). */
export function DbInstances({ range, onOpen }: { range: RangeSpec; onOpen: (i: DbInstance) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = useNow();
  // Starts on total time, which has no column of its own: it is what "heaviest" has always meant here.
  const list = useListState<DbInstanceSort>("time", "desc", 50, "DbInstances");
  const q = useQuery(dbInstancesPageQuery(range, list.state));
  const { toFirstPage } = list;
  useEffect(toFirstPage, [range.range, range.from, range.to, toFirstPage]);
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (q.data.instances.length === 0 && list.state.offset === 0) {
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
            <SortableHead column="instance" defaultDir="asc" {...list.head}>{t("db.columns.instance")}</SortableHead>
            <SortableHead column="host" defaultDir="asc" {...list.head}>{t("db.columns.host")}</SortableHead>
            <SortableHead column="calls" className="text-right" {...list.head}>{t("db.columns.throughput")}</SortableHead>
            <SortableHead column="avg" className="text-right" {...list.head}>{t("db.columns.avg")}</SortableHead>
            <SortableHead column="aas" className="text-right" {...list.head}>{t("db.columns.aas")}</SortableHead>
            <SortableHead column="wait" defaultDir="asc" {...list.head}>{t("db.columns.topWait")}</SortableHead>
            <SortableHead column="last_seen" {...list.head}>{t("db.columns.lastSeen")}</SortableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {q.data.instances.map((d) => (
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
      <ListPager list={list} count={q.data.instances.length} hasMore={q.data.has_more ?? false} />
    </div>
  );
}
