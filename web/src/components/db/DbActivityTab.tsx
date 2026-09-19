// The instance's load (db-monitoring.md §5 activity): average active sessions per wait type over time — the one
// chart that shows at a glance whether a database is busy on CPU, on locks or on I/O — the top wait events and the
// statements the sampled sessions were running.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { dbActivityQuery } from "@/api/db";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { WAIT_ORDER } from "@/lib/db";
import { formatNumber } from "@/lib/format";
import type { RangeSpec } from "@/lib/time";

export function DbActivityTab({ instance, range, onOpenQuery }: { instance: string; range: RangeSpec; onOpenQuery: (fingerprint: string) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(dbActivityQuery({ instance, range }));
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const d = q.data;
  const series = d.series.map((s) => ({ label: s.wait_type, points: s.points.map((p) => [p[0]!, p[1]!] as [number, number]) }));
  return (
    <div className="flex flex-col gap-4">
      <Card className="gap-2">
        <CardHeader>
          <CardTitle>
            <h3>{t("db.activity.title")}</h3>
          </CardTitle>
          <p className="text-sm text-muted-foreground">{t("db.activity.description")}</p>
        </CardHeader>
        <CardContent>
          {series.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("db.activity.noSamples")}</p>
          ) : (
            <TimeSeriesChart title={t("db.activity.title")} series={series} unit="number" stacked order={WAIT_ORDER} from={Date.parse(d.from)} to={Date.parse(d.to)} height={240} />
          )}
        </CardContent>
      </Card>
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Card className="min-w-0 gap-2">
          <CardHeader>
            <CardTitle>
              <h3>{t("db.activity.waits")}</h3>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <Table mobile="stack">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("db.columns.waitType")}</TableHead>
                  <TableHead>{t("db.columns.waitEvent")}</TableHead>
                  <TableHead className="text-right">{t("db.columns.share")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {d.waits.map((w) => (
                  <TableRow key={`${w.type}/${w.event}`}>
                    <TableCell className="font-medium">{w.type}</TableCell>
                    <TableCell label={t("db.columns.waitEvent")} className="font-mono text-xs">
                      {w.event || "–"}
                    </TableCell>
                    <TableCell label={t("db.columns.share")} className="text-right tabular-nums">
                      {formatNumber(w.share * 100, locale)} %
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
        <Card className="min-w-0 gap-2">
          <CardHeader>
            <CardTitle>
              <h3>{t("db.activity.topQueries")}</h3>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <Table mobile="stack">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("db.columns.statement")}</TableHead>
                  <TableHead className="text-right">{t("db.columns.aas")}</TableHead>
                  <TableHead>{t("db.columns.topWait")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {d.top_queries.map((tq) => (
                  <TableRow key={tq.fingerprint}>
                    <TableCell className="max-w-md">
                      <button type="button" className="line-clamp-2 text-left font-mono text-xs break-all text-primary hover:underline" onClick={() => onOpenQuery(tq.fingerprint)}>
                        {tq.text}
                      </button>
                    </TableCell>
                    <TableCell label={t("db.columns.aas")} className="text-right tabular-nums">
                      {formatNumber(tq.avg_active_sessions, locale)}
                    </TableCell>
                    <TableCell label={t("db.columns.topWait")} className="font-mono text-xs">
                      {tq.top_wait}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
