// Deployments of a service (versions first seen, GET …/deployments) with a before/after comparison panel
// (GET …/deployments/compare): RED + Apdex deltas and error groups that appeared after the deployment.
import { useQuery } from "@tanstack/react-query";
import { ArrowDown, ArrowRight, ArrowUp, X } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { apmDeploymentCompareQuery, apmDeploymentsQuery, type ApmDeployment, type ApmPeriod } from "@/api/apm";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatApdex, formatMs, formatRate, formatRpm, type ServiceScope } from "@/lib/apm";
import { COMPARE_METRICS, COMPARE_WINDOWS, compareDelta, versionLabel, type CompareMetric, type CompareWindow } from "@/lib/deployments";
import { formatDateTime } from "@/lib/format";
import type { RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";

const METRIC_LABEL = {
  throughput: "apm.metrics.throughput",
  error_rate: "apm.metrics.errorRate",
  avg_ms: "apm.metrics.avg",
  p95_ms: "apm.metrics.p95",
  p99_ms: "apm.metrics.p99",
  apdex: "apm.metrics.apdex",
} as const satisfies Record<CompareMetric, string>;

function formatMetric(metric: CompareMetric, v: number | null | undefined, locale: string): string {
  switch (metric) {
    case "throughput":
      return formatRpm(v, locale);
    case "error_rate":
      return formatRate(v, locale);
    case "apdex":
      return formatApdex(v, locale);
    default:
      return formatMs(v, locale);
  }
}

export function DeploymentsCard({ scope, range, onOpenErrorGroup }: { scope: ServiceScope; range: RangeSpec; onOpenErrorGroup?: (groupId: string) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(apmDeploymentsQuery(scope, range));
  const [compare, setCompare] = useState<ApmDeployment | null>(null);
  const deployments = [...(q.data?.deployments ?? [])].sort((a, b) => b.t - a.t);
  const keyOf = (d: ApmDeployment) => `${d.t}|${d.service_namespace}|${d.environment}|${d.version}`;

  return (
    <Card data-testid="deployments">
      <CardHeader>
        <CardTitle>
          <h2>{t("apm.deployments.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4 px-0">
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : deployments.length === 0 ? (
          <EmptyState className="py-6">{t("apm.deployments.empty")}</EmptyState>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("apm.deployments.time")}</TableHead>
                <TableHead>{t("apm.deployments.change")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("apm.columns.environment")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("apm.deployments.compare")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {deployments.map((d) => {
                const open = compare !== null && keyOf(compare) === keyOf(d);
                return (
                  <TableRow key={keyOf(d)} data-state={open ? "selected" : undefined}>
                    <TableCell className="whitespace-nowrap text-xs">
                      <time dateTime={d.timestamp}>{formatDateTime(d.t, locale)}</time>
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap items-center gap-1.5 font-mono text-xs">
                        {d.previous_version && !d.initial ? (
                          <>
                            <span className="text-muted-foreground">{versionLabel(d.previous_version)}</span>
                            <ArrowRight className="size-3 text-muted-foreground" aria-label={t("apm.deployments.to")} />
                          </>
                        ) : null}
                        <span className="font-semibold">{versionLabel(d.version)}</span>
                        {d.initial && <Badge variant="muted">{t("apm.deployments.initial")}</Badge>}
                        {d.rollback && <Badge variant="warning">{t("apm.deployments.rollback")}</Badge>}
                      </div>
                    </TableCell>
                    <TableCell className="hidden text-xs md:table-cell">{[d.environment, d.service_namespace].filter(Boolean).join(" · ") || "–"}</TableCell>
                    <TableCell className="text-right">
                      <Button
                        variant={open ? "secondary" : "outline"}
                        size="sm"
                        aria-pressed={open}
                        aria-label={t("apm.deployments.compareLabel", { version: versionLabel(d.version) })}
                        onClick={() => setCompare(open ? null : d)}
                      >
                        {t("apm.deployments.compare")}
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
        {compare && (
          <div className="px-4 md:px-6">
            <DeploymentCompare scope={scope} deployment={compare} onClose={() => setCompare(null)} onOpenErrorGroup={onOpenErrorGroup} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function DeploymentCompare({ scope, deployment, onClose, onOpenErrorGroup }: { scope: ServiceScope; deployment: ApmDeployment; onClose: () => void; onOpenErrorGroup?: (groupId: string) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const id = useId();
  const [win, setWin] = useState<CompareWindow>("30m");
  const exact: ServiceScope = { service: scope.service, namespace: scope.namespace ?? deployment.service_namespace, environment: scope.environment ?? deployment.environment };
  const q = useQuery(apmDeploymentCompareQuery(exact, deployment.t, win));
  const version = versionLabel(deployment.version);

  return (
    <section className="flex flex-col gap-3 rounded-lg border p-3" data-testid="deployment-compare" aria-labelledby={`${id}-title`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 id={`${id}-title`} className="text-sm font-semibold">
          {t("apm.deployments.compareTitle", { version })}
        </h3>
        <div className="flex items-center gap-2">
          <label htmlFor={`${id}-win`} className="text-xs text-muted-foreground">
            {t("apm.deployments.window")}
          </label>
          <NativeSelect id={`${id}-win`} value={win} onChange={(e) => setWin(e.target.value as CompareWindow)}>
            {COMPARE_WINDOWS.map((w) => (
              <option key={w} value={w}>
                {t(`apm.deployments.windows.${w}`)}
              </option>
            ))}
          </NativeSelect>
          <Button variant="ghost" size="icon" aria-label={t("apm.deployments.closeCompare")} onClick={onClose}>
            <X className="size-4" aria-hidden="true" />
          </Button>
        </div>
      </div>
      {q.isPending ? (
        <LoadingState />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <>
          <Table data-testid="compare-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t("apm.deployments.metric")}</TableHead>
                <TableHead className="text-right">{t("apm.deployments.before")}</TableHead>
                <TableHead className="text-right">{t("apm.deployments.after")}</TableHead>
                <TableHead className="text-right">{t("apm.deployments.delta")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {COMPARE_METRICS.map((m) => (
                <CompareRow key={m} metric={m} before={q.data.before} after={q.data.after} locale={locale} />
              ))}
            </TableBody>
          </Table>
          <div>
            <h4 className="mb-1 text-xs font-semibold text-muted-foreground">{t("apm.deployments.newGroups")}</h4>
            {q.data.new_error_groups.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("apm.deployments.noNewGroups")}</p>
            ) : (
              <ul className="flex flex-col divide-y text-sm" data-testid="new-error-groups">
                {q.data.new_error_groups.map((g) => (
                  <li key={g.group_id} className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 py-1.5">
                    {onOpenErrorGroup ? (
                      <button type="button" className="min-w-0 text-left" onClick={() => onOpenErrorGroup(g.group_id)} aria-label={t("apm.errors.open", { type: g.error_type })}>
                        <span className="font-mono text-sm font-semibold text-destructive-text hover:underline">{g.error_type}</span>{" "}
                        <span className="break-words">{g.message}</span>
                      </button>
                    ) : (
                      <span className="min-w-0">
                        <span className="font-mono font-semibold text-destructive-text">{g.error_type}</span> {g.message}
                      </span>
                    )}
                    <span className="text-xs text-muted-foreground">{formatDateTime(Date.parse(g.first_seen), locale)}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </>
      )}
    </section>
  );
}

function CompareRow({ metric, before, after, locale }: { metric: CompareMetric; before: ApmPeriod; after: ApmPeriod; locale: string }) {
  const { t } = useTranslation();
  const b = before[metric];
  const a = after[metric];
  const d = compareDelta(metric, b, a);
  const Icon = d.direction === "up" ? ArrowUp : d.direction === "down" ? ArrowDown : ArrowRight;
  const abs = d.delta === null ? "–" : formatMetric(metric, Math.abs(d.delta), locale);
  const pct = d.ratio !== null && metric !== "error_rate" && metric !== "apdex" ? ` (${d.ratio > 0 ? "+" : d.ratio < 0 ? "−" : ""}${formatRate(Math.abs(d.ratio), locale)})` : "";
  const verdict = d.verdict === "good" ? t("apm.deployments.better") : d.verdict === "bad" ? t("apm.deployments.worse") : d.direction === "flat" ? t("apm.deployments.unchanged") : "";
  return (
    <TableRow data-testid={`compare-${metric}`}>
      <TableCell>{t(METRIC_LABEL[metric])}</TableCell>
      <TableCell className="text-right font-mono tabular-nums">{formatMetric(metric, b, locale)}</TableCell>
      <TableCell className="text-right font-mono tabular-nums">{formatMetric(metric, a, locale)}</TableCell>
      <TableCell className={cn("text-right font-mono tabular-nums whitespace-nowrap", d.verdict === "good" && "text-success-text", d.verdict === "bad" && "text-destructive-text")}>
        {d.delta === null ? (
          "–"
        ) : (
          <span className="inline-flex items-center justify-end gap-1">
            <Icon className="size-3" aria-hidden="true" />
            {d.direction === "flat" ? "" : d.direction === "up" ? "+" : "−"}
            {abs}
            {pct}
            {verdict && <span className="sr-only">, {verdict}</span>}
          </span>
        )}
      </TableCell>
    </TableRow>
  );
}
