import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { KubernetesPodDetail } from "@/api/kubernetes";
import { EmptyState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatBytes } from "@/lib/format";
import { containerStateVariant, formatCores, rangeOnly, shortId } from "@/lib/kubernetes";

/** "250m / 1" request and limit of a container resource. */
function ReqLimit({ req, limit, cores }: { req: number | null; limit: number | null; cores?: boolean }) {
  const { i18n } = useTranslation();
  const fmt = (v: number | null) => (v == null ? "–" : cores ? formatCores(v, i18n.resolvedLanguage) : formatBytes(v));
  if (req == null && limit == null) return <span className="text-muted-foreground">–</span>;
  return (
    <span className="whitespace-nowrap">
      {fmt(req)} / {fmt(limit)}
    </span>
  );
}

export function PodContainers({ pod }: { pod: Pick<KubernetesPodDetail, "containers"> }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  if (pod.containers.length === 0) return <EmptyState>{t("kubernetes.pod.containers.empty")}</EmptyState>;
  return (
    <div className="min-w-0 rounded-xl border bg-card">
      <Table mobile="stack" data-testid="k8s-pod-containers">
        <TableHeader>
          <TableRow>
            <TableHead>{t("kubernetes.columns.name")}</TableHead>
            <TableHead>{t("kubernetes.pod.containers.state")}</TableHead>
            <TableHead className="text-right">{t("kubernetes.columns.restarts")}</TableHead>
            <TableHead className="text-right">{t("kubernetes.columns.cpu")}</TableHead>
            <TableHead className="text-right">{t("kubernetes.columns.memory")}</TableHead>
            <TableHead>{t("kubernetes.pod.containers.cpuReqLimit")}</TableHead>
            <TableHead>{t("kubernetes.pod.containers.memoryReqLimit")}</TableHead>
            <TableHead>{t("kubernetes.pod.containers.image")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {pod.containers.map((c) => {
            const id = c.container_id.replace(/^[a-z]+:\/\//, "");
            return (
              <TableRow key={c.name}>
                <TableCell>
                  {c.known && id ? (
                    <Link
                      to="/containers/$containerId"
                      params={{ containerId: id }}
                      search={rangeOnly}
                      className="font-medium text-primary hover:underline"
                      aria-label={t("kubernetes.pod.containers.open", { name: c.name })}
                    >
                      {c.name}
                    </Link>
                  ) : (
                    <span className="font-medium" title={id ? t("kubernetes.pod.containers.unknown") : undefined}>
                      {c.name}
                    </span>
                  )}
                  {id && <div className="font-mono text-xs text-muted-foreground">{shortId(id)}</div>}
                </TableCell>
                <TableCell className="max-md:w-auto">
                  <span className="inline-flex flex-wrap items-center gap-1">
                    <Badge variant={containerStateVariant(c)}>{c.reason || c.state || "–"}</Badge>
                    {c.state === "running" && !c.ready && <Badge variant="warning">{t("kubernetes.status.notReady")}</Badge>}
                  </span>
                </TableCell>
                <TableCell label={t("kubernetes.columns.restarts")} className="text-right font-mono tabular-nums">
                  {c.restarts}
                </TableCell>
                <TableCell label={t("kubernetes.columns.cpu")} className="text-right font-mono tabular-nums">
                  {formatCores(c.cpu_usage, locale)}
                </TableCell>
                <TableCell label={t("kubernetes.columns.memory")} className="text-right font-mono tabular-nums">
                  {c.memory_working_set == null ? "–" : formatBytes(c.memory_working_set)}
                </TableCell>
                <TableCell label={t("kubernetes.pod.containers.cpuReqLimit")} className="font-mono text-xs">
                  <ReqLimit req={c.cpu_request} limit={c.cpu_limit} cores />
                </TableCell>
                <TableCell label={t("kubernetes.pod.containers.memoryReqLimit")} className="font-mono text-xs">
                  <ReqLimit req={c.memory_request} limit={c.memory_limit} />
                </TableCell>
                <TableCell label={t("kubernetes.pod.containers.image")} className="max-w-80 truncate font-mono text-xs" title={c.image}>
                  {c.image || "–"}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
