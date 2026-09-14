import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { KubernetesClusterDetail } from "@/api/kubernetes";
import { EmptyState } from "@/components/StateViews";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatBytes, formatValue } from "@/lib/format";
import { formatCores, POD_PHASES, totalPods, usageRatio, WORKLOAD_HEALTHS, type WorkloadHealthParam, type WorkloadKind } from "@/lib/kubernetes";

function Tile({ title, value, sub, tone, testId }: { title: string; value: React.ReactNode; sub?: React.ReactNode; tone?: "bad" | "warn"; testId?: string }) {
  return (
    <Card className="min-w-0 gap-1 py-4" data-testid={testId}>
      <CardHeader className="px-4">
        <CardTitle className="text-xs font-medium text-muted-foreground">
          <h2>{title}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="px-4">
        <p className={`font-mono text-2xl font-semibold tabular-nums ${tone === "bad" ? "text-destructive-text" : ""}`}>{value}</p>
        {sub && <p className="mt-0.5 text-xs text-muted-foreground">{sub}</p>}
      </CardContent>
    </Card>
  );
}

export function ClusterTiles({ cluster }: { cluster: KubernetesClusterDetail }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const pods = totalPods(cluster);
  const phases = POD_PHASES.filter((p) => p !== "Running" && cluster.pods[p] > 0);
  const cpuRatio = usageRatio(cluster.cpu_usage, cluster.allocatable_cpu);
  const memRatio = usageRatio(cluster.memory_working_set, cluster.allocatable_memory);
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6" data-testid="k8s-cluster-tiles">
      <Tile
        testId="tile-nodes"
        title={t("kubernetes.kpi.nodesReady")}
        value={`${cluster.nodes_ready}/${cluster.nodes}`}
        tone={cluster.nodes_ready < cluster.nodes ? "bad" : undefined}
      />
      <Tile
        testId="tile-pods"
        title={t("kubernetes.kpi.pods")}
        value={pods}
        sub={[t("kubernetes.kpi.running", { count: cluster.pods.Running }), ...phases.map((p) => `${t(`kubernetes.phases.${p}`)} ${cluster.pods[p]}`)].join(" · ")}
      />
      <Tile
        testId="tile-not-ready"
        title={t("kubernetes.kpi.podsNotReady")}
        value={cluster.pods_not_ready}
        sub={t("kubernetes.kpi.podsNotReadyHint")}
        tone={cluster.pods_not_ready > 0 ? "bad" : undefined}
      />
      <Tile
        testId="tile-workloads"
        title={t("kubernetes.kpi.workloadsUnhealthy")}
        value={cluster.workloads_unhealthy}
        sub={t("kubernetes.kpi.ofTotal", { count: cluster.workloads })}
        tone={cluster.workloads_unhealthy > 0 ? "bad" : undefined}
      />
      <Tile
        title={t("kubernetes.kpi.cpu")}
        value={formatCores(cluster.cpu_usage, locale)}
        sub={cluster.allocatable_cpu != null ? t("kubernetes.kpi.ofAllocatable", { value: formatCores(cluster.allocatable_cpu, locale), ratio: formatValue(cpuRatio, "percent", locale) }) : undefined}
      />
      <Tile
        title={t("kubernetes.kpi.memory")}
        value={cluster.memory_working_set == null ? "–" : formatBytes(cluster.memory_working_set)}
        sub={cluster.allocatable_memory != null ? t("kubernetes.kpi.ofAllocatable", { value: formatBytes(cluster.allocatable_memory), ratio: formatValue(memRatio, "percent", locale) }) : undefined}
      />
    </div>
  );
}

export function WorkloadsByKind({ cluster }: { cluster: KubernetesClusterDetail }) {
  const { t } = useTranslation();
  if (cluster.workloads_by_kind.length === 0) return <EmptyState>{t("kubernetes.workloads.empty")}</EmptyState>;
  const cell = (kind: string, health: WorkloadHealthParam, n: number) =>
    n === 0 ? (
      <span className="text-muted-foreground">0</span>
    ) : (
      <Link
        to="/kubernetes/workloads"
        search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, cluster: cluster.cluster_uid, kind: kind as WorkloadKind, health })}
        className={health === "healthy" ? "hover:underline" : "font-semibold text-destructive-text hover:underline"}
      >
        {n}
      </Link>
    );
  return (
    <Table data-testid="k8s-workloads-by-kind">
      <TableHeader>
        <TableRow>
          <TableHead>{t("kubernetes.columns.kind")}</TableHead>
          <TableHead className="text-right">{t("kubernetes.columns.total")}</TableHead>
          {WORKLOAD_HEALTHS.map((h) => (
            <TableHead key={h} className="text-right">
              {t(`kubernetes.health.${h}`)}
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {cluster.workloads_by_kind.map((k) => (
          <TableRow key={k.kind}>
            <TableCell className="font-medium">{k.kind}</TableCell>
            <TableCell className="text-right font-mono tabular-nums">{k.total}</TableCell>
            {WORKLOAD_HEALTHS.map((h) => (
              <TableCell key={h} className="text-right font-mono tabular-nums">
                {cell(k.kind, h, k[h])}
              </TableCell>
            ))}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
