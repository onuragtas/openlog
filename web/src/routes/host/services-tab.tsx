import { useQuery } from "@tanstack/react-query";
import { Code2, Plug } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { servicesQuery } from "@/api/queries";
import type { DiscoveredService, InventoryItem } from "@/api/types";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { translateOptional } from "@/i18n/dynamic";
import { formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
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

type IntegrationStatus = NonNullable<NonNullable<DiscoveredService["integration"]>["status"]>;

const STATUS_VARIANT: Record<IntegrationStatus, "success" | "warning" | "muted"> = {
  enabled: "success",
  needs_configuration: "warning",
  not_available: "muted",
};

export function asService(item: InventoryItem): DiscoveredService {
  return item.data && typeof item.data === "object" ? (item.data as DiscoveredService) : {};
}

function ServiceCard({ item }: { item: InventoryItem }) {
  const { t } = useTranslation();
  const id = useId();
  const [apmOpen, setApmOpen] = useState(false);
  const s = asService(item);
  const status: IntegrationStatus = s.integration?.status ?? "not_available";
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
          <Badge variant={STATUS_VARIANT[status] ?? "muted"} title={t("services.integrationHint")}>
            <Plug aria-hidden="true" />
            {t(`services.integration.${status}`)}
          </Badge>
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
          {s.pids && s.pids.length > 0 && (
            <>
              <dt className="text-muted-foreground">{t("services.pids")}</dt>
              <dd className="font-mono">{s.pids.join(", ")}</dd>
            </>
          )}
        </dl>
      </CardContent>
      {hint && (
        <CardFooter className="flex-col items-stretch gap-2 border-t pt-3">
          <Button variant="outline" size="sm" aria-expanded={apmOpen} aria-controls={`${id}-apm`} onClick={() => setApmOpen((o) => !o)}>
            <Code2 aria-hidden="true" />
            {t("services.apmInstall", { language })}
          </Button>
          {apmOpen && (
            <div id={`${id}-apm`} className="rounded-md bg-accent p-3 text-xs text-accent-foreground">
              <p className="mb-1 font-semibold">{t("services.apmTitle")}</p>
              <p>{t("services.apmBody", { name, language, agent: hint.agent ?? "" })}</p>
              <p className="mt-1 text-muted-foreground">{t("services.apmSoon")}</p>
            </div>
          )}
        </CardFooter>
      )}
    </Card>
  );
}

export function HostServicesTab({ hostId }: { hostId: string }) {
  const { t, i18n } = useTranslation();
  const now = useNow();
  const query = useQuery(servicesQuery(hostId));
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
        {snap !== null && <span className="text-xs text-muted-foreground">{t("services.snapshotAt", { when: formatRelative(snap, now, i18n.resolvedLanguage ?? "en") })}</span>}
      </div>
      {items.length === 0 ? (
        <EmptyState>{t("services.empty")}</EmptyState>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-3">
          {items.map((it) => (
            <ServiceCard key={it.key} item={it} />
          ))}
        </div>
      )}
    </section>
  );
}
