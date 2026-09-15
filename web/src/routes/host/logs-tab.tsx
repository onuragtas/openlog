import { useQuery } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { FileText } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { hostQuery } from "@/api/queries";
import { LogFilters } from "@/components/LogFilters";
import { LogTable } from "@/components/LogTable";
import { CopyCommand } from "@/components/onboarding/CopyCommand";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { AGENT_CONFIG_PATH, agentRestart, DEFAULT_LOG_PATH, hostLogsYaml, hostOsOf, type HostOs } from "@/lib/host-os";
import { useLogPages } from "@/lib/use-logs";

const route = getRouteApi("/app/hosts/$hostId");

/** Agent log collection docs; link text is shown without a link until the page exists. */
const LOGS_DOCS_URL: string | undefined = undefined;

const EMPTY_BODY: Record<HostOs, "logs.hostEmptyBody" | "logs.hostEmptyBodyDarwin" | "logs.hostEmptyBodyWindows"> = {
  linux: "logs.hostEmptyBody",
  darwin: "logs.hostEmptyBodyDarwin",
  windows: "logs.hostEmptyBodyWindows",
};

/** Empty state of the host logs tab: the config.yaml logs section and the restart for the host's OS (D-112). */
export function NoHostLogs({ os }: { os: HostOs }) {
  const { t } = useTranslation();
  const restart = agentRestart(os);
  return (
    <EmptyState icon={<FileText className="size-5" aria-hidden="true" />} className="px-4">
      <div className="mx-auto flex max-w-xl min-w-0 flex-col gap-2 text-left" data-testid="host-logs-empty" data-os={os}>
        <p className="text-center font-medium text-foreground">{t("logs.hostEmptyTitle")}</p>
        <p>{t(EMPTY_BODY[os], { path: AGENT_CONFIG_PATH[os] })}</p>
        <CopyCommand code={hostLogsYaml(os, DEFAULT_LOG_PATH[os], true)} label={t("logs.hostEmptyConfig")} lang="yaml" testId="host-logs-config" />
        <CopyCommand code={restart.code} label={t("logs.hostEmptyRestart")} lang={restart.lang} testId="host-logs-restart" />
        <p className="mt-1">
          {LOGS_DOCS_URL ? (
            <a href={LOGS_DOCS_URL} target="_blank" rel="noreferrer" className="font-medium text-primary hover:underline">
              {t("logs.hostEmptyDocs")}
            </a>
          ) : (
            <>
              <span className="font-medium text-foreground">{t("logs.hostEmptyDocs")}</span> {t("logs.docsSoon")}
            </>
          )}
        </p>
      </div>
    </EmptyState>
  );
}

/**
 * Logs of one host. Agent log records carry no service.name, so the tab filters by host_id plus
 * the agent's source attributes (openlog.log.source, log.file.path, openlog.discovery.id, openlog.systemd.unit).
 */
export function HostLogsTab({ hostId }: { hostId: string }) {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/hosts/$hostId" });
  const os = hostOsOf(useQuery(hostQuery(hostId)).data);
  const { query, logs } = useLogPages({
    range: { range: search.range, from: search.from, to: search.to },
    hostId,
    q: search.lq,
    severity: search.severity,
    attrs: { source: search.lsrc, filePath: search.lfile, discoveryId: search.ldisc, systemdUnit: search.lunit },
  });
  const filterValue = useMemo(
    () => ({
      q: search.lq ?? "",
      severity: search.severity ?? "",
      service: "",
      host: "",
      source: search.lsrc ?? "",
      file: search.lfile ?? "",
      discovery: search.ldisc ?? "",
      unit: search.lunit ?? "",
    }),
    [search.lq, search.severity, search.lsrc, search.lfile, search.ldisc, search.lunit],
  );
  const filtered = !!(search.lq || search.severity || search.lsrc || search.lfile || search.ldisc || search.lunit);

  return (
    <section className="flex flex-col gap-3" aria-label={t("logs.hostTitle")}>
      <LogFilters
        value={filterValue}
        showHost={false}
        showService={false}
        showSourceFilters
        os={os}
        onApply={(v) =>
          void navigate({
            search: (prev) => ({
              ...prev,
              lq: v.q || undefined,
              severity: v.severity || undefined,
              lsrc: v.source || undefined,
              lfile: v.file?.trim() || undefined,
              ldisc: v.discovery?.trim() || undefined,
              lunit: v.unit?.trim() || undefined,
            }),
          })
        }
      />
      <div className="overflow-hidden rounded-xl border bg-card">
        {query.isPending ? (
          <LoadingState />
        ) : query.isError && !logs ? (
          <ErrorState error={query.error} onRetry={() => void query.refetch()} />
        ) : !logs || logs.length === 0 ? (
          filtered ? <EmptyState>{t("logs.empty")}</EmptyState> : <NoHostLogs os={os} />
        ) : (
          <LogTable
            logs={logs}
            showHost={false}
            hasMore={query.hasNextPage}
            loadingMore={query.isFetchingNextPage}
            onLoadMore={() => void query.fetchNextPage()}
          />
        )}
      </div>
    </section>
  );
}
