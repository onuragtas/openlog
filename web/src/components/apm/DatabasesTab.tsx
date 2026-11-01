import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { apmDatabasesQuery, type DbSort } from "@/api/apm";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { ServerViewLink } from "@/components/apm/ServerViewLink";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow, type SortDir } from "@/components/ui/table";
import { formatMs, formatRate, formatRpm, type ServiceScope } from "@/lib/apm";
import { useListState } from "@/lib/list";

export function DatabasesTab({ scope, range, sort, onSort }: { scope: ServiceScope; range: import("@/lib/time").RangeSpec; sort: DbSort; onSort: (s: DbSort) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  // The URL holds the column, so a shared link opens on it, and the rest of the sort and the page stay on this screen.
  const list = useListState<DbSort>(sort, sort === "statement" || sort === "database" ? "asc" : "desc", 50, "DatabasesTab");
  const q = useQuery(apmDatabasesQuery(scope, range, list.state));
  const { toFirstPage } = list;
  // Another range is another list, so page five of it would usually show nothing.
  useEffect(toFirstPage, [range.range, range.from, range.to, toFirstPage]);
  const head = {
    ...list.head,
    onSort: (column: DbSort, dir: SortDir) => {
      list.head.onSort(column, dir);
      onSort(column);
    },
  };
  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>
          <h2>{t("apm.service.tabs.databases")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="px-0">
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.data.queries.length === 0 && list.state.offset === 0 ? (
          <EmptyState>{t("apm.databases.empty")}</EmptyState>
        ) : (
          <>
            <Table data-testid="db-queries">
              <TableHeader>
                <TableRow>
                  <SortableHead column="statement" defaultDir="asc" {...head}>{t("apm.databases.statement")}</SortableHead>
                  <SortableHead column="database" defaultDir="asc" {...head}>{t("apm.databases.system")}</SortableHead>
                  <SortableHead column="calls" className="text-right" {...head}>{t("apm.metrics.throughput")}</SortableHead>
                  <SortableHead column="slowest" className="text-right" {...head}>{t("apm.metrics.avg")}</SortableHead>
                  <TableHead className="text-right">{t("apm.metrics.p95")}</TableHead>
                  <SortableHead column="error_rate" className="text-right" {...head}>{t("apm.metrics.errorRate")}</SortableHead>
                  <SortableHead column="time" className="text-right" {...head}>{t("apm.metrics.timeShare")}</SortableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {q.data.queries.map((d) => (
                  <TableRow key={`${d.db_system}|${d.db_name}|${d.statement}`}>
                    <TableCell className="max-w-[36rem]">
                      <code className="block truncate font-mono text-xs max-md:max-w-[50vw]" title={d.statement}>
                        {d.statement}
                      </code>
                      <ServerViewLink dbSystem={d.db_system} statement={d.statement} range={range} />
                    </TableCell>
                    <TableCell className="whitespace-nowrap">
                      <Badge variant="secondary">{d.db_name ? `${d.db_system}/${d.db_name}` : d.db_system}</Badge>
                      {d.db_operation && <span className="ml-1.5 text-xs text-muted-foreground">{d.db_operation}</span>}
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatRpm(d.throughput, locale)}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatMs(d.avg_ms, locale)}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatMs(d.p95_ms, locale)}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatRate(d.error_rate, locale)}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatRate(d.time_share, locale)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <ListPager list={list} count={q.data.queries.length} hasMore={q.data.has_more ?? false} />
          </>
        )}
      </CardContent>
    </Card>
  );
}
