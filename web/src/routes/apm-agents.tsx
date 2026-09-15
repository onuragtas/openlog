// Agent versions (/apm/agents, D-124): every service × language agent × version with its status compared with the
// latest release, an "outdated only" filter and CSV export. Upgrade commands are on each service's overview.
import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Download, ExternalLink, Search } from "lucide-react";
import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { apmAgentsQuery } from "@/api/apm";
import { AgentVersionsTable } from "@/components/apm/AgentVersions";
import { PageHeader } from "@/components/AppShell";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { AGENT_UPDATES_DOCS, agentRows, agentRowsCsv, downloadText, filterAgentRows } from "@/lib/agent-versions";
import type { ApmAgentsSearch } from "@/router";

const route = getRouteApi("/app/apm/agents");

export function ApmAgentsPage() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/apm/agents" });
  const ids = { q: useId(), env: useId(), outdated: useId() };
  const range = useMemo(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const agents = useQuery(apmAgentsQuery(range, { upgrade: false }));
  const allRows = useMemo(() => agentRows(agents.data?.services ?? []), [agents.data]);
  const environments = useMemo(() => [...new Set(allRows.map((r) => r.environment).filter(Boolean))].sort(), [allRows]);
  const rows = useMemo(() => filterAgentRows(allRows, { q: search.q, environment: search.env, outdatedOnly: search.outdated }), [allRows, search.q, search.env, search.outdated]);
  const setSearch = (patch: Partial<ApmAgentsSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  const release = agents.data?.release;

  return (
    <div>
      <Link to="/apm" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })} className="mb-2 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-3" aria-hidden="true" />
        {t("apm.agents.back")}
      </Link>
      <PageHeader
        title={t("apm.agents.title")}
        subtitle={t("apm.agents.subtitle")}
        actions={
          <div className="contents">
            <label htmlFor={ids.env} className="sr-only">
              {t("apm.environment")}
            </label>
            <NativeSelect id={ids.env} value={search.env ?? ""} onChange={(e) => setSearch({ env: e.target.value || undefined })}>
              <option value="">{t("apm.allEnvironments")}</option>
              {environments.map((env) => (
                <option key={env} value={env}>
                  {env}
                </option>
              ))}
            </NativeSelect>
            <div className="relative w-full sm:w-56">
              <label htmlFor={ids.q} className="sr-only">
                {t("apm.agents.searchLabel")}
              </label>
              <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden="true" />
              <Input id={ids.q} type="search" className="pl-8" placeholder={t("apm.agents.searchPlaceholder")} value={search.q ?? ""} onChange={(e) => setSearch({ q: e.target.value || undefined })} />
            </div>
            <label htmlFor={ids.outdated} className="flex items-center gap-2 text-sm">
              <input id={ids.outdated} type="checkbox" className="size-4" checked={search.outdated ?? false} onChange={(e) => setSearch({ outdated: e.target.checked || undefined })} />
              {t("apm.agents.outdatedOnly")}
            </label>
            <Button type="button" variant="outline" disabled={rows.length === 0} onClick={() => downloadText("openlog-agent-versions.csv", agentRowsCsv(rows, release?.latest), "text/csv;charset=utf-8")}>
              <Download className="size-4" aria-hidden="true" />
              {t("apm.agents.exportCsv")}
            </Button>
          </div>
        }
      />
      {release && (
        <p className="mb-3 text-sm text-muted-foreground" data-testid="agent-release">
          {release.catalog === "ok" && release.latest
            ? [t("apm.agents.release.ok", { channel: release.channel, version: release.latest }), release.oldest_supported ? t("apm.agents.release.oldest", { version: release.oldest_supported }) : ""]
                .filter(Boolean)
                .join(" · ")
            : release.catalog === "disabled"
              ? t("apm.agents.release.disabled")
              : t("apm.agents.release.unavailable")}
        </p>
      )}
      <div className="rounded-xl border bg-card">
        {agents.isPending ? (
          <LoadingState />
        ) : agents.isError ? (
          <ErrorState error={agents.error} onRetry={() => void agents.refetch()} />
        ) : allRows.length === 0 ? (
          <EmptyState>{t("apm.agents.empty")}</EmptyState>
        ) : rows.length === 0 ? (
          <EmptyState>{t("apm.agents.noMatch")}</EmptyState>
        ) : (
          <AgentVersionsTable
            rows={rows}
            renderService={(r) => (
              <Link
                to="/apm/services/$service"
                params={{ service: r.service_name }}
                search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, ns: r.service_namespace || undefined, env: r.environment || undefined }) as never}
                className="hover:underline"
              >
                {r.service_name}
              </Link>
            )}
          />
        )}
      </div>
      <p className="mt-3 text-xs text-muted-foreground">
        {t("apm.agents.notice.why")}{" "}
        <a href={AGENT_UPDATES_DOCS} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-primary hover:underline">
          {t("apm.agents.notice.automate")}
          <ExternalLink className="size-3" aria-hidden="true" />
        </a>
      </p>
    </div>
  );
}
