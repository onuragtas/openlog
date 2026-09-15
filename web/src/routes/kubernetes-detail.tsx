import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, FileText } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import {
  kubernetesEventsQuery,
  kubernetesPodEventsQuery,
  kubernetesPodQuery,
  kubernetesPodTimeseriesQuery,
  kubernetesWorkloadQuery,
  kubernetesWorkloadTimeseriesQuery,
  type WorkloadRef,
} from "@/api/kubernetes";
import { EventList } from "@/components/kubernetes/EventList";
import { PodContainers } from "@/components/kubernetes/PodContainers";
import { PodStatusBadge, WorkloadHealthBadge } from "@/components/kubernetes/K8sBadges";
import { PodTable, WorkloadLink } from "@/components/kubernetes/KubernetesTables";
import type { QueryFilter } from "@/api/explorer";
import { EmbeddedLogsExplorer } from "@/components/logs-explorer/EmbeddedLogsExplorer";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime, formatRelative, type UnitKind } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { podChartSeries, rangeOnly, replicasText, timeseriesBounds, workloadChartSeries } from "@/lib/kubernetes";
import type { ChartSeriesInput } from "@/lib/series";
import { parseTimeParam, type RangeSpec } from "@/lib/time";
import { K8S_POD_TABS, type K8sPodTab, type KubernetesPodSearch } from "@/router";

const workloadRoute = getRouteApi("/app/kubernetes/workloads/$clusterUid/$namespace/$kind/$name");
const podRoute = getRouteApi("/app/kubernetes/pods/$podUid");

function BackLink({ to, label }: { to: "/kubernetes/workloads" | "/kubernetes/pods"; label: string }) {
  return (
    <Link to={to} search={rangeOnly} className="mb-2 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-3" aria-hidden="true" />
      {label}
    </Link>
  );
}

function When({ at }: { at: string | null }) {
  const { i18n } = useTranslation();
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  const ms = at ? parseTimeParam(at) : null;
  if (ms == null) return <>–</>;
  return (
    <time dateTime={at ?? undefined} title={formatDateTime(ms, locale)}>
      {formatRelative(ms, now, locale)}
    </time>
  );
}

function Fields({ fields, testId }: { fields: [string, React.ReactNode][]; testId?: string }) {
  return (
    <dl className="mt-2 flex flex-wrap gap-x-6 gap-y-1 text-xs" data-testid={testId}>
      {fields.map(([k, v]) => (
        <div key={k} className="flex min-w-0 gap-1.5">
          <dt className="shrink-0 text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-all">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

interface ChartSpec {
  id: string;
  title: string;
  unit: UnitKind;
}

function ChartGrid({ charts, series, q }: { charts: ChartSpec[]; series: Record<string, ChartSeriesInput[]> | undefined; q: { data?: { from: number | string; to: number | string; step: string; series: object }; isPending: boolean; error: unknown; refetch: () => unknown } }) {
  const bounds = timeseriesBounds(q.data as never);
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      {charts.map((c) => (
        <Card key={c.id} className="min-w-0 gap-2">
          <CardHeader>
            <CardTitle>
              <h2>{c.title}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <TimeSeriesChart
              title={c.title}
              series={series?.[c.id]}
              unit={c.unit}
              from={bounds.from}
              to={bounds.to}
              isLoading={q.isPending}
              error={q.error ?? undefined}
              onRetry={() => void q.refetch()}
            />
          </CardContent>
        </Card>
      ))}
    </div>
  );
}

function NotFound({ text, back }: { text: string; back: React.ReactNode }) {
  return (
    <EmptyState>
      <p className="mb-2 font-medium text-foreground">{text}</p>
      {back}
    </EmptyState>
  );
}

const isNotFound = (e: unknown) => e instanceof ApiError && (e.status === 404 || e.status === 400);

export function KubernetesWorkloadPage() {
  const { t } = useTranslation();
  const params = workloadRoute.useParams();
  const search = workloadRoute.useSearch();
  const range: RangeSpec = useMemo(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const ref: WorkloadRef = { clusterUid: params.clusterUid, namespace: params.namespace, kind: params.kind, name: params.name };
  const detail = useQuery(kubernetesWorkloadQuery(ref, range));
  const ts = useQuery(kubernetesWorkloadTimeseriesQuery(ref, range));
  const events = useQuery(kubernetesEventsQuery(range, { clusterUid: ref.clusterUid, namespace: ref.namespace, objectKind: ref.kind, objectName: ref.name }));
  const back = <BackLink to="/kubernetes/workloads" label={t("kubernetes.workload.back")} />;

  if (detail.isPending) return <LoadingState />;
  if (detail.isError) {
    if (isNotFound(detail.error)) return <NotFound text={t("kubernetes.workload.notFound")} back={back} />;
    return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  }
  const w = detail.data;
  const fields: [string, React.ReactNode][] = [
    [t("kubernetes.columns.cluster"), w.cluster_name || w.cluster_uid],
    [t("kubernetes.columns.namespace"), w.namespace],
  ];
  if (w.kind !== "CronJob") {
    fields.push([t("kubernetes.workload.fields.replicas"), <span className="font-mono">{replicasText(w.ready, w.desired)}</span>]);
    fields.push([w.kind === "Job" ? t("kubernetes.workload.fields.succeeded") : t("kubernetes.workload.fields.available"), <span className="font-mono">{w.available}</span>]);
    fields.push([w.kind === "Job" ? t("kubernetes.workload.fields.failed") : t("kubernetes.workload.fields.updated"), <span className="font-mono">{w.updated}</span>]);
  }
  fields.push([t("kubernetes.columns.restarts"), <span className="font-mono">{w.restarts}</span>]);
  if (w.hpa) {
    fields.push([
      t("kubernetes.workload.fields.hpa"),
      t("kubernetes.workload.hpaValue", { name: w.hpa.name, current: w.hpa.current_replicas ?? "–", desired: w.hpa.desired_replicas ?? "–", min: w.hpa.min_replicas ?? "–", max: w.hpa.max_replicas ?? "–" }),
    ]);
  }
  fields.push([t("kubernetes.columns.age"), <When at={w.created_at} />]);

  const labels = {
    cpu: t("kubernetes.charts.cpu"),
    memory: t("kubernetes.charts.memory"),
    ready: t("kubernetes.charts.ready"),
    desired: t("kubernetes.charts.desired"),
    restarts: t("kubernetes.charts.restarts"),
  };
  const series = ts.data ? workloadChartSeries(ts.data.series, labels) : undefined;
  const charts: ChartSpec[] = [
    { id: "cpu", title: t("kubernetes.charts.cpuCores"), unit: "number" },
    { id: "memory", title: labels.memory, unit: "bytes" },
    { id: "replicas", title: t("kubernetes.charts.replicas"), unit: "number" },
    { id: "restarts", title: labels.restarts, unit: "number" },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div>
        {back}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <h1 className="text-xl font-semibold tracking-tight break-all">{w.name}</h1>
          <Badge variant="outline">{w.kind}</Badge>
          <WorkloadHealthBadge health={w.health} />
          {!w.reporting && <Badge variant="muted">{t("kubernetes.status.notReporting")}</Badge>}
        </div>
        <Fields fields={fields} testId="k8s-workload-fields" />
      </div>
      <ChartGrid charts={charts} series={series} q={ts} />
      <section className="min-w-0 rounded-xl border bg-card" aria-labelledby="k8s-workload-pods">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
          <h2 id="k8s-workload-pods" className="text-sm font-semibold">
            {t("kubernetes.workload.pods")}
          </h2>
          <Link
            to="/kubernetes/pods"
            search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, cluster: w.cluster_uid, ns: w.namespace, wkind: w.kind, wname: w.name })}
            className="text-xs text-primary hover:underline"
          >
            {t("kubernetes.workload.allPods")}
          </Link>
        </div>
        {w.pod_list.length === 0 ? <EmptyState>{t("kubernetes.workload.podsEmpty")}</EmptyState> : <PodTable pods={w.pod_list} showWorkload={false} showCluster={false} />}
      </section>
      <section className="min-w-0 rounded-xl border bg-card" aria-labelledby="k8s-workload-events">
        <h2 id="k8s-workload-events" className="border-b px-4 py-2 text-sm font-semibold">
          {t("kubernetes.events.title")}
        </h2>
        {events.isPending ? <LoadingState /> : events.isError ? <ErrorState error={events.error} onRetry={() => void events.refetch()} /> : <EventList events={events.data} showObject={false} />}
      </section>
    </div>
  );
}

export function KubernetesPodPage() {
  const { t } = useTranslation();
  const { podUid } = podRoute.useParams();
  const search = podRoute.useSearch();
  const navigate = useNavigate({ from: "/kubernetes/pods/$podUid" });
  const range: RangeSpec = useMemo(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const detail = useQuery(kubernetesPodQuery(podUid, range));
  const tab: K8sPodTab = search.tab ?? "overview";
  const back = <BackLink to="/kubernetes/pods" label={t("kubernetes.pod.back")} />;

  if (detail.isPending) return <LoadingState />;
  if (detail.isError) {
    if (isNotFound(detail.error)) return <NotFound text={t("kubernetes.pod.notFound")} back={back} />;
    return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  }
  const p = detail.data;
  const fields: [string, React.ReactNode][] = [
    [t("kubernetes.columns.cluster"), p.cluster_name || p.cluster_uid],
    [t("kubernetes.columns.namespace"), p.namespace],
    [
      t("kubernetes.columns.node"),
      p.host_id ? (
        <Link to="/hosts/$hostId" params={{ hostId: p.host_id }} search={rangeOnly} className="text-primary hover:underline" aria-label={t("kubernetes.nodes.openHost", { name: p.host_name || p.node_name })}>
          {p.node_name || p.host_name}
        </Link>
      ) : (
        p.node_name || "–"
      ),
    ],
  ];
  if (p.workload_kind && p.workload_kind !== "Pod") {
    fields.push([
      t("kubernetes.columns.workload"),
      <WorkloadLink w={{ cluster_uid: p.cluster_uid, namespace: p.namespace, kind: p.workload_kind, name: p.workload_name }} className="text-primary hover:underline">
        {p.workload_kind} {p.workload_name}
      </WorkloadLink>,
    ]);
  }
  if (p.pod_ip) fields.push([t("kubernetes.columns.ip"), <span className="font-mono">{p.pod_ip}</span>]);
  if (p.qos_class) fields.push([t("kubernetes.pod.fields.qos"), p.qos_class]);
  fields.push([t("kubernetes.columns.restarts"), <span className="font-mono">{p.restarts}</span>]);
  fields.push([t("kubernetes.pod.fields.started"), <When at={p.started_at ?? p.created_at} />]);
  if (p.services.length > 0) {
    fields.push([
      t("kubernetes.pod.fields.services"),
      <span className="flex flex-wrap gap-1.5" data-testid="k8s-pod-services">
        {p.services.map((s) => (
          <Link
            key={`${s.service_name}|${s.service_namespace}|${s.deployment_environment}`}
            to="/apm/services/$service"
            params={{ service: s.service_name }}
            search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, ns: s.service_namespace || undefined, env: s.deployment_environment || undefined })}
            className="text-primary hover:underline"
            aria-label={t("apm.openService", { name: s.service_name })}
          >
            {s.service_name}
          </Link>
        ))}
      </span>,
    ]);
  }

  return (
    <div className="flex flex-col gap-4">
      <div>
        {back}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <h1 className="text-xl font-semibold tracking-tight break-all">{p.pod_name}</h1>
          <PodStatusBadge pod={p} />
        </div>
        <Fields fields={fields} testId="k8s-pod-fields" />
      </div>
      <Tabs value={tab} onValueChange={(v) => void navigate({ search: (prev) => ({ ...prev, tab: v === "overview" ? undefined : (v as K8sPodTab) }) })}>
        <div className="overflow-x-auto">
          <TabsList aria-label={t("kubernetes.pod.tabs.label")}>
            {K8S_POD_TABS.map((k) => (
              <TabsTrigger key={k} value={k}>
                {t(`kubernetes.pod.tabs.${k}`)}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
        <TabsContent value="overview">{tab === "overview" && <PodCharts podUid={podUid} range={range} />}</TabsContent>
        <TabsContent value="containers">{tab === "containers" && <PodContainers pod={p} />}</TabsContent>
        <TabsContent value="logs">{tab === "logs" && <PodLogs podUid={podUid} range={range} search={search} />}</TabsContent>
        <TabsContent value="events">{tab === "events" && <PodEvents podUid={podUid} range={range} />}</TabsContent>
        <TabsContent value="labels">{tab === "labels" && <PodLabels labels={p.labels} />}</TabsContent>
      </Tabs>
    </div>
  );
}

function PodCharts({ podUid, range }: { podUid: string; range: RangeSpec }) {
  const { t } = useTranslation();
  const q = useQuery(kubernetesPodTimeseriesQuery(podUid, range));
  const labels = {
    cpu: t("kubernetes.charts.cpu"),
    memory: t("kubernetes.charts.memory"),
    receive: t("kubernetes.charts.receive"),
    transmit: t("kubernetes.charts.transmit"),
    restarts: t("kubernetes.charts.restarts"),
  };
  const series = q.data ? podChartSeries(q.data.series, labels) : undefined;
  const charts: ChartSpec[] = [
    { id: "cpu", title: t("kubernetes.charts.cpuCores"), unit: "number" },
    { id: "memory", title: labels.memory, unit: "bytes" },
    { id: "network", title: t("kubernetes.charts.network"), unit: "bytesPerSec" },
    { id: "restarts", title: labels.restarts, unit: "number" },
  ];
  return <ChartGrid charts={charts} series={series} q={q} />;
}

/** Logs of one pod: the Logs Explorer with `resource.k8s.pod.uid` locked; `severity` opens as a chip. */
function PodLogs({ podUid, range, search }: { podUid: string; range: RangeSpec; search: KubernetesPodSearch }) {
  const { t } = useTranslation();
  const locked = useMemo<QueryFilter[]>(() => [{ key: "resource.k8s.pod.uid", op: "=", value: podUid }], [podUid]);
  const emptyUnfiltered = (
    <EmptyState icon={<FileText className="size-5" aria-hidden="true" />} className="px-4">
      {t("kubernetes.pod.logsEmpty")}
    </EmptyState>
  );
  return (
    <section className="flex min-w-0 flex-col gap-3" aria-label={t("kubernetes.pod.tabs.logs")}>
      <EmbeddedLogsExplorer range={range} search={search} locked={locked} legacy={{ severity: search.severity }} legacyParamNames={POD_LEGACY_PARAMS} emptyUnfiltered={emptyUnfiltered} />
    </section>
  );
}

const POD_LEGACY_PARAMS = ["severity"] as const;

function PodEvents({ podUid, range }: { podUid: string; range: RangeSpec }) {
  const q = useQuery(kubernetesPodEventsQuery(podUid, range));
  return (
    <div className="min-w-0 rounded-xl border bg-card">
      {q.isPending ? <LoadingState /> : q.isError ? <ErrorState error={q.error} onRetry={() => void q.refetch()} /> : <EventList events={q.data} showObject={false} />}
    </div>
  );
}

function PodLabels({ labels }: { labels: Record<string, string> }) {
  const { t } = useTranslation();
  const entries = Object.entries(labels).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return <EmptyState>{t("kubernetes.pod.labelsEmpty")}</EmptyState>;
  return (
    <dl className="grid grid-cols-1 gap-x-4 gap-y-1 rounded-xl border bg-card p-4 text-xs sm:grid-cols-[minmax(10rem,max-content)_1fr]" data-testid="k8s-pod-labels">
      {entries.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="font-mono text-muted-foreground">{k}</dt>
          <dd className="mb-1 font-mono break-all sm:mb-0">{v}</dd>
        </div>
      ))}
    </dl>
  );
}
