import { getRouteApi, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { PagedContainers } from "@/components/containers/ContainerTable";
import type { RangeSpec } from "@/lib/time";

const route = getRouteApi("/app/hosts/$hostId");

/** Containers of one host (GET /containers?host_id=). */
export function HostContainersTab({ hostId }: { hostId: string }) {
  const { t } = useTranslation();
  const search = route.useSearch();
  const range: RangeSpec = { range: search.range, from: search.from, to: search.to };
  return (
    <section className="flex flex-col gap-2" aria-label={t("host.tabs.containers")}>
      <div className="flex justify-end">
        <Link to="/containers" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, host: hostId, group: true })} className="text-xs text-primary hover:underline">
          {t("containers.host.viewAll")}
        </Link>
      </div>
      <PagedContainers range={range} filters={{ hostId }} showHost={false} empty={t("containers.host.empty")} />
    </section>
  );
}
