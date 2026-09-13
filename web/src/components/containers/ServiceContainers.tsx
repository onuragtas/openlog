import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { apmServiceContainersQuery } from "@/api/containers";
import type { ServiceScope } from "@/lib/apm";
import { containerStatus, shortContainerId } from "@/lib/containers";
import { formatValue } from "@/lib/format";
import type { RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";

const DOT: Record<string, string> = {
  success: "bg-success",
  warning: "bg-warning",
  destructive: "bg-destructive",
  secondary: "bg-muted-foreground",
  muted: "bg-muted-foreground/50",
};

/** "Containers" of an APM service (apm.md §1): containers whose span resources carried container.id, with state and CPU. */
export function ServiceContainers({ scope, range }: { scope: ServiceScope; range: RangeSpec }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(apmServiceContainersQuery(scope, range));
  const containers = q.data ?? [];
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <dt className="text-muted-foreground">{t("apm.service.containers")}</dt>
      <dd className="flex flex-wrap gap-1.5" data-testid="service-containers">
        {q.isPending ? (
          <span className="text-muted-foreground">…</span>
        ) : containers.length === 0 ? (
          <span className="text-muted-foreground">{t("apm.service.noContainers")}</span>
        ) : (
          containers.map((c) => {
            const name = c.name || shortContainerId(c.container_id);
            if (!c.known) {
              return (
                <span key={c.container_id} className="rounded-md border border-dashed px-1.5 py-0.5 font-mono text-muted-foreground" title={t("apm.service.containerUnknown")}>
                  {name}
                </span>
              );
            }
            const status = containerStatus({ state: c.state, health: "", reporting: c.reporting });
            return (
              <Link
                key={c.container_id}
                to="/containers/$containerId"
                params={{ containerId: c.container_id }}
                search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}
                className="inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 font-mono hover:border-primary"
                aria-label={t("apm.service.openContainer", { name })}
                title={[t(`containers.states.${status.key}`), c.host_name].filter(Boolean).join(" · ")}
              >
                <span className={cn("size-1.5 rounded-full", DOT[status.variant])} aria-hidden="true" />
                {name}
                {c.cpu_utilization != null && <span className="text-muted-foreground">{formatValue(c.cpu_utilization, "percent", locale)}</span>}
              </Link>
            );
          })
        )}
      </dd>
    </div>
  );
}
