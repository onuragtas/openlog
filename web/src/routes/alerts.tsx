import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, Outlet, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { alertRuleQuery, alertTemplateRenderQuery } from "@/api/alerts";
import { PageHeader } from "@/components/AppShell";
import { ChannelsManager } from "@/components/alerts/ChannelsManager";
import { EvaluationHistory } from "@/components/alerts/EvaluationHistory";
import { IncidentDetail, IncidentsList, type IncidentStateFilter } from "@/components/alerts/Incidents";
import { MutesManager } from "@/components/alerts/MutesManager";
import { RuleEditor } from "@/components/alerts/RuleEditor";
import { RulesList } from "@/components/alerts/RulesList";
import { RuleStateBadge } from "@/components/alerts/badges";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { TemplateGallery } from "@/components/alerts/TemplateGallery";
import { Button } from "@/components/ui/button";
import { applyPrefill, draftFromInput } from "@/lib/alerts";
import { templateLanguage, TEMPLATE_CATEGORIES } from "@/lib/alert-templates";

const incidentsRoute = getRouteApi("/app/alerts/incidents");
const incidentRoute = getRouteApi("/app/alerts/incidents/$incidentId");
const ruleNewRoute = getRouteApi("/app/alerts/rules/new");
const templatesRoute = getRouteApi("/app/alerts/templates");
const ruleRoute = getRouteApi("/app/alerts/rules/$ruleId");

const TABS = [
  { to: "/alerts/incidents", label: "alerts.tabs.incidents" },
  { to: "/alerts/rules", label: "alerts.tabs.rules" },
  { to: "/alerts/templates", label: "alerts.tabs.templates" },
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

function parseTemplateParams(raw: string | undefined): Record<string, unknown> {
  try {
    const v: unknown = JSON.parse(raw ?? "{}");
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

export function AlertsRuleNewPage() {
  const { t, i18n } = useTranslation();
  const search = ruleNewRoute.useSearch();
  const navigate = useNavigate();
  const key = JSON.stringify(search);
  // A recommended template is rendered by the server and opened as a draft (alerting.md §2.8).
  const rendered = useQuery(
    alertTemplateRenderQuery(search.template ?? "", search.template ? { params: parseTemplateParams(search.tparams), language: templateLanguage(i18n.resolvedLanguage) } : null),
  );
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const initial = useMemo(() => (search.template ? (rendered.data ? draftFromInput(rendered.data.rule) : undefined) : applyPrefill(search)), [key, rendered.data]);
  return (
    <div className="flex flex-col gap-3">
      <BackLink to="/alerts/rules" label={t("alerts.editor.back")} />
      <h2 className="text-lg font-semibold">{t("alerts.editor.newTitle")}</h2>
      {search.template && rendered.isError ? (
        <ErrorState error={rendered.error} onRetry={() => void rendered.refetch()} />
      ) : !initial ? (
        <LoadingState />
      ) : (
        <RuleEditor
          key={`${key}:${rendered.dataUpdatedAt}`}
          initial={initial}
          onSaved={(r) => void navigate({ to: "/alerts/rules/$ruleId", params: { ruleId: r.id } })}
          onCancel={() => void navigate({ to: "/alerts/rules" })}
        />
      )}
    </div>
  );
}

/** Recommended alert templates: hosts, containers, APM services and integrations. */
export function AlertsTemplatesPage() {
  const { t } = useTranslation();
  const search = templatesRoute.useSearch();
  const navigate = useNavigate({ from: "/alerts/templates" });
  const target = { hostId: search.host, hostName: search.hostName, serviceName: search.service };
  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted-foreground">{t("alerts.templates.intro")}</p>
      <div role="group" aria-label={t("alerts.templates.filter")} className="flex flex-wrap gap-2">
        <Button type="button" size="sm" className="min-h-10" variant={search.category ? "outline" : "secondary"} aria-pressed={!search.category}
          onClick={() => void navigate({ search: (prev) => ({ ...prev, category: undefined, integration: undefined }), replace: true })}>
          {t("alerts.templates.all")}
        </Button>
        {TEMPLATE_CATEGORIES.map((c) => (
          <Button key={c} type="button" size="sm" className="min-h-10" variant={search.category === c ? "secondary" : "outline"} aria-pressed={search.category === c}
            onClick={() => void navigate({ search: (prev) => ({ ...prev, category: c, integration: undefined }), replace: true })}>
            {t(`alerts.templates.categories.${c}`)}
          </Button>
        ))}
      </div>
      {(search.hostName || search.host || search.service) && (
        <p className="text-sm" data-testid="template-target">
          {t("alerts.templates.targetFor", { target: search.service ?? search.hostName ?? search.host ?? "" })}
        </p>
      )}
      <TemplateGallery key={JSON.stringify(search)} category={search.category} integration={search.integration} target={target} columns="3" />
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
          <EvaluationHistory rule={q.data} />
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
