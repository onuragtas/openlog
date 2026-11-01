// Who uses the application and whether they come back (analytics.md §6).
//
// This tab reads over days and months, so the page's range picker does not apply: an hour of DAU means nothing. An
// application that sends no visitor id gets a sentence saying what to turn on rather than empty charts, because an
// empty retention table reads as "nobody came back".
import { useQuery } from "@tanstack/react-query";
import { Users } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  analyticsAcquisitionQuery,
  analyticsAudienceQuery,
  type AnalyticsAudienceBy,
  analyticsActivityQuery,
  analyticsEventsQuery,
  analyticsRetentionQuery,
  type AnalyticsCohort,
  type AnalyticsGrain,
  type AnalyticsSourceBy,
} from "@/api/rum";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { formatNumber } from "@/lib/format";
import { formatShare } from "@/lib/rum";

const GRAINS: AnalyticsGrain[] = ["day", "week", "month"];
/** Columns of the retention table. Beyond this it stops fitting and stops being what anyone reads. */
const MAX_PERIODS = 12;

function Total({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-xl border bg-card p-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-2xl font-semibold tabular-nums">{value}</span>
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

/** One named thing per row with its counts. These read as lists, so a table beats a chart and shows every number. */
function Breakdown({ testId, nameLabel, rows, columns }: { testId: string; nameLabel: string; rows: { name: string; values: number[] }[]; columns: string[] }) {
  const { t, i18n } = useTranslation();
  if (rows.length === 0) return <p className="rounded-xl border border-dashed p-4 text-center text-sm text-muted-foreground">{t("rum.analytics.noRows")}</p>;
  return (
    <div className="overflow-x-auto rounded-xl border bg-card p-3">
      <table className="w-full text-sm" data-testid={testId}>
        <thead>
          <tr className="text-xs text-muted-foreground">
            <th scope="col" className="px-2 py-1 text-left font-normal">{nameLabel}</th>
            {columns.map((c) => (
              <th key={c} scope="col" className="px-2 py-1 text-right font-normal">{c}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.name} className="border-t">
              <th scope="row" className="px-2 py-1.5 text-left font-mono text-xs font-normal break-all">{r.name}</th>
              {r.values.map((v, i) => (
                <td key={i} className="px-2 py-1.5 text-right tabular-nums">{formatNumber(v, i18n.language)}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function EventsSection({ app, environment }: { app: string; environment?: string }) {
  const { t } = useTranslation();
  const events = useQuery(analyticsEventsQuery(app, environment));
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
          columns={[t("rum.analytics.count"), t("rum.analytics.sessions"), t("rum.analytics.users")]}
          rows={events.data.events.map((e) => ({ name: e.name, values: [e.count, e.sessions, e.users] }))}
        />
      )}
    </section>
  );
}

function AcquisitionSection({ app, environment }: { app: string; environment?: string }) {
  const { t } = useTranslation();
  const [by, setBy] = useState<AnalyticsSourceBy>("referrer");
  const sources = useQuery(analyticsAcquisitionQuery(app, by, environment));
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
          columns={[t("rum.analytics.sessions"), t("rum.analytics.users")]}
          rows={sources.data.sources.map((s) => ({ name: s.value || t("rum.analytics.direct"), values: [s.sessions, s.users] }))}
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

function AudienceSection({ app, environment }: { app: string; environment?: string }) {
  const { t, i18n } = useTranslation();
  const [by, setBy] = useState<AnalyticsAudienceBy>("country");
  const audience = useQuery(analyticsAudienceQuery(app, by, environment));
  const name = (value: string) => {
    if (value === "") return t("rum.analytics.unknown");
    return by === "country" ? countryName(value, i18n.language) : value;
  };
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
            columns={[t("rum.analytics.sessions"), t("rum.analytics.users")]}
            rows={audience.data.rows.map((r) => ({ name: name(r.value), values: [r.sessions, r.users] }))}
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
  const activity = useQuery(analyticsActivityQuery(app, environment));
  const retention = useQuery(analyticsRetentionQuery(app, grain, environment));

  if (activity.isPending) return <LoadingState />;
  if (activity.isError) return <ErrorState error={activity.error} onRetry={() => void activity.refetch()} />;
  if (!activity.data.collecting) return <NotCollecting />;

  const { days } = activity.data;
  // The last full picture: today is still filling up, but it is also what somebody opens this screen to see.
  const latest = days[days.length - 1];
  const series = (pick: (d: (typeof days)[number]) => number): [number, number][] => days.map((d) => [Date.parse(d.date), pick(d)]);
  const newUsers = days.reduce((sum, d) => sum + d.new_users, 0);

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
        <Total label={t("rum.analytics.dau")} value={formatNumber(latest?.dau ?? 0, i18n.language)} />
        <Total label={t("rum.analytics.wau")} value={formatNumber(latest?.wau ?? 0, i18n.language)} />
        <Total label={t("rum.analytics.mau")} value={formatNumber(latest?.mau ?? 0, i18n.language)} />
        <Total label={t("rum.analytics.newUsers")} value={formatNumber(newUsers, i18n.language)} />
        <Total label={t("rum.analytics.stickiness")} value={formatShare(latest?.stickiness ?? 0)} />
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
        <EventsSection app={app} environment={environment} />
        <AcquisitionSection app={app} environment={environment} />
        <AudienceSection app={app} environment={environment} />
      </div>
    </div>
  );
}
