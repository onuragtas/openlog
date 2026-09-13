import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { apmOverviewQuery, apmTransactionsQuery } from "@/api/apm";
import { RedTiles } from "@/components/apm/Charts";
import { TransactionTable } from "@/components/apm/TransactionsTab";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { latencySeries, metricPoints, type ServiceScope } from "@/lib/apm";
import type { RangeSpec } from "@/lib/time";

export function OverviewTab({ scope, range, onOpenTransaction, onViewAll }: { scope: ServiceScope; range: RangeSpec; onOpenTransaction: (name: string) => void; onViewAll: () => void }) {
  const { t } = useTranslation();
  const overview = useQuery(apmOverviewQuery(scope, range));
  const top = useQuery(apmTransactionsQuery(scope, range, "time", 5));
  const points = useMemo(() => overview.data?.series ?? [], [overview.data]);
  const common = { from: overview.data?.from, to: overview.data?.to, isLoading: overview.isPending, error: overview.error, onRetry: () => void overview.refetch(), height: 180 };

  return (
    <div className="flex flex-col gap-4">
      {overview.data && <RedTiles red={overview.data.totals} apdexTMs={overview.data.apdex_t_ms} />}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <ChartCard title={t("apm.metrics.throughput")}>
          <TimeSeriesChart {...common} title={t("apm.metrics.throughput")} unit="number" series={[{ label: t("apm.metrics.throughput"), points: metricPoints(points, "throughput") }]} />
        </ChartCard>
        <ChartCard title={t("apm.metrics.latency")}>
          <TimeSeriesChart {...common} title={t("apm.metrics.latency")} unit="ms" series={latencySeries(points)} />
        </ChartCard>
        <ChartCard title={t("apm.metrics.errorRate")}>
          <TimeSeriesChart {...common} title={t("apm.metrics.errorRate")} unit="percent" yCap={1} series={[{ label: t("apm.metrics.errorRate"), points: metricPoints(points, "error_rate") }]} />
        </ChartCard>
        <ChartCard title={t("apm.metrics.apdex")}>
          <TimeSeriesChart {...common} title={t("apm.metrics.apdex")} unit="number" yMax={1} series={[{ label: t("apm.metrics.apdex"), points: metricPoints(points, "apdex") }]} />
        </ChartCard>
      </div>
      <Card>
        <CardHeader className="flex flex-row items-center justify-between gap-2">
          <CardTitle>
            <h2>{t("apm.overview.topTransactions")}</h2>
          </CardTitle>
          <Button variant="outline" size="sm" onClick={onViewAll}>
            {t("apm.overview.viewAll")}
          </Button>
        </CardHeader>
        <CardContent className="px-0">
          {top.isPending ? (
            <LoadingState />
          ) : top.isError ? (
            <ErrorState error={top.error} onRetry={() => void top.refetch()} />
          ) : top.data.transactions.length === 0 ? (
            <p className="px-6 py-4 text-sm text-muted-foreground">{t("apm.overview.noData")}</p>
          ) : (
            <TransactionTable transactions={top.data.transactions} apdexTMs={top.data.apdex_t_ms} onOpen={onOpenTransaction} />
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export function ChartCard({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle>
          <h2>{title}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}
