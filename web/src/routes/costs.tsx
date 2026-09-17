import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { type CostHost, costHostsQuery, type CostService, costServicesQuery, costSummaryQuery, costTrendQuery, formatMoney, formatShare } from "@/api/costs";
import { PageHeader } from "@/components/AppShell";
import { AddDataLink } from "@/components/onboarding/AddDataLink";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { ChartSeriesInput } from "@/lib/series";
import type { RangeSpec } from "@/lib/time";

const route = getRouteApi("/app/costs");

/** The Costs view (cost.md, api.md "Costs"). CostsPage reads the range; this takes it as a prop so it can be rendered on its own. */
export function CostsView({ range }: { range: RangeSpec }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const summary = useQuery(costSummaryQuery(range));
  const hosts = useQuery(costHostsQuery(range));
  const services = useQuery(costServicesQuery(range));
  const trend = useQuery(costTrendQuery(range));

  if (summary.isPending) return <LoadingState />;
  if (summary.isError) return <ErrorState error={summary.error} onRetry={() => void summary.refetch()} />;

  const s = summary.data.summary;
  const pricing = summary.data.pricing;
  const money = (v: number) => formatMoney(v, s.currency || pricing.currency, locale);

  if (s.hosts === 0) {
    return (
      <EmptyState>
        <p>{t("costs.empty")}</p>
        <AddDataLink target="linux" label={t("addData.empty.hosts")} />
      </EmptyState>
    );
  }

  const tiles = [
    { key: "total", label: t("costs.tiles.total"), value: money(s.total), hint: null },
    { key: "perHour", label: t("costs.tiles.perHour"), value: money(s.per_hour), hint: null },
    { key: "idle", label: t("costs.tiles.idle"), value: money(s.idle), hint: t("costs.tiles.idleOf", { percent: formatShare(s.idle_share, locale) }) },
    { key: "services", label: t("costs.tiles.services"), value: money(s.services), hint: null },
  ];

  // The four buckets always add up to the total (cost.md §3), so the bar is an exact decomposition.
  const buckets = [
    { key: "services", value: s.services, label: t("costs.buckets.services"), help: t("costs.buckets.servicesHelp"), className: "bg-primary" },
    { key: "unallocated", value: s.unallocated, label: t("costs.buckets.unallocated"), help: t("costs.buckets.unallocatedHelp"), className: "bg-primary/60" },
    { key: "unattributed", value: s.unattributed, label: t("costs.buckets.unattributed"), help: t("costs.buckets.unattributedHelp"), className: "bg-primary/30" },
    { key: "idle", value: s.idle, label: t("costs.buckets.idle"), help: t("costs.buckets.idleHelp"), className: "bg-muted-foreground/25" },
  ].filter((b) => b.value > 0);

  const points = trend.data?.points ?? [];
  const trendSeries: ChartSeriesInput[] = [
    { label: t("costs.trend.total"), points: points.map((p) => [p.t, p.total] as [number, number]) },
    { label: t("costs.trend.idle"), points: points.map((p) => [p.t, p.idle] as [number, number]) },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {tiles.map((tile) => (
          <div key={tile.key} data-testid={`cost-tile-${tile.key}`} className="rounded-xl border bg-card p-3">
            <div className="text-xs text-muted-foreground">{tile.label}</div>
            <div className="mt-1 text-xl font-semibold tabular-nums">{tile.value}</div>
            {tile.hint && <div className="text-xs text-muted-foreground">{tile.hint}</div>}
          </div>
        ))}
      </div>

      {/* The caveat travels with the numbers: these are list-price estimates, not a bill. */}
      <p className="text-xs text-muted-foreground">
        {t("costs.pricingNote", { version: pricing.version, updated: pricing.updated, note: pricing.note })}
        {pricing.override_file ? ` ${t("costs.override", { file: pricing.override_file })}` : ""}
      </p>
      {s.unpriced_hosts > 0 && <p className="text-xs text-muted-foreground">{t("costs.unpriced", { count: s.unpriced_hosts })}</p>}

      <Card>
        <CardHeader>
          <CardTitle>
            <h2>{t("costs.buckets.title")}</h2>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="flex h-3 w-full overflow-hidden rounded-full bg-muted" role="img" aria-label={buckets.map((b) => `${b.label}: ${money(b.value)}`).join(", ")}>
            {buckets.map((b) => (
              <div key={b.key} className={b.className} style={{ width: `${(b.value / s.total) * 100}%` }} />
            ))}
          </div>
          <ul className="mt-3 flex flex-wrap gap-x-6 gap-y-2">
            {buckets.map((b) => (
              <li key={b.key} className="flex items-start gap-2">
                <span className={`mt-1 size-2.5 shrink-0 rounded-full ${b.className}`} aria-hidden="true" />
                <span className="min-w-0">
                  <span className="text-xs font-medium">{b.label}</span>
                  <span className="ml-2 text-xs tabular-nums text-muted-foreground">{money(b.value)}</span>
                  <span className="block text-xs text-muted-foreground">{b.help}</span>
                </span>
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>
            <h2>{t("costs.trend.title")}</h2>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <TimeSeriesChart
            title={t("costs.trend.title")}
            series={trendSeries}
            unit="number"
            from={trend.data?.from}
            to={trend.data?.to}
            isLoading={trend.isPending}
            error={trend.error}
            onRetry={() => void trend.refetch()}
          />
        </CardContent>
      </Card>

      <div className="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>{t("costs.byService.title")}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="px-0">
            <ServiceTable services={services.data?.services ?? []} money={money} isPending={services.isPending} />
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>
              <h2>{t("costs.byHost.title")}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="px-0">
            <HostTable hosts={hosts.data?.hosts ?? []} money={money} locale={locale} isPending={hosts.isPending} />
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function ServiceTable({ services, money, isPending }: { services: CostService[]; money: (v: number) => string; isPending: boolean }) {
  const { t } = useTranslation();
  if (isPending) return <LoadingState />;
  if (services.length === 0) return <EmptyState>{t("costs.byService.empty")}</EmptyState>;
  return (
    <Table mobile="stack">
      <TableHeader>
        <TableRow>
          <TableHead>{t("costs.columns.service")}</TableHead>
          <TableHead className="hidden md:table-cell">{t("costs.columns.environment")}</TableHead>
          <TableHead className="hidden md:table-cell">{t("costs.columns.containers")}</TableHead>
          <TableHead>{t("costs.columns.cost")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {services.map((s) => (
          <TableRow key={`${s.service_name}|${s.service_namespace}|${s.environment}`}>
            <TableCell className="font-medium">
              <Link
                to="/apm/services/$service"
                params={{ service: s.service_name }}
                search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, ns: s.service_namespace || undefined, env: s.environment || undefined })}
                className="hover:underline"
              >
                {s.service_name}
              </Link>
            </TableCell>
            <TableCell label={t("costs.columns.environment")} className="hidden text-muted-foreground md:table-cell">
              {s.environment || "–"}
            </TableCell>
            <TableCell label={t("costs.columns.containers")} className="hidden tabular-nums md:table-cell">
              {s.containers}
            </TableCell>
            <TableCell label={t("costs.columns.cost")} className="tabular-nums">
              {money(s.total)}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function HostTable({ hosts, money, locale, isPending }: { hosts: CostHost[]; money: (v: number) => string; locale: string; isPending: boolean }) {
  const { t } = useTranslation();
  if (isPending) return <LoadingState />;
  if (hosts.length === 0) return <EmptyState>{t("costs.byHost.empty")}</EmptyState>;
  return (
    <Table mobile="stack">
      <TableHeader>
        <TableRow>
          <TableHead>{t("costs.columns.host")}</TableHead>
          <TableHead className="hidden md:table-cell">{t("costs.columns.instance")}</TableHead>
          <TableHead>{t("costs.columns.cost")}</TableHead>
          <TableHead>{t("costs.columns.idle")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {hosts.map((h) => (
          <TableRow key={h.host_id}>
            <TableCell className="font-medium">
              <Link to="/hosts/$hostId" params={{ hostId: h.host_id }} search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })} className="hover:underline">
                {h.host_name || h.host_id}
              </Link>
            </TableCell>
            <TableCell label={t("costs.columns.instance")} className="hidden font-mono text-xs text-muted-foreground md:table-cell">
              {h.instance_type || (h.priced ? t("costs.source.fallback") : "–")}
              {h.region ? ` · ${h.region}` : ""}
            </TableCell>
            <TableCell label={t("costs.columns.cost")} className="tabular-nums">
              {h.priced ? money(h.total) : <span className="text-muted-foreground">{t("costs.notPriced")}</span>}
            </TableCell>
            <TableCell label={t("costs.columns.idle")} className="tabular-nums">
              {h.priced ? formatShare(h.idle_share, locale) : "–"}
              {h.oversubscribed && (
                <span className="ml-1 text-muted-foreground" title={t("costs.oversubscribed")}>
                  *
                </span>
              )}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export function CostsPage() {
  const { t } = useTranslation();
  const search = route.useSearch();
  return (
    <div>
      <PageHeader title={t("costs.title")} subtitle={t("costs.subtitle")} />
      <CostsView range={{ range: search.range, from: search.from, to: search.to }} />
    </div>
  );
}
