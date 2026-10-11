// The application's Core Web Vitals, its page view series and the totals of the range (rum.md §7).
// The p75 leads every vital card because that is the percentile the Core Web Vitals assessment is defined
// on, and the rating beside it comes from the server: the thresholds are constants of the programme, not
// settings, so the UI never recomputes them.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { rumOverviewQuery } from "@/api/rum";
import { VitalCard } from "@/components/rum/VitalCard";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { formatNumber } from "@/lib/format";
import { apiTimeMs, formatMs, sortVitals } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

function Total({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border bg-card p-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1 text-xl font-semibold tabular-nums">{value}</p>
    </div>
  );
}

export function RumOverviewTab({ range, app, environment }: { range: RangeSpec; app: string; environment?: string }) {
  const { t, i18n } = useTranslation();
  const overview = useQuery(rumOverviewQuery(range, app, environment));

  if (overview.isPending) return <LoadingState />;
  if (overview.isError) return <ErrorState error={overview.error} onRetry={() => void overview.refetch()} />;

  const { totals, points, vitals } = overview.data;
  // The x bounds are the window the server answered for, like every other chart: resolving the range again
  // here would read the clock during render, and the empty edges would drift away from the data.
  const from = apiTimeMs(overview.data.from) ?? undefined;
  const to = apiTimeMs(overview.data.to) ?? undefined;
  const views: [number, number][] = points.map((p) => [p.t, p.views]);
  const load: [number, number][] = points.filter((p) => p.avg_ms !== null).map((p) => [p.t, p.avg_ms as number]);

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Total label={t("rum.overview.views")} value={formatNumber(totals.views, i18n.language)} />
        <Total label={t("rum.overview.sessions")} value={formatNumber(totals.sessions, i18n.language)} />
        <Total label={t("rum.overview.errors")} value={formatNumber(totals.errors, i18n.language)} />
        <Total label={t("rum.overview.avgLoad")} value={formatMs(totals.avg_ms, i18n.language)} />
      </div>

      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">{t("rum.overview.vitals")}</h2>
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
          {sortVitals(vitals).map((v) => (
            <VitalCard key={v.name} vital={v} />
          ))}
        </div>
      </section>

      <div className="grid gap-3 xl:grid-cols-2">
        <div className="rounded-xl border bg-card p-3">
          <TimeSeriesChart title={t("rum.overview.viewsChart")} unit="number" from={from} to={to} height={200} series={[{ label: t("rum.overview.views"), points: views }]} />
        </div>
        <div className="rounded-xl border bg-card p-3">
          <TimeSeriesChart title={t("rum.overview.loadChart")} unit="ms" from={from} to={to} height={200} series={[{ label: t("rum.overview.avgLoad"), points: load }]} />
        </div>
      </div>
    </div>
  );
}
