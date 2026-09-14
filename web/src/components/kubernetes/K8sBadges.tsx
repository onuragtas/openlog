import { useTranslation } from "react-i18next";
import type { KubernetesNode, KubernetesPod } from "@/api/kubernetes";
import { Badge } from "@/components/ui/badge";
import { eventVariant, nodeStatus, podStatus, workloadHealthVariant } from "@/lib/kubernetes";

export function WorkloadHealthBadge({ health }: { health: string }) {
  const { t } = useTranslation();
  return (
    <Badge variant={workloadHealthVariant(health)} data-testid="workload-health">
      {t(`kubernetes.health.${health as "healthy"}`, { defaultValue: health })}
    </Badge>
  );
}

export function PodStatusBadge({ pod }: { pod: Pick<KubernetesPod, "status" | "phase" | "reason" | "ready" | "reporting"> }) {
  const { t } = useTranslation();
  const s = podStatus(pod);
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <Badge variant={s.variant} data-testid="pod-status" title={s.notReporting ? t("kubernetes.status.notReportingTitle") : undefined}>
        {s.notReporting ? t("kubernetes.status.notReporting") : s.label}
      </Badge>
      {s.notReady && (
        <Badge variant="warning" data-testid="pod-not-ready">
          {t("kubernetes.status.notReady")}
        </Badge>
      )}
    </span>
  );
}

export function NodeStatusBadge({ node }: { node: Pick<KubernetesNode, "ready" | "reporting" | "unschedulable"> }) {
  const { t } = useTranslation();
  const s = nodeStatus(node);
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <Badge variant={s.variant} data-testid="node-status">
        {t(`kubernetes.nodeStatus.${s.key}`)}
      </Badge>
      {node.unschedulable && <Badge variant="muted">{t("kubernetes.unschedulable")}</Badge>}
    </span>
  );
}

export function EventTypeBadge({ type }: { type: string }) {
  return <Badge variant={eventVariant(type)}>{type || "–"}</Badge>;
}
