// Who uses the application and whether they come back (analytics.md §6).
//
// This tab reads over days and months, so the page's range picker does not apply: an hour of DAU means nothing. An
// application that sends no visitor id gets a sentence saying what to turn on rather than empty charts, because an
// empty retention table reads as "nobody came back".
import { useQuery } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Users } from "lucide-react";
import { useEffect, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  analyticsAcquisitionQuery,
  analyticsAudienceQuery,
  type AnalyticsAudienceBy,
  analyticsActivityQuery,
  type AnalyticsMetric,
  analyticsCompareQuery,
  analyticsEventsQuery,
  analyticsRetentionQuery,
  type AnalyticsCohort,
  type AnalyticsGrain,
  type AnalyticsSourceBy,
} from "@/api/rum";
import { ListPager } from "@/components/ListPager";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { SortableHead } from "@/components/ui/table";
import { formatNumber } from "@/lib/format";
import { useLocalList } from "@/lib/list";
import { formatShare } from "@/lib/rum";
import { cn } from "@/lib/utils";

const GRAINS: AnalyticsGrain[] = ["day", "week", "month"];
/** Columns of the retention table. Beyond this it stops fitting and stops being what anyone reads. */
const MAX_PERIODS = 12;

const PERIODS = [7, 28, 90];

/** One decimal, because a whole number would show a 0.4% move as nothing having happened. */
const formatPercent = (share: number, locale: string) => new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 1 }).format(share);

/** How far a number moved: the count and the share, as Google Analytics shows them. An arrow and a sign carry the direction, so it never rests on color alone. */
function Delta({ change, rate, format }: { change: number | null | undefined; rate?: number | null; format?: (n: number) => string }) {
  const { t, i18n } = useTranslation();
  if (change === null || change === undefined) return <span className="text-xs text-muted-foreground">–</span>;
  const number = format ?? ((n: number) => formatNumber(n, i18n.language));
  if (change === 0) return <span className="text-xs text-muted-foreground">{t("rum.analytics.noChange")}</span>;
  const up = change > 0;
  const Arrow = up ? ArrowUp : ArrowDown;
  return (
    <span className={cn("inline-flex items-center gap-0.5 text-xs tabular-nums", up ? "text-emerald-700 dark:text-emerald-400" : "text-destructive")} data-testid="analytics-delta">
      <Arrow className="size-3" aria-hidden="true" />
      {up ? "+" : "−"}
      {number(Math.abs(change))}
      <span className="ml-1">
        {rate === null || rate === undefined ? t("rum.analytics.fromNothing") : `(${up ? "+" : "−"}${formatPercent(Math.abs(rate), i18n.language)})`}
      </span>
    </span>
  );
}

function Total({ label, value, metric, format }: { label: string; value: string; metric?: AnalyticsMetric; format?: (n: number) => string }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-xl border bg-card p-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-2xl font-semibold tabular-nums">{value}</span>
      {metric && <Delta change={metric.change} rate={metric.change_rate} format={format} />}
    </div>
  );
}

function NotCollecting() {
  const { t } = useTranslation();
  return (
    <p className="rounded-xl border border-dashed p-6 text-center text-sm text-muted-foreground" data-testid="analytics-not-collecting">
      <Users className="mx-auto mb-2 size-5" aria-hidden="true" />
      {t("rum.analytics.notCollecting")}
    </p>
  );
}

/** One cohort's share in one period. The number is always written, so the tint only makes the table quicker to scan. */
function RetentionCell({ rate, users }: { rate: number; users: number }) {
  const { i18n } = useTranslation();
  // One hue from nothing up to about half strength, which keeps the ink on top readable in both themes.
  const tint = Math.round(Math.max(0, Math.min(1, rate)) * 55);
  return (
    <td
      className="min-w-14 rounded px-2 py-1.5 text-center text-xs tabular-nums"
      style={{ backgroundColor: `color-mix(in oklab, var(--primary) ${tint}%, transparent)` }}
      title={formatNumber(users, i18n.language)}
    >
      {formatShare(rate)}
    </td>
  );
}

function RetentionTable({ cohorts, grain }: { cohorts: AnalyticsCohort[]; grain: AnalyticsGrain }) {
  const { t, i18n } = useTranslation();
  const periods = Math.min(MAX_PERIODS, Math.max(...cohorts.flatMap((c) => c.periods.map((p) => p.period))) + 1);
  return (
    <div className="overflow-x-auto rounded-xl border bg-card p-3">
      <table className="w-full border-separate border-spacing-0.5" data-testid="analytics-retention">
        <thead>
          <tr className="text-xs font-normal text-muted-foreground">
            <th scope="col" className="px-2 py-1 text-left font-normal">{t("rum.analytics.cohort")}</th>
            <th scope="col" className="px-2 py-1 text-right font-normal">{t("rum.analytics.size")}</th>
            {Array.from({ length: periods }, (_, i) => (
              <th key={i} scope="col" className="px-2 py-1 text-center font-normal">
                {t(`rum.analytics.period.${grain}`, { n: i })}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {cohorts.map((c) => (
            <tr key={c.cohort}>
              <th scope="row" className="px-2 py-1.5 text-left text-xs font-normal whitespace-nowrap">{c.cohort}</th>
              <td className="px-2 py-1.5 text-right text-xs tabular-nums">{formatNumber(c.size, i18n.language)}</td>
              {Array.from({ length: periods }, (_, i) => {
                const p = c.periods.find((x) => x.period === i);
                // A period nobody came back in is a real 0%, unlike one that has not happened yet, which this
                // table cannot tell apart from it here: both stay blank instead of claiming a number.
                return p ? <RetentionCell key={i} rate={p.rate} users={p.users} /> : <td key={i} />;
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const SOURCE_BYS: AnalyticsSourceBy[] = ["referrer", "source", "medium", "campaign"];

type BreakdownRow = { name: string; values: number[]; previous?: (number | undefined)[] };
type BreakdownSort = "name" | `value${number}`;

/**
 * One named thing per row with its counts. These read as lists, so a table beats a chart and shows every number.
 *
 * Sorted and paged here and not on the server, because a list with a comparison is two lists merged after they are read: the server takes the top
 * 200 of each period and adds what only the previous one had, so a page cut there would need both read again, and would have to place those added
 * rows anyway. Holding them all means such a row is sorted with the rest at its zero and stays reachable. It also means a sort covers those rows
 * and no others: a value outside the top 200 of both periods is not in the list.
 */
function Breakdown({ testId, nameLabel, rows, columns, resetKey }: { testId: string; nameLabel: string; rows: BreakdownRow[]; columns: string[]; resetKey: string }) {
  const { t, i18n } = useTranslation();
  const read = useMemo(() => {
    const out: Record<string, (r: BreakdownRow) => string | number | undefined> = { name: (r) => r.name };
    columns.forEach((_, i) => {
      out[`value${i}`] = (r) => r.values[i];
    });
    return out as Record<BreakdownSort, (r: BreakdownRow) => string | number | undefined>;
  }, [columns]);
  const list = useLocalList<BreakdownRow, BreakdownSort>(rows, read, "value0", "desc", 50, "RumAnalyticsTab");
  const { toFirstPage } = list;
  // Another dimension or period is another list, and its first page is the one that exists.
  useEffect(toFirstPage, [resetKey, toFirstPage]);
  if (rows.length === 0) return <p className="rounded-xl border border-dashed p-4 text-center text-sm text-muted-foreground">{t("rum.analytics.noRows")}</p>;
  return (
    <div className="overflow-x-auto rounded-xl border bg-card p-3">
      <table className="w-full text-sm" data-testid={testId}>
        <thead>
          <tr className="text-xs text-muted-foreground">
            <SortableHead<BreakdownSort> column="name" defaultDir="asc" scope="col" className="h-auto px-2 py-1 font-normal" {...list.head}>{nameLabel}</SortableHead>
            {columns.map((c, i) => (
              <SortableHead<BreakdownSort> key={c} column={`value${i}`} scope="col" className="h-auto px-2 py-1 text-right font-normal" {...list.head}>{c}</SortableHead>
            ))}
          </tr>
        </thead>
        <tbody>
          {list.visible.map((r) => (
            <tr key={r.name} className="border-t" data-testid={`${testId}-row`}>
              <th scope="row" className="px-2 py-1.5 text-left font-mono text-xs font-normal break-all">{r.name}</th>
              {r.values.map((v, i) => (
                <td key={i} className="px-2 py-1.5 text-right tabular-nums">
                  <div>{formatNumber(v, i18n.language)}</div>
                  {r.previous?.[i] !== undefined && <Delta change={v - r.previous[i]!} rate={r.previous[i] === 0 ? null : (v - r.previous[i]!) / r.previous[i]!} />}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <ListPager list={list} count={list.visible.length} hasMore={list.hasMore} />
    </div>
  );
}

function EventsSection({ app, days, environment }: { app: string; days: number; environment?: string }) {
  const { t } = useTranslation();
  const events = useQuery(analyticsEventsQuery(app, days, environment));
  const columns = useMemo(() => [t("rum.analytics.count"), t("rum.analytics.sessions"), t("rum.analytics.users")], [t]);
  const rows = useMemo(
    () => (events.data?.events ?? []).map((e) => ({ name: e.name, values: [e.count, e.sessions, e.users], previous: [e.previous_count, e.previous_sessions, e.previous_users] })),
    [events.data],
  );
  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-sm font-medium">{t("rum.analytics.events")}</h2>
      {events.isPending ? (
        <LoadingState />
      ) : events.isError ? (
        <ErrorState error={events.error} onRetry={() => void events.refetch()} />
      ) : (
        <Breakdown
          testId="analytics-events"
          nameLabel={t("rum.analytics.eventName")}
          columns={columns}
          rows={rows}
          resetKey={String(days)}
        />
      )}
    </section>
  );
}

function AcquisitionSection({ app, days, environment }: { app: string; days: number; environment?: string }) {
  const { t } = useTranslation();
  const [by, setBy] = useState<AnalyticsSourceBy>("referrer");
  const sources = useQuery(analyticsAcquisitionQuery(app, by, days, environment));
  const columns = useMemo(() => [t("rum.analytics.sessions"), t("rum.analytics.users")], [t]);
  const rows = useMemo(
    () => (sources.data?.sources ?? []).map((s) => ({ name: s.value || t("rum.analytics.direct"), values: [s.sessions, s.users], previous: [s.previous_sessions, s.previous_users] })),
    [sources.data, t],
  );
  return (
    <section className="flex flex-col gap-2">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <h2 className="text-sm font-medium">{t("rum.analytics.acquisition")}</h2>
        <div className="flex items-center gap-2">
          <Label htmlFor="analytics-source-by" className="text-xs text-muted-foreground">
            {t("rum.analytics.sourceByLabel")}
          </Label>
          <NativeSelect id="analytics-source-by" className="w-32" value={by} onChange={(e) => setBy(e.target.value as AnalyticsSourceBy)}>
            {SOURCE_BYS.map((b) => (
              <option key={b} value={b}>
                {t(`rum.analytics.sourceBy.${b}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
      </div>
      {sources.isPending ? (
        <LoadingState />
      ) : sources.isError ? (
        <ErrorState error={sources.error} onRetry={() => void sources.refetch()} />
      ) : (
        <Breakdown
          testId="analytics-sources"
          nameLabel={t(`rum.analytics.sourceBy.${by}`)}
          columns={columns}
          rows={rows}
          resetKey={`${by}/${days}`}
        />
      )}
    </section>
  );
}

const AUDIENCE_BYS: AnalyticsAudienceBy[] = ["country", "device", "model", "os", "browser", "version"];

/** A country's name in the reader's language, from the browser's own data. A code it does not know stays a code. */
function countryName(code: string, language: string): string {
  try {
    return new Intl.DisplayNames([language], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

function AudienceSection({ app, days, environment }: { app: string; days: number; environment?: string }) {
  const { t, i18n } = useTranslation();
  const [by, setBy] = useState<AnalyticsAudienceBy>("country");
  const audience = useQuery(analyticsAudienceQuery(app, by, days, environment));
  const columns = useMemo(() => [t("rum.analytics.sessions"), t("rum.analytics.users")], [t]);
  const rows = useMemo(
    () =>
      (audience.data?.rows ?? []).map((r) => ({
        name: r.value === "" ? t("rum.analytics.unknown") : by === "country" ? countryName(r.value, i18n.language) : r.value,
        values: [r.sessions, r.users],
        previous: [r.previous_sessions, r.previous_users],
      })),
    [audience.data, by, i18n.language, t],
  );
  return (
    <section className="flex flex-col gap-2">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <h2 className="text-sm font-medium">{t("rum.analytics.audience")}</h2>
        <div className="flex items-center gap-2">
          <Label htmlFor="analytics-audience-by" className="text-xs text-muted-foreground">
            {t("rum.analytics.sourceByLabel")}
          </Label>
          <NativeSelect id="analytics-audience-by" className="w-36" value={by} onChange={(e) => setBy(e.target.value as AnalyticsAudienceBy)}>
            {AUDIENCE_BYS.map((b) => (
              <option key={b} value={b}>
                {t(`rum.analytics.audienceBy.${b}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
      </div>
      {audience.isPending ? (
        <LoadingState />
      ) : audience.isError ? (
        <ErrorState error={audience.error} onRetry={() => void audience.refetch()} />
      ) : (
        <>
          <Breakdown
            testId="analytics-audience"
            nameLabel={t(`rum.analytics.audienceBy.${by}`)}
            columns={columns}
            rows={rows}
            resetKey={`${by}/${days}`}
          />
          {by === "country" && audience.data.rows.length > 0 && audience.data.rows.every((r) => r.value === "") && (
            <p className="text-xs text-muted-foreground">{t("rum.analytics.noCountry")}</p>
          )}
        </>
      )}
    </section>
  );
}

export function RumAnalyticsTab({ app, environment }: { app: string; environment?: string }) {
  const { t, i18n } = useTranslation();
  const [grain, setGrain] = useState<AnalyticsGrain>("week");
  const [days, setDays] = useState(28);
  const periodId = useId();
  const activity = useQuery(analyticsActivityQuery(app, days, environment));
  const compare = useQuery(analyticsCompareQuery(app, days, environment));
  const retention = useQuery(analyticsRetentionQuery(app, grain, environment));

  if (activity.isPending) return <LoadingState />;
  if (activity.isError) return <ErrorState error={activity.error} onRetry={() => void activity.refetch()} />;
  if (!activity.data.collecting) return <NotCollecting />;

  const series0 = activity.data.days;
  // The last full picture: today is still filling up, but it is also what somebody opens this screen to see.
  const latest = series0[series0.length - 1];
  const series = (pick: (d: (typeof series0)[number]) => number): [number, number][] => series0.map((d) => [Date.parse(d.date), pick(d)]);
  const m = compare.data?.metrics;
  const n = (metric: AnalyticsMetric | undefined, fallback = 0) => formatNumber(metric?.current ?? fallback, i18n.language);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={periodId} className="text-xs text-muted-foreground">
            {t("rum.analytics.periodLabel")}
          </Label>
          <NativeSelect id={periodId} className="w-36" value={String(days)} onChange={(e) => setDays(Number(e.target.value))}>
            {PERIODS.map((p) => (
              <option key={p} value={p}>
                {t("rum.analytics.lastDays", { days: p })}
              </option>
            ))}
          </NativeSelect>
        </div>
        {compare.data && (
          <p className="text-xs text-muted-foreground" data-testid="analytics-compare-range">
            {t("rum.analytics.comparedWith", { from: compare.data.previous.from, to: compare.data.previous.to })}
          </p>
        )}
      </div>
      {compare.data?.previous_partial && <p className="text-xs text-muted-foreground">{t("rum.analytics.previousPartial")}</p>}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-5" data-testid="analytics-totals">
        <Total label={t("rum.analytics.users")} value={n(m?.users)} metric={m?.users} />
        <Total label={t("rum.analytics.newUsersLabel")} value={n(m?.new_users)} metric={m?.new_users} />
        <Total label={t("rum.analytics.sessions")} value={n(m?.sessions)} metric={m?.sessions} />
        <Total label={t("rum.analytics.pageViews")} value={n(m?.page_views)} metric={m?.page_views} />
        <Total label={t("rum.analytics.events")} value={n(m?.events)} metric={m?.events} />
        <Total label={t("rum.analytics.dau")} value={n(m?.dau, latest?.dau)} metric={m?.dau} />
        <Total label={t("rum.analytics.wau")} value={n(m?.wau, latest?.wau)} metric={m?.wau} />
        <Total label={t("rum.analytics.mau")} value={n(m?.mau, latest?.mau)} metric={m?.mau} />
        <Total label={t("rum.analytics.avgDailyUsers")} value={n(m?.avg_daily_users)} metric={m?.avg_daily_users} />
        <Total label={t("rum.analytics.stickiness")} value={formatShare(m?.stickiness.current ?? latest?.stickiness ?? 0)} metric={m?.stickiness} format={formatShare} />
      </div>

      <div className="rounded-xl border bg-card p-3">
        <TimeSeriesChart
          title={t("rum.analytics.activityChart")}
          unit="number"
          height={240}
          series={[
            { label: t("rum.analytics.dau"), points: series((d) => d.dau) },
            { label: t("rum.analytics.wau"), points: series((d) => d.wau) },
            { label: t("rum.analytics.mau"), points: series((d) => d.mau) },
          ]}
        />
      </div>

      <section className="flex flex-col gap-2">
        <div className="flex flex-wrap items-end justify-between gap-2">
          <h2 className="text-sm font-medium">{t("rum.analytics.retention")}</h2>
          <div className="flex items-center gap-2">
            <Label htmlFor="analytics-grain" className="text-xs text-muted-foreground">
              {t("rum.analytics.grainLabel")}
            </Label>
            <NativeSelect id="analytics-grain" className="w-32" value={grain} onChange={(e) => setGrain(e.target.value as AnalyticsGrain)}>
              {GRAINS.map((g) => (
                <option key={g} value={g}>
                  {t(`rum.analytics.grain.${g}`)}
                </option>
              ))}
            </NativeSelect>
          </div>
        </div>
        {retention.isPending ? (
          <LoadingState />
        ) : retention.isError ? (
          <ErrorState error={retention.error} onRetry={() => void retention.refetch()} />
        ) : retention.data.cohorts.length === 0 ? (
          <NotCollecting />
        ) : (
          <>
            <RetentionTable cohorts={retention.data.cohorts} grain={grain} />
            {retention.data.sampling_changed && <p className="text-xs text-muted-foreground">{t("rum.analytics.samplingChanged")}</p>}
          </>
        )}
      </section>

      <div className="grid gap-4 xl:grid-cols-2">
        <EventsSection app={app} days={days} environment={environment} />
        <AcquisitionSection app={app} days={days} environment={environment} />
        <AudienceSection app={app} days={days} environment={environment} />
      </div>
    </div>
  );
}
