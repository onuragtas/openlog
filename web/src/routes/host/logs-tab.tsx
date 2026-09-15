import { useQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import { FileText } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import type { QueryFilter } from "@/api/explorer";
import { hostQuery } from "@/api/queries";
import { EmbeddedLogsExplorer } from "@/components/logs-explorer/EmbeddedLogsExplorer";
import { CopyCommand } from "@/components/onboarding/CopyCommand";
import { EmptyState } from "@/components/StateViews";
import { AGENT_CONFIG_PATH, agentRestart, DEFAULT_LOG_PATH, hostLogsYaml, hostOsOf, type HostOs } from "@/lib/host-os";

const route = getRouteApi("/app/hosts/$hostId");

const HOST_LEGACY_PARAMS = ["severity", "lsrc", "lfile", "ldisc", "lunit"] as const;

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
 * Logs of one host: the Logs Explorer with `host.id` locked (agent log records carry no service.name). The former
 * source filters (`lsrc`, `lfile`, `ldisc`, `lunit`) and `severity` open as attribute and severity chips.
 */
export function HostLogsTab({ hostId }: { hostId: string }) {
  const { t } = useTranslation();
  const search = route.useSearch();
  const os = hostOsOf(useQuery(hostQuery(hostId)).data);
  const range = useMemo(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const locked = useMemo<QueryFilter[]>(() => [{ key: "host.id", op: "=", value: hostId }], [hostId]);
  return (
    <section className="flex min-w-0 flex-col gap-3" aria-label={t("logs.hostTitle")}>
      <EmbeddedLogsExplorer
        range={range}
        search={search}
        locked={locked}
        legacy={{ severity: search.severity, source: search.lsrc, file: search.lfile, discovery: search.ldisc, unit: search.lunit }}
        legacyParamNames={HOST_LEGACY_PARAMS}
        emptyUnfiltered={<NoHostLogs os={os} />}
      />
    </section>
  );
}
