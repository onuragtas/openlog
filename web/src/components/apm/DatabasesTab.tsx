import { useQuery } from "@tanstack/react-query";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { apmDatabasesQuery, type DbSort } from "@/api/apm";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatMs, formatRate, formatRpm, type ServiceScope } from "@/lib/apm";

const SORTS: DbSort[] = ["time", "calls", "slowest", "errors"];

export function DatabasesTab({ scope, range, sort, onSort }: { scope: ServiceScope; range: import("@/lib/time").RangeSpec; sort: DbSort; onSort: (s: DbSort) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const id = useId();
  const q = useQuery(apmDatabasesQuery(scope, range, sort));
  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>
          <h2>{t("apm.service.tabs.databases")}</h2>
        </CardTitle>
        <div className="flex items-center gap-2">
          <Label htmlFor={id}>{t("apm.databases.sort")}</Label>
          <NativeSelect id={id} value={sort} onChange={(e) => onSort(e.target.value as DbSort)}>
            {SORTS.map((s) => (
              <option key={s} value={s}>
                {t(`apm.databases.sorts.${s}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
      </CardHeader>
      <CardContent className="px-0">
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.data.length === 0 ? (
          <EmptyState>{t("apm.databases.empty")}</EmptyState>
        ) : (
          <Table data-testid="db-queries">
            <TableHeader>
              <TableRow>
                <TableHead>{t("apm.databases.statement")}</TableHead>
                <TableHead>{t("apm.databases.system")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.throughput")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.avg")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.p95")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.errorRate")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.timeShare")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {q.data.map((d) => (
                <TableRow key={`${d.db_system}|${d.db_name}|${d.statement}`}>
                  <TableCell className="max-w-[36rem]">
                    <code className="block truncate font-mono text-xs" title={d.statement}>
                      {d.statement}
                    </code>
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
        )}
      </CardContent>
    </Card>
  );
}
