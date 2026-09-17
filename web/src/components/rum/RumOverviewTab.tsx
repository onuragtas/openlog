// The application's Core Web Vitals, its page view series and the totals of the range (rum.md §7).
// The p75 leads every vital card because that is the percentile the Core Web Vitals assessment is defined
// on, and the rating beside it comes from the server: the thresholds are constants of the programme, not
// settings, so the UI never recomputes them.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { rumOverviewQuery, type RumVital } from "@/api/rum";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Badge } from "@/components/ui/badge";
import { formatNumber } from "@/lib/format";
import { apiTimeMs, formatMs, formatShare, formatVital, ratingKey, ratingVariant, sortVitals } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

function Total({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border bg-card p-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1 text-xl font-semibold tabular-nums">{value}</p>
    </div>
  );
}

/** The three bands are shares of the same total, so they read as one bar rather than three numbers. */
function Bands({ vital }: { vital: RumVital }) {
  const { t } = useTranslation();
  const bands = [
    { key: "good", share: vital.good, className: "bg-success" },
    { key: "needs_improvement", share: vital.needs_improvement, className: "bg-warning" },
    { key: "poor", share: vital.poor, className: "bg-destructive" },
  ];
  return (
    <div className="mt-3 flex flex-col gap-1">
      <div className="flex h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
        {bands.map((b) => (b.share > 0 ? <div key={b.key} className={b.className} style={{ width: `${b.share * 100}%` }} /> : null))}
      </div>
      <p className="text-xs text-muted-foreground">
        {t("rum.vitals.bands", {
          good: formatShare(vital.good),
          needsImprovement: formatShare(vital.needs_improvement),
          poor: formatShare(vital.poor),
        })}
      </p>
    </div>
  );
}

function VitalCard({ vital }: { vital: RumVital }) {
  const { t, i18n } = useTranslation();
  const measured = vital.count > 0;
  return (
    <div className="rounded-xl border bg-card p-3" data-testid="rum-vital">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs font-medium uppercase">{t(`rum.vitals.names.${vital.name}`)}</p>
        <Badge variant={ratingVariant(vital.rating)}>{t(ratingKey(vital.rating))}</Badge>
      </div>
      <p className="mt-1 text-xl font-semibold tabular-nums">{formatVital(vital.p75, vital.name, i18n.language)}</p>
      <p className="text-xs text-muted-foreground">
        {measured ? t("rum.vitals.count", { n: Math.round(vital.count) }) : t("rum.vitals.noData")}
      </p>
      {measured && <Bands vital={vital} />}
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
