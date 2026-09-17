import { useQueries, useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, BellPlus, CircleAlert, CircleCheck, CircleMinus, CircleX, Cloud, Container, Info, LineChart, Search, Settings2 } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { hostQuery, inventorySearchQuery, metricQuery, servicesQuery, type MetricRequest } from "@/api/queries";
import { can } from "@/api/roles";
import { hostOsOf } from "@/lib/host-os";
import { PageHeader } from "@/components/AppShell";
import { KPIS, type KpiSpec } from "@/components/integrations/kpis";
import {
  IIS_APP_POOL,
  IIS_POOLS_QUERY,
  IIS_SITE,
  IIS_SITE_KEY,
  IIS_SITES_QUERY,
  panelCharts,
  panelResource,
  PG_DATABASE,
  PG_QUERY_ID,
  PG_QUERY_TEXT,
  PG_TABLE,
  type PanelChart,
} from "@/components/integrations/panels";
import { WriteGuardLink } from "@/components/ReadOnly";
import { Badge } from "@/components/ui/badge";
import { TemplateGallery } from "@/components/alerts/TemplateGallery";
import { NativeSelect } from "@/components/ui/native-select";
import { ApplyNotice, HostIntegrationToggle, IntegrationConfigPanel, useApplyState } from "@/components/integrations/IntegrationConfig";
import { isConfigurable } from "@/lib/integration-settings";
import { IntegrationStatusBadge } from "@/components/integrations/StatusBadge";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { DiscoveredService } from "@/api/types";
import { formatBytes, formatDateTime, formatNumber, formatRelative, formatValue } from "@/lib/format";
import {
  attributeValues,
  filterIntegrationRows,
  iisPoolRows,
  INTEGRATION_IDS,
  INTEGRATION_STATUSES,
  instanceAlertSearch,
  instanceLabel,
  instanceResourceFilter,
  integrationForRule,
  integrationOf,
  isIntegrationId,
  needsAttention,
  serviceKey,
  summarizeIntegrations,
  topByLast,
  type IntegrationId,
  type IntegrationRow,
  type IntegrationStatus,
  type InstanceRef,
} from "@/lib/integrations";
import { usePermissions } from "@/lib/org-writable";
import type { ChartSeriesInput } from "@/lib/series";
import type { RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";

const panelRoute = getRouteApi("/app/hosts/$hostId/integrations/$discoveryId/$instance");
const listRoute = getRouteApi("/app/integrations");

// ---- panel ----

/** Status explanation for integrations without remote settings (no form, no snippet). */
function LegacyConfigHelp({ status, error, id }: { status: IntegrationStatus; error?: string; id: string }) {
  const { t } = useTranslation();
  const isError = status === "error";
  return (
    <Notice tone="warning" testId="integration-config-help">
      {isError ? t("integrations.panel.errorTitle") : t("integrations.panel.needsConfigTitle")}
      {error ? `: ${error}. ` : ". "}
      {t("integrations.panel.noHint", { id })}
    </Notice>
  );
}

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

function PanelChartCard({
  chart,
  inst,
  resource,
  range,
  hostName,
  canAlert,
}: {
  chart: PanelChart;
  inst: InstanceRef;
  /** Resource filters of the queries (panelResource: the instance, or one IIS site of it). */
  resource: Record<string, string>;
  range: RangeSpec;
  hostName: string;
  /** The role may create alerts; in a read-only organization the shortcut is disabled with the reason. */
  canAlert: boolean;
}) {
  const { t, i18n } = useTranslation();
  const title = t(`integrations.charts.${chart.id}`);
  const keys = Object.keys(chart.queries);
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
  // Metrics only some setups send (nginx Plus/VTS, Redis Cluster, lock stats) do not leave empty cards behind.
  if (chart.optional && !isLoading && !error && series && series.length === 0) return null;

  return (
    <Card className="min-w-0 gap-2" data-testid="integration-chart">
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle>
          <h3>{title}</h3>
        </CardTitle>
        {canAlert && alertQuery && (
          <WriteGuardLink
            disabled={
              <Button type="button" variant="ghost" size="icon" className="-my-3 size-10" aria-label={`${t("integrations.alerts.fromChart")}: ${title}`}>
                <BellPlus aria-hidden="true" />
              </Button>
            }
          >
            <Link
              to="/alerts/rules/new"
              search={instanceAlertSearch({ metric: alertQuery.name, agg: alertQuery.agg, ref: inst, hostName, name: `${alertQuery.name} on ${hostName}`, site: resource[IIS_SITE_KEY] }) as never}
              aria-label={`${t("integrations.alerts.fromChart")}: ${title}`}
              title={t("integrations.alerts.fromChart")}
              className={buttonVariants({ variant: "ghost", size: "icon", className: "-my-3 size-10" })}
            >
              <BellPlus aria-hidden="true" />
            </Link>
          </WriteGuardLink>
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

/** Top statements by execution time from pg_stat_statements (opt-in `integrations.postgresql.query_stats`). */
function TopQueriesCard({ inst, range }: { inst: InstanceRef; range: RangeSpec }) {
  const { t, i18n } = useTranslation();
  const q = useQuery(
    metricQuery({ hostId: inst.hostId, name: "postgresql.query.total_exec_time", agg: "rate", groupBy: [PG_DATABASE, PG_QUERY_ID, PG_QUERY_TEXT], range, resource: instanceResourceFilter(inst) }),
  );
  const calls = useQuery(
    metricQuery({ hostId: inst.hostId, name: "postgresql.query.calls", agg: "rate", groupBy: [PG_QUERY_ID], range, resource: instanceResourceFilter(inst) }),
  );
  const rows = useMemo(() => topByLast(q.data?.series ?? [], 10), [q.data]);
  const callsById = useMemo(() => new Map(topByLast(calls.data?.series ?? [], 1000).map((r) => [r.labels[PG_QUERY_ID], r.value] as const)), [calls.data]);
  // Not enabled: no card (the agent setting is opt-in).
  if (!q.isPending && !q.isError && rows.length === 0) return null;
  return (
    <Card className="min-w-0 gap-2 lg:col-span-2" data-testid="integration-top-queries">
      <CardHeader>
        <CardTitle>
          <h3>{t("integrations.topQueries.title")}</h3>
        </CardTitle>
        <CardDescription>{t("integrations.topQueries.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("integrations.topQueries.query")}</TableHead>
                <TableHead>{t("integrations.topTables.database")}</TableHead>
                <TableHead className="text-right">{t("integrations.topQueries.time")}</TableHead>
                <TableHead className="text-right">{t("integrations.topQueries.calls")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => {
                const c = callsById.get(r.labels[PG_QUERY_ID]);
                return (
                  <TableRow key={`${r.labels[PG_DATABASE]}/${r.labels[PG_QUERY_ID]}`}>
                    <TableCell className="max-w-xl font-mono text-xs break-all whitespace-normal">{r.labels[PG_QUERY_TEXT] || "–"}</TableCell>
                    <TableCell label={t("integrations.topTables.database")} className="font-mono text-xs">
                      {r.labels[PG_DATABASE] || "–"}
                    </TableCell>
                    <TableCell label={t("integrations.topQueries.time")} className="text-right tabular-nums">
                      {t("integrations.topQueries.msPerSecond", { value: formatNumber(r.value, i18n.resolvedLanguage) })}
                    </TableCell>
                    <TableCell label={t("integrations.topQueries.calls")} className="text-right tabular-nums">
                      {c === undefined ? "–" : t("integrations.topQueries.perSecond", { value: formatNumber(c, i18n.resolvedLanguage) })}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

/** IIS site filter of the panel: all sites, or one site found in the instance's metrics (group_by=resource.iis.site). */
export function IisSiteSelector({ inst, range, value, onChange }: { inst: InstanceRef; range: RangeSpec; value?: string; onChange: (site: string | undefined) => void }) {
  const { t } = useTranslation();
  const uid = useId();
  const q = useQuery(metricQuery({ hostId: inst.hostId, ...IIS_SITES_QUERY, range, resource: instanceResourceFilter(inst) }));
  const sites = useMemo(() => {
    const names = attributeValues(q.data?.series ?? [], IIS_SITE);
    // A site from the URL stays selectable while loading or when it reported nothing in this range.
    return value && !names.includes(value) ? [...names, value].sort((a, b) => a.localeCompare(b)) : names;
  }, [q.data, value]);
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2" data-testid="iis-site-selector">
      <label htmlFor={uid} className="text-sm font-medium">
        {t("integrations.iis.site")}
      </label>
      <NativeSelect id={uid} className="max-w-full min-w-0 sm:max-w-sm" value={value ?? ""} onChange={(e) => onChange(e.target.value || undefined)}>
        <option value="">{t("integrations.iis.allSites")}</option>
        {sites.map((s) => (
          <option key={s} value={s}>
            {s}
          </option>
        ))}
      </NativeSelect>
    </div>
  );
}

const POOL_ICON = { success: CircleCheck, warning: CircleAlert, destructive: CircleX, muted: CircleMinus } as const;

/** Latest state of every IIS application pool of the instance (group_by=resource.iis.application_pool, agg last). */
export function IisAppPoolsCard({ inst, range }: { inst: InstanceRef; range: RangeSpec }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.resolvedLanguage ?? "en";
  const q = useQuery(metricQuery({ hostId: inst.hostId, ...IIS_POOLS_QUERY, range, resource: instanceResourceFilter(inst) }));
  const rows = useMemo(() => iisPoolRows(q.data?.series ?? [], IIS_APP_POOL), [q.data]);
  return (
    <Card className="min-w-0 gap-2 lg:col-span-2" data-testid="iis-app-pools">
      <CardHeader>
        <CardTitle>
          <h3>{t("integrations.iis.pools.title")}</h3>
        </CardTitle>
        <CardDescription>{t("integrations.iis.pools.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState>{t("integrations.iis.pools.empty")}</EmptyState>
        ) : (
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("integrations.iis.pools.pool")}</TableHead>
                <TableHead>{t("integrations.iis.pools.state")}</TableHead>
                <TableHead className="text-right">{t("integrations.iis.pools.lastSeen")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => {
                const Icon = POOL_ICON[r.tone];
                const name = t(`integrations.iis.poolStates.${r.state}`);
                return (
                  <TableRow key={r.pool} data-testid="iis-app-pool">
                    <TableCell className="font-mono text-xs break-all whitespace-normal">{r.pool}</TableCell>
                    <TableCell label={t("integrations.iis.pools.state")}>
                      <Badge variant={r.tone} data-state={r.state}>
                        <Icon aria-hidden="true" />
                        {r.state === "unknown" && r.value !== null ? `${name} (${r.value})` : name}
                      </Badge>
                    </TableCell>
                    <TableCell label={t("integrations.iis.pools.lastSeen")} className="text-right tabular-nums">
                      {r.lastSeen === null ? "–" : <time dateTime={new Date(r.lastSeen).toISOString()} title={formatDateTime(r.lastSeen, lang)}>{formatRelative(r.lastSeen, Math.max(q.dataUpdatedAt, r.lastSeen), lang)}</time>}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function RecommendedAlerts({ id, inst, hostName, instanceName }: { id: IntegrationId; inst: InstanceRef; hostName: string; instanceName: string }) {
  const { t } = useTranslation();
  const titleId = useId();
  const target = useMemo(
    () => ({ hostId: inst.hostId, hostName, discoveryId: inst.discoveryId, instance: inst.instance }),
    [inst.hostId, inst.discoveryId, inst.instance, hostName],
  );
  return (
    <Card className="gap-3" aria-labelledby={titleId} role="region">
      <CardHeader>
        <CardTitle>
          <h2 id={titleId}>{t("integrations.alerts.title")}</h2>
        </CardTitle>
        <CardDescription>{t("integrations.alerts.description", { name: instanceName })}</CardDescription>
      </CardHeader>
      <CardContent>
        <TemplateGallery category="integration" integration={id} target={target} columns="3" />
      </CardContent>
    </Card>
  );
}

/** Switches between instances of the same integration (this host first, then other hosts). */
function InstanceSelector({ integration, current }: { integration: string; current: InstanceRef }) {
  const { t } = useTranslation();
  const uid = useId();
  const navigate = useNavigate();
  const q = useQuery(inventorySearchQuery("discovered_service", ""));
  const rows = useMemo(
    () =>
      summarizeIntegrations(q.data ?? [])
        .rows.filter((r) => r.integration.id === integration && r.panel)
        .sort((a, b) => Number(b.hostId === current.hostId) - Number(a.hostId === current.hostId)),
    [q.data, integration, current.hostId],
  );
  if (rows.length < 2) return null;
  const key = (r: { hostId: string; discoveryId: string; instance: string }) => `${r.hostId}\u0000${r.discoveryId}\u0000${r.instance}`;
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2" data-testid="instance-selector">
      <label htmlFor={uid} className="text-xs text-muted-foreground">
        {t("integrations.panel.switchInstance")}
      </label>
      <NativeSelect
        id={uid}
        className="max-w-full min-w-0 sm:max-w-md"
        value={key(current)}
        onChange={(e) => {
          const r = rows.find((x) => key(x) === e.target.value);
          // The IIS site filter belongs to one instance.
          if (r) void navigate({ to: "/hosts/$hostId/integrations/$discoveryId/$instance", params: { hostId: r.hostId, discoveryId: r.discoveryId, instance: r.instance }, search: (prev) => ({ ...prev, site: undefined }) as never });
        }}
      >
        {rows.map((r) => (
          <option key={key(r)} value={key(r)}>
            {r.hostName} · {instanceLabel(r).primary}
            {r.integration.status !== "enabled" ? ` (${t(`integrations.status.${r.integration.status}`)})` : ""}
          </option>
        ))}
      </NativeSelect>
    </div>
  );
}

/** Docker has no engine metrics (semantic-conventions §6.6): reachability and a link to the host's containers. */
function DockerEngineCard({ hostId, status, error }: { hostId: string; status: IntegrationStatus; error?: string }) {
  const { t } = useTranslation();
  return (
    <Card className="gap-3" data-testid="docker-engine">
      <CardHeader>
        <CardTitle>
          <h2>{t("integrations.docker.title")}</h2>
        </CardTitle>
        <CardDescription>{t(status === "enabled" ? "integrations.docker.reachable" : "integrations.docker.unreachable")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {error && <p className="break-words text-muted-foreground">{error}</p>}
        <p className="text-muted-foreground">{t("integrations.docker.noMetrics")}</p>
        <Link
          to="/hosts/$hostId"
          params={{ hostId }}
          search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, tab: "containers" as const })}
          className={buttonVariants({ variant: "outline", size: "sm", className: "min-h-10 self-start" })}
        >
          <Container aria-hidden="true" />
          {t("integrations.docker.containers")}
        </Link>
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
  const navigate = useNavigate({ from: "/hosts/$hostId/integrations/$discoveryId/$instance" });
  const role = useMe().data?.role;
  const perms = usePermissions();
  // Shown for roles that may create alerts; a read-only organization disables the shortcut (WriteGuardLink).
  const canAlert = can(role, "alerts.write");
  // Every role reads alert templates (writes are checked in the gallery).
  const canReadAlerts = !!role;
  const canManage = perms.can("fleet.manage");
  const apply = useApplyState(hostId, services.data?.snapshot_time);
  const inst: InstanceRef = { hostId, discoveryId, instance };

  if (services.isPending) return <LoadingState />;
  if (services.isError) return <ErrorState error={services.error} onRetry={() => void services.refetch()} />;

  const item = services.data.items.find((it) => it.key === serviceKey(discoveryId, instance));
  const svc = item?.data && typeof item.data === "object" ? (item.data as DiscoveredService) : undefined;
  const integ = integrationOf(svc);
  const id: IntegrationId | undefined = isIntegrationId(integ.id) ? integ.id : item ? undefined : integrationForRule(discoveryId);
  const hostName = host.data?.host_name || hostId;
  // Includes docker, which has no metrics panel but can be switched off per host.
  const configId = integ.id ?? (item ? undefined : integrationForRule(discoveryId));
  const configurable = isConfigurable(configId);
  const name = svc?.name || discoveryId;
  const status: IntegrationStatus = item ? integ.status : "not_available";
  const showCharts = !!id && (!item || integ.status === "enabled");
  const site = id === "iis" ? search.site : undefined;
  // Process name first (e.g. redis-server); the executable path (/usr/bin/redis-check-rdb) stays secondary.
  const label = instanceLabel({ command: svc?.command, instance, displayInstance: svc?.display_instance });

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
        {configId && <div className="mt-2"><InstanceSelector integration={configId} current={inst} /></div>}
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
      {item && configurable && <ApplyNotice phase={apply.phase} />}
      {item && needsAttention(integ.status) && (configurable ? (
        <IntegrationConfigPanel
          hostId={hostId}
          hostName={hostName}
          instance={instance}
          integration={configId}
          status={integ.status}
          error={integ.error}
          hint={integ.hint}
          canManage={canManage}
          onSaved={apply.markSaved}
          os={hostOsOf(host.data)}
        />
      ) : (
        <LegacyConfigHelp status={integ.status} error={integ.error} id={integ.id ?? discoveryId} />
      ))}
      {item && configurable && (
        <HostIntegrationToggle hostId={hostId} hostName={hostName} integration={configId} name={name} canManage={canManage} onSaved={apply.markSaved} />
      )}
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
      {item && configId === "docker" && integ.status !== "not_available" && <DockerEngineCard hostId={hostId} status={integ.status} error={integ.error} />}
      {item && !id && configId !== "docker" && integ.status === "enabled" && <EmptyState>{t("integrations.panel.unsupported")}</EmptyState>}

      {showCharts && id && (
        <>
          {id === "iis" && (
            <IisSiteSelector inst={inst} range={range} value={site} onChange={(s) => void navigate({ search: (prev) => ({ ...prev, site: s }), replace: true })} />
          )}
          {/* 1 column on phones/tablets, 2 columns from 1024px. */}
          <section aria-label={t("integrations.panel.metrics")} className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            {panelCharts(id, site).map((chart) => (
              <PanelChartCard key={chart.id} chart={chart} inst={inst} resource={panelResource(id, inst, site)} range={range} hostName={hostName} canAlert={canAlert} />
            ))}
            {id === "iis" && <IisAppPoolsCard inst={inst} range={range} />}
            {id === "postgresql" && <TopTablesCard inst={inst} range={range} />}
            {id === "postgresql" && <TopQueriesCard inst={inst} range={range} />}
          </section>
          {canReadAlerts && <RecommendedAlerts id={id} inst={inst} hostName={hostName} instanceName={label.primary} />}
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

function KpiValue({ spec, row, range }: { spec: KpiSpec; row: IntegrationRow; range: RangeSpec }) {
  const { t, i18n } = useTranslation();
  const keys = Object.keys(spec.queries);
  const resource = instanceResourceFilter(row);
  const results = useQueries({
    queries: keys.map((k) => {
      const q = spec.queries[k]!;
      return metricQuery({ hostId: row.hostId, name: q.name, agg: q.agg, groupBy: q.groupBy, range, resource });
    }),
  });
  const pending = results.some((r) => r.isPending);
  const failed = results.some((r) => r.isError);
  const value = pending || failed ? null : spec.compute(Object.fromEntries(keys.map((k, i) => [k, results[i]!.data!.series])));
  return (
    <div className="min-w-0">
      <dt className="truncate text-xs text-muted-foreground">{t(`integrations.kpis.${spec.id}`)}</dt>
      <dd className="text-base font-semibold tabular-nums" aria-busy={pending}>
        {pending ? "…" : formatValue(value, spec.unit, i18n.resolvedLanguage)}
      </dd>
    </div>
  );
}

const DASHBOARD_PAGE = 12;

/** Key figures of every instance of one integration, with configuration links for instances that need attention. */
function IntegrationDashboard({ integration, rows, range }: { integration: string; rows: IntegrationRow[]; range: RangeSpec }) {
  const { t } = useTranslation();
  const [limit, setLimit] = useState(DASHBOARD_PAGE);
  const specs = isIntegrationId(integration) ? KPIS[integration] : [];
  const shown = rows.slice(0, limit);
  if (rows.length === 0) return <EmptyState>{t("integrations.dashboard.empty")}</EmptyState>;
  return (
    <section aria-label={t("integrations.dashboard.title", { name: integration })} className="mb-4 flex flex-col gap-3" data-testid="integration-dashboard">
      <ul className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {shown.map((r) => {
          const attention = needsAttention(r.integration.status);
          return (
            <li key={`${r.hostId}/${r.key}`} className="flex min-w-0 flex-col gap-3 rounded-xl border bg-card p-3" data-testid="integration-kpi-card">
              <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
                <div className="min-w-0">
                  <Link to="/hosts/$hostId" params={{ hostId: r.hostId }} className="text-sm font-medium text-primary hover:underline">
                    {r.hostName}
                  </Link>
                  <InstanceName row={r} truncate />
                </div>
                <IntegrationStatusBadge status={r.integration.status} />
              </div>
              {r.integration.status === "enabled" && specs.length > 0 ? (
                <dl className="grid grid-cols-2 gap-2">
                  {specs.map((spec) => (
                    <KpiValue key={spec.id} spec={spec} row={r} range={range} />
                  ))}
                </dl>
              ) : (
                <p className="text-xs break-words text-muted-foreground">
                  {r.integration.error ?? (attention ? t("integrations.dashboard.needsConfig") : r.integration.status === "enabled" ? t("integrations.docker.noMetrics") : t("integrations.panel.notAvailable"))}
                </p>
              )}
              {r.panel && (
                <Link
                  to="/hosts/$hostId/integrations/$discoveryId/$instance"
                  params={{ hostId: r.hostId, discoveryId: r.discoveryId, instance: r.instance }}
                  aria-label={t(attention ? "integrations.dashboard.configureFor" : "integrations.openPanelFor", { name: r.name, host: r.hostName })}
                  className={buttonVariants({ variant: attention ? "default" : "outline", size: "sm", className: "mt-auto min-h-10 self-start" })}
                >
                  {attention ? <Settings2 aria-hidden="true" /> : <LineChart aria-hidden="true" />}
                  {attention ? t("integrations.dashboard.configure") : t("integrations.openPanel")}
                </Link>
              )}
            </li>
          );
        })}
      </ul>
      {rows.length > limit && (
        <Button type="button" variant="outline" className="min-h-10 self-start" onClick={() => setLimit((l) => l + DASHBOARD_PAGE)}>
          {t("integrations.dashboard.more", { count: rows.length - limit })}
        </Button>
      )}
    </section>
  );
}

export function IntegrationsPage() {
  const { t } = useTranslation();
  const search = listRoute.useSearch();
  const navigate = useNavigate({ from: "/integrations" });
  const inputId = useId();
  const query = useQuery(inventorySearchQuery("discovered_service", ""));
  const { rows: allRows, counts: allCounts } = useMemo(() => summarizeIntegrations(query.data ?? []), [query.data]);
  const integrationIds = useMemo(() => [...new Set([...INTEGRATION_IDS, ...allRows.map((r) => r.integration.id!)])].filter((id) => allRows.some((r) => r.integration.id === id)), [allRows]);
  const integration = search.integration && integrationIds.includes(search.integration) ? search.integration : undefined;
  const rows = useMemo(() => (integration ? allRows.filter((r) => r.integration.id === integration) : allRows), [allRows, integration]);
  const counts = useMemo(() => {
    if (!integration) return allCounts;
    const c = { enabled: 0, needs_configuration: 0, error: 0, not_available: 0 };
    for (const r of rows) c[r.integration.status]++;
    return c;
  }, [allCounts, rows, integration]);
  const status = (INTEGRATION_STATUSES as readonly string[]).includes(search.status ?? "") ? (search.status as IntegrationStatus) : undefined;
  const q = search.q ?? "";
  const shown = useMemo(() => filterIntegrationRows(rows, status, q), [rows, status, q]);
  const range: RangeSpec = { range: search.range, from: search.from, to: search.to };
  const setIntegration = (id: string | undefined) => void navigate({ search: (prev) => ({ ...prev, integration: id }), replace: true });
  const setStatus = (s: IntegrationStatus | undefined) => void navigate({ search: (prev) => ({ ...prev, status: s }), replace: true });

  return (
    <div>
      <PageHeader
        title={t("integrations.title")}
        subtitle={t("integrations.subtitle")}
        actions={
          <div className="flex w-full flex-col gap-2 sm:w-auto sm:flex-row sm:items-center">
            {/* Managed cloud services have no agent, so they are configured here rather than discovered (D-135). */}
            <Link
              to="/integrations/cloud"
              title={t("cloud.openDescription")}
              className={buttonVariants({ variant: "outline", className: "min-h-10 justify-center" })}
            >
              <Cloud aria-hidden="true" />
              {t("cloud.open")}
            </Link>
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
          </div>
        }
      />
      {integrationIds.length > 0 && (
        <div role="group" aria-label={t("integrations.dashboard.filterLabel")} className="mb-3 flex flex-wrap gap-2" data-testid="integration-types">
          <Button variant={integration ? "outline" : "secondary"} size="sm" className="min-h-10" aria-pressed={!integration} onClick={() => setIntegration(undefined)}>
            {t("integrations.dashboard.allTypes")} <span className="tabular-nums text-muted-foreground">{allRows.length}</span>
          </Button>
          {integrationIds.map((id) => (
            <Button key={id} variant={integration === id ? "secondary" : "outline"} size="sm" className="min-h-10" aria-pressed={integration === id} onClick={() => setIntegration(integration === id ? undefined : id)} data-integration={id}>
              {t(`integrations.names.${id}`, { defaultValue: id })} <span className="tabular-nums text-muted-foreground">{allRows.filter((r) => r.integration.id === id).length}</span>
            </Button>
          ))}
        </div>
      )}
      {integration && !query.isPending && <IntegrationDashboard key={integration} integration={integration} rows={rows} range={range} />}
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
