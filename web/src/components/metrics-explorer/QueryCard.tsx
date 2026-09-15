// One metrics explorer query (A, B, …): metric, filters, aggregation and group-by, its chart, series table and
// "Add to dashboard" with the equivalent OQL (lib/metrics-explorer.ts metricOql).
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import { LayoutDashboard, ListTree, Trash2, X } from "lucide-react";
import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import { metricDetailQuery, type MetricAggregation, type MetricQueryResponse } from "@/api/explorer";
import { AddToDashboardButton } from "@/components/oql/AddToDashboardButton";
import { KeyPicker } from "@/components/querybuilder/KeyPicker";
import { QueryBuilder } from "@/components/querybuilder/QueryBuilder";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { MAX_GROUP_BY, metricOql, metricSeriesLabel, scaleSeries, unitDisplay, type MetricQueryState } from "@/lib/metrics-explorer";
import type { RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";
import { SeriesTable } from "./SeriesTable";

export type MetricResult = UseQueryResult<MetricQueryResponse & { from: number; to: number }>;

export interface QueryCardProps {
  query: MetricQueryState;
  result: MetricResult;
  range: RangeSpec;
  active: boolean;
  onActivate: () => void;
  onChooseMetric: () => void;
  onChange: (q: MetricQueryState) => void;
  onRemove?: () => void;
}

export function QueryCard({ query, result, range, active, onActivate, onChooseMetric, onChange, onRemove }: QueryCardProps) {
  const { t } = useTranslation();
  const id = useId();
  const detail = useQuery(metricDetailQuery(query.metric, range));
  const d = detail.data;
  const aggregation: MetricAggregation | undefined = query.aggregation ?? d?.default_aggregation;
  const unit = unitDisplay(query.metric, result.data?.metric.unit ?? d?.unit ?? "", result.data?.aggregation ?? aggregation);
  const data = result.data;
  const series = useMemo(
    () => (data ? scaleSeries(data.series, unit.scale).map((s) => ({ label: metricSeriesLabel(s, query.groupBy, query.metric), points: s.points, attributes: s.attributes })) : undefined),
    [data, unit.scale, query.groupBy, query.metric],
  );
  const groupKeys = useMemo(() => [...(d?.attribute_keys ?? []), ...(d?.resource_keys ?? [])], [d]);
  const oql = d ? metricOql(query, d) : null;
  const title = query.metric ? `${query.id}: ${query.metric}` : query.id;
  const notFound = detail.error instanceof ApiError && detail.error.status === 404;

  return (
    <section
      aria-labelledby={`${id}-title`}
      className={cn("flex min-w-0 flex-col gap-3 rounded-xl border bg-card p-3", active && "ring-2 ring-primary/40")}
      onFocusCapture={onActivate}
      onPointerDownCapture={onActivate}
      data-testid={`metric-query-${query.id}`}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span id={`${id}-title`} className="inline-flex size-6 shrink-0 items-center justify-center rounded-md bg-primary text-xs font-semibold text-primary-foreground" aria-label={t("metricsExplorer.query", { id: query.id })}>
          {query.id}
        </span>
        <Button type="button" variant="outline" size="sm" className="max-w-full min-w-0 font-mono" onClick={onChooseMetric} aria-label={t("metricsExplorer.chooseMetricFor", { id: query.id })}>
          <span className="truncate">{query.metric || t("metricsExplorer.chooseMetric")}</span>
        </Button>
        {d && (
          <span className="flex flex-wrap items-center gap-1">
            <Badge variant="secondary">{d.type}</Badge>
            {d.unit && <Badge variant="outline" className="font-mono">{d.unit}</Badge>}
          </span>
        )}
        <div className="ml-auto flex items-center gap-1">
          {query.metric && oql && (oql.ok ? (
            <AddToDashboardButton query={oql.query} title={title} className="my-0 size-8" />
          ) : (
            <span title={t(`metricsExplorer.oqlUnsupported.${oql.reason}`)} className="inline-flex">
              <Button type="button" variant="ghost" size="icon" className="size-8" disabled aria-label={`${t("oql.addToDashboard.button")}: ${t(`metricsExplorer.oqlUnsupported.${oql.reason}`)}`}>
                <LayoutDashboard aria-hidden="true" />
              </Button>
            </span>
          ))}
          {onRemove && (
            <Button type="button" variant="ghost" size="icon" className="size-8" onClick={onRemove} aria-label={t("metricsExplorer.removeQuery", { id: query.id })}>
              <Trash2 aria-hidden="true" />
            </Button>
          )}
        </div>
      </div>
      {d?.description && <p className="text-xs text-muted-foreground">{d.description}</p>}
      {query.metric && (
        <>
          <QueryBuilder signal="metrics" metric={query.metric} range={range} value={{ filters: query.filters, groups: query.groups, q: "" }} onChange={(v) => onChange({ ...query, filters: v.filters, groups: v.groups })} />
          <div className="flex flex-wrap items-center gap-2">
            <label htmlFor={`${id}-agg`} className="text-xs text-muted-foreground">
              {t("metricsExplorer.aggregation")}
            </label>
            <NativeSelect id={`${id}-agg`} value={aggregation ?? ""} disabled={!d} onChange={(e) => onChange({ ...query, aggregation: e.target.value as MetricAggregation })} className="h-8 text-xs">
              {(d?.aggregations ?? (aggregation ? [aggregation] : [])).map((a) => (
                <option key={a} value={a}>
                  {t(`metricsExplorer.aggregations.${a}`)}
                </option>
              ))}
            </NativeSelect>
            <span className="ml-1 text-xs text-muted-foreground">{t("metricsExplorer.groupBy")}</span>
            {query.groupBy.map((k) => (
              <Badge key={k} variant="secondary" className="max-w-full gap-1 font-mono">
                <span className="truncate">{k}</span>
                <button type="button" aria-label={t("metricsExplorer.removeGroupBy", { key: k })} onClick={() => onChange({ ...query, groupBy: query.groupBy.filter((x) => x !== k) })} className="pointer-coarse:p-1.5">
                  <X aria-hidden="true" />
                </button>
              </Badge>
            ))}
            <KeyPicker
              signal="metrics"
              metric={query.metric}
              range={range}
              keys={d ? groupKeys : undefined}
              label={t("metricsExplorer.addGroupBy")}
              selected={query.groupBy}
              multiple
              disabled={!d}
              onSelect={(k) =>
                onChange({ ...query, groupBy: query.groupBy.includes(k) ? query.groupBy.filter((x) => x !== k) : [...query.groupBy, k].slice(0, MAX_GROUP_BY) })
              }
            >
              <ListTree aria-hidden="true" />
              {t("metricsExplorer.addGroupBy")}
            </KeyPicker>
          </div>
          {notFound ? (
            <p className="py-6 text-center text-sm text-muted-foreground">{t("metricsExplorer.noData")}</p>
          ) : (
            <>
              <TimeSeriesChart
                series={series}
                unit={unit.kind}
                from={data?.from}
                to={data?.to}
                height={220}
                title={title}
                isLoading={result.isPending}
                error={result.isError && !data ? result.error : undefined}
                onRetry={() => void result.refetch()}
              />
              {data?.truncated && (
                <p role="note" className="text-xs text-muted-foreground">
                  {t("metricsExplorer.truncated", { count: data.series.length })}
                </p>
              )}
              {series && series.length > 0 && <SeriesTable series={series} unit={unit.kind} caption={title} />}
            </>
          )}
        </>
      )}
    </section>
  );
}
