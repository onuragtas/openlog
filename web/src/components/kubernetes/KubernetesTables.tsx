import { Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { KubernetesNode, KubernetesPod, KubernetesWorkload } from "@/api/kubernetes";
import { Sparkline } from "@/components/apm/Charts";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatBytes, formatDateTime, formatRelative, formatValue } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { formatCores, nodeProblems, rangeOnly, replicasText, usageRatio } from "@/lib/kubernetes";
import { parseTimeParam } from "@/lib/time";
import { NodeStatusBadge, PodStatusBadge, WorkloadHealthBadge } from "./K8sBadges";

const Dash = () => <span className="text-muted-foreground">–</span>;

/** Ignore row clicks that land on a link or button inside the row. */
const fromControl = (e: React.MouseEvent) => !!(e.target as HTMLElement).closest("a,button");

function Since({ at }: { at: string | null }) {
  const { i18n } = useTranslation();
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  const ms = at ? parseTimeParam(at) : null;
  if (ms == null) return <Dash />;
  return (
    <time dateTime={at ?? undefined} title={formatDateTime(ms, locale)}>
      {formatRelative(ms, now, locale)}
    </time>
  );
}

/** "1.2 GiB · 40%" of a usage against a total. */
function UsageOf({ used, total, cores }: { used: number | null; total?: number | null; cores?: boolean }) {
  const { i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  if (used == null) return <Dash />;
  const ratio = usageRatio(used, total);
  return (
    <span className="whitespace-nowrap">
      {cores ? formatCores(used, locale) : formatBytes(used)}
      {ratio != null && <span className="text-muted-foreground"> · {formatValue(ratio, "percent", locale)}</span>}
    </span>
  );
}

export function WorkloadLink({ w, className, children }: { w: Pick<KubernetesWorkload, "cluster_uid" | "namespace" | "kind" | "name">; className?: string; children?: React.ReactNode }) {
  const { t } = useTranslation();
  return (
    <Link
      to="/kubernetes/workloads/$clusterUid/$namespace/$kind/$name"
      params={{ clusterUid: w.cluster_uid, namespace: w.namespace, kind: w.kind, name: w.name }}
      search={rangeOnly}
      className={className ?? "hover:underline"}
      aria-label={t("kubernetes.workloads.open", { name: w.name })}
    >
      {children ?? w.name}
    </Link>
  );
}

export function WorkloadTable({ workloads, showCluster = true }: { workloads: KubernetesWorkload[]; showCluster?: boolean }) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const locale = i18n.resolvedLanguage ?? "en";
  return (
    <Table data-testid="k8s-workload-table">
      <TableHeader>
        <TableRow>
          <TableHead>{t("kubernetes.columns.name")}</TableHead>
          <TableHead>{t("kubernetes.columns.kind")}</TableHead>
          <TableHead>{t("kubernetes.columns.health")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.replicas")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.restarts")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.cpu")}</TableHead>
          <TableHead>{t("kubernetes.columns.cpuTrend")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.memory")}</TableHead>
          <TableHead>{t("kubernetes.columns.memoryTrend")}</TableHead>
          {showCluster && <TableHead>{t("kubernetes.columns.cluster")}</TableHead>}
        </TableRow>
      </TableHeader>
      <TableBody>
        {workloads.map((w) => (
          <TableRow
            key={`${w.cluster_uid}/${w.namespace}/${w.kind}/${w.name}`}
            className="cursor-pointer"
            onClick={(e) => {
              if (fromControl(e)) return;
              void navigate({
                to: "/kubernetes/workloads/$clusterUid/$namespace/$kind/$name",
                params: { clusterUid: w.cluster_uid, namespace: w.namespace, kind: w.kind, name: w.name },
                search: rangeOnly,
              });
            }}
          >
            <TableCell className="max-w-72">
              <WorkloadLink w={w} className="block truncate font-medium hover:underline" />
              <div className="truncate text-xs text-muted-foreground">{w.namespace}</div>
            </TableCell>
            <TableCell className="text-xs">{w.kind}</TableCell>
            <TableCell>
              <WorkloadHealthBadge health={w.health} />
            </TableCell>
            <TableCell className="text-right font-mono tabular-nums">{w.kind === "CronJob" ? <Dash /> : replicasText(w.ready, w.desired)}</TableCell>
            <TableCell className="text-right font-mono tabular-nums">{w.restarts}</TableCell>
            <TableCell className="text-right font-mono tabular-nums">{formatCores(w.cpu_usage, locale)}</TableCell>
            <TableCell>
              <Sparkline points={w.cpu_sparkline} label={t("kubernetes.columns.cpuTrend")} width={96} />
            </TableCell>
            <TableCell className="text-right font-mono tabular-nums">{w.memory_working_set == null ? <Dash /> : formatBytes(w.memory_working_set)}</TableCell>
            <TableCell>
              <Sparkline points={w.memory_sparkline} label={t("kubernetes.columns.memoryTrend")} width={96} />
            </TableCell>
            {showCluster && <TableCell className="text-xs">{w.cluster_name || w.cluster_uid}</TableCell>}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export function PodLink({ pod, className }: { pod: Pick<KubernetesPod, "pod_uid" | "pod_name">; className?: string }) {
  const { t } = useTranslation();
  return (
    <Link to="/kubernetes/pods/$podUid" params={{ podUid: pod.pod_uid }} search={rangeOnly} className={className ?? "hover:underline"} aria-label={t("kubernetes.pods.open", { name: pod.pod_name })}>
      {pod.pod_name}
    </Link>
  );
}

export function PodTable({ pods, showWorkload = true, showCluster = true }: { pods: KubernetesPod[]; showWorkload?: boolean; showCluster?: boolean }) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const locale = i18n.resolvedLanguage ?? "en";
  return (
    <Table data-testid="k8s-pod-table">
      <TableHeader>
        <TableRow>
          <TableHead>{t("kubernetes.columns.name")}</TableHead>
          <TableHead>{t("kubernetes.columns.status")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.restarts")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.cpu")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.memory")}</TableHead>
          {showWorkload && <TableHead>{t("kubernetes.columns.workload")}</TableHead>}
          <TableHead>{t("kubernetes.columns.node")}</TableHead>
          <TableHead>{t("kubernetes.columns.ip")}</TableHead>
          <TableHead>{t("kubernetes.columns.age")}</TableHead>
          {showCluster && <TableHead>{t("kubernetes.columns.cluster")}</TableHead>}
        </TableRow>
      </TableHeader>
      <TableBody>
        {pods.map((p) => (
          <TableRow
            key={p.pod_uid}
            className="cursor-pointer"
            onClick={(e) => {
              if (fromControl(e)) return;
              void navigate({ to: "/kubernetes/pods/$podUid", params: { podUid: p.pod_uid }, search: rangeOnly });
            }}
          >
            <TableCell className="max-w-80">
              <PodLink pod={p} className="block truncate font-medium hover:underline" />
              <div className="truncate text-xs text-muted-foreground">{p.namespace}</div>
            </TableCell>
            <TableCell>
              <PodStatusBadge pod={p} />
            </TableCell>
            <TableCell className="text-right font-mono tabular-nums">{p.restarts}</TableCell>
            <TableCell className="text-right font-mono tabular-nums">{formatCores(p.cpu_usage, locale)}</TableCell>
            <TableCell className="text-right font-mono tabular-nums">{p.memory_working_set == null ? <Dash /> : formatBytes(p.memory_working_set)}</TableCell>
            {showWorkload && (
              <TableCell className="text-xs">
                {p.workload_kind && p.workload_kind !== "Pod" ? (
                  <WorkloadLink w={{ cluster_uid: p.cluster_uid, namespace: p.namespace, kind: p.workload_kind, name: p.workload_name }}>
                    <span className="text-muted-foreground">{p.workload_kind}</span> {p.workload_name}
                  </WorkloadLink>
                ) : (
                  <Dash />
                )}
              </TableCell>
            )}
            <TableCell className="text-xs">{p.node_name || <Dash />}</TableCell>
            <TableCell className="font-mono text-xs">{p.pod_ip || <Dash />}</TableCell>
            <TableCell className="text-xs whitespace-nowrap">
              <Since at={p.created_at} />
            </TableCell>
            {showCluster && <TableCell className="text-xs">{p.cluster_name || p.cluster_uid}</TableCell>}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export function NodeTable({ nodes, showCluster = true }: { nodes: KubernetesNode[]; showCluster?: boolean }) {
  const { t } = useTranslation();
  return (
    <Table data-testid="k8s-node-table">
      <TableHeader>
        <TableRow>
          <TableHead>{t("kubernetes.columns.name")}</TableHead>
          <TableHead>{t("kubernetes.columns.status")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.pods")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.cpu")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.memory")}</TableHead>
          <TableHead>{t("kubernetes.columns.host")}</TableHead>
          <TableHead>{t("kubernetes.columns.kubelet")}</TableHead>
          <TableHead>{t("kubernetes.columns.lastSeen")}</TableHead>
          {showCluster && <TableHead>{t("kubernetes.columns.cluster")}</TableHead>}
        </TableRow>
      </TableHeader>
      <TableBody>
        {nodes.map((n) => {
          const problems = nodeProblems(n);
          return (
            <TableRow key={`${n.cluster_uid}/${n.node_name}`}>
              <TableCell className="max-w-72">
                <span className="block truncate font-medium">{n.node_name}</span>
                <div className="truncate text-xs text-muted-foreground">{[n.roles.join(", "), n.internal_ip].filter(Boolean).join(" · ") || "–"}</div>
              </TableCell>
              <TableCell>
                <NodeStatusBadge node={n} />
                {problems.length > 0 && <div className="mt-1 text-xs text-destructive-text">{problems.join(", ")}</div>}
              </TableCell>
              <TableCell className="text-right font-mono tabular-nums">
                {n.pods}
                {n.allocatable_pods != null && <span className="text-muted-foreground">/{n.allocatable_pods}</span>}
              </TableCell>
              <TableCell className="text-right font-mono tabular-nums">
                <UsageOf used={n.cpu_usage} total={n.allocatable_cpu} cores />
              </TableCell>
              <TableCell className="text-right font-mono tabular-nums">
                <UsageOf used={n.memory_working_set} total={n.allocatable_memory} />
              </TableCell>
              <TableCell className="text-xs">
                {n.host_id ? (
                  <Link
                    to="/hosts/$hostId"
                    params={{ hostId: n.host_id }}
                    search={rangeOnly}
                    className="text-primary hover:underline"
                    aria-label={t("kubernetes.nodes.openHost", { name: n.host_name || n.node_name })}
                  >
                    {n.host_name || n.host_id}
                  </Link>
                ) : (
                  <Dash />
                )}
              </TableCell>
              <TableCell className="text-xs">{n.kubelet_version || <Dash />}</TableCell>
              <TableCell className="text-xs whitespace-nowrap">
                <Since at={n.last_seen} />
              </TableCell>
              {showCluster && <TableCell className="text-xs">{n.cluster_name || n.cluster_uid}</TableCell>}
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
