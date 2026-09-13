import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, FileText } from "lucide-react";
import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import { containerQuery, containerTimeseriesQuery, type ContainerDetail } from "@/api/containers";
import { ContainerServices } from "@/components/containers/ContainerServices";
import { ContainerStatusBadge } from "@/components/containers/ContainerTable";
import { LogFilters } from "@/components/LogFilters";
import { LogTable } from "@/components/LogTable";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { containerChartSeries, containerImage, containerName } from "@/lib/containers";
import { formatDateTime, formatRelative, type UnitKind } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseTimeParam, type RangeSpec } from "@/lib/time";
import { useLogPages } from "@/lib/use-logs";
import { CONTAINER_TABS, type ContainerDetailSearch, type ContainerTab } from "@/router";

const route = getRouteApi("/app/containers/$containerId");

export function ContainerDetailPage() {
  const { t, i18n } = useTranslation();
  const { containerId } = route.useParams();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/containers/$containerId" });
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  const range: RangeSpec = useMemo(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const detail = useQuery(containerQuery(containerId, range));
  const tab: ContainerTab = search.tab ?? "overview";
  const back = (
    <Link to="/containers" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })} className="mb-2 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-3" aria-hidden="true" />
      {t("containers.detail.back")}
    </Link>
  );

  if (detail.isPending) return <LoadingState />;
  if (detail.isError) {
    if (detail.error instanceof ApiError && (detail.error.status === 404 || detail.error.status === 400)) {
      return (
        <EmptyState>
          <p className="mb-2 font-medium text-foreground">{t("containers.detail.notFound")}</p>
          {back}
        </EmptyState>
      );
    }
    return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  }
  const c = detail.data;
  const seen = parseTimeParam(c.last_seen) ?? 0;
  const started = c.started_at ? parseTimeParam(c.started_at) : null;
  const fields: [string, React.ReactNode][] = [
    [t("containers.detail.fields.id"), <span className="font-mono break-all">{c.container_id}</span>],
    [t("containers.detail.fields.image"), <span className="font-mono break-all">{containerImage(c) || "–"}</span>],
    [t("containers.detail.fields.runtime"), c.runtime || "–"],
    [
      t("containers.detail.fields.host"),
      <Link to="/hosts/$hostId" params={{ hostId: c.host_id }} search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, tab: "containers" as const })} className="text-primary hover:underline">
        {c.host_name || c.host_id}
      </Link>,
    ],
  ];
  if (c.compose_project || c.compose_service) {
    fields.push([
      t("containers.detail.fields.compose"),
      <Link to="/containers" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, project: c.compose_project, group: true })} className="text-primary hover:underline">
        {[c.compose_project, c.compose_service].filter(Boolean).join(" / ")}
      </Link>,
    ]);
  }
  if (c.k8s_pod_name) fields.push([t("containers.detail.fields.k8s"), [c.k8s_namespace_name, c.k8s_pod_name, c.k8s_container_name].filter(Boolean).join(" / ")]);
  if (started) fields.push([t("containers.detail.fields.started"), <time dateTime={c.started_at ?? undefined} title={formatDateTime(started, locale)}>{formatRelative(started, now, locale)}</time>]);
  fields.push([t("containers.detail.fields.restarts"), String(c.restart_count)]);
  if (c.health) fields.push([t("containers.detail.fields.health"), t(`containers.health.${c.health as "healthy"}`, { defaultValue: c.health })]);

  return (
    <div className="flex flex-col gap-4">
      <div>
        {back}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <h1 className="text-xl font-semibold tracking-tight break-all">{containerName(c)}</h1>
          <ContainerStatusBadge container={c} />
          <span className="text-sm text-muted-foreground">
            <time dateTime={c.last_seen} title={formatDateTime(seen, locale)}>
              {t("containers.detail.lastSeen", { when: formatRelative(seen, now, locale) })}
            </time>
          </span>
        </div>
        <dl className="mt-2 flex flex-wrap gap-x-6 gap-y-1 text-xs">
          {fields.map(([k, v]) => (
            <div key={k} className="flex min-w-0 gap-1.5">
              <dt className="shrink-0 text-muted-foreground">{k}</dt>
              <dd className="min-w-0">{v}</dd>
            </div>
          ))}
        </dl>
      </div>
      <Tabs value={tab} onValueChange={(v) => void navigate({ search: (prev) => ({ ...prev, tab: v === "overview" ? undefined : (v as ContainerTab) }) })}>
        <TabsList aria-label={t("containers.detail.tabs.label")}>
          {CONTAINER_TABS.map((k) => (
            <TabsTrigger key={k} value={k}>
              {t(`containers.detail.tabs.${k}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="overview">{tab === "overview" && <ContainerCharts containerId={containerId} range={range} />}</TabsContent>
        <TabsContent value="services">
          {tab === "services" && (
            <div className="rounded-xl border bg-card">
              <ContainerServices containerId={containerId} range={range} />
            </div>
          )}
        </TabsContent>
        <TabsContent value="logs">{tab === "logs" && <ContainerLogs containerId={containerId} range={range} search={search} />}</TabsContent>
        <TabsContent value="attributes">{tab === "attributes" && <ContainerAttributes container={c} />}</TabsContent>
      </Tabs>
    </div>
  );
}

function ContainerCharts({ containerId, range }: { containerId: string; range: RangeSpec }) {
  const { t } = useTranslation();
  const q = useQuery(containerTimeseriesQuery(containerId, range));
  const labels = {
    usage: t("containers.detail.charts.usage"),
    limit: t("containers.detail.charts.limit"),
    receive: t("containers.detail.charts.receive"),
    transmit: t("containers.detail.charts.transmit"),
    read: t("containers.detail.charts.read"),
    write: t("containers.detail.charts.write"),
  };
  const series = q.data ? containerChartSeries(q.data.series, labels) : undefined;
  const charts: { id: "cpu" | "memory" | "network" | "blockio"; unit: UnitKind; yCap?: number }[] = [
    { id: "cpu", unit: "percent", yCap: 1 },
    { id: "memory", unit: "bytes" },
    { id: "network", unit: "bytesPerSec" },
    { id: "blockio", unit: "bytesPerSec" },
  ];
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      {charts.map((c) => {
        const title = t(`containers.detail.charts.${c.id}`);
        return (
          <Card key={c.id} className="min-w-0 gap-2">
            <CardHeader>
              <CardTitle>
                <h2>{title}</h2>
              </CardTitle>
            </CardHeader>
            <CardContent>
              <TimeSeriesChart
                title={title}
                series={series?.[c.id]}
                unit={c.unit}
                yCap={c.yCap}
                from={q.data?.from}
                to={q.data?.to}
                isLoading={q.isPending}
                error={q.error ?? undefined}
                onRetry={() => void q.refetch()}
              />
            </CardContent>
          </Card>
        );
      })}
    </div>
  );
}

function ContainerLogs({ containerId, range, search }: { containerId: string; range: RangeSpec; search: ContainerDetailSearch }) {
  const { t } = useTranslation();
  const navigate = useNavigate({ from: "/containers/$containerId" });
  const streamId = useId();
  const { query, logs } = useLogPages({ range, containerId, q: search.lq, severity: search.severity, attrs: { stream: search.stream } });
  const value = useMemo(() => ({ q: search.lq ?? "", severity: search.severity ?? "", service: "", host: "" }), [search.lq, search.severity]);
  return (
    <section className="flex flex-col gap-3" aria-label={t("containers.detail.tabs.logs")}>
      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-0 flex-1">
          <LogFilters
            value={value}
            showHost={false}
            showService={false}
            onApply={(v) => void navigate({ search: (prev) => ({ ...prev, lq: v.q || undefined, severity: v.severity || undefined }) })}
          />
        </div>
        <div className="flex flex-col gap-1">
          <label htmlFor={streamId} className="text-xs text-muted-foreground">
            {t("containers.detail.streamLabel")}
          </label>
          <NativeSelect
            id={streamId}
            value={search.stream ?? ""}
            onChange={(e) => void navigate({ search: (prev) => ({ ...prev, stream: (e.target.value || undefined) as ContainerDetailSearch["stream"] }), replace: true })}
          >
            <option value="">{t("containers.detail.anyStream")}</option>
            <option value="stdout">stdout</option>
            <option value="stderr">stderr</option>
          </NativeSelect>
        </div>
      </div>
      <div className="overflow-hidden rounded-xl border bg-card">
        {query.isPending ? (
          <LoadingState />
        ) : query.isError && !logs ? (
          <ErrorState error={query.error} onRetry={() => void query.refetch()} />
        ) : !logs || logs.length === 0 ? (
          <EmptyState icon={<FileText className="size-5" aria-hidden="true" />} className="px-4">
            {search.lq || search.severity || search.stream ? t("logs.empty") : t("containers.detail.logsEmpty")}
          </EmptyState>
        ) : (
          <LogTable logs={logs} showHost={false} hasMore={query.hasNextPage} loadingMore={query.isFetchingNextPage} onLoadMore={() => void query.fetchNextPage()} />
        )}
      </div>
    </section>
  );
}

function ContainerAttributes({ container }: { container: ContainerDetail }) {
  const { t } = useTranslation();
  const entries = Object.entries(container.attributes).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return <EmptyState>{t("containers.detail.attributesEmpty")}</EmptyState>;
  return (
    <dl className="grid grid-cols-1 gap-x-4 gap-y-1 rounded-xl border bg-card p-4 text-xs sm:grid-cols-[minmax(10rem,max-content)_1fr]" data-testid="container-attributes">
      {entries.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="font-mono text-muted-foreground">{k}</dt>
          <dd className="mb-1 font-mono break-all sm:mb-0">{v}</dd>
        </div>
      ))}
    </dl>
  );
}
