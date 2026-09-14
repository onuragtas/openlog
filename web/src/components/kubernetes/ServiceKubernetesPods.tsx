import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { apmServiceKubernetesQuery } from "@/api/kubernetes";
import type { ServiceScope } from "@/lib/apm";
import { podStatus } from "@/lib/kubernetes";
import type { RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";

const DOT: Record<string, string> = {
  success: "bg-success",
  warning: "bg-warning",
  destructive: "bg-destructive",
  secondary: "bg-muted-foreground",
  muted: "bg-muted-foreground/50",
};

/** "Kubernetes pods" of an APM service; renders nothing for services that do not run in Kubernetes. */
export function ServiceKubernetesPods({ scope, range }: { scope: ServiceScope; range: RangeSpec }) {
  const { t } = useTranslation();
  const q = useQuery(apmServiceKubernetesQuery(scope, range));
  const pods = q.data ?? [];
  if (pods.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <dt className="text-muted-foreground">{t("kubernetes.service.pods")}</dt>
      <dd className="flex flex-wrap gap-1.5" data-testid="service-k8s-pods">
        {pods.map((p) => {
          const s = podStatus({ status: "", reason: "", phase: p.phase, ready: p.ready, reporting: p.reporting });
          return (
            <Link
              key={p.pod_uid}
              to="/kubernetes/pods/$podUid"
              params={{ podUid: p.pod_uid }}
              search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}
              className="inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 font-mono hover:border-primary"
              aria-label={t("kubernetes.service.openPod", { name: p.pod_name })}
              title={[s.notReporting ? t("kubernetes.status.notReporting") : s.label, p.namespace, p.cluster_name, p.node_name].filter(Boolean).join(" · ")}
            >
              <span className={cn("size-1.5 rounded-full", DOT[s.variant])} aria-hidden="true" />
              {p.pod_name}
            </Link>
          );
        })}
      </dd>
    </div>
  );
}
