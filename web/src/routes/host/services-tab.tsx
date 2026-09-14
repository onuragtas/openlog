import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { LineChart } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { hostQuery, inventoryQuery, servicesQuery } from "@/api/queries";
import type { DiscoveredService, InventoryItem } from "@/api/types";
import { IntegrationStatusBadge } from "@/components/integrations/StatusBadge";
import { ApmHintFooter } from "@/components/onboarding/ApmHintFooter";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { translateOptional } from "@/i18n/dynamic";
import { formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { hasPanel, integrationOf } from "@/lib/integrations";
import { formatPort } from "@/lib/ports";
import { parseTimeParam } from "@/lib/time";
import { cn } from "@/lib/utils";

const LANGUAGE_NAMES: Record<string, string> = {
  php: "PHP",
  java: "Java",
  jvm: "Java",
  node: "Node.js",
  nodejs: "Node.js",
  python: "Python",
  dotnet: ".NET",
  go: "Go",
  ruby: "Ruby",
};

export function asService(item: InventoryItem): DiscoveredService {
  return item.data && typeof item.data === "object" ? (item.data as DiscoveredService) : {};
}

function ServiceCard({ item, hostId }: { item: InventoryItem; hostId: string }) {
  const { t } = useTranslation();
  const s = asService(item);
  const integration = integrationOf(s);
  const status = integration.status;
  const panel = hasPanel(s) && !!s.rule_id && !!s.instance;
  const hint = s.apm_hint ?? null;
  const language = hint?.language ? (LANGUAGE_NAMES[hint.language.toLowerCase()] ?? hint.language) : "";
  const name = s.name || s.rule_id || item.key;
  // `command` (process name) is what operators recognise; `instance` (executable path) is secondary.
  const command = s.command?.trim() || "";

  return (
    <Card className="gap-3" data-testid="service-card">
      <CardHeader>
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <CardTitle>
              <h3 className="truncate text-base">{name}</h3>
            </CardTitle>
            <CardDescription>{s.category ? translateOptional(`services.category.${s.category}`, s.category) : t("common.unknown")}</CardDescription>
            {command && (
              <p className="mt-1 truncate font-mono text-sm font-medium text-foreground" title={`${t("services.command")}: ${command}`}>
                <span className="sr-only">{t("services.command")}: </span>
                {command}
              </p>
            )}
          </div>
          <IntegrationStatusBadge status={status} label={t(`services.integration.${status}`)} title={integration.error ?? t("services.integrationHint")} />
        </div>
      </CardHeader>
      <CardContent>
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
          <dt className="text-muted-foreground">{t("services.version")}</dt>
          <dd className="font-mono">{s.version || "–"}</dd>
          <dt className="text-muted-foreground">{t("services.ports")}</dt>
          <dd className="flex flex-wrap gap-1">
            {s.ports && s.ports.length > 0
              ? s.ports.map((p, i) => (
                  <Badge key={i} variant="outline" className="font-mono">
                    {formatPort(p)}
                  </Badge>
                ))
              : "–"}
          </dd>
          <dt className="text-muted-foreground">{t("services.matchedBy")}</dt>
          <dd className="flex flex-wrap gap-1">
            {(s.matched_by ?? []).map((m) => (
              <Badge key={m} variant="secondary">
                {m}
              </Badge>
            ))}
          </dd>
          {s.instance && (
            <>
              <dt className="text-muted-foreground">{t("services.instance")}</dt>
              <dd className={cn("truncate font-mono", command && "text-muted-foreground")} title={s.instance}>
                {s.instance}
              </dd>
            </>
          )}
          {s.systemd_units && s.systemd_units.length > 0 && (
            <>
              <dt className="text-muted-foreground">{t("services.units")}</dt>
              <dd className="font-mono">{s.systemd_units.join(", ")}</dd>
            </>
          )}
          {s.services && s.services.length > 0 && (
            <>
              <dt className="text-muted-foreground">{t("services.serviceNames")}</dt>
              <dd className="font-mono">{s.services.join(", ")}</dd>
            </>
          )}
          {s.pids && s.pids.length > 0 && (
            <>
              <dt className="text-muted-foreground">{t("services.pids")}</dt>
              <dd className="font-mono">{s.pids.join(", ")}</dd>
            </>
          )}
        </dl>
      </CardContent>
      {panel && (
        <CardFooter className={cn("flex-col items-stretch gap-2 pt-3", "border-t")}>
          <Link
            to="/hosts/$hostId/integrations/$discoveryId/$instance"
            params={{ hostId, discoveryId: s.rule_id!, instance: s.instance! }}
            className={buttonVariants({ variant: "outline", size: "sm", className: "min-h-10" })}
            data-testid="open-integration"
          >
            <LineChart aria-hidden="true" />
            {t("services.openIntegration")}
          </Link>
        </CardFooter>
      )}
      {hint && <ApmHintFooter hint={hint} hostId={hostId} serviceName={name} language={language} />}
    </Card>
  );
}

/** Service manager inventory of macOS (launchd_service) and Windows (windows_service) hosts (semantic-conventions §3.3). */
const SYSTEM_SERVICE_CATEGORIES: readonly string[] = ["launchd_service", "windows_service"];

interface SystemService {
  label?: string;
  name?: string;
  display_name?: string;
  state?: string;
  start_type?: string;
  pid?: number | null;
  last_exit_status?: number | null;
  program?: string;
  binary_path?: string;
  path?: string;
}

function SystemServices({ hostId }: { hostId: string }) {
  const { t } = useTranslation();
  const query = useQuery(inventoryQuery(hostId));
  const items = useMemo(() => (query.data?.items ?? []).filter((it) => SYSTEM_SERVICE_CATEGORIES.includes(it.category)), [query.data]);
  if (items.length === 0) return null;
  return (
    <section aria-labelledby="system-services-title" className="mt-6" data-testid="system-services">
      <h2 id="system-services-title" className="mb-3 text-base font-semibold">
        {t("services.systemServices")}
      </h2>
      <div className="rounded-xl border bg-card">
        <Table mobile="stack">
          <TableHeader>
            <TableRow>
              <TableHead>{t("services.systemColumns.name")}</TableHead>
              <TableHead>{t("services.systemColumns.state")}</TableHead>
              <TableHead>{t("services.systemColumns.startType")}</TableHead>
              <TableHead>{t("services.systemColumns.pid")}</TableHead>
              <TableHead>{t("services.systemColumns.program")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((it) => {
              const d = (it.data && typeof it.data === "object" ? it.data : {}) as SystemService;
              const name = d.label || d.name || it.key;
              const pid = typeof d.pid === "number" && d.pid > 0 ? d.pid : null;
              const state = d.state
                ? translateOptional(`services.states.${d.state}`, d.state.replace(/_/g, " "))
                : pid !== null
                  ? t("services.states.running")
                  : typeof d.last_exit_status === "number"
                    ? t("services.lastExit", { code: d.last_exit_status })
                    : t("services.loaded");
              const program = d.binary_path || d.program || d.path || "";
              return (
                <TableRow key={`${it.category}|${it.key}`}>
                  <TableCell className="max-w-72">
                    <span className="block truncate font-mono text-xs" title={name}>
                      {name}
                    </span>
                    {d.display_name && d.display_name !== name && <span className="block truncate text-xs text-muted-foreground">{d.display_name}</span>}
                  </TableCell>
                  <TableCell label={t("services.systemColumns.state")} className="text-xs">
                    {state}
                  </TableCell>
                  <TableCell label={t("services.systemColumns.startType")} className="text-xs">
                    {d.start_type ? translateOptional(`services.startTypes.${d.start_type}`, d.start_type) : "–"}
                  </TableCell>
                  <TableCell label={t("services.systemColumns.pid")} className="font-mono text-xs">
                    {pid ?? "–"}
                  </TableCell>
                  <TableCell label={t("services.systemColumns.program")} className="max-w-96">
                    <span className="block truncate font-mono text-xs text-muted-foreground" title={program}>
                      {program || "–"}
                    </span>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </section>
  );
}

export function HostServicesTab({ hostId }: { hostId: string }) {
  const { t, i18n } = useTranslation();
  const now = useNow();
  const query = useQuery(servicesQuery(hostId));
  const hostName = useQuery(hostQuery(hostId)).data?.host_name || hostId;
  if (query.isPending) return <LoadingState />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const { snapshot_id, snapshot_time, items } = query.data;
  if (!snapshot_id) return <EmptyState>{t("services.noSnapshot")}</EmptyState>;
  const snap = parseTimeParam(snapshot_time);
  return (
    <section aria-labelledby="services-title">
      <div className="mb-3 flex items-baseline justify-between gap-2">
        <h2 id="services-title" className="text-base font-semibold">
          {t("services.title")}
        </h2>
        <div className="flex flex-wrap items-baseline justify-end gap-x-3 gap-y-1">
          {snap !== null && <span className="text-xs text-muted-foreground">{t("services.snapshotAt", { when: formatRelative(snap, now, i18n.resolvedLanguage ?? "en") })}</span>}
          <Link to="/integrations" className="text-xs text-primary hover:underline">
            {t("services.allIntegrations")}
          </Link>
          <Link to="/alerts/templates" search={{ category: "host", host: hostId, hostName } as never} className="text-xs text-primary hover:underline" data-testid="host-recommended-alerts">
            {t("services.recommendedAlerts")}
          </Link>
        </div>
      </div>
      {items.length === 0 ? (
        <EmptyState>{t("services.empty")}</EmptyState>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-3">
          {items.map((it) => (
            <ServiceCard key={it.key} item={it} hostId={hostId} />
          ))}
        </div>
      )}
      <SystemServices hostId={hostId} />
    </section>
  );
}
