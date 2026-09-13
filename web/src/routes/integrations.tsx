import { useQueries, useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, BellPlus, CircleAlert, CircleX, Copy, Info, LineChart, Search } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { hostQuery, inventorySearchQuery, metricQuery, servicesQuery, type MetricRequest } from "@/api/queries";
import { can } from "@/api/roles";
import { PageHeader } from "@/components/AppShell";
import { PANELS, PG_DATABASE, PG_TABLE, type PanelChart } from "@/components/integrations/panels";
import { IntegrationStatusBadge } from "@/components/integrations/StatusBadge";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { DiscoveredService } from "@/api/types";
import { formatBytes, formatValue } from "@/lib/format";
import {
  filterIntegrationRows,
  INTEGRATION_STATUSES,
  instanceAlertSearch,
  instanceLabel,
  instanceResourceFilter,
  integrationForRule,
  integrationOf,
  isIntegrationId,
  latestMax,
  needsAttention,
  presetsFor,
  presetSearch,
  serviceKey,
  summarizeIntegrations,
  topByLast,
  type IntegrationId,
  type IntegrationRow,
  type IntegrationStatus,
  type InstanceRef,
} from "@/lib/integrations";
import type { ChartSeriesInput } from "@/lib/series";
import type { RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";

const panelRoute = getRouteApi("/app/hosts/$hostId/integrations/$discoveryId/$instance");
const listRoute = getRouteApi("/app/integrations");

// ---- panel ----

function Notice({ tone, children, testId }: { tone: "info" | "warning"; children: React.ReactNode; testId?: string }) {
  const Icon = tone === "warning" ? CircleAlert : Info;
  return (
    <div
      data-testid={testId}
      className={cn("flex items-start gap-2 rounded-lg border p-3 text-sm", tone === "warning" ? "border-warning/50 bg-warning/10" : "bg-muted/50 text-muted-foreground")}
    >
      <Icon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <p className="min-w-0 break-words">{children}</p>
    </div>
  );
}

function ConfigHelp({ status, error, hint, id, hostName }: { status: IntegrationStatus; error?: string; hint?: string; id: string; hostName: string }) {
  const { t } = useTranslation();
  const titleId = useId();
  const [copy, setCopy] = useState<"idle" | "copied" | "failed">("idle");
  const isError = status === "error";
  const Icon = isError ? CircleX : CircleAlert;
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(hint ?? "");
      setCopy("copied");
    } catch {
      setCopy("failed");
    }
  };
  return (
    <section
      aria-labelledby={titleId}
      data-testid="integration-config-help"
      className={cn("rounded-xl border p-4", isError ? "border-destructive/40 bg-destructive/10" : "border-warning/50 bg-warning/10")}
    >
      <h2 id={titleId} className="flex items-center gap-2 text-base font-semibold">
        <Icon className={cn("size-4", isError && "text-destructive-text")} aria-hidden="true" />
        {isError ? t("integrations.panel.errorTitle") : t("integrations.panel.needsConfigTitle")}
      </h2>
      {error && <p className="mt-2 font-mono text-sm break-words">{error}</p>}
      {hint ? (
        <>
          <p className="mt-3 text-sm">{t("integrations.panel.hintIntro", { host: hostName })}</p>
          <div className="mt-2 overflow-hidden rounded-md border bg-card">
            <div className="flex items-center justify-between gap-2 border-b px-3 py-1.5">
              <span className="text-xs text-muted-foreground">{t("integrations.panel.hintLabel")}</span>
              <Button variant="ghost" size="sm" className="min-h-10" onClick={() => void onCopy()}>
                <Copy aria-hidden="true" />
                {copy === "copied" ? t("integrations.panel.copied") : t("integrations.panel.copy")}
              </Button>
            </div>
            <pre className="overflow-x-auto p-3 text-xs leading-relaxed" aria-label={t("integrations.panel.hintLabel")}>
              <code>{hint}</code>
            </pre>
          </div>
          <p className="sr-only" aria-live="polite">
            {copy === "copied" ? t("integrations.panel.copied") : copy === "failed" ? t("integrations.panel.copyFailed") : ""}
          </p>
          {copy === "failed" && <p className="mt-1 text-xs text-destructive-text">{t("integrations.panel.copyFailed")}</p>}
        </>
      ) : (
        <p className="mt-3 text-sm">{t("integrations.panel.noHint", { id })}</p>
      )}
    </section>
  );
}

function PanelChartCard({ chart, inst, range, hostName, canAlert }: { chart: PanelChart; inst: InstanceRef; range: RangeSpec; hostName: string; canAlert: boolean }) {
  const { t, i18n } = useTranslation();
  const title = t(`integrations.charts.${chart.id}`);
  const keys = Object.keys(chart.queries);
  const resource = instanceResourceFilter(inst);
  const results = useQueries({
    queries: keys.map((k) => {
      const q = chart.queries[k]!;
      return metricQuery({ hostId: inst.hostId, name: q.name, agg: q.agg, groupBy: q.groupBy, range, resource } satisfies MetricRequest);
    }),
  });
  const isLoading = results.some((r) => r.isPending);
  const error = results.find((r) => r.isError)?.error;
  const dataKey = results.map((r) => r.dataUpdatedAt).join(",");
  const series = useMemo<ChartSeriesInput[] | undefined>(() => {
    if (results.some((r) => !r.data)) return undefined;
    const data = Object.fromEntries(keys.map((k, i) => [k, results[i]!.data!.series]));
    return chart.build(data, (k) => t(`integrations.series.${k}`));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataKey, chart, i18n.resolvedLanguage]);
  const first = results[0]?.data;
  const alertQuery = chart.alert ? chart.queries[chart.alert] : undefined;

  return (
    <Card className="min-w-0 gap-2" data-testid="integration-chart">
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle>
          <h3>{title}</h3>
        </CardTitle>
        {canAlert && alertQuery && (
          <Link
            to="/alerts/rules/new"
            search={instanceAlertSearch({ metric: alertQuery.name, agg: alertQuery.agg, ref: inst, hostName, name: `${alertQuery.name} on ${hostName}` }) as never}
            aria-label={`${t("integrations.alerts.fromChart")}: ${title}`}
            title={t("integrations.alerts.fromChart")}
            className={buttonVariants({ variant: "ghost", size: "icon", className: "-my-3 size-10" })}
          >
            <BellPlus aria-hidden="true" />
          </Link>
        )}
      </CardHeader>
      <CardContent>
        <TimeSeriesChart
          title={title}
          series={series}
          unit={chart.unit}
          stacked={chart.stacked}
          order={chart.order}
          yMax={chart.yMax}
          from={first?.from}
          to={first?.to}
          isLoading={isLoading}
          error={error}
          onRetry={() => results.forEach((r) => void r.refetch())}
        />
      </CardContent>
    </Card>
  );
}

function TopTablesCard({ inst, range }: { inst: InstanceRef; range: RangeSpec }) {
  const { t } = useTranslation();
  const q = useQuery(
    metricQuery({ hostId: inst.hostId, name: "postgresql.table.size", agg: "last", groupBy: [PG_DATABASE, PG_TABLE], range, resource: instanceResourceFilter(inst) }),
  );
  const rows = useMemo(() => topByLast(q.data?.series ?? [], 10), [q.data]);
  return (
    <Card className="min-w-0 gap-2" data-testid="integration-top-tables">
      <CardHeader>
        <CardTitle>
          <h3>{t("integrations.topTables.title")}</h3>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState>{t("integrations.topTables.empty")}</EmptyState>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("integrations.topTables.table")}</TableHead>
                <TableHead>{t("integrations.topTables.database")}</TableHead>
                <TableHead className="text-right">{t("integrations.topTables.size")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={`${r.labels[PG_DATABASE]}/${r.labels[PG_TABLE]}`}>
                  <TableCell className="font-mono text-xs">{r.labels[PG_TABLE] || "–"}</TableCell>
                  <TableCell className="font-mono text-xs">{r.labels[PG_DATABASE] || "–"}</TableCell>
                  <TableCell className="text-right tabular-nums">{formatBytes(r.value)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function RecommendedAlerts({ id, inst, range, hostName, instanceName }: { id: IntegrationId; inst: InstanceRef; range: RangeSpec; hostName: string; instanceName: string }) {
  const { t, i18n } = useTranslation();
  const presets = presetsFor(id);
  const refMetrics = [...new Set(presets.flatMap((p) => (typeof p.threshold === "number" ? [] : [p.threshold.ratioOf])))];
  const refs = useQueries({
    queries: refMetrics.map((name) => metricQuery({ hostId: inst.hostId, name, agg: "last", range, resource: instanceResourceFilter(inst) })),
  });
  const titleId = useId();
  if (presets.length === 0) return null;
  return (
    <Card className="gap-3" aria-labelledby={titleId} role="region">
      <CardHeader>
        <CardTitle>
          <h2 id={titleId}>{t("integrations.alerts.title")}</h2>
        </CardTitle>
        <CardDescription>{t("integrations.alerts.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <ul className="grid grid-cols-1 gap-2 md:grid-cols-2 xl:grid-cols-4">
          {presets.map((p) => {
            const title = t(`integrations.alerts.presets.${p.id}.title`);
            const refMetric = typeof p.threshold === "number" ? undefined : p.threshold.ratioOf;
            const reference = refMetric ? latestMax(refs[refMetrics.indexOf(refMetric)]?.data?.series) : null;
            const s = presetSearch(p, inst, { name: `${title} – ${hostName} (${instanceName})`, hostName, reference });
            // Ratio thresholds show the resolved value, e.g. " (921.6 MiB)"; empty when unavailable.
            const threshold = refMetric && s?.threshold ? ` (${formatValue(Number(s.threshold), p.metric.startsWith("redis.memory") ? "bytes" : "number", i18n.resolvedLanguage)})` : "";
            return (
              <li key={p.id} className="flex flex-col justify-between gap-2 rounded-lg border p-3" data-testid="alert-preset">
                <div>
                  <p className="text-sm font-medium">{title}</p>
                  <p className="text-xs text-muted-foreground">{t(`integrations.alerts.presets.${p.id}.body`, { threshold })}</p>
                </div>
                {s ? (
                  <Link
                    to="/alerts/rules/new"
                    search={s as never}
                    aria-label={t("integrations.alerts.createFor", { title })}
                    className={buttonVariants({ variant: "outline", size: "sm", className: "min-h-10 self-start" })}
                  >
                    <BellPlus aria-hidden="true" />
                    {t("integrations.alerts.create")}
                  </Link>
                ) : (
                  <p className="text-xs text-muted-foreground">{t("integrations.alerts.unavailable", { metric: refMetric ?? p.metric })}</p>
                )}
              </li>
            );
          })}
        </ul>
      </CardContent>
    </Card>
  );
}

export function HostIntegrationPage() {
  const { t } = useTranslation();
  const { hostId, discoveryId, instance } = panelRoute.useParams();
  const search = panelRoute.useSearch();
  const range: RangeSpec = { range: search.range, from: search.from, to: search.to };
  const host = useQuery(hostQuery(hostId));
  const services = useQuery(servicesQuery(hostId));
  const canAlert = can(useMe().data?.role, "alerts.write");
  const inst: InstanceRef = { hostId, discoveryId, instance };

  if (services.isPending) return <LoadingState />;
  if (services.isError) return <ErrorState error={services.error} onRetry={() => void services.refetch()} />;

  const item = services.data.items.find((it) => it.key === serviceKey(discoveryId, instance));
  const svc = item?.data && typeof item.data === "object" ? (item.data as DiscoveredService) : undefined;
  const integ = integrationOf(svc);
  const id: IntegrationId | undefined = isIntegrationId(integ.id) ? integ.id : item ? undefined : integrationForRule(discoveryId);
  const hostName = host.data?.host_name || hostId;
  const name = svc?.name || discoveryId;
  const status: IntegrationStatus = item ? integ.status : "not_available";
  const showCharts = !!id && (!item || integ.status === "enabled");
  // Process name first (e.g. redis-server); the executable path (/usr/bin/redis-check-rdb) stays secondary.
  const label = instanceLabel({ command: svc?.command, instance });

  return (
    <div className="flex flex-col gap-4">
      <div>
        <Link
          to="/hosts/$hostId"
          params={{ hostId }}
          search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, tab: "services" as const })}
          className="mb-2 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3" aria-hidden="true" />
          {t("integrations.panel.back", { host: hostName })}
        </Link>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <h1 className="text-xl font-semibold tracking-tight">{t("integrations.panel.title", { name })}</h1>
          {item && <IntegrationStatusBadge status={status} />}
        </div>
        {label.secondary && (
          <p className="mt-1 truncate font-mono text-sm font-medium" title={`${t("integrations.panel.command")}: ${label.primary}`} data-testid="integration-command">
            <span className="sr-only">{t("integrations.panel.command")}: </span>
            {label.primary}
          </p>
        )}
        <dl className="mt-2 flex flex-wrap gap-x-6 gap-y-1 text-xs">
          <div className="flex gap-1.5">
            <dt className="text-muted-foreground">{t("integrations.panel.host")}</dt>
            <dd>
              <Link to="/hosts/$hostId" params={{ hostId }} className="text-primary hover:underline">
                {hostName}
              </Link>
            </dd>
          </div>
          <div className="flex min-w-0 max-w-full gap-1.5">
            <dt className="shrink-0 text-muted-foreground">{t("integrations.panel.instance")}</dt>
            <dd className={cn("min-w-0 truncate font-mono", label.secondary && "text-muted-foreground")} title={instance}>
              {instance}
            </dd>
          </div>
          {integ.endpoint && (
            <div className="flex min-w-0 max-w-full gap-1.5">
              <dt className="shrink-0 text-muted-foreground">{t("integrations.panel.endpoint")}</dt>
              <dd className="min-w-0 font-mono break-all">{integ.endpoint}</dd>
            </div>
          )}
          {svc?.version && (
            <div className="flex gap-1.5">
              <dt className="text-muted-foreground">{t("integrations.panel.version")}</dt>
              <dd className="font-mono">{svc.version}</dd>
            </div>
          )}
        </dl>
      </div>

      {!item && <Notice tone="info">{t("integrations.panel.notInSnapshot")}</Notice>}
      {item && needsAttention(integ.status) && <ConfigHelp status={integ.status} error={integ.error} hint={integ.hint} id={integ.id ?? discoveryId} hostName={hostName} />}
      {item && integ.status === "enabled" && integ.error && (
        <Notice tone="warning" testId="integration-partial">
          {t("integrations.panel.partial", { error: integ.error })}
        </Notice>
      )}
      {item && integ.status === "not_available" && (
        <Notice tone="info">
          {t("integrations.panel.notAvailable")}
          {integ.error ? ` ${integ.error}` : ""}
        </Notice>
      )}
      {item && !id && integ.status === "enabled" && <EmptyState>{t("integrations.panel.unsupported")}</EmptyState>}

      {showCharts && id && (
        <>
          {canAlert && <RecommendedAlerts id={id} inst={inst} range={range} hostName={hostName} instanceName={label.primary} />}
          {/* 1 column on phones/tablets, 2 columns from 1024px. */}
          <section aria-label={t("integrations.panel.metrics")} className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            {PANELS[id].map((chart) => (
              <PanelChartCard key={chart.id} chart={chart} inst={inst} range={range} hostName={hostName} canAlert={canAlert} />
            ))}
            {id === "postgresql" && <TopTablesCard inst={inst} range={range} />}
          </section>
        </>
      )}
    </div>
  );
}

// ---- overview ----

/** Command as the name with the executable path below it; just the path when the agent sent no command. */
function InstanceName({ row, truncate }: { row: IntegrationRow; truncate?: boolean }) {
  const { t } = useTranslation();
  const label = instanceLabel(row);
  const wrap = truncate ? "truncate" : "break-all";
  return (
    <div className="min-w-0 font-mono text-xs" title={row.instance}>
      <p className={cn(wrap, label.secondary ? "font-medium text-foreground" : "text-muted-foreground")}>
        {label.secondary && <span className="sr-only">{t("integrations.panel.command")}: </span>}
        {label.primary}
      </p>
      {label.secondary && (
        <p className={cn(wrap, "text-muted-foreground")}>
          <span className="sr-only">{t("integrations.panel.instance")}: </span>
          {label.secondary}
        </p>
      )}
    </div>
  );
}

export function IntegrationsPage() {
  const { t } = useTranslation();
  const search = listRoute.useSearch();
  const navigate = useNavigate({ from: "/integrations" });
  const inputId = useId();
  const query = useQuery(inventorySearchQuery("discovered_service", ""));
  const { rows, counts } = useMemo(() => summarizeIntegrations(query.data ?? []), [query.data]);
  const status = (INTEGRATION_STATUSES as readonly string[]).includes(search.status ?? "") ? (search.status as IntegrationStatus) : undefined;
  const q = search.q ?? "";
  const shown = useMemo(() => filterIntegrationRows(rows, status, q), [rows, status, q]);
  const setStatus = (s: IntegrationStatus | undefined) => void navigate({ search: (prev) => ({ ...prev, status: s }), replace: true });

  return (
    <div>
      <PageHeader
        title={t("integrations.title")}
        subtitle={t("integrations.subtitle")}
        actions={
          <div className="relative w-full sm:w-72">
            <label htmlFor={inputId} className="sr-only">
              {t("integrations.searchLabel")}
            </label>
            <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden="true" />
            <Input
              id={inputId}
              type="search"
              className="pl-8"
              placeholder={t("integrations.searchPlaceholder")}
              value={q}
              onChange={(e) => void navigate({ search: (prev) => ({ ...prev, q: e.target.value || undefined }), replace: true })}
            />
          </div>
        }
      />
      <div role="group" aria-label={t("integrations.filterLabel")} className="mb-3 flex flex-wrap gap-2" data-testid="integration-counts">
        <Button variant={status ? "outline" : "secondary"} size="sm" className="min-h-10" aria-pressed={!status} onClick={() => setStatus(undefined)}>
          {t("integrations.all")} <span className="tabular-nums text-muted-foreground">{rows.length}</span>
        </Button>
        {(["enabled", "needs_configuration", "error", "not_available"] as const).map((s) => (
          <Button key={s} variant={status === s ? "secondary" : "outline"} size="sm" className="min-h-10" aria-pressed={status === s} onClick={() => setStatus(status === s ? undefined : s)} data-status={s}>
            {t(`integrations.status.${s}`)} <span className="tabular-nums text-muted-foreground">{counts[s]}</span>
          </Button>
        ))}
      </div>
      <div className="rounded-xl border bg-card">
        {query.isPending ? (
          <LoadingState />
        ) : query.isError ? (
          <ErrorState error={query.error} onRetry={() => void query.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState>{t("integrations.empty")}</EmptyState>
        ) : shown.length === 0 ? (
          <EmptyState>{t("integrations.noMatch")}</EmptyState>
        ) : (
          <>
          {/* Phones: stacked cards; from 768px: table. */}
          <ul className="divide-y md:hidden">
            {shown.map((r) => (
              <li key={`${r.hostId}/${r.key}`} className="flex flex-col gap-2 p-3" data-testid="integration-card">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="min-w-0">
                    <span className="font-medium">{r.name}</span> <span className="text-xs text-muted-foreground">{r.integration.id}</span>
                  </span>
                  <IntegrationStatusBadge status={r.integration.status} />
                </div>
                <Link to="/hosts/$hostId" params={{ hostId: r.hostId }} className="inline-flex min-h-10 items-center self-start text-sm text-primary hover:underline">
                  {r.hostName}
                </Link>
                <InstanceName row={r} />
                {(r.integration.error || r.integration.endpoint) && (
                  <p className={cn("text-xs break-words", r.integration.status === "enabled" && "font-mono")}>
                    {r.integration.status === "enabled" ? r.integration.endpoint : r.integration.error}
                  </p>
                )}
                {r.panel && (
                  <Link
                    to="/hosts/$hostId/integrations/$discoveryId/$instance"
                    params={{ hostId: r.hostId, discoveryId: r.discoveryId, instance: r.instance }}
                    aria-label={t("integrations.openPanelFor", { name: r.name, host: r.hostName })}
                    className={buttonVariants({ variant: "outline", size: "sm", className: "min-h-10 self-start" })}
                  >
                    <LineChart aria-hidden="true" />
                    {t("integrations.openPanel")}
                  </Link>
                )}
              </li>
            ))}
          </ul>
          <div className="hidden md:block">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("integrations.columns.host")}</TableHead>
                <TableHead>{t("integrations.columns.service")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("integrations.columns.instance")}</TableHead>
                <TableHead>{t("integrations.columns.status")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("integrations.columns.details")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("integrations.openPanel")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {shown.map((r) => (
                <TableRow key={`${r.hostId}/${r.key}`} data-testid="integration-row">
                  <TableCell>
                    <Link to="/hosts/$hostId" params={{ hostId: r.hostId }} className="font-medium text-primary hover:underline">
                      {r.hostName}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <span className="font-medium">{r.name}</span> <span className="text-xs text-muted-foreground">{r.integration.id}</span>
                  </TableCell>
                  <TableCell className="hidden max-w-64 md:table-cell">
                    <InstanceName row={r} truncate />
                  </TableCell>
                  <TableCell>
                    <IntegrationStatusBadge status={r.integration.status} />
                  </TableCell>
                  <TableCell className="hidden max-w-96 truncate text-xs lg:table-cell" title={r.integration.error ?? r.integration.endpoint}>
                    {r.integration.status === "enabled" ? <span className="font-mono">{r.integration.endpoint ?? "–"}</span> : (r.integration.error ?? "–")}
                  </TableCell>
                  <TableCell className="text-right">
                    {r.panel && (
                      <Link
                        to="/hosts/$hostId/integrations/$discoveryId/$instance"
                        params={{ hostId: r.hostId, discoveryId: r.discoveryId, instance: r.instance }}
                        aria-label={t("integrations.openPanelFor", { name: r.name, host: r.hostName })}
                        className={buttonVariants({ variant: "outline", size: "sm", className: "min-h-10" })}
                      >
                        <LineChart aria-hidden="true" />
                        {t("integrations.openPanel")}
                      </Link>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          </div>
          </>
        )}
      </div>
    </div>
  );
}
