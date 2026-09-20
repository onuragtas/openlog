import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { atLeast } from "@/api/roles";
import {
  downloadUsageExport,
  orgPlanQuery,
  periodOptions,
  plansQuery,
  putOrgPlan,
  usageDailyQuery,
  usageOverviewQuery,
  usageTopQuery,
  type OrgPlan,
  type OrgPlanInput,
  type QuotaMetric,
  type UsageOverview,
  type UsagePeriod,
  type UsageSignal,
} from "@/api/usage";
import { ErrorState, EmptyState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatBytes, formatNumber } from "@/lib/format";
import { cn } from "@/lib/utils";
import { FormError, SettingsSection } from "./common";
import { QueryLimitsSettings } from "./QueryLimitsSettings";

const SIGNALS: readonly UsageSignal[] = ["traces", "logs", "metrics"];

function formatMetric(metric: QuotaMetric["metric"], v: number, locale: string): string {
  return metric === "ingest_bytes" ? formatBytes(v) : formatNumber(v, locale);
}

const LEVEL_BAR = { ok: "bg-primary", warning: "bg-warning", exceeded: "bg-destructive" } as const;
const LEVEL_BADGE = { ok: "secondary", warning: "outline", exceeded: "destructive" } as const;

function Meter({ m, projection, locale }: { m: QuotaMetric; projection?: UsageOverview["projection"]; locale: string }) {
  const { t } = useTranslation();
  const label = t(`usage.metrics.${m.metric}`);
  const unlimited = m.limit <= 0;
  return (
    <div className="flex min-w-0 flex-col gap-2 rounded-lg border p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">{label}</span>
        {!unlimited && <Badge variant={LEVEL_BADGE[m.level]}>{t(`usage.levels.${m.level}`)}</Badge>}
      </div>
      <div className="text-lg font-semibold tabular-nums">
        {unlimited
          ? `${formatMetric(m.metric, m.used, locale)} · ${t("usage.unlimited")}`
          : t("usage.of", { used: formatMetric(m.metric, m.used, locale), limit: formatMetric(m.metric, m.limit, locale) })}
      </div>
      {!unlimited && (
        <div
          role="progressbar"
          aria-label={label}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.min(100, Math.round(m.percent))}
          className="h-2 w-full overflow-hidden rounded-full bg-muted"
        >
          <div className={cn("h-full rounded-full", LEVEL_BAR[m.level])} style={{ width: `${Math.min(100, m.percent)}%` }} />
        </div>
      )}
      {m.metric === "ingest_bytes" && projection && projection.ingest_bytes > 0 && (
        <p className="text-xs text-muted-foreground">
          {projection.ingest_percent !== undefined && !unlimited
            ? t("usage.projectedPercent", { value: formatBytes(projection.ingest_bytes), percent: Math.round(projection.ingest_percent) })
            : t("usage.projected", { value: formatBytes(projection.ingest_bytes) })}
        </p>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex min-w-0 flex-col rounded-lg border p-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="truncate text-base font-semibold tabular-nums">{value}</span>
    </div>
  );
}

/** Settings → Usage & plan: period usage vs plan limits, daily breakdown, top services/hosts, export, operator override. */
export function UsageSettings() {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const id = useId();
  const me = useMe().data;
  const [period, setPeriod] = useState<UsagePeriod>("current");
  const [by, setBy] = useState<"service" | "host">("service");
  const overview = useQuery(usageOverviewQuery(period));
  const daily = useQuery(usageDailyQuery(period));
  const top = useQuery(usageTopQuery(period, by));
  const options = useMemo(() => periodOptions(), []);
  const [exportError, setExportError] = useState<unknown>(null);

  const o = overview.data;
  const canExport = atLeast(me?.role, "admin");

  const series = useMemo(() => {
    const days = daily.data?.days ?? [];
    return SIGNALS.map((s) => ({
      label: t(`usage.signals.${s}`),
      points: days.map((d): [number, number] => [Date.parse(`${d.day}T00:00:00Z`), d.signals.find((x) => x.signal === s)?.ingest_bytes ?? 0]),
    }));
  }, [daily.data, t]);

  return (
    <div className="flex flex-col gap-4">
      <SettingsSection title={t("usage.title")} description={t("usage.description")}>
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={`${id}-period`}>{t("usage.period")}</Label>
            <NativeSelect id={`${id}-period`} value={period} onChange={(e) => setPeriod(e.target.value)}>
              {options.map((p) => (
                <option key={p} value={p}>
                  {p === "current" ? t("usage.periods.current") : p === "previous" ? t("usage.periods.previous") : p}
                </option>
              ))}
            </NativeSelect>
          </div>
          {o && (
            <div className="flex flex-wrap items-center gap-2 pb-1 text-sm">
              <span className="text-muted-foreground">{t("usage.plan")}</span>
              <Badge>{o.plan.name}</Badge>
              {!o.plan_assigned && <span className="text-xs text-muted-foreground">({t("usage.planDefault")})</span>}
              <Badge variant="outline">{o.saas_mode ? t("usage.saasMode") : t("usage.selfHosted")}</Badge>
              <span className="text-xs text-muted-foreground">
                {o.period.start.slice(0, 10)} – {o.period.end.slice(0, 10)}
              </span>
            </div>
          )}
          {canExport && o && (
            <div className="ml-auto flex flex-wrap gap-2">
              {(["csv", "json"] as const).map((f) => (
                <Button
                  key={f}
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setExportError(null);
                    downloadUsageExport(period, f, o.organization.tenant_id).catch(setExportError);
                  }}
                >
                  {t(`usage.export.${f}`)}
                </Button>
              ))}
            </div>
          )}
        </div>
        <FormError error={exportError} />

        {overview.isLoading && <LoadingState />}
        {overview.error && <ErrorState error={overview.error} onRetry={() => void overview.refetch()} />}
        {o && (
          <>
            {o.ingest_blocked && (
              <p role="alert" className="rounded-md border border-destructive/60 bg-destructive/10 px-3 py-2 text-sm">
                {t("usage.blocked")}
              </p>
            )}
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {o.limits.map((m) => (
                <Meter key={m.metric} m={m} projection={o.projection} locale={locale} />
              ))}
            </div>
            <div className="grid grid-cols-2 gap-3 md:grid-cols-3 lg:grid-cols-5">
              <Stat label={t("usage.stats.containers")} value={formatNumber(o.usage.containers, locale)} />
              <Stat label={t("usage.stats.services")} value={formatNumber(o.usage.services, locale)} />
              <Stat label={t("usage.stats.queries")} value={formatNumber(o.usage.query.queries, locale)} />
              <Stat label={t("usage.stats.queryCpu")} value={`${formatNumber(Math.round(o.usage.query.cpu_seconds), locale)} s`} />
              <Stat label={t("usage.stats.stored")} value={formatBytes(o.stored.reduce((sum, s) => sum + s.compressed_bytes, 0))} />
            </div>
            <div className="overflow-x-auto">
              <Table mobile="stack">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("usage.table.signal")}</TableHead>
                    <TableHead className="text-right">{t("usage.table.items")}</TableHead>
                    <TableHead className="text-right">{t("usage.table.ingest")}</TableHead>
                    <TableHead className="text-right">{t("usage.table.stored")}</TableHead>
                    <TableHead className="text-right">{t("usage.table.retention")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {SIGNALS.map((s) => {
                    const u = o.usage.signals.find((x) => x.signal === s);
                    const st = o.stored.find((x) => x.signal === s);
                    return (
                      <TableRow key={s}>
                        <TableCell className="max-md:w-full max-md:font-medium">{t(`usage.signals.${s}`)}</TableCell>
                        <TableCell label={t("usage.table.items")} className="text-right tabular-nums">
                          {formatNumber(u?.items ?? 0, locale)}
                        </TableCell>
                        <TableCell label={t("usage.table.ingest")} className="text-right tabular-nums">
                          {formatBytes(u?.ingest_bytes ?? 0)}
                        </TableCell>
                        <TableCell label={t("usage.table.stored")} className="text-right tabular-nums">
                          {formatBytes(st?.compressed_bytes ?? 0)}
                        </TableCell>
                        <TableCell label={t("usage.table.retention")} className="text-right tabular-nums">
                          {t("usage.retentionDays", { count: st?.retention_days ?? 0 })}
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            </div>
          </>
        )}
      </SettingsSection>

      <SettingsSection title={t("usage.daily.title")}>
        {daily.data && daily.data.days.length === 0 ? (
          <EmptyState>{t("usage.daily.empty")}</EmptyState>
        ) : (
          <TimeSeriesChart
            title={t("usage.daily.title")}
            series={daily.isLoading ? undefined : series}
            isLoading={daily.isLoading}
            error={daily.error}
            onRetry={() => void daily.refetch()}
            unit="bytes"
            stacked
            bars
            height={220}
            from={o ? Date.parse(o.period.start) : undefined}
            to={o ? Date.parse(o.period.end) : undefined}
          />
        )}
      </SettingsSection>

      <SettingsSection title={t("usage.top.title")}>
        <div className="flex min-w-0 flex-col gap-1.5 sm:max-w-xs">
          <Label htmlFor={`${id}-by`}>{t("usage.top.by")}</Label>
          <NativeSelect id={`${id}-by`} value={by} onChange={(e) => setBy(e.target.value as "service" | "host")}>
            <option value="service">{t("usage.top.service")}</option>
            <option value="host">{t("usage.top.host")}</option>
          </NativeSelect>
        </div>
        {top.isLoading && <LoadingState />}
        {top.error && <ErrorState error={top.error} onRetry={() => void top.refetch()} />}
        {top.data && top.data.entries.length === 0 && <EmptyState>{t("usage.top.empty")}</EmptyState>}
        {top.data && top.data.entries.length > 0 && (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("usage.top.name")}</TableHead>
                  <TableHead className="text-right">{t("usage.top.bytes")}</TableHead>
                  {SIGNALS.map((s) => (
                    <TableHead key={s} className="text-right">
                      {t(`usage.signals.${s}`)}
                    </TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {top.data.entries.map((e) => (
                  <TableRow key={e.key}>
                    <TableCell className="max-w-[16rem] truncate font-mono text-xs" title={e.key}>
                      {e.key}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{formatBytes(e.bytes)}</TableCell>
                    {SIGNALS.map((s) => (
                      <TableCell key={s} className="text-right tabular-nums">
                        {formatBytes(e.bytes_by_signal[s] ?? 0)}
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </SettingsSection>

      {o && <QueryLimitsSettings />}

      {o?.can_manage_plan && <PlanOverride orgId={o.organization.id} />}
    </div>
  );
}

/** Operator form (OPENLOG_SUPERADMIN_EMAILS): assign a plan and overrides to this organization. */
export function PlanOverride({ orgId }: { orgId: string }) {
  const plans = useQuery(plansQuery());
  const current = useQuery(orgPlanQuery(orgId));
  if (current.isLoading || plans.isLoading) return <LoadingState />;
  if (current.error || plans.error) return <ErrorState error={current.error ?? plans.error} />;
  if (!current.data || !plans.data) return null;
  return <PlanOverrideForm key={current.data.updated_at ?? "none"} orgId={orgId} current={current.data} plans={plans.data.plans} />;
}

function PlanOverrideForm({ orgId, current, plans }: { orgId: string; current: OrgPlan; plans: { id: string; name: string }[] }) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const [planId, setPlanId] = useState(current.plan_id);
  const [overrides, setOverrides] = useState(() => JSON.stringify(current.overrides ?? {}, null, 2));
  const [note, setNote] = useState(current.note);
  const [jsonError, setJsonError] = useState(false);
  const save = useMutation({
    mutationFn: (body: OrgPlanInput) => putOrgPlan(orgId, body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["usage"] }),
  });
  return (
    <SettingsSection title={t("usage.admin.title")} description={t("usage.admin.description")}>
      <form
        className="grid grid-cols-1 gap-3 md:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault();
          let parsed: OrgPlanInput["overrides"];
          try {
            const v: unknown = JSON.parse(overrides || "{}");
            if (typeof v !== "object" || v === null || Array.isArray(v)) throw new Error("not an object");
            parsed = v as OrgPlanInput["overrides"];
          } catch {
            setJsonError(true);
            return;
          }
          setJsonError(false);
          save.mutate({ plan_id: planId, overrides: parsed, note });
        }}
      >
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-plan`}>{t("usage.admin.plan")}</Label>
          <NativeSelect id={`${id}-plan`} value={planId} onChange={(e) => setPlanId(e.target.value)}>
            {plans.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-note`}>{t("usage.admin.note")}</Label>
          <Input id={`${id}-note`} value={note} maxLength={1000} onChange={(e) => setNote(e.target.value)} />
        </div>
        <div className="flex min-w-0 flex-col gap-1.5 md:col-span-2">
          <Label htmlFor={`${id}-overrides`}>{t("usage.admin.overrides")}</Label>
          <textarea
            id={`${id}-overrides`}
            className="min-h-32 w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-xs"
            value={overrides}
            spellCheck={false}
            onChange={(e) => setOverrides(e.target.value)}
          />
          {jsonError && (
            <p role="alert" className="text-sm text-destructive">
              {t("usage.admin.invalidJson")}
            </p>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-3 md:col-span-2">
          <Button type="submit" disabled={save.isPending}>
            {t("usage.admin.save")}
          </Button>
          {save.isSuccess && <span className="text-sm text-muted-foreground">{t("usage.admin.saved")}</span>}
          <FormError error={save.error} />
        </div>
      </form>
    </SettingsSection>
  );
}
