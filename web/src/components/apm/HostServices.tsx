import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Box } from "lucide-react";
import { useTranslation } from "react-i18next";
import { apmHostServicesQuery } from "@/api/apm";

/** "Services on this host" (apm.md §1): APM services whose resources carried this host.id in the last 24h. */
export function HostServices({ hostId }: { hostId: string }) {
  const { t } = useTranslation();
  const q = useQuery(apmHostServicesQuery(hostId));
  if (!q.data || q.data.length === 0) return null;
  return (
    <section aria-labelledby="host-apm-services" className="mt-3" data-testid="host-apm-services">
      <h2 id="host-apm-services" className="mb-1 text-xs font-semibold text-muted-foreground">
        {t("apm.host.title")}
      </h2>
      <ul className="flex flex-wrap gap-2">
        {q.data.map((s) => (
          <li key={`${s.service_name}|${s.service_namespace}|${s.environment}`}>
            <Link
              to="/apm/services/$service"
              params={{ service: s.service_name }}
              search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, ns: s.service_namespace || undefined, env: s.environment || undefined })}
              className="inline-flex items-center gap-1.5 rounded-md border bg-card px-2 py-1 text-xs hover:border-primary"
              aria-label={t("apm.openService", { name: s.service_name })}
            >
              <Box className="size-3 text-muted-foreground" aria-hidden="true" />
              <span className="font-medium">{s.service_name}</span>
              {s.environment && <span className="text-muted-foreground">{s.environment}</span>}
            </Link>
          </li>
        ))}
      </ul>
    </section>
  );
}
