import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { containersQuery } from "@/api/containers";
import { ContainerTable } from "@/components/containers/ContainerTable";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import type { RangeSpec } from "@/lib/time";

const route = getRouteApi("/app/hosts/$hostId");

/** Containers of one host (GET /containers?host_id=). */
export function HostContainersTab({ hostId }: { hostId: string }) {
  const { t } = useTranslation();
  const search = route.useSearch();
  const range: RangeSpec = { range: search.range, from: search.from, to: search.to };
  const q = useQuery(containersQuery(range, { hostId }));
  return (
    <section className="flex flex-col gap-2" aria-label={t("host.tabs.containers")}>
      <div className="flex justify-end">
        <Link to="/containers" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, host: hostId, group: true })} className="text-xs text-primary hover:underline">
          {t("containers.host.viewAll")}
        </Link>
      </div>
      <div className="rounded-xl border bg-card">
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.data.containers.length === 0 ? (
          <EmptyState>{t("containers.host.empty")}</EmptyState>
        ) : (
          <ContainerTable containers={q.data.containers} showHost={false} />
        )}
      </div>
    </section>
  );
}
