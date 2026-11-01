// APM screens: services list (/apm), service page (/apm/services/$service) and service map (/apm/map).
// Filters, tabs and selections live in the URL (router.tsx validateSearch).
import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Bug, Network, PackageCheck, Search } from "lucide-react";
import { lazy, Suspense, useEffect, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { apmAgentsQuery, apmMapPathQuery, apmMapQuery, apmServiceQuery, apmServicesPageQuery, apmServicesQuery, type ServiceSort } from "@/api/apm";
import { AgentVersionBadge } from "@/components/apm/AgentVersions";
import { indexAgents, serviceKey } from "@/lib/agent-versions";
import { ApiError } from "@/api/client";
import { ApdexSettings } from "@/components/apm/ApdexSettings";
import { ApdexBadge, Sparkline } from "@/components/apm/Charts";
import { DatabasesTab } from "@/components/apm/DatabasesTab";
import { ErrorsTab } from "@/components/apm/ErrorsTab";
import { MapPathControl } from "@/components/apm/MapPathControl";
import { layoutStorageKey } from "@/lib/apm-map";
import { OverviewTab } from "@/components/apm/OverviewTab";
import { TracesTab } from "@/components/apm/TracesTab";
import { TransactionsTab } from "@/components/apm/TransactionsTab";
import { ServiceContainers } from "@/components/containers/ServiceContainers";
import { ServiceKubernetesPods } from "@/components/kubernetes/ServiceKubernetesPods";
import { PageHeader } from "@/components/AppShell";
import { ListPager } from "@/components/ListPager";
import { AddDataLink } from "@/components/onboarding/AddDataLink";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatMs, formatRate, formatRpm, serviceNodeId, type ServiceScope } from "@/lib/apm";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { useListState } from "@/lib/list";
import { parseTimeParam, type RangeSpec } from "@/lib/time";
import { APM_TABS, type ApmMapSearch, type ApmServiceSearch, type ApmTab } from "@/router";
import type { ServiceMapProps } from "@/components/apm/ServiceMap";

// React Flow + dagre (~80 kB gzip) load only when a map is shown, not with every APM screen.
const LazyServiceMap = lazy(() => import("@/components/apm/ServiceMap").then((m) => ({ default: m.ServiceMap })));

function ServiceMap(props: ServiceMapProps) {
  return (
    <Suspense fallback={<LoadingState />}>
      <LazyServiceMap {...props} />
    </Suspense>
  );
}

const servicesRoute = getRouteApi("/app/apm");
const serviceRoute = getRouteApi("/app/apm/services/$service");
const mapRoute = getRouteApi("/app/apm/map");

// Every keystroke of a search would be a round of statements over the whole range.
const SEARCH_DELAY_MS = 250;

function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return settled;
}

export function ApmServicesPage() {
  const search = servicesRoute.useSearch();
  const navigate = useNavigate({ from: "/apm" });
  return (
    <ApmServicesView
      range={{ range: search.range, from: search.from, to: search.to }}
      q={search.q ?? ""}
      env={search.env}
      onQ={(q) => void navigate({ search: (prev) => ({ ...prev, q: q || undefined }), replace: true })}
      onEnv={(env) => void navigate({ search: (prev) => ({ ...prev, env: env || undefined }), replace: true })}
    />
  );
}

export function ApmServicesView({ range, q, env, onQ, onEnv }: { range: RangeSpec; q: string; env?: string; onQ: (q: string) => void; onEnv: (env: string) => void }) {
  const { t, i18n } = useTranslation();
  const ids = { q: useId(), env: useId() };
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  const list = useListState<ServiceSort>("name", "asc", 50, "apm");
  const settledQ = useDebounced(q, SEARCH_DELAY_MS);
  const page = useQuery(apmServicesPageQuery(range, list.state, { q: settledQ, environment: env }));
  // Agent version badges (D-124): no upgrade instructions needed here, so no registry checks.
  const agents = useQuery(apmAgentsQuery(range, { upgrade: false }));
  const agentIndex = useMemo(() => indexAgents(agents.data?.services ?? []), [agents.data]);
  const environments = page.data?.environments ?? [];
  const services = page.data?.services ?? [];
  const { toFirstPage } = list;
  // New filters are a new list, so page five of it would usually show nothing.
  useEffect(toFirstPage, [settledQ, env, range.range, range.from, range.to, toFirstPage]);

  return (
    <div>
      <PageHeader
        title={t("apm.title")}
        subtitle={t("apm.subtitle")}
        actions={
          <div className="contents">
            <label htmlFor={ids.env} className="sr-only">
              {t("apm.environment")}
            </label>
            <NativeSelect id={ids.env} value={env ?? ""} onChange={(e) => onEnv(e.target.value)}>
              <option value="">{t("apm.allEnvironments")}</option>
              {environments.map((e) => (
                <option key={e} value={e}>
                  {e}
                </option>
              ))}
            </NativeSelect>
            <div className="relative w-full sm:w-64">
              <label htmlFor={ids.q} className="sr-only">
                {t("apm.searchLabel")}
              </label>
              <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden="true" />
              <Input
                id={ids.q}
                type="search"
                className="pl-8"
                placeholder={t("apm.searchPlaceholder")}
                value={q}
                onChange={(e) => onQ(e.target.value)}
              />
            </div>
            <Button asChild variant="outline">
              <Link to="/apm/map" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}>
                <Network className="size-4" aria-hidden="true" />
                {t("apm.serviceMap")}
              </Link>
            </Button>
            <Button asChild variant="outline">
              <Link to="/apm/errors" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, env })}>
                <Bug className="size-4" aria-hidden="true" />
                {t("apm.errors.inboxLink")}
              </Link>
            </Button>
            <Button asChild variant="outline">
              <Link to="/apm/agents" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, env })}>
                <PackageCheck className="size-4" aria-hidden="true" />
                {t("apm.agents.link")}
              </Link>
            </Button>
          </div>
        }
      />
      <div className="rounded-xl border bg-card">
        {page.isPending ? (
          <LoadingState />
        ) : page.isError ? (
          <ErrorState error={page.error} onRetry={() => void page.refetch()} />
        ) : services.length === 0 && list.state.offset === 0 ? (
          q || env ? (
            <EmptyState>{t("apm.noMatch", { q })}</EmptyState>
          ) : (
            <EmptyState>
              <p>{t("apm.empty")}</p>
              <AddDataLink label={t("addData.empty.apm")} />
            </EmptyState>
          )
        ) : (
          <>
            <Table data-testid="apm-services">
              <TableHeader>
                <TableRow>
                  <SortableHead column="name" defaultDir="asc" {...list.head}>{t("apm.columns.service")}</SortableHead>
                  <SortableHead column="environment" defaultDir="asc" className="hidden md:table-cell" {...list.head}>{t("apm.columns.environment")}</SortableHead>
                  <SortableHead column="throughput" className="text-right" {...list.head}>{t("apm.metrics.throughput")}</SortableHead>
                  <SortableHead column="error_rate" className="text-right" {...list.head}>{t("apm.metrics.errorRate")}</SortableHead>
                  <SortableHead column="p95" className="text-right" {...list.head}>{t("apm.metrics.p95")}</SortableHead>
                  <SortableHead column="apdex" className="text-right" {...list.head}>{t("apm.metrics.apdex")}</SortableHead>
                  <TableHead className="hidden lg:table-cell">{t("apm.columns.trend")}</TableHead>
                  <SortableHead column="last_seen" className="hidden md:table-cell" {...list.head}>{t("apm.columns.lastSeen")}</SortableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {services.map((s) => {
                  const seen = parseTimeParam(s.last_seen) ?? 0;
                  const linkSearch = (prev: Record<string, unknown>) =>
                    ({ range: prev.range, from: prev.from, to: prev.to, ns: s.service_namespace || undefined, env: s.environment || undefined }) as never;
                  return (
                    <TableRow key={`${s.service_name}|${s.service_namespace}|${s.environment}`}>
                      <TableCell>
                        <div className="flex flex-wrap items-center gap-1.5">
                          <Link
                            to="/apm/services/$service"
                            params={{ service: s.service_name }}
                            search={linkSearch}
                            className="font-medium hover:underline"
                            aria-label={t("apm.openService", { name: s.service_name })}
                          >
                            {s.service_name}
                          </Link>
                          <AgentVersionBadge service={agentIndex.get(serviceKey(s))} latest={agents.data?.release.latest} />
                        </div>
                        <div className="text-xs text-muted-foreground">{[s.language, s.version].filter(Boolean).join(" · ")}</div>
                      </TableCell>
                      <TableCell className="hidden md:table-cell">
                        <span className="text-sm">{s.environment || "–"}</span>
                        {s.service_namespace && <div className="text-xs text-muted-foreground">{s.service_namespace}</div>}
                      </TableCell>
                      <TableCell className="text-right font-mono tabular-nums">{formatRpm(s.throughput, locale)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums">{formatRate(s.error_rate, locale)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums">{formatMs(s.p95_ms, locale)}</TableCell>
                      <TableCell className="text-right">
                        <ApdexBadge apdex={s.apdex} tMs={s.apdex_t_ms} />
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        <Sparkline points={s.sparkline} label={t("apm.columns.trend")} />
                      </TableCell>
                      <TableCell className="hidden whitespace-nowrap md:table-cell">
                        <time dateTime={s.last_seen} title={formatDateTime(seen, locale)}>
                          {formatRelative(seen, now, locale)}
                        </time>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
            <ListPager list={list} count={services.length} hasMore={page.data.has_more ?? false} />
          </>
        )}
      </div>
    </div>
  );
}

export function ApmServicePage() {
  const { t } = useTranslation();
  const { service } = serviceRoute.useParams();
  const search = serviceRoute.useSearch();
  const navigate = useNavigate({ from: "/apm/services/$service" });
  const scope: ServiceScope = useMemo(() => ({ service, namespace: search.ns, environment: search.env }), [service, search.ns, search.env]);
  const range = useMemo(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const detail = useQuery(apmServiceQuery(scope));
  const tab: ApmTab = search.tab ?? "overview";
  const setSearch = (patch: Partial<ApmServiceSearch>, replace = false) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace });

  if (detail.isPending) return <LoadingState />;
  if (detail.isError) {
    if (detail.error instanceof ApiError && detail.error.status === 404) {
      return (
        <EmptyState>
          <p className="mb-2 font-medium text-foreground">{t("apm.service.notFound")}</p>
          <Link to="/apm" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })} className="text-primary hover:underline">
            {t("apm.service.back")}
          </Link>
        </EmptyState>
      );
    }
    return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  }
  const d = detail.data;
  const instance = d.instances[0];

  return (
    <div className="flex flex-col gap-4">
      <div>
        <Link to="/apm" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })} className="mb-2 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
          <ArrowLeft className="size-3" aria-hidden="true" />
          {t("apm.service.back")}
        </Link>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <h1 className="text-xl font-semibold tracking-tight">{d.service_name}</h1>
          {search.env && <Badge variant="secondary">{search.env}</Badge>}
          {search.ns && <Badge variant="outline">{search.ns}</Badge>}
          {instance?.language && <Badge variant="muted">{instance.language}</Badge>}
          {instance?.version && <span className="font-mono text-xs text-muted-foreground">v{instance.version}</span>}
          <ApdexSettings scope={scope} />
        </div>
        <dl className="mt-2 flex flex-wrap items-center gap-x-6 gap-y-1 text-xs">
          <div className="flex flex-wrap items-center gap-1.5">
            <dt className="text-muted-foreground">{t("apm.service.hosts")}</dt>
            <dd className="flex flex-wrap gap-1.5" data-testid="service-hosts">
              {d.hosts.length === 0 ? (
                <span className="text-muted-foreground">{t("apm.service.noHosts")}</span>
              ) : (
                d.hosts.map((h) =>
                  h.known ? (
                    <Link
                      key={h.host_id}
                      to="/hosts/$hostId"
                      params={{ hostId: h.host_id }}
                      search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}
                      className="rounded-md border px-1.5 py-0.5 font-mono hover:border-primary"
                      aria-label={t("apm.service.openHost", { name: h.host_name || h.host_id })}
                    >
                      {h.host_name || h.host_id}
                    </Link>
                  ) : (
                    <span key={h.host_id} className="rounded-md border border-dashed px-1.5 py-0.5 font-mono text-muted-foreground" title={t("apm.service.hostUnknown")}>
                      {h.host_name || h.host_id}
                    </span>
                  ),
                )
              )}
            </dd>
          </div>
          <ServiceContainers scope={scope} range={range} />
          <ServiceKubernetesPods scope={scope} range={range} />
          {d.instances.length > 1 && (
            <div className="flex gap-1.5">
              <dt className="text-muted-foreground">{t("apm.service.environments")}</dt>
              <dd>{d.instances.map((i) => i.environment || "–").join(", ")}</dd>
            </div>
          )}
        </dl>
      </div>
      <Tabs value={tab} onValueChange={(v) => setSearch({ tab: (APM_TABS as readonly string[]).includes(v) ? (v as ApmTab) : undefined }, true)}>
        <TabsList>
          {APM_TABS.map((v) => (
            <TabsTrigger key={v} value={v}>
              {t(`apm.service.tabs.${v}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="overview">
          {tab === "overview" && (
            <OverviewTab
              scope={scope}
              range={range}
              onOpenTransaction={(name) => setSearch({ tab: "transactions", txn: name })}
              onViewAll={() => setSearch({ tab: "transactions" })}
              onOpenErrorGroup={(group) => setSearch({ tab: "errors", group })}
            />
          )}
        </TabsContent>
        <TabsContent value="transactions">
          {tab === "transactions" && (
            <TransactionsTab
              scope={scope}
              range={range}
              sort={search.tsort ?? "time"}
              selected={search.txn}
              onSort={(tsort) => setSearch({ tsort }, true)}
              onSelect={(txn) => setSearch({ txn })}
              onShowTraces={(name) => setSearch({ tab: "traces", qtxn: name, qsort: "duration" })}
            />
          )}
        </TabsContent>
        <TabsContent value="errors">
          {tab === "errors" && (
            <ErrorsTab
              scope={scope}
              range={range}
              selected={search.group}
              onSelect={(group) => setSearch({ group })}
              filters={{ status: search.estatus ?? "unresolved", assignee: search.eassignee ?? "any", q: search.eq ?? "", sort: search.esort ?? "count" }}
              onFilters={(p) =>
                setSearch(
                  {
                    ...(p.status !== undefined ? { estatus: p.status === "unresolved" ? undefined : p.status } : {}),
                    ...(p.assignee !== undefined ? { eassignee: p.assignee === "any" ? undefined : p.assignee } : {}),
                    ...(p.q !== undefined ? { eq: p.q || undefined } : {}),
                    ...(p.sort !== undefined ? { esort: p.sort === "count" ? undefined : p.sort } : {}),
                  },
                  true,
                )
              }
              onOpenTransaction={(txn) => setSearch({ tab: "transactions", txn })}
            />
          )}
        </TabsContent>
        <TabsContent value="databases">{tab === "databases" && <DatabasesTab scope={scope} range={range} sort={search.dsort ?? "time"} onSort={(dsort) => setSearch({ dsort }, true)} />}</TabsContent>
        <TabsContent value="map">{tab === "map" && <ServiceMapTab scope={scope} range={range} />}</TabsContent>
        <TabsContent value="traces">
          {tab === "traces" && (
            <TracesTab
              key={JSON.stringify([search.qtxn, search.qmin, search.qmax, search.qerr, search.qattr])}
              scope={scope}
              range={range}
              filters={{ qtxn: search.qtxn, qmin: search.qmin, qmax: search.qmax, qerr: search.qerr, qattr: search.qattr, qsort: search.qsort }}
              onChange={(f) => setSearch({ ...f })}
            />
          )}
        </TabsContent>
      </Tabs>
    </div>
  );
}

function ServiceMapTab({ scope, range }: { scope: ServiceScope; range: { range?: string; from?: string; to?: string } }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const me = useMe();
  const q = useQuery(apmMapQuery(range, scope));
  const [txn, setTxn] = useState("");
  const path = useQuery(apmMapPathQuery(range, scope, txn));
  const focusId = serviceNodeId(scope.service, scope.namespace ?? "", scope.environment ?? "");
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <MapPathControl
          range={range}
          service={scope.service}
          transaction={txn}
          namespace={scope.namespace}
          environment={scope.environment}
          onChange={(v) => setTxn(v.transaction)}
          path={txn ? path.data : undefined}
          loading={!!txn && path.isFetching && !path.data}
        />
        <Link to="/apm/map" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })} className="text-xs text-primary hover:underline">
          {t("apm.map.full")}
        </Link>
      </div>
      {q.isPending ? (
        <LoadingState />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <ServiceMap
          data={q.data}
          focusId={focusId}
          path={txn ? path.data : null}
          layoutKey={me.data ? layoutStorageKey(me.data.user?.id ?? me.data.auth, { focusId }) : undefined}
          onOpenService={(s) =>
            void navigate({
              to: "/apm/services/$service",
              params: { service: s.name },
              search: (prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to, ns: s.namespace || undefined, env: s.environment || undefined, tab: "map" }) as never,
            })
          }
        />
      )}
    </div>
  );
}

export function ApmMapPage() {
  const { t } = useTranslation();
  const search = mapRoute.useSearch();
  const navigate = useNavigate();
  const me = useMe();
  const ids = { env: useId(), ns: useId() };
  const range = useMemo(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const q = useQuery(apmMapQuery(range, undefined, { namespace: search.ns, environment: search.env }));
  const services = useQuery(apmServicesQuery(range));
  const list = services.data?.services ?? [];
  const uniq = (values: string[]) => [...new Set(values.filter(Boolean))].sort();
  const environments = uniq(list.map((s) => s.environment));
  const namespaces = uniq(list.map((s) => s.service_namespace));
  const serviceNames = uniq(list.filter((s) => (!search.env || s.environment === search.env) && (!search.ns || s.service_namespace === search.ns)).map((s) => s.service_name));
  const hsvc = search.hsvc ?? "";
  const htxn = search.htxn ?? "";
  const path = useQuery(apmMapPathQuery(range, { service: hsvc, namespace: search.ns, environment: search.env }, htxn));
  const setSearch = (patch: Partial<ApmMapSearch>) => void navigate({ to: "/apm/map", search: (prev: Record<string, unknown>) => ({ ...prev, ...patch }) as never, replace: true });
  const filterSelect = (id: string, key: "env" | "ns", label: string, all: string, values: string[]) => (
    <>
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      <NativeSelect id={id} value={search[key] ?? ""} className="max-w-[12rem]" onChange={(e) => setSearch({ [key]: e.target.value || undefined })}>
        <option value="">{all}</option>
        {values.map((v) => (
          <option key={v} value={v}>
            {v}
          </option>
        ))}
      </NativeSelect>
    </>
  );
  return (
    <div className="flex flex-col gap-3">
      <PageHeader
        title={t("apm.map.title")}
        subtitle={t("apm.map.subtitle")}
        actions={
          <div className="contents">
            {filterSelect(ids.env, "env", t("apm.environment"), t("apm.allEnvironments"), environments)}
            {filterSelect(ids.ns, "ns", t("apm.map.namespace"), t("apm.map.allNamespaces"), namespaces)}
          </div>
        }
      />
      <MapPathControl
        range={range}
        serviceOptions={serviceNames}
        service={hsvc}
        transaction={htxn}
        namespace={search.ns}
        environment={search.env}
        onChange={(v) => setSearch({ hsvc: v.service || undefined, htxn: v.transaction || undefined })}
        path={htxn ? path.data : undefined}
        loading={!!htxn && path.isFetching && !path.data}
      />
      {q.isPending ? (
        <LoadingState />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <ServiceMap
          data={q.data}
          height={600}
          path={hsvc && htxn ? path.data : null}
          layoutKey={me.data ? layoutStorageKey(me.data.user?.id ?? me.data.auth, { environment: search.env, namespace: search.ns }) : undefined}
          onOpenService={(s) =>
            void navigate({
              to: "/apm/services/$service",
              params: { service: s.name },
              search: (prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to, ns: s.namespace || undefined, env: s.environment || undefined }) as never,
            })
          }
        />
      )}
    </div>
  );
}
