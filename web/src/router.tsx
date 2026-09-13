// Code-based route tree. Every route inherits the time range search params
// (`range`, `from`, `to`) from the root; screens add their own filters, so all
// filters live in the URL.
import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, createRoute, createRouter, lazyRouteComponent, Outlet, redirect } from "@tanstack/react-router";
import { meQuery } from "@/api/account";
import { ApiError } from "@/api/client";
import { AppShell } from "@/components/AppShell";
import { LoadingState } from "@/components/StateViews";
import { NotFoundPage } from "@/routes/not-found";
import { LoginPage } from "@/routes/login";
import { validateRangeSearch, type RangeSpec } from "@/lib/time";

// Screens are code-split per route (uPlot only loads with host detail).
const HostsPage = lazyRouteComponent(() => import("@/routes/hosts"), "HostsPage");
const HostDetailPage = lazyRouteComponent(() => import("@/routes/host-detail"), "HostDetailPage");
const LogsPage = lazyRouteComponent(() => import("@/routes/logs"), "LogsPage");
const TracePage = lazyRouteComponent(() => import("@/routes/trace"), "TracePage");
const InventorySearchPage = lazyRouteComponent(() => import("@/routes/inventory-search"), "InventorySearchPage");
const InvitePage = lazyRouteComponent(() => import("@/routes/invite"), "InvitePage");
const FleetPage = lazyRouteComponent(() => import("@/routes/fleet"), "FleetPage");
const ApmServicesPage = lazyRouteComponent(() => import("@/routes/apm"), "ApmServicesPage");
const ApmServicePage = lazyRouteComponent(() => import("@/routes/apm"), "ApmServicePage");
const ApmMapPage = lazyRouteComponent(() => import("@/routes/apm"), "ApmMapPage");
const AlertsLayout = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsLayout");
const AlertsIncidentsPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsIncidentsPage");
const AlertsIncidentPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsIncidentPage");
const AlertsRulesPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsRulesPage");
const AlertsRuleNewPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsRuleNewPage");
const AlertsRuleEditPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsRuleEditPage");
const AlertsChannelsPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsChannelsPage");
const AlertsMutesPage = lazyRouteComponent(() => import("@/routes/alerts"), "AlertsMutesPage");
const SettingsLayout = lazyRouteComponent(() => import("@/routes/settings"), "SettingsLayout");
const OrganizationSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "OrganizationSettingsPage");
const MembersSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "MembersSettingsPage");
const LicenseKeysSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "LicenseKeysSettingsPage");
const ApiKeysSettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "ApiKeysSettingsPage");
const SecuritySettingsPage = lazyRouteComponent(() => import("@/routes/settings"), "SecuritySettingsPage");

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
}

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (s: Record<string, unknown>): LoginSearch => ({
    redirect: str(s.redirect)?.startsWith("/") ? str(s.redirect) : undefined,
    expired: s.expired === true || s.expired === "true" || s.expired === 1 ? true : undefined,
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

export const HOST_TABS = ["overview", "services", "inventory", "logs"] as const;
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

export interface LogsSearch {
  q?: string;
  severity?: string;
  service?: string;
  host?: string;
  trace?: string;
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
}

const oneOf = <T extends string>(values: readonly T[], v: unknown): T | undefined => ((values as readonly string[]).includes(String(v)) ? (v as T) : undefined);

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
  }),
  component: ApmServicePage,
});

const apmMapRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/apm/map",
  component: ApmMapPage,
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
  }),
  component: AlertsRuleNewPage,
});
const alertsRuleRoute = createRoute({ getParentRoute: () => alertsRoute, path: "/rules/$ruleId", component: AlertsRuleEditPage });
const alertsChannelsRoute = createRoute({ getParentRoute: () => alertsRoute, path: "/channels", component: AlertsChannelsPage });
const alertsMutesRoute = createRoute({ getParentRoute: () => alertsRoute, path: "/mutes", component: AlertsMutesPage });

const settingsRoute = createRoute({ getParentRoute: () => appRoute, path: "/settings", component: SettingsLayout });
const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/settings/organization" });
  },
});
const settingsOrganizationRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/organization", component: OrganizationSettingsPage });
const settingsMembersRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/members", component: MembersSettingsPage });
const settingsLicenseKeysRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/license-keys", component: LicenseKeysSettingsPage });
const settingsApiKeysRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/api-keys", component: ApiKeysSettingsPage });
const settingsSecurityRoute = createRoute({ getParentRoute: () => settingsRoute, path: "/security", component: SecuritySettingsPage });

export const routeTree = rootRoute.addChildren([
  loginRoute,
  inviteRoute,
  appRoute.addChildren([
    indexRoute,
    hostsRoute,
    hostDetailRoute,
    apmServicesRoute,
    apmServiceRoute,
    apmMapRoute,
    logsRoute,
    traceRoute,
    inventorySearchRoute,
    fleetRoute,
    alertsRoute.addChildren([
      alertsIndexRoute,
      alertsIncidentsRoute,
      alertsIncidentRoute,
      alertsRulesRoute,
      alertsRuleNewRoute,
      alertsRuleRoute,
      alertsChannelsRoute,
      alertsMutesRoute,
    ]),
    settingsRoute.addChildren([
      settingsIndexRoute,
      settingsOrganizationRoute,
      settingsMembersRoute,
      settingsLicenseKeysRoute,
      settingsApiKeysRoute,
      settingsSecurityRoute,
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
