import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, Outlet, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { alertRuleQuery } from "@/api/alerts";
import { PageHeader } from "@/components/AppShell";
import { ChannelsManager } from "@/components/alerts/ChannelsManager";
import { IncidentDetail, IncidentsList, type IncidentStateFilter } from "@/components/alerts/Incidents";
import { MutesManager } from "@/components/alerts/MutesManager";
import { RuleEditor } from "@/components/alerts/RuleEditor";
import { RulesList } from "@/components/alerts/RulesList";
import { RuleStateBadge } from "@/components/alerts/badges";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { applyPrefill } from "@/lib/alerts";

const incidentsRoute = getRouteApi("/app/alerts/incidents");
const incidentRoute = getRouteApi("/app/alerts/incidents/$incidentId");
const ruleNewRoute = getRouteApi("/app/alerts/rules/new");
const ruleRoute = getRouteApi("/app/alerts/rules/$ruleId");

const TABS = [
  { to: "/alerts/incidents", label: "alerts.tabs.incidents" },
  { to: "/alerts/rules", label: "alerts.tabs.rules" },
  { to: "/alerts/channels", label: "alerts.tabs.channels" },
  { to: "/alerts/mutes", label: "alerts.tabs.mutes" },
] as const;

/** Alarmlar / Alerts: incidents, rules, channels and mutes (docs/contracts/alerting.md). */
export function AlertsLayout() {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("alerts.title")} subtitle={t("alerts.subtitle")} />
      <nav aria-label={t("alerts.tabs.sections")} className="-mt-2 flex gap-1 overflow-x-auto border-b [scrollbar-width:none]">
        {TABS.map((tab) => (
          <Link
            key={tab.to}
            to={tab.to}
            className="-mb-px border-b-2 border-transparent px-3 py-2 text-sm whitespace-nowrap pointer-coarse:py-2.5 text-muted-foreground hover:text-foreground data-[status=active]:border-primary data-[status=active]:font-medium data-[status=active]:text-foreground"
          >
            {t(tab.label)}
          </Link>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}

const STATE_FILTERS: readonly IncidentStateFilter[] = ["active", "open", "acknowledged", "resolved", "all"];

export function AlertsIncidentsPage() {
  const search = incidentsRoute.useSearch();
  const navigate = useNavigate({ from: "/alerts/incidents" });
  const state = (STATE_FILTERS as readonly string[]).includes(search.state ?? "") ? (search.state as IncidentStateFilter) : "active";
  return (
    <IncidentsList
      stateFilter={state}
      severity={search.severity}
      onFilterChange={(f) => void navigate({ search: (prev) => ({ ...prev, state: f.state === "active" ? undefined : f.state, severity: f.severity }), replace: true })}
    />
  );
}

function BackLink({ to, label }: { to: "/alerts/incidents" | "/alerts/rules"; label: string }) {
  return (
    <Link to={to} className="inline-flex w-fit items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" aria-hidden="true" />
      {label}
    </Link>
  );
}

export function AlertsIncidentPage() {
  const { t } = useTranslation();
  const { incidentId } = incidentRoute.useParams();
  return (
    <div className="flex flex-col gap-3">
      <BackLink to="/alerts/incidents" label={t("alerts.incident.back")} />
      <IncidentDetail id={incidentId} />
    </div>
  );
}

export function AlertsRulesPage() {
  return <RulesList />;
}

export function AlertsRuleNewPage() {
  const { t } = useTranslation();
  const search = ruleNewRoute.useSearch();
  const navigate = useNavigate();
  const key = JSON.stringify(search);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const initial = useMemo(() => applyPrefill(search), [key]);
  return (
    <div className="flex flex-col gap-3">
      <BackLink to="/alerts/rules" label={t("alerts.editor.back")} />
      <h2 className="text-lg font-semibold">{t("alerts.editor.newTitle")}</h2>
      <RuleEditor
        key={key}
        initial={initial}
        onSaved={(r) => void navigate({ to: "/alerts/rules/$ruleId", params: { ruleId: r.id } })}
        onCancel={() => void navigate({ to: "/alerts/rules" })}
      />
    </div>
  );
}

export function AlertsRuleEditPage() {
  const { t } = useTranslation();
  const { ruleId } = ruleRoute.useParams();
  const navigate = useNavigate();
  const q = useQuery(alertRuleQuery(ruleId));
  return (
    <div className="flex flex-col gap-3">
      <BackLink to="/alerts/rules" label={t("alerts.editor.back")} />
      {q.isPending ? (
        <LoadingState />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-lg font-semibold">{t("alerts.editor.editTitle")}</h2>
            <RuleStateBadge state={q.data.status.state} />
          </div>
          <RuleEditor key={`${q.data.id}:${q.data.version}`} rule={q.data} onCancel={() => void navigate({ to: "/alerts/rules" })} />
        </>
      )}
    </div>
  );
}

export function AlertsChannelsPage() {
  return <ChannelsManager />;
}

export function AlertsMutesPage() {
  return <MutesManager />;
}
