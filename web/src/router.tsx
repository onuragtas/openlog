// Code-based route tree. Every route inherits the time range search params
// (`range`, `from`, `to`) from the root; screens add their own filters, so all
// filters live in the URL.
import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, createRoute, createRouter, lazyRouteComponent, Outlet, redirect } from "@tanstack/react-router";
import { meQuery } from "@/api/account";
import { ApiError } from "@/api/client";
import { SSO_ERROR_CODES, ssoLogoutStatus, type SsoErrorCode, type SsoLogoutStatus } from "@/api/sso";
import { AppShell } from "@/components/AppShell";
import { LoadingState } from "@/components/StateViews";
import { NotFoundPage } from "@/routes/not-found";
import { LoginPage } from "@/routes/login";
import { validateRangeSearch, type RangeSpec } from "@/lib/time";
import { sanitizeFiltersSearch, type DashboardFilter } from "@/lib/dashboard-filters";
import { sanitizeVarsSearch } from "@/lib/dashboards";
import { POD_PHASES, WORKLOAD_HEALTHS, WORKLOAD_KINDS, type PodPhaseParam, type WorkloadHealthParam, type WorkloadKind } from "@/lib/kubernetes";

// Screens are code-split per route (uPlot only loads with host detail).
const HostsPage = lazyRouteComponent(() => import("@/routes/hosts"), "HostsPage");
const HostDetailPage = lazyRouteComponent(() => import("@/routes/host-detail"), "HostDetailPage");
const ContainersPage = lazyRouteComponent(() => import("@/routes/containers"), "ContainersPage");
const ContainerDetailPage = lazyRouteComponent(() => import("@/routes/container-detail"), "ContainerDetailPage");
const KubernetesOverviewPage = lazyRouteComponent(() => import("@/routes/kubernetes"), "KubernetesOverviewPage");
const KubernetesWorkloadsPage = lazyRouteComponent(() => import("@/routes/kubernetes"), "KubernetesWorkloadsPage");
const KubernetesPodsPage = lazyRouteComponent(() => import("@/routes/kubernetes"), "KubernetesPodsPage");
const KubernetesNodesPage = lazyRouteComponent(() => import("@/routes/kubernetes"), "KubernetesNodesPage");
const KubernetesWorkloadPage = lazyRouteComponent(() => import("@/routes/kubernetes-detail"), "KubernetesWorkloadPage");
const KubernetesPodPage = lazyRouteComponent(() => import("@/routes/kubernetes-detail"), "KubernetesPodPage");
const HostIntegrationPage = lazyRouteComponent(() => import("@/routes/integrations"), "HostIntegrationPage");
const IntegrationsPage = lazyRouteComponent(() => import("@/routes/integrations"), "IntegrationsPage");
const LogsPage = lazyRouteComponent(() => import("@/routes/logs"), "LogsPage");
const TracePage = lazyRouteComponent(() => import("@/routes/trace"), "TracePage");
const InventorySearchPage = lazyRouteComponent(() => import("@/routes/inventory-search"), "InventorySearchPage");
const InvitePage = lazyRouteComponent(() => import("@/routes/invite"), "InvitePage");
const SignupPage = lazyRouteComponent(() => import("@/routes/signup"), "SignupPage");
const VerifyEmailPage = lazyRouteComponent(() => import("@/routes/verify-email"), "VerifyEmailPage");
const FleetPage = lazyRouteComponent(() => import("@/routes/fleet"), "FleetPage");
const ApmServicesPage = lazyRouteComponent(() => import("@/routes/apm"), "ApmServicesPage");
const ApmServicePage = lazyRouteComponent(() => import("@/routes/apm"), "ApmServicePage");
const ApmMapPage = lazyRouteComponent(() => import("@/routes/apm"), "ApmMapPage");
const ApmErrorsPage = lazyRouteComponent(() => import("@/routes/apm-errors"), "ApmErrorsPage");
const AlertsLayout = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsLayout");
const AlertsIncidentsPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsIncidentsPage");
const AlertsIncidentPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsIncidentPage");
const AlertsRulesPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsRulesPage");
const AlertsRuleNewPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsRuleNewPage");
const AlertsRuleEditPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsRuleEditPage");
const AlertsChannelsPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsChannelsPage");
const AlertsMutesPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsMutesPage");
const AlertsTemplatesPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsTemplatesPage");
const SettingsLayout = lazyRouteComponent(() => import("@/routes/settings"), "SettingsLayout");
const OrganizationSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "OrganizationSettingsPage");
const ProfileSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "ProfileSettingsPage");
const MembersSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "MembersSettingsPage");
const LicenseKeysSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "LicenseKeysSettingsPage");
const ApiKeysSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "ApiKeysSettingsPage");
const SecuritySettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "SecuritySettingsPage");
const AuditLogSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "AuditLogSettingsPage");
const TailSamplingSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "TailSamplingSettingsPage");
const UsageSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "UsageSettingsPage");
const QueryPage = lazyRouteComponent(() => import("@/routes/query"), "QueryPage");
const DashboardsPage = lazyRouteComponent(() => import("@/routes/dashboards"), "DashboardsPage");
const DashboardPage = lazyRouteComponent(() => import("@/routes/dashboards"), "DashboardPage");

export interface RouterContext {
  queryClient: QueryClient;
}

const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : typeof v === "number" ? String(v) : undefined);

const rootRoute = createRootRouteWithContext<RouterContext>()({
  validateSearch: (search: Record<string, unknown>): RangeSpec => validateRangeSearch(search),
  component: Outlet,
  notFoundComponent: NotFoundPage,
});

export interface LoginSearch {
  redirect?: string;
  expired?: boolean;
  /** Failed single sign-on (docs/contracts/api.md "Single sign-on", components/settings/SsoSignIn). */
  sso_error?: SsoErrorCode;
  /** "Sign out everywhere" returned from the identity provider (components/settings/SsoLogoutNotice). */
  sso_logout?: SsoLogoutStatus;
}

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (s: Record<string, unknown>): LoginSearch => ({
    redirect: str(s.redirect)?.startsWith("/") ? str(s.redirect) : undefined,
    expired: s.expired === true || s.expired === "true" || s.expired === 1 ? true : undefined,
    sso_error: oneOf(SSO_ERROR_CODES, s.sso_error),
    sso_logout: ssoLogoutStatus(s.sso_logout),
  }),
  component: LoginPage,
});

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  // The session cookie is HttpOnly: ask the API who we are (cached; refreshed after org switches).
  beforeLoad: async ({ context, location }) => {
    try {
      await context.queryClient.ensureQueryData(meQuery());
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        throw redirect({ to: "/login", search: { redirect: location.href } });
      }
      throw e;
    }
  },
  component: AppShell,
});

// Accepting an invitation needs no session.
const inviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/invite",
  component: InvitePage,
});

// Self-service sign-up and e-mail verification links need no session either.
const signupRoute = createRoute({ getParentRoute: () => rootRoute, path: "/signup", component: SignupPage });
const verifyEmailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/verify-email", component: VerifyEmailPage });

const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/hosts" });
  },
});

export interface HostsSearch {
  q?: string;
}

const hostsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/hosts",
  validateSearch: (s: Record<string, unknown>): HostsSearch => ({ q: str(s.q) }),
  component: HostsPage,
});

export const HOST_TABS = ["overview", "services", "containers", "inventory", "logs"] as const;
export type HostTab = (typeof HOST_TABS)[number];

export interface HostDetailSearch {
  tab?: HostTab;
  /** inventory category filter */
  category?: string;
  /** inventory text filter */
  iq?: string;
  /** host logs text filter */
  lq?: string;
  /** host logs minimum severity */
  severity?: string;
  /** host logs attribute filters: openlog.log.source, log.file.path, openlog.discovery.id, openlog.systemd.unit */
  lsrc?: string;
  lfile?: string;
  ldisc?: string;
  lunit?: string;
}

const hostDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/hosts/$hostId",
  validateSearch: (s: Record<string, unknown>): HostDetailSearch => ({
    tab: (HOST_TABS as readonly string[]).includes(String(s.tab)) ? (s.tab as HostTab) : undefined,
    category: str(s.category),
    iq: str(s.iq),
    lq: str(s.lq),
    severity: str(s.severity),
    lsrc: str(s.lsrc),
    lfile: str(s.lfile),
    ldisc: str(s.ldisc),
    lunit: str(s.lunit),
  }),
  component: HostDetailPage,
});

// ---- Containers (routes/containers.tsx, routes/container-detail.tsx) ----

export const CONTAINER_STATES = ["running", "paused", "restarting", "exited", "created", "dead", "removing", "unknown"] as const;
export type ContainerStateParam = (typeof CONTAINER_STATES)[number];

export interface ContainersSearch {
  q?: string;
  host?: string;
  project?: string;
  state?: ContainerStateParam;
  /** list layout: flat table or grouped by compose project/service */
  group?: boolean;
}

const containersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/containers",
  validateSearch: (s: Record<string, unknown>): ContainersSearch => ({
    q: str(s.q),
    host: str(s.host),
    project: typeof s.project === "string" ? s.project : undefined,
    state: (CONTAINER_STATES as readonly string[]).includes(String(s.state)) ? (s.state as ContainerStateParam) : undefined,
    group: s.group === true || s.group === "true" ? true : undefined,
  }),
  component: ContainersPage,
});

export const CONTAINER_TABS = ["overview", "services", "logs", "attributes"] as const;
export type ContainerTab = (typeof CONTAINER_TABS)[number];

export interface ContainerDetailSearch {
  tab?: ContainerTab;
  /** logs text filter, minimum severity, stream (stdout/stderr) */
  lq?: string;
  severity?: string;
  stream?: "stdout" | "stderr";
}

const containerDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/containers/$containerId",
  validateSearch: (s: Record<string, unknown>): ContainerDetailSearch => ({
    tab: (CONTAINER_TABS as readonly string[]).includes(String(s.tab)) ? (s.tab as ContainerTab) : undefined,
    lq: str(s.lq),
    severity: str(s.severity),
    stream: s.stream === "stdout" || s.stream === "stderr" ? s.stream : undefined,
  }),
  component: ContainerDetailPage,
});

// ---- Kubernetes (routes/kubernetes.tsx, routes/kubernetes-detail.tsx) ----

const pick = <T extends string>(values: readonly T[], v: unknown): T | undefined => ((values as readonly string[]).includes(String(v)) ? (v as T) : undefined);

export interface KubernetesOverviewSearch {
  /** selected cluster uid (default: first reporting cluster) */
  cluster?: string;
}

const kubernetesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/kubernetes",
  validateSearch: (s: Record<string, unknown>): KubernetesOverviewSearch => ({ cluster: str(s.cluster) }),
  component: KubernetesOverviewPage,
});

export interface KubernetesWorkloadsSearch {
  cluster?: string;
  ns?: string;
  kind?: WorkloadKind;
  health?: WorkloadHealthParam;
  q?: string;
}

const kubernetesWorkloadsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/kubernetes/workloads",
  validateSearch: (s: Record<string, unknown>): KubernetesWorkloadsSearch => ({
    cluster: str(s.cluster),
    ns: str(s.ns),
    kind: pick(WORKLOAD_KINDS, s.kind),
    health: pick(WORKLOAD_HEALTHS, s.health),
    q: str(s.q),
  }),
  component: KubernetesWorkloadsPage,
});

export interface KubernetesPodsSearch {
  cluster?: string;
  ns?: string;
  node?: string;
  phase?: PodPhaseParam;
  /** pods of one workload: kind and name */
  wkind?: string;
  wname?: string;
  q?: string;
}

const kubernetesPodsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/kubernetes/pods",
  validateSearch: (s: Record<string, unknown>): KubernetesPodsSearch => ({
    cluster: str(s.cluster),
    ns: str(s.ns),
    node: str(s.node),
    phase: pick(POD_PHASES, s.phase),
    wkind: str(s.wkind),
    wname: str(s.wname),
    q: str(s.q),
  }),
  component: KubernetesPodsPage,
});

export interface KubernetesNodesSearch {
  cluster?: string;
  q?: string;
}

const kubernetesNodesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/kubernetes/nodes",
  validateSearch: (s: Record<string, unknown>): KubernetesNodesSearch => ({ cluster: str(s.cluster), q: str(s.q) }),
  component: KubernetesNodesPage,
});

const kubernetesWorkloadRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/kubernetes/workloads/$clusterUid/$namespace/$kind/$name",
  component: KubernetesWorkloadPage,
});

export const K8S_POD_TABS = ["overview", "containers", "logs", "events", "labels"] as const;
export type K8sPodTab = (typeof K8S_POD_TABS)[number];

export interface KubernetesPodSearch {
  tab?: K8sPodTab;
  /** logs text filter and minimum severity */
  lq?: string;
  severity?: string;
}

const kubernetesPodRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/kubernetes/pods/$podUid",
  validateSearch: (s: Record<string, unknown>): KubernetesPodSearch => ({ tab: pick(K8S_POD_TABS, s.tab), lq: str(s.lq), severity: str(s.severity) }),
  component: KubernetesPodPage,
});

// ---- Integrations (routes/integrations.tsx) ----

/** Panel of one integration instance: `$instance` is the discovered service instance (URL-encoded). */
const hostIntegrationRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/hosts/$hostId/integrations/$discoveryId/$instance",
  component: HostIntegrationPage,
});

export interface IntegrationsSearch {
  status?: string;
  q?: string;
  /** integration id: fleet dashboard of its instances */
  integration?: string;
}

const integrationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/integrations",
  validateSearch: (s: Record<string, unknown>): IntegrationsSearch => ({ status: str(s.status), q: str(s.q), integration: str(s.integration) }),
  component: IntegrationsPage,
});

export interface LogsSearch {
  q?: string;
  severity?: string;
  service?: string;
  host?: string;
  trace?: string;
  /** span id (logs of one span) */
  span?: string;
  /** transaction name and its service (logs of that transaction's traces) */
  txn?: string;
  txnsvc?: string;
}

const logsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/logs",
  validateSearch: (s: Record<string, unknown>): LogsSearch => ({
    q: str(s.q),
    severity: str(s.severity),
    service: str(s.service),
    host: str(s.host),
    trace: str(s.trace),
    span: str(s.span),
    txn: str(s.txn),
    txnsvc: str(s.txnsvc),
  }),
  component: LogsPage,
});

export interface TraceSearch {
  span?: string;
}

const traceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/traces/$traceId",
  validateSearch: (s: Record<string, unknown>): TraceSearch => ({ span: str(s.span) }),
  component: TracePage,
});

// ---- APM (routes/apm.tsx) ----

export interface ApmServicesSearch {
  q?: string;
  env?: string;
}

const apmServicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/apm",
  validateSearch: (s: Record<string, unknown>): ApmServicesSearch => ({ q: str(s.q), env: str(s.env) }),
  component: ApmServicesPage,
});

export const APM_TABS = ["overview", "transactions", "errors", "databases", "map", "traces"] as const;
export type ApmTab = (typeof APM_TABS)[number];
const TX_SORTS = ["time", "throughput", "slowest", "errors"] as const;
const DB_SORTS = ["time", "calls", "slowest", "errors"] as const;

export interface ApmServiceSearch {
  tab?: ApmTab;
  /** service.namespace / deployment.environment (omitted = all) */
  ns?: string;
  env?: string;
  /** selected transaction and transaction list sort */
  txn?: string;
  tsort?: (typeof TX_SORTS)[number];
  /** selected error group id */
  group?: string;
  dsort?: (typeof DB_SORTS)[number];
  /** trace search filters */
  qtxn?: string;
  qmin?: string;
  qmax?: string;
  qerr?: boolean;
  qattr?: string;
  qsort?: "timestamp" | "duration";
  /** error inbox filters: status tab, assignee (any | me | none | user id), search, sort */
  estatus?: (typeof ERROR_STATUS_PARAMS)[number];
  eassignee?: string;
  eq?: string;
  esort?: (typeof ERROR_SORT_PARAMS)[number];
}

export const ERROR_STATUS_PARAMS = ["unresolved", "resolved", "ignored", "all"] as const;
export const ERROR_SORT_PARAMS = ["count", "last_seen", "first_seen"] as const;

const oneOf =<T extends string>(values: readonly T[], v: unknown): T | undefined => ((values as readonly string[]).includes(String(v)) ? (v as T) : undefined);

const apmServiceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/apm/services/$service",
  validateSearch: (s: Record<string, unknown>): ApmServiceSearch => ({
    tab: oneOf(APM_TABS, s.tab),
    ns: str(s.ns),
    env: str(s.env),
    txn: str(s.txn),
    tsort: oneOf(TX_SORTS, s.tsort),
    group: str(s.group),
    dsort: oneOf(DB_SORTS, s.dsort),
    qtxn: str(s.qtxn),
    qmin: str(s.qmin),
    qmax: str(s.qmax),
    qerr: s.qerr === true || s.qerr === "true" ? true : undefined,
    qattr: str(s.qattr),
    qsort: oneOf(["timestamp", "duration"] as const, s.qsort),
    estatus: oneOf(ERROR_STATUS_PARAMS, s.estatus),
    eassignee: str(s.eassignee),
    eq: str(s.eq),
    esort: oneOf(ERROR_SORT_PARAMS, s.esort),
  }),
  component: ApmServicePage,
});

export interface ApmMapSearch {
  /** whole-map environment / namespace filter */
  env?: string;
  ns?: string;
  /** highlighted transaction path: service and transaction name */
  hsvc?: string;
  htxn?: string;
}

const apmMapRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/apm/map",
  validateSearch: (s: Record<string, unknown>): ApmMapSearch => ({ env: str(s.env), ns: str(s.ns), hsvc: str(s.hsvc), htxn: str(s.htxn) }),
  component: ApmMapPage,
});

/** Organization-wide error inbox (routes/apm-errors.tsx). */
export interface ApmErrorsSearch {
  svc?: string;
  ns?: string;
  env?: string;
  estatus?: (typeof ERROR_STATUS_PARAMS)[number];
  eassignee?: string;
  eq?: string;
  esort?: (typeof ERROR_SORT_PARAMS)[number];
}

const apmErrorsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/apm/errors",
  validateSearch: (s: Record<string, unknown>): ApmErrorsSearch => ({
    svc: str(s.svc),
    ns: str(s.ns),
    env: str(s.env),
    estatus: oneOf(ERROR_STATUS_PARAMS, s.estatus),
    eassignee: str(s.eassignee),
    eq: str(s.eq),
    esort: oneOf(ERROR_SORT_PARAMS, s.esort),
  }),
  component: ApmErrorsPage,
});

export interface InventorySearchSearch {
  category?: string;
  q?: string;
}

const inventorySearchRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/inventory",
  validateSearch: (s: Record<string, unknown>): InventorySearchSearch => ({ category: str(s.category), q: str(s.q) }),
  component: InventorySearchPage,
});

export interface FleetSearch {
  q?: string;
  version?: string;
  state?: string;
}

const fleetRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/fleet",
  validateSearch: (s: Record<string, unknown>): FleetSearch => ({ q: str(s.q), version: str(s.version), state: str(s.state) }),
  component: FleetPage,
});

// ---- Alerting (routes/alerts.tsx) ----

const alertsRoute = createRoute({ getParentRoute: () => appRoute, path: "/alerts", component: AlertsLayout });
const alertsIndexRoute = createRoute({
  getParentRoute: () => alertsRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/alerts/incidents" });
  },
});

export interface AlertIncidentsSearch {
  /** open | acknowledged | resolved | all (default open,acknowledged) */
  state?: string;
  severity?: string;
}

const alertsIncidentsRoute = createRoute({
  getParentRoute: () => alertsRoute,
  path: "/incidents",
  validateSearch: (s: Record<string, unknown>): AlertIncidentsSearch => ({ state: str(s.state), severity: str(s.severity) }),
  component: AlertsIncidentsPage,
});
const alertsIncidentRoute = createRoute({ getParentRoute: () => alertsRoute, path: "/incidents/$incidentId", component: AlertsIncidentPage });
const alertsRulesRoute = createRoute({ getParentRoute: () => alertsRoute, path: "/rules", component: AlertsRulesPage });

/** Prefill of a new rule (lib/alerts.ts RuleEditorSearch), e.g. from a host chart. */
export interface AlertRuleNewSearch {
  type?: string;
  metric?: string;
  host?: string;
  hostName?: string;
  agg?: string;
  seriesAgg?: string;
  groupBy?: string;
  exclude?: string;
  name?: string;
  filters?: string;
  operator?: string;
  threshold?: string;
  window?: string;
  forSeconds?: string;
  severity?: string;
  /** Recommended template id and its render params (JSON), see components/alerts/TemplateGallery. */
  template?: string;
  tparams?: string;
}

const alertsRuleNewRoute = createRoute({
  getParentRoute: () => alertsRoute,
  path: "/rules/new",
  validateSearch: (s: Record<string, unknown>): AlertRuleNewSearch => ({
    type: str(s.type),
    metric: str(s.metric),
    host: str(s.host),
    hostName: str(s.hostName),
    agg: str(s.agg),
    seriesAgg: str(s.seriesAgg),
    groupBy: typeof s.groupBy === "string" ? s.groupBy : undefined,
    exclude: str(s.exclude),
    name: str(s.name),
    filters: str(s.filters),
    operator: str(s.operator),
    threshold: str(s.threshold),
    window: str(s.window),
    forSeconds: str(s.forSeconds),
    severity: str(s.severity),
    template: str(s.template),
    tparams: str(s.tparams),
  }),
  component: AlertsRuleNewPage,
});

/** Recommended alert templates, optionally for one host or APM service (routes/alerts.tsx). */
export interface AlertTemplatesSearch {
  category?: "host" | "container" | "apm" | "integration" | "kubernetes";
  integration?: string;
  host?: string;
  hostName?: string;
  service?: string;
}

const alertsTemplatesRoute = createRoute({
  getParentRoute: () => alertsRoute,
  path: "/templates",
  validateSearch: (s: Record<string, unknown>): AlertTemplatesSearch => ({
    category: oneOf(["host", "container", "apm", "integration", "kubernetes"] as const, s.category),
    integration: str(s.integration),
    host: str(s.host),
    hostName: str(s.hostName),
    service: str(s.service),
  }),
  component: AlertsTemplatesPage,
});
const alertsRuleRoute = createRoute({ getParentRoute: () => alertsRoute, path: "/rules/$ruleId", component: AlertsRuleEditPage });
const alertsChannelsRoute = createRoute({ getParentRoute: () => alertsRoute, path: "/channels", component: AlertsChannelsPage });
const alertsMutesRoute = createRoute({ getParentRoute: () => alertsRoute, path: "/mutes", component: AlertsMutesPage });

// ---- Query console and dashboards (routes/query.tsx, routes/dashboards.tsx; docs/contracts/oql.md) ----

export interface QuerySearch {
  /** OQL query text (shared/reloaded with the URL) */
  q?: string;
  view?: "chart" | "table";
}

const queryRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/query",
  validateSearch: (s: Record<string, unknown>): QuerySearch => ({
    q: typeof s.q === "string" && s.q.trim() !== "" ? s.q.slice(0, 8192) : undefined,
    view: oneOf(["chart", "table"] as const, s.view),
  }),
  component: QueryPage,
});

export interface DashboardsSearch {
  q?: string;
}

const dashboardsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/dashboards",
  validateSearch: (s: Record<string, unknown>): DashboardsSearch => ({ q: str(s.q) }),
  component: DashboardsPage,
});

export interface DashboardSearch {
  page?: string;
  /** Selected variable values by variable name (lib/dashboards.ts sanitizeVarsSearch). */
  vars?: Record<string, string[]>;
  edit?: boolean;
  /** Cross-widget filters (lib/dashboard-filters.ts sanitizeFiltersSearch). */
  filters?: DashboardFilter[];
}

const dashboardRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/dashboards/$dashboardId",
  validateSearch: (s: Record<string, unknown>): DashboardSearch => ({
    page: str(s.page),
    vars: sanitizeVarsSearch(s.vars),
    filters: sanitizeFiltersSearch(s.filters),
    edit: s.edit === true || s.edit === "true" ? true : undefined,
  }),
  component: DashboardPage,
});

// ---- Add data (routes/add-data.tsx): guided agent installs, deep links such as /add-data/linux, /add-data/apm/node ----

const AddDataPage = lazyRouteComponent(() => import("@/routes/add-data"), "AddDataPage");
const AddDataTargetPage = lazyRouteComponent(() => import("@/routes/add-data"), "AddDataTargetPage");

export interface AddDataSearch {
  /** Prefill from a host's Services tab: host id, host name and discovered service name. */
  host?: string;
  hostName?: string;
  service?: string;
}

const addDataRoute = createRoute({ getParentRoute: () => appRoute, path: "/add-data", component: AddDataPage });
const addDataTargetRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/add-data/$",
  validateSearch: (s: Record<string, unknown>): AddDataSearch => ({ host: str(s.host), hostName: str(s.hostName), service: str(s.service) }),
  component: AddDataTargetPage,
});

// ---- SaaS operator console (routes/operator.tsx; superadmins only, D-105) ----
const operatorRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/operator",
  component: lazyRouteComponent(() => import("@/routes/operator"), "OperatorPage"),
});
const operatorOrgRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/operator/orgs/$orgId",
  component: lazyRouteComponent(() => import("@/routes/operator"), "OperatorOrgPage"),
});

const settingsRoute = createRoute({ getParentRoute: () => appRoute, path: "/settings", component: SettingsLayout });
const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/settings/organization" });
  },
});
const settingsOrganizationRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/organization", component: OrganizationSettingsPage });
const settingsProfileRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/profile", component: ProfileSettingsPage });
const settingsMembersRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/members", component: MembersSettingsPage });
const settingsLicenseKeysRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/license-keys", component: LicenseKeysSettingsPage });
const settingsApiKeysRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/api-keys", component: ApiKeysSettingsPage });
const settingsSecurityRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/security", component: SecuritySettingsPage });
const settingsAuditLogRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/audit-log", component: AuditLogSettingsPage });
const settingsTailSamplingRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/apm-sampling", component: TailSamplingSettingsPage });
const settingsUsageRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/usage", component: UsageSettingsPage });
// Single sign-on settings and the public domain verification link (components/settings/Sso*.tsx, D-077).
const SsoSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "SsoSettingsPage");
const settingsSsoRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/sso",
  // Set by the server when a test sign-in returns (components/settings/SsoSettings).
  validateSearch: (s: Record<string, unknown>): { sso_test?: "ok" | "failed" } => ({ sso_test: s.sso_test === "ok" || s.sso_test === "failed" ? s.sso_test : undefined }),
  component: SsoSettingsPage,
});
const ssoVerifyDomainRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/sso/verify-domain",
  component: lazyRouteComponent(() => import("@/components/settings/SsoVerifyDomain"), "SsoVerifyDomainPage"),
});
// Public read-only dashboard share links: no session and no app navigation (routes/shared-dashboard.tsx, D-087).
// Public status page: no session and no app navigation (routes/status.tsx, D-108).
const statusRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/status",
  component: lazyRouteComponent(() => import("@/routes/status"), "StatusPage"),
});
// Incidents and maintenance of the status page (superadmins; components/settings/StatusPageSettings.tsx).
const settingsStatusPageRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/status-page",
  component: lazyRouteComponent(() => import("@/routes/settings"), "StatusPageSettingsPage"),
});
const sharedDashboardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/shared/dashboards/$token",
  component: lazyRouteComponent(() => import("@/routes/shared-dashboard"), "SharedDashboardPage"),
});
// Report print view for openlog-renderer: render token in the URL fragment, no session (routes/print-dashboard.tsx, D-097).
const printDashboardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/print/dashboard",
  component: lazyRouteComponent(() => import("@/routes/print-dashboard"), "PrintDashboardPage"),
});

export const routeTree = rootRoute.addChildren([
  loginRoute,
  inviteRoute,
  signupRoute,
  verifyEmailRoute,
  ssoVerifyDomainRoute,
  sharedDashboardRoute,
  printDashboardRoute,
  statusRoute,
  appRoute.addChildren([
    indexRoute,
    hostsRoute,
    hostDetailRoute,
    containersRoute,
    containerDetailRoute,
    kubernetesRoute,
    kubernetesWorkloadsRoute,
    kubernetesWorkloadRoute,
    kubernetesPodsRoute,
    kubernetesPodRoute,
    kubernetesNodesRoute,
    hostIntegrationRoute,
    integrationsRoute,
    apmServicesRoute,
    apmServiceRoute,
    apmMapRoute,
    apmErrorsRoute,
    logsRoute,
    traceRoute,
    inventorySearchRoute,
    fleetRoute,
    queryRoute,
    dashboardsRoute,
    dashboardRoute,
    addDataRoute,
    addDataTargetRoute,
    operatorRoute,
    operatorOrgRoute,
    alertsRoute.addChildren([
      alertsIndexRoute,
      alertsIncidentsRoute,
      alertsIncidentRoute,
      alertsRulesRoute,
      alertsRuleNewRoute,
      alertsRuleRoute,
      alertsTemplatesRoute,
      alertsChannelsRoute,
      alertsMutesRoute,
    ]),
    settingsRoute.addChildren([
      settingsIndexRoute,
      settingsProfileRoute,
      settingsOrganizationRoute,
      settingsMembersRoute,
      settingsLicenseKeysRoute,
      settingsApiKeysRoute,
      settingsSecurityRoute,
      settingsAuditLogRoute,
      settingsTailSamplingRoute,
      settingsUsageRoute,
      settingsSsoRoute,
      settingsStatusPageRoute,
    ]),
  ]),
]);

export function buildRouter(queryClient: QueryClient) {
  return createRouter({
    routeTree,
    context: { queryClient },
    defaultPreload: "intent",
    defaultPendingComponent: () => <LoadingState />,
    defaultPreloadStaleTime: 0,
    scrollRestoration: true,
  });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof buildRouter>;
  }
}
