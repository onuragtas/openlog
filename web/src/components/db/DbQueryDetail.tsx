// One statement (db-monitoring.md §5): its load over the range, the waits its sessions spent time in, the plans the
// server chose for it (with a plan change called out) and the APM services that send it — the hop from the server's
// view back to the code.
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { AlertTriangle } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { dbQueryDetailQuery, type DbPlan } from "@/api/db";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { dbSystemName, formatPlan, postgresPlanTree, type PlanNode } from "@/lib/db";
import { formatDateTime, formatNumber, formatValue } from "@/lib/format";
import type { RangeSpec } from "@/lib/time";

function Kpi({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border bg-card p-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1 text-xl font-semibold tabular-nums">{value}</p>
    </div>
  );
}

function PlanTree({ node, locale }: { node: PlanNode; locale: string }) {
  return (
    <li className="flex flex-col gap-1">
      <div className="flex flex-wrap items-baseline gap-2 text-sm">
        <span className="font-medium">{node.label}</span>
        {node.cost !== undefined && <span className="text-xs text-muted-foreground tabular-nums">cost {formatNumber(node.cost, locale)}</span>}
        {node.rows !== undefined && <span className="text-xs text-muted-foreground tabular-nums">rows {formatNumber(node.rows, locale)}</span>}
      </div>
      {node.detail && <span className="font-mono text-xs break-all text-muted-foreground">{node.detail}</span>}
      {node.children.length > 0 && (
        <ul className="ml-2 flex flex-col gap-2 border-l pl-4">
          {node.children.map((c, i) => (
            <PlanTree key={i} node={c} locale={locale} />
          ))}
        </ul>
      )}
    </li>
  );
}

function PlanView({ plan, system }: { plan: DbPlan; system: string }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const tree = useMemo(() => (system === "postgresql" && plan.format === "json" ? postgresPlanTree(plan.plan) : null), [plan, system]);
  const [raw, setRaw] = useState(tree === null);
  return (
    <div className="flex flex-col gap-2" data-testid="db-plan">
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        {plan.is_current && <Badge variant="success">{t("db.plans.current")}</Badge>}
        <span>{t("db.plans.seen", { first: formatDateTime(Date.parse(plan.first_seen), locale), last: formatDateTime(Date.parse(plan.last_seen), locale) })}</span>
        <span className="tabular-nums">{t("db.plans.cost", { cost: formatNumber(plan.total_cost, locale) })}</span>
        {tree && (
          <Button type="button" variant="ghost" size="sm" onClick={() => setRaw(!raw)}>
            {raw ? t("db.plans.showTree") : t("db.plans.showRaw")}
          </Button>
        )}
      </div>
      {tree && !raw ? (
        <ul className="flex flex-col gap-2">
          <PlanTree node={tree} locale={locale} />
        </ul>
      ) : (
        <pre className="max-h-96 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs">{formatPlan(plan.format, plan.plan)}</pre>
      )}
    </div>
  );
}

export function DbQueryDetail({ instance, fingerprint, range }: { instance: string; fingerprint: string; range: RangeSpec }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(dbQueryDetailQuery({ instance, fingerprint, range }));
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const d = q.data;
  const from = Date.parse(d.from);
  const to = Date.parse(d.to);
  const throughput = [{ label: t("db.columns.throughput"), points: d.points.map((p) => [p.t, p.throughput] as [number, number]) }];
  const latency = [{ label: t("db.columns.avg"), points: d.points.filter((p) => p.avg_ms !== null).map((p) => [p.t, p.avg_ms!] as [number, number]) }];
  const query = d.query;
  return (
    <div className="flex flex-col gap-4">
      <Card className="gap-2">
        <CardHeader>
          <CardTitle className="flex flex-wrap items-center gap-2">
            <h2>{t("db.query.statement")}</h2>
            <Badge variant="muted">{dbSystemName(d.db_system)}</Badge>
            {query.db_names.map((n) => (
              <Badge key={n} variant="outline">
                {n}
              </Badge>
            ))}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="max-h-64 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs whitespace-pre-wrap break-all" data-testid="db-query-text">
            {query.text}
          </pre>
          {query.query_id && <p className="mt-2 text-xs text-muted-foreground">{t("db.query.id", { id: query.query_id })}</p>}
        </CardContent>
      </Card>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
        <Kpi label={t("db.columns.throughput")} value={t("db.perSecond", { value: formatNumber(query.throughput, locale) })} />
        <Kpi label={t("db.columns.avg")} value={query.avg_ms === null ? "–" : formatValue(query.avg_ms, "ms", locale)} />
        <Kpi label={t("db.columns.share")} value={`${formatNumber(query.time_share * 100, locale)} %`} />
        <Kpi label={t("db.columns.rowsPerCall")} value={query.rows_per_call === null ? "–" : formatNumber(query.rows_per_call, locale)} />
        <Kpi label={t("db.columns.cacheHit")} value={query.cache_hit_ratio === null ? "–" : `${formatNumber(query.cache_hit_ratio * 100, locale)} %`} />
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Card className="min-w-0 gap-2">
          <CardHeader>
            <CardTitle>
              <h3>{t("db.columns.throughput")}</h3>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <TimeSeriesChart title={t("db.columns.throughput")} series={throughput} unit="number" from={from} to={to} showLegend={false} />
          </CardContent>
        </Card>
        <Card className="min-w-0 gap-2">
          <CardHeader>
            <CardTitle>
              <h3>{t("db.columns.avg")}</h3>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <TimeSeriesChart title={t("db.columns.avg")} series={latency} unit="ms" from={from} to={to} showLegend={false} />
          </CardContent>
        </Card>
      </div>

      <Card className="gap-2" data-testid="db-callers">
        <CardHeader>
          <CardTitle>
            <h3>{t("db.query.callers")}</h3>
          </CardTitle>
          <p className="text-sm text-muted-foreground">{t("db.query.callersDescription")}</p>
        </CardHeader>
        <CardContent>
          {d.callers.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("db.query.noCallers")}</p>
          ) : (
            <Table mobile="stack">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("db.columns.service")}</TableHead>
                  <TableHead className="text-right">{t("db.columns.calls")}</TableHead>
                  <TableHead className="text-right">{t("db.columns.clientAvg")}</TableHead>
                  <TableHead className="text-right">{t("db.columns.errors")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {d.callers.map((c) => (
                  <TableRow key={`${c.service_name}/${c.environment}`}>
                    <TableCell>
                      <Link to="/apm/services/$service" params={{ service: c.service_name }} search={{ tab: "databases" } as never} className="font-medium text-primary hover:underline">
                        {c.service_name}
                      </Link>
                      {c.environment && <span className="ml-2 text-xs text-muted-foreground">{c.environment}</span>}
                    </TableCell>
                    <TableCell label={t("db.columns.calls")} className="text-right tabular-nums">
                      {formatNumber(c.calls, locale)}
                    </TableCell>
                    <TableCell label={t("db.columns.clientAvg")} className="text-right tabular-nums">
                      {c.avg_ms === null ? "–" : formatValue(c.avg_ms, "ms", locale)}
                    </TableCell>
                    <TableCell label={t("db.columns.errors")} className="text-right tabular-nums">
                      {formatNumber(c.errors, locale)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="min-w-0 gap-2 xl:col-span-2" data-testid="db-plans">
          <CardHeader>
            <CardTitle className="flex flex-wrap items-center gap-2">
              <h3>{t("db.plans.title")}</h3>
              {d.plans[0]?.plan_change && (
                <Badge variant="warning" className="gap-1">
                  <AlertTriangle className="size-3.5" aria-hidden="true" />
                  {t("db.plans.changed")}
                </Badge>
              )}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-6">
            {d.plans.length === 0 ? <p className="text-sm text-muted-foreground">{t("db.plans.none")}</p> : d.plans.map((p) => <PlanView key={p.plan_hash} plan={p} system={d.db_system} />)}
          </CardContent>
        </Card>
        <Card className="min-w-0 gap-2">
          <CardHeader>
            <CardTitle>
              <h3>{t("db.activity.waits")}</h3>
            </CardTitle>
          </CardHeader>
          <CardContent>
            {d.waits.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("db.activity.noSamples")}</p>
            ) : (
              <ul className="flex flex-col gap-2">
                {d.waits.map((w) => (
                  <li key={`${w.type}/${w.event}`} className="flex flex-col gap-1 text-sm">
                    <span className="flex justify-between gap-2">
                      <span className="font-medium">
                        {w.type}
                        {w.event && <span className="ml-1 font-mono text-xs text-muted-foreground">{w.event}</span>}
                      </span>
                      <span className="tabular-nums">{formatNumber(w.share * 100, locale)} %</span>
                    </span>
                    <span className="h-1 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                      <span className="block h-full bg-primary" style={{ width: `${w.share * 100}%` }} />
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
