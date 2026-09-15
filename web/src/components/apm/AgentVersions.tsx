// Language agent version UI (D-124): the services list badge, the service overview upgrade notice and the agent
// versions table of /apm/agents. Agents are application dependencies and never update themselves; the notice shows
// the upgrade command instead.
import { useQuery } from "@tanstack/react-query";
import { ArrowUpCircle, ExternalLink, X } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { apmAgentsQuery, type ApmAgentStatus, type ApmAgentUpgrade, type ApmServiceAgents } from "@/api/apm";
import { CopyCommand } from "@/components/onboarding/CopyCommand";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AGENT_UPDATES_DOCS, agentProduct, attentionAgents, dismissNotice, isNoticeDismissed, noticeKey, outdatedVersions, serviceKey, type AgentVersionRow } from "@/lib/agent-versions";
import type { ServiceScope } from "@/lib/apm";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseTimeParam, type RangeSpec } from "@/lib/time";

const STATUS_VARIANT: Record<ApmAgentStatus, "destructive" | "warning" | "success" | "muted" | "outline"> = {
  unsupported: "destructive",
  outdated: "warning",
  ok: "success",
  unknown: "muted",
  third_party: "outline",
};

export function AgentStatusBadge({ status }: { status: ApmAgentStatus }) {
  const { t } = useTranslation();
  return (
    <Badge variant={STATUS_VARIANT[status]} data-status={status}>
      {t(`apm.agents.status.${status}`)}
    </Badge>
  );
}

/** Badge next to a service name when an agent is outdated or unsupported; the title shows running and latest version. */
export function AgentVersionBadge({ service, latest }: { service: ApmServiceAgents | undefined; latest: string | null | undefined }) {
  const { t } = useTranslation();
  const agent = attentionAgents(service)[0];
  if (!agent || !latest) return null;
  const detail = t("apm.agents.badgeTooltip", { product: agentProduct(agent), versions: outdatedVersions(agent).join(", "), latest });
  return (
    <Badge variant={agent.status === "unsupported" ? "destructive" : "warning"} title={detail} data-testid="agent-version-badge">
      <ArrowUpCircle aria-hidden="true" />
      {agent.status === "unsupported" ? t("apm.agents.badge.unsupported") : t("apm.agents.badge.outdated")}
      <span className="sr-only">: {detail}</span>
    </Badge>
  );
}

function UpgradeNote({ note }: { note: ApmAgentUpgrade["notes"][number] }) {
  const { t } = useTranslation();
  return <li>{t(`apm.agents.notice.notes.${note}`)}</li>;
}

/** Upgrade notices of the service overview: one card per outdated or unsupported agent, dismissible until the next release. */
export function AgentUpgradeNotice({ scope, range }: { scope: ServiceScope; range: RangeSpec }) {
  const { t } = useTranslation();
  const agents = useQuery(apmAgentsQuery(range, { service: scope.service, namespace: scope.namespace, environment: scope.environment }));
  const [dismissed, setDismissed] = useState<string[]>([]);
  const latest = agents.data?.release.latest;
  if (!agents.data || !latest) return null;
  const items = agents.data.services
    .filter((sv) => sv.service_name === scope.service)
    .flatMap((sv) => attentionAgents(sv).map((agent) => ({ sv, agent, key: noticeKey(sv, agent.kind, latest) })))
    .filter((i) => !dismissed.includes(i.key) && !isNoticeDismissed(i.key));
  if (items.length === 0) return null;

  return (
    <div className="flex flex-col gap-3">
      {items.map(({ sv, agent, key }) => {
        const product = agentProduct(agent);
        const title = agent.status === "unsupported" ? t("apm.agents.notice.titleUnsupported", { product }) : t("apm.agents.notice.titleOutdated", { product });
        const up = agent.upgrade;
        return (
          <Card key={key} role="region" aria-label={title} data-testid="agent-upgrade-notice" className={agent.status === "unsupported" ? "border-destructive/50" : "border-warning/60"}>
            <CardHeader className="flex flex-row items-start justify-between gap-2">
              <div className="min-w-0">
                <CardTitle className="flex flex-wrap items-center gap-2 text-sm">
                  <ArrowUpCircle className="size-4 shrink-0" aria-hidden="true" />
                  {title}
                  {sv.environment && <Badge variant="outline">{sv.environment}</Badge>}
                </CardTitle>
                <p className="mt-1 text-xs text-muted-foreground">{t("apm.agents.notice.why")}</p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="shrink-0"
                aria-label={t("apm.agents.notice.dismiss")}
                title={t("apm.agents.notice.dismiss")}
                onClick={() => {
                  dismissNotice(key);
                  setDismissed((d) => [...d, key]);
                }}
              >
                <X aria-hidden="true" />
              </Button>
            </CardHeader>
            <CardContent className="flex min-w-0 flex-col gap-3 text-sm">
              <div className="flex flex-wrap gap-x-8 gap-y-2">
                <div className="min-w-0">
                  <div className="text-xs text-muted-foreground">{t("apm.agents.notice.running")}</div>
                  <ul className="mt-1 flex flex-col gap-1" data-testid="agent-running-versions">
                    {agent.versions.map((v) => (
                      <li key={v.version} className="flex flex-wrap items-center gap-2">
                        <span className="font-mono">{v.version}</span>
                        <AgentStatusBadge status={v.status} />
                        <span className="text-xs text-muted-foreground">{t("apm.agents.notice.instances", { count: v.instances })}</span>
                      </li>
                    ))}
                  </ul>
                </div>
                <div>
                  <div className="text-xs text-muted-foreground">{t("apm.agents.notice.latest")}</div>
                  <div className="mt-1 font-mono">{latest}</div>
                  {agents.data.release.oldest_supported && (
                    <div className="mt-1 text-xs text-muted-foreground">{t("apm.agents.release.oldest", { version: agents.data.release.oldest_supported })}</div>
                  )}
                </div>
              </div>
              {up && (
                <>
                  <CopyCommand code={up.command} label={t("apm.agents.notice.commandLabel", { product })} lang={up.lang} testId="agent-upgrade-command" />
                  {up.notes.length > 0 && (
                    <ul className="list-disc pl-5 text-xs text-muted-foreground">
                      {up.notes.map((n) => (
                        <UpgradeNote key={n} note={n} />
                      ))}
                    </ul>
                  )}
                </>
              )}
              <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                {up?.docs_url && (
                  <a href={up.docs_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-primary hover:underline">
                    {up.docs_section ? t("apm.agents.notice.docsSection", { section: up.docs_section }) : t("apm.agents.notice.docs")}
                    <ExternalLink className="size-3" aria-hidden="true" />
                  </a>
                )}
                <a href={AGENT_UPDATES_DOCS} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-primary hover:underline">
                  {t("apm.agents.notice.automate")}
                  <ExternalLink className="size-3" aria-hidden="true" />
                </a>
              </div>
            </CardContent>
          </Card>
        );
      })}
    </div>
  );
}

/** Services × agents × versions (/apm/agents). renderService renders the service cell (a link on the page). */
export function AgentVersionsTable({ rows, renderService }: { rows: AgentVersionRow[]; renderService?: (row: AgentVersionRow) => ReactNode }) {
  const { t, i18n } = useTranslation();
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  return (
    <Table data-testid="agent-versions">
      <TableHeader>
        <TableRow>
          <TableHead>{t("apm.agents.columns.service")}</TableHead>
          <TableHead className="hidden md:table-cell">{t("apm.agents.columns.environment")}</TableHead>
          <TableHead>{t("apm.agents.columns.agent")}</TableHead>
          <TableHead>{t("apm.agents.columns.version")}</TableHead>
          <TableHead className="hidden text-right sm:table-cell">{t("apm.agents.columns.instances")}</TableHead>
          <TableHead>{t("apm.agents.columns.status")}</TableHead>
          <TableHead className="hidden lg:table-cell">{t("apm.agents.columns.lastSeen")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((r) => {
          const seen = parseTimeParam(r.last_seen) ?? 0;
          return (
            <TableRow key={`${serviceKey(r)}|${r.kind}|${r.product}|${r.version}`}>
              <TableCell className="max-w-[14rem]">
                <div className="truncate font-medium">{renderService ? renderService(r) : r.service_name}</div>
                {r.service_namespace && <div className="truncate text-xs text-muted-foreground">{r.service_namespace}</div>}
              </TableCell>
              <TableCell className="hidden md:table-cell">{r.environment || "–"}</TableCell>
              <TableCell>
                <div className="text-sm">{r.product}</div>
                <div className="text-xs text-muted-foreground">{t(`apm.agents.kinds.${r.kind}`)}</div>
              </TableCell>
              <TableCell className="font-mono text-sm">{r.version || "–"}</TableCell>
              <TableCell className="hidden text-right font-mono tabular-nums sm:table-cell">{r.instances}</TableCell>
              <TableCell>
                <AgentStatusBadge status={r.status} />
              </TableCell>
              <TableCell className="hidden whitespace-nowrap lg:table-cell">
                <time dateTime={r.last_seen} title={formatDateTime(seen, locale)}>
                  {formatRelative(seen, now, locale)}
                </time>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
