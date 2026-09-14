import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import { hostQuery } from "@/api/queries";
import { HostServices } from "@/components/apm/HostServices";
import { AttributeChips } from "@/components/AttributeChips";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseTimeParam } from "@/lib/time";
import { HOST_TABS, type HostTab } from "@/router";
import { HostContainersTab } from "./host/containers-tab";
import { HostInventoryTab } from "./host/inventory-tab";
import { HostLogsTab } from "./host/logs-tab";
import { HostOverviewTab } from "./host/overview-tab";
import { HostServicesTab } from "./host/services-tab";

const route = getRouteApi("/app/hosts/$hostId");

export function HostDetailPage() {
  const { t, i18n } = useTranslation();
  const { hostId } = route.useParams();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/hosts/$hostId" });
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  const host = useQuery(hostQuery(hostId));
  // Containers are collected on Linux only (D-104): hide the tab when the host reports another os.type.
  const osType = host.data?.resource_attributes?.["os.type"];
  const tabs = HOST_TABS.filter((k) => k !== "containers" || !osType || osType === "linux");
  const tab: HostTab = search.tab && tabs.includes(search.tab) ? search.tab : "overview";

  if (host.isPending) return <LoadingState />;
  if (host.isError) {
    if (host.error instanceof ApiError && host.error.status === 404) {
      return (
        <EmptyState>
          <p className="mb-2 font-medium text-foreground">{t("host.notFound")}</p>
          <Link to="/hosts" className="text-primary hover:underline">
            {t("host.back")}
          </Link>
        </EmptyState>
      );
    }
    return <ErrorState error={host.error} onRetry={() => void host.refetch()} />;
  }
  const h = host.data;
  const seen = parseTimeParam(h.last_seen) ?? 0;

  return (
    <div className="flex flex-col gap-4">
      <div>
        <Link
          to="/hosts"
          search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}
          className="mb-2 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3" aria-hidden="true" />
          {t("host.back")}
        </Link>
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <h1 className="text-xl font-semibold tracking-tight">{h.host_name || h.host_id}</h1>
          <span className="text-sm text-muted-foreground">
            <time dateTime={h.last_seen} title={formatDateTime(seen, locale)}>
              {t("host.lastSeen", { when: formatRelative(seen, now, locale) })}
            </time>
          </span>
        </div>
        <dl className="mt-2 flex flex-wrap gap-x-6 gap-y-1 text-xs">
          {(
            [
              ["host.fields.os", h.os_description],
              ["host.fields.arch", h.arch],
              ["host.fields.agent", h.agent_version],
              ["host.fields.id", h.host_id],
            ] as const
          ).map(([k, v]) => (
            <div key={k} className="flex min-w-0 gap-1.5">
              <dt className="shrink-0 text-muted-foreground">{t(k)}</dt>
              <dd className="min-w-0 font-mono break-all">{v || "–"}</dd>
            </div>
          ))}
        </dl>
        <div className="mt-2">
          <AttributeChips attributes={h.resource_attributes} max={12} />
        </div>
        <HostServices hostId={hostId} />
      </div>

      <Tabs value={tab} onValueChange={(v) => void navigate({ search: (prev) => ({ ...prev, tab: v === "overview" ? undefined : (v as HostTab) }) })}>
        <TabsList aria-label={t("host.tabs.label")}>
          {tabs.map((k) => (
            <TabsTrigger key={k} value={k}>
              {t(`host.tabs.${k}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="overview">
          <HostOverviewTab hostId={hostId} />
        </TabsContent>
        <TabsContent value="services">
          <HostServicesTab hostId={hostId} />
        </TabsContent>
        <TabsContent value="containers">{tab === "containers" && <HostContainersTab hostId={hostId} />}</TabsContent>
        <TabsContent value="inventory">
          <HostInventoryTab hostId={hostId} />
        </TabsContent>
        <TabsContent value="logs">
          <HostLogsTab hostId={hostId} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
