// Traces Explorer charts (POST /traces/aggregate): span count per bucket split by a key, and p50/p95/p99 span duration.
// Dragging across either plot selects a time range to zoom into.
import { useQuery } from "@tanstack/react-query";
import { ListTree } from "lucide-react";
import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { tracesAggregateQuery, type QueryFilter } from "@/api/explorer";
import { AddToDashboardButton } from "@/components/oql/AddToDashboardButton";
import { KeyPicker } from "@/components/querybuilder/KeyPicker";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { NativeSelect } from "@/components/ui/native-select";
import { spanCountOql, spanLatencyOql, type ExplorerOqlResult } from "@/lib/explorer-oql";
import type { RangeSpec } from "@/lib/time";
import { latencySeries, NO_SPAN_GROUP, spanCountSeries } from "@/lib/traces-explorer";

const PRESETS = ["service.name", "name", "status_code", "kind"];

export interface SpanChartsProps {
  range: RangeSpec;
  filter: { filters: QueryFilter[]; groups: QueryFilter[][] };
  rootOnly: boolean;
  groupBy: string;
  onGroupByChange: (key: string) => void;
  onZoom: (from: number, to: number) => void;
}

export function SpanCharts({ range, filter, rootOnly, groupBy, onGroupByChange, onZoom }: SpanChartsProps) {
  const { t, i18n } = useTranslation();
  const id = useId();
  const grouped = groupBy !== NO_SPAN_GROUP;
  const agg = useQuery(tracesAggregateQuery({ range, filter, rootOnly, groupBy: grouped ? groupBy : undefined }));
  const counts = useMemo(
    () =>
      agg.data
        ? spanCountSeries(agg.data, agg.data.from, agg.data.to, (s) => (s.other ? t("tracesExplorer.charts.other") : s.group || (grouped ? t("tracesExplorer.charts.missing") : t("tracesExplorer.charts.spans"))))
        : undefined,
    [agg.data, grouped, t],
  );
  const latency = useMemo(() => (agg.data ? latencySeries(agg.data) : undefined), [agg.data]);
  const error = agg.isError && !agg.data ? agg.error : undefined;
  const countOql = useMemo(() => spanCountOql({ filter, rootOnly, groupBy: grouped ? groupBy : undefined }), [filter, rootOnly, grouped, groupBy]);
  const latencyOql = useMemo(() => spanLatencyOql({ filter, rootOnly }), [filter, rootOnly]);
  const unsupported = (r: ExplorerOqlResult) => (r.ok ? undefined : t(`explorer.oqlUnsupported.${r.reason}`));

  return (
    <div className="grid min-w-0 gap-3 lg:grid-cols-2">
      <section aria-labelledby={`${id}-count`} className="min-w-0 rounded-xl border bg-card p-3">
        <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-2">
          <h2 id={`${id}-count`} className="text-sm font-medium">
            {t("tracesExplorer.charts.count")}
          </h2>
          {agg.data && <span className="text-xs text-muted-foreground">{t("tracesExplorer.charts.total", { count: agg.data.total, value: agg.data.total.toLocaleString(i18n.resolvedLanguage) })}</span>}
          <div className="ml-auto flex min-w-0 items-center gap-1.5">
            <AddToDashboardButton
              query={countOql.ok ? countOql.query : ""}
              title={grouped ? t("tracesExplorer.charts.countBy", { key: groupBy }) : t("tracesExplorer.charts.count")}
              visualization="bar"
              options={{ legend: true, stacked: true }}
              disabledReason={unsupported(countOql)}
              className="my-0 size-8"
            />
            <label htmlFor={`${id}-gb`} className="text-xs text-muted-foreground">
              {t("tracesExplorer.charts.groupBy")}
            </label>
            <NativeSelect id={`${id}-gb`} value={groupBy} onChange={(e) => onGroupByChange(e.target.value)} className="h-8 max-w-44 min-w-0 text-xs">
              <option value={NO_SPAN_GROUP}>{t("tracesExplorer.charts.noGroup")}</option>
              {PRESETS.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
              {grouped && !PRESETS.includes(groupBy) && <option value={groupBy}>{groupBy}</option>}
            </NativeSelect>
            <KeyPicker signal="traces" range={range} label={t("tracesExplorer.charts.groupByKey")} selected={grouped ? [groupBy] : []} onSelect={onGroupByChange} className="size-8 px-0">
              <ListTree aria-hidden="true" />
            </KeyPicker>
          </div>
        </div>
        <TimeSeriesChart
          series={counts}
          unit="number"
          bars
          stacked
          height={140}
          from={agg.data?.from}
          to={agg.data?.to}
          title={t("tracesExplorer.charts.count")}
          isLoading={agg.isPending}
          error={error}
          onRetry={() => void agg.refetch()}
          onSelectRange={onZoom}
          showLegend={grouped}
        />
      </section>
      <section aria-labelledby={`${id}-latency`} className="min-w-0 rounded-xl border bg-card p-3">
        <div className="mb-2 flex min-h-8 items-center">
          <h2 id={`${id}-latency`} className="text-sm font-medium">
            {t("tracesExplorer.charts.latency")}
          </h2>
          <div className="ml-auto flex items-center">
            <AddToDashboardButton
              query={latencyOql.ok ? latencyOql.query : ""}
              title={t("tracesExplorer.charts.latency")}
              unit="ms"
              disabledReason={unsupported(latencyOql)}
              className="my-0 size-8"
            />
          </div>
        </div>
        <TimeSeriesChart
          series={latency}
          unit="ms"
          height={140}
          from={agg.data?.from}
          to={agg.data?.to}
          title={t("tracesExplorer.charts.latency")}
          isLoading={agg.isPending}
          error={error}
          onRetry={() => void agg.refetch()}
          onSelectRange={onZoom}
          showLegend
        />
        <p className="mt-1 text-[11px] text-muted-foreground">{t("tracesExplorer.charts.latencyHint")}</p>
      </section>
    </div>
  );
}
