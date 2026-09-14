import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { BellPlus, Search, Ship, X } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import {
  kubernetesClusterQuery,
  kubernetesClustersQuery,
  kubernetesNodesQuery,
  kubernetesPodsQuery,
  kubernetesWorkloadsQuery,
  type KubernetesCluster,
} from "@/api/kubernetes";
import { PageHeader } from "@/components/AppShell";
import { ClusterTiles, WorkloadsByKind } from "@/components/kubernetes/ClusterOverview";
import { EventList } from "@/components/kubernetes/EventList";
import { KubernetesNav } from "@/components/kubernetes/KubernetesNav";
import { NodeTable, PodTable, WorkloadTable } from "@/components/kubernetes/KubernetesTables";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import {
  clusterNamespaces,
  pickCluster,
  POD_PHASES,
  WORKLOAD_HEALTHS,
  WORKLOAD_KINDS,
  type PodPhaseParam,
  type WorkloadHealthParam,
  type WorkloadKind,
} from "@/lib/kubernetes";
import { parseTimeParam, type RangeSpec } from "@/lib/time";
import type { KubernetesNodesSearch, KubernetesPodsSearch, KubernetesWorkloadsSearch } from "@/router";

const overviewRoute = getRouteApi("/app/kubernetes");
const workloadsRoute = getRouteApi("/app/kubernetes/workloads");
const podsRoute = getRouteApi("/app/kubernetes/pods");
const nodesRoute = getRouteApi("/app/kubernetes/nodes");

function NoClusters() {
  const { t } = useTranslation();
  return (
    <EmptyState icon={<Ship className="size-5" aria-hidden="true" />}>
      <p className="font-medium text-foreground">{t("kubernetes.empty")}</p>
      <p className="mt-1">{t("kubernetes.emptyHint")}</p>
    </EmptyState>
  );
}

function RecommendedAlertsButton() {
  const { t } = useTranslation();
  return (
    <Button asChild variant="outline">
      <Link to="/alerts/templates" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, category: "kubernetes" as const })}>
        <BellPlus aria-hidden="true" />
        {t("kubernetes.recommendedAlerts")}
      </Link>
    </Button>
  );
}

function SearchBox({ label, placeholder, value, onChange }: { label: string; placeholder: string; value?: string; onChange: (v: string | undefined) => void }) {
  const id = useId();
  return (
    <div className="relative w-full sm:w-56">
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden="true" />
      <Input id={id} type="search" className="pl-8" placeholder={placeholder} value={value ?? ""} onChange={(e) => onChange(e.target.value || undefined)} />
    </div>
  );
}

function Select({ label, value, onChange, all, options }: { label: string; value?: string; onChange: (v: string | undefined) => void; all: string; options: readonly { value: string; label: string }[] }) {
  const id = useId();
  return (
    <>
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      <NativeSelect id={id} className="min-w-0 flex-1 sm:flex-none" value={value ?? ""} onChange={(e) => onChange(e.target.value || undefined)}>
        <option value="">{all}</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </NativeSelect>
    </>
  );
}

function ClusterSelect({ clusters, value, onChange, allowAll = true }: { clusters: KubernetesCluster[]; value?: string; onChange: (v: string | undefined) => void; allowAll?: boolean }) {
  const { t } = useTranslation();
  const id = useId();
  if (!allowAll) {
    return (
      <>
        <label htmlFor={id} className="sr-only">
          {t("kubernetes.clusterLabel")}
        </label>
        <NativeSelect id={id} className="min-w-0 flex-1 sm:flex-none" value={value ?? ""} onChange={(e) => onChange(e.target.value || undefined)}>
          {clusters.map((c) => (
            <option key={c.cluster_uid} value={c.cluster_uid}>
              {c.cluster_name || c.cluster_uid}
            </option>
          ))}
        </NativeSelect>
      </>
    );
  }
  return (
    <Select
      label={t("kubernetes.clusterLabel")}
      value={value}
      onChange={onChange}
      all={t("kubernetes.allClusters")}
      options={clusters.map((c) => ({ value: c.cluster_uid, label: c.cluster_name || c.cluster_uid }))}
    />
  );
}

function Section({ title, action, children, testId }: { title: string; action?: React.ReactNode; children: React.ReactNode; testId?: string }) {
  return (
    <section className="min-w-0 rounded-xl border bg-card" data-testid={testId}>
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <h2 className="text-sm font-semibold">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  );
}

export function KubernetesOverviewPage() {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = useNow();
  const search = overviewRoute.useSearch();
  const navigate = useNavigate({ from: "/kubernetes" });
  const range: RangeSpec = { range: search.range, from: search.from, to: search.to };
  const clusters = useQuery(kubernetesClustersQuery(range));
  const selected = clusters.data ? pickCluster(clusters.data, search.cluster) : undefined;
  const uid = selected?.cluster_uid ?? "";
  const detail = useQuery(kubernetesClusterQuery(uid, range));
  const nodes = useQuery({ ...kubernetesNodesQuery(range, { clusterUid: uid }), enabled: uid !== "" });
  const actions = (
    <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto">
      {clusters.data && clusters.data.length > 1 && (
        <ClusterSelect clusters={clusters.data} value={uid} allowAll={false} onChange={(v) => void navigate({ search: (prev) => ({ ...prev, cluster: v }), replace: true })} />
      )}
      <RecommendedAlertsButton />
    </div>
  );

  let body: React.ReactNode;
  if (clusters.isPending) body = <LoadingState />;
  else if (clusters.isError) body = <ErrorState error={clusters.error} onRetry={() => void clusters.refetch()} />;
  else if (!selected) body = <NoClusters />;
  else if (detail.isPending) body = <LoadingState />;
  else if (detail.isError) body = <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  else {
    const c = detail.data;
    const seen = parseTimeParam(c.last_seen) ?? 0;
    const linkSearch = (prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to, cluster: c.cluster_uid }) as never;
    body = (
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
          <h2 className="text-base font-semibold break-all">{c.cluster_name || c.cluster_uid}</h2>
          {!c.reporting && (
            <Badge variant="muted" title={t("kubernetes.status.notReportingTitle")}>
              {t("kubernetes.status.notReporting")}
            </Badge>
          )}
          {c.version && <span className="text-muted-foreground">{t("kubernetes.version", { version: c.version })}</span>}
          <time className="text-muted-foreground" dateTime={c.last_seen} title={formatDateTime(seen, locale)}>
            {t("kubernetes.lastSeen", { when: formatRelative(seen, now, locale) })}
          </time>
        </div>
        <ClusterTiles cluster={c} />
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <Section
            title={t("kubernetes.sections.workloadsByKind")}
            action={
              <Link to="/kubernetes/workloads" search={linkSearch} className="text-xs text-primary hover:underline">
                {t("kubernetes.sections.allWorkloads")}
              </Link>
            }
          >
            <WorkloadsByKind cluster={c} />
          </Section>
          <Section title={t("kubernetes.sections.warningEvents")} testId="k8s-warning-events">
            <EventList events={c.warning_events} emptyText={t("kubernetes.sections.noWarnings")} />
          </Section>
        </div>
        <Section
          title={t("kubernetes.sections.nodes")}
          action={
            <Link to="/kubernetes/nodes" search={linkSearch} className="text-xs text-primary hover:underline">
              {t("kubernetes.sections.allNodes")}
            </Link>
          }
        >
          {nodes.isPending ? (
            <LoadingState />
          ) : nodes.isError ? (
            <ErrorState error={nodes.error} onRetry={() => void nodes.refetch()} />
          ) : nodes.data.nodes.length === 0 ? (
            <EmptyState>{t("kubernetes.nodes.empty")}</EmptyState>
          ) : (
            <NodeTable nodes={nodes.data.nodes} showCluster={false} />
          )}
        </Section>
      </div>
    );
  }

  return (
    <div>
      <PageHeader title={t("kubernetes.title")} subtitle={t("kubernetes.subtitle")} actions={actions} />
      <KubernetesNav active="overview" />
      {body}
    </div>
  );
}

function ListFrame({ children, shown, total }: { children: React.ReactNode; shown?: number; total?: number }) {
  const { t } = useTranslation();
  return (
    <>
      <div className="min-w-0 rounded-xl border bg-card">{children}</div>
      {shown !== undefined && total !== undefined && total > shown && <p className="mt-2 text-xs text-muted-foreground">{t("kubernetes.shown", { shown, total })}</p>}
    </>
  );
}

export function KubernetesWorkloadsPage() {
  const { t } = useTranslation();
  const search = workloadsRoute.useSearch();
  const navigate = useNavigate({ from: "/kubernetes/workloads" });
  const range: RangeSpec = { range: search.range, from: search.from, to: search.to };
  const clusters = useQuery(kubernetesClustersQuery(range)).data ?? [];
  const list = useQuery(kubernetesWorkloadsQuery(range, { clusterUid: search.cluster, namespace: search.ns, kind: search.kind, health: search.health, q: search.q }));
  const set = (patch: Partial<KubernetesWorkloadsSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  const filtered = !!(search.cluster || search.ns || search.kind || search.health || search.q);
  return (
    <div>
      <PageHeader
        title={t("kubernetes.workloads.title")}
        subtitle={t("kubernetes.workloads.subtitle")}
        actions={
          <div className="flex w-full flex-wrap items-center gap-2" data-testid="k8s-workload-filters">
            <SearchBox label={t("kubernetes.searchLabel")} placeholder={t("kubernetes.workloads.searchPlaceholder")} value={search.q} onChange={(q) => set({ q })} />
            {clusters.length > 1 && <ClusterSelect clusters={clusters} value={search.cluster} onChange={(cluster) => set({ cluster, ns: undefined })} />}
            <Select
              label={t("kubernetes.namespaceLabel")}
              value={search.ns}
              onChange={(ns) => set({ ns })}
              all={t("kubernetes.allNamespaces")}
              options={clusterNamespaces(clusters, search.cluster).map((n) => ({ value: n, label: n }))}
            />
            <Select label={t("kubernetes.kindLabel")} value={search.kind} onChange={(kind) => set({ kind: kind as WorkloadKind | undefined })} all={t("kubernetes.allKinds")} options={WORKLOAD_KINDS.map((k) => ({ value: k, label: k }))} />
            <Select
              label={t("kubernetes.healthLabel")}
              value={search.health}
              onChange={(health) => set({ health: health as WorkloadHealthParam | undefined })}
              all={t("kubernetes.allHealth")}
              options={WORKLOAD_HEALTHS.map((h) => ({ value: h, label: t(`kubernetes.health.${h}`) }))}
            />
          </div>
        }
      />
      <KubernetesNav active="workloads" />
      <ListFrame shown={list.data?.workloads.length} total={list.data?.total}>
        {list.isPending ? (
          <LoadingState />
        ) : list.isError ? (
          <ErrorState error={list.error} onRetry={() => void list.refetch()} />
        ) : list.data.workloads.length === 0 ? (
          <EmptyState>{filtered ? t("kubernetes.noMatch") : t("kubernetes.workloads.empty")}</EmptyState>
        ) : (
          <WorkloadTable workloads={list.data.workloads} showCluster={clusters.length > 1 && !search.cluster} />
        )}
      </ListFrame>
    </div>
  );
}

export function KubernetesPodsPage() {
  const { t } = useTranslation();
  const search = podsRoute.useSearch();
  const navigate = useNavigate({ from: "/kubernetes/pods" });
  const range: RangeSpec = { range: search.range, from: search.from, to: search.to };
  const clusters = useQuery(kubernetesClustersQuery(range)).data ?? [];
  const list = useQuery(
    kubernetesPodsQuery(range, { clusterUid: search.cluster, namespace: search.ns, node: search.node, phase: search.phase, workloadKind: search.wkind, workloadName: search.wname, q: search.q }),
  );
  const set = (patch: Partial<KubernetesPodsSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  const filtered = !!(search.cluster || search.ns || search.node || search.phase || search.wkind || search.wname || search.q);
  return (
    <div>
      <PageHeader
        title={t("kubernetes.pods.title")}
        subtitle={t("kubernetes.pods.subtitle")}
        actions={
          <div className="flex w-full flex-wrap items-center gap-2" data-testid="k8s-pod-filters">
            <SearchBox label={t("kubernetes.searchLabel")} placeholder={t("kubernetes.pods.searchPlaceholder")} value={search.q} onChange={(q) => set({ q })} />
            {clusters.length > 1 && <ClusterSelect clusters={clusters} value={search.cluster} onChange={(cluster) => set({ cluster, ns: undefined })} />}
            <Select
              label={t("kubernetes.namespaceLabel")}
              value={search.ns}
              onChange={(ns) => set({ ns })}
              all={t("kubernetes.allNamespaces")}
              options={clusterNamespaces(clusters, search.cluster).map((n) => ({ value: n, label: n }))}
            />
            <Select label={t("kubernetes.phaseLabel")} value={search.phase} onChange={(phase) => set({ phase: phase as PodPhaseParam | undefined })} all={t("kubernetes.allPhases")} options={POD_PHASES.map((p) => ({ value: p, label: t(`kubernetes.phases.${p}`) }))} />
          </div>
        }
      />
      <KubernetesNav active="pods" />
      {(search.wname || search.node) && (
        <div className="mb-3 flex flex-wrap gap-2">
          {search.wname && (
            <Button variant="secondary" size="sm" onClick={() => set({ wkind: undefined, wname: undefined })} aria-label={t("kubernetes.clearFilter")}>
              {t("kubernetes.workloadFilter", { kind: search.wkind ?? "", name: search.wname })}
              <X aria-hidden="true" />
            </Button>
          )}
          {search.node && (
            <Button variant="secondary" size="sm" onClick={() => set({ node: undefined })} aria-label={t("kubernetes.clearFilter")}>
              {t("kubernetes.nodeFilter", { name: search.node })}
              <X aria-hidden="true" />
            </Button>
          )}
        </div>
      )}
      <ListFrame shown={list.data?.pods.length} total={list.data?.total}>
        {list.isPending ? (
          <LoadingState />
        ) : list.isError ? (
          <ErrorState error={list.error} onRetry={() => void list.refetch()} />
        ) : list.data.pods.length === 0 ? (
          <EmptyState>{filtered ? t("kubernetes.noMatch") : t("kubernetes.pods.empty")}</EmptyState>
        ) : (
          <PodTable pods={list.data.pods} showCluster={clusters.length > 1 && !search.cluster} />
        )}
      </ListFrame>
    </div>
  );
}

export function KubernetesNodesPage() {
  const { t } = useTranslation();
  const search = nodesRoute.useSearch();
  const navigate = useNavigate({ from: "/kubernetes/nodes" });
  const range: RangeSpec = { range: search.range, from: search.from, to: search.to };
  const clusters = useQuery(kubernetesClustersQuery(range)).data ?? [];
  const list = useQuery(kubernetesNodesQuery(range, { clusterUid: search.cluster, q: search.q }));
  const set = (patch: Partial<KubernetesNodesSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  return (
    <div>
      <PageHeader
        title={t("kubernetes.nodes.title")}
        subtitle={t("kubernetes.nodes.subtitle")}
        actions={
          <div className="flex w-full flex-wrap items-center gap-2">
            <SearchBox label={t("kubernetes.searchLabel")} placeholder={t("kubernetes.nodes.searchPlaceholder")} value={search.q} onChange={(q) => set({ q })} />
            {clusters.length > 1 && <ClusterSelect clusters={clusters} value={search.cluster} onChange={(cluster) => set({ cluster })} />}
          </div>
        }
      />
      <KubernetesNav active="nodes" />
      <ListFrame shown={list.data?.nodes.length} total={list.data?.total}>
        {list.isPending ? (
          <LoadingState />
        ) : list.isError ? (
          <ErrorState error={list.error} onRetry={() => void list.refetch()} />
        ) : list.data.nodes.length === 0 ? (
          <EmptyState>{search.q || search.cluster ? t("kubernetes.noMatch") : t("kubernetes.nodes.empty")}</EmptyState>
        ) : (
          <NodeTable nodes={list.data.nodes} showCluster={clusters.length > 1 && !search.cluster} />
        )}
      </ListFrame>
    </div>
  );
}
