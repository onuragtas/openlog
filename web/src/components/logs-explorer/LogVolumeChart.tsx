// Log volume over time (POST /logs/aggregate) as stacked bars split by a key (severity by default). Dragging across
// the plot selects a time range to zoom into.
import { useQuery } from "@tanstack/react-query";
import { ListTree } from "lucide-react";
import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { logsAggregateQuery, type FilterState } from "@/api/explorer";
import { KeyPicker } from "@/components/querybuilder/KeyPicker";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { NativeSelect } from "@/components/ui/native-select";
import { NO_GROUP, SEVERITY_ORDER, severityColor, volumeSeries } from "@/lib/logs-explorer";
import type { RangeSpec } from "@/lib/time";

const PRESETS = ["severity_text", "service.name", "host.name"];

export interface LogVolumeChartProps {
  range: RangeSpec;
  filter: FilterState;
  groupBy: string;
  onGroupByChange: (key: string) => void;
  onZoom: (from: number, to: number) => void;
}

export function LogVolumeChart({ range, filter, groupBy, onGroupByChange, onZoom }: LogVolumeChartProps) {
  const { t, i18n } = useTranslation();
  const id = useId();
  const grouped = groupBy !== NO_GROUP;
  const agg = useQuery(logsAggregateQuery({ range, filter, groupBy: grouped ? groupBy : undefined }));
  const series = useMemo(
    () =>
      agg.data
        ? volumeSeries(agg.data, agg.data.from, agg.data.to, (s) => (s.other ? t("logsExplorer.volume.other") : s.group || (grouped ? t("logsExplorer.volume.missing") : t("logsExplorer.volume.logs"))))
        : undefined,
    [agg.data, grouped, t],
  );
  const severity = groupBy === "severity_text";

  return (
    <section aria-labelledby={`${id}-title`} className="rounded-xl border bg-card p-3">
      <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 id={`${id}-title`} className="text-sm font-medium">
          {t("logsExplorer.volume.title")}
        </h2>
        {agg.data && <span className="text-xs text-muted-foreground">{t("logsExplorer.volume.total", { count: agg.data.total, value: agg.data.total.toLocaleString(i18n.resolvedLanguage) })}</span>}
        <div className="ml-auto flex min-w-0 items-center gap-1.5">
          <label htmlFor={`${id}-gb`} className="text-xs text-muted-foreground">
            {t("logsExplorer.volume.groupBy")}
          </label>
          <NativeSelect id={`${id}-gb`} value={groupBy} onChange={(e) => onGroupByChange(e.target.value)} className="h-8 max-w-44 min-w-0 text-xs">
            <option value={NO_GROUP}>{t("logsExplorer.volume.noGroup")}</option>
            {PRESETS.map((k) => (
              <option key={k} value={k}>
                {k}
              </option>
            ))}
            {grouped && !PRESETS.includes(groupBy) && <option value={groupBy}>{groupBy}</option>}
          </NativeSelect>
          <KeyPicker signal="logs" range={range} label={t("logsExplorer.volume.groupByKey")} selected={grouped ? [groupBy] : []} onSelect={onGroupByChange} className="size-8 px-0">
            <ListTree aria-hidden="true" />
          </KeyPicker>
        </div>
      </div>
      <TimeSeriesChart
        series={series}
        unit="number"
        bars
        stacked
        height={120}
        from={agg.data?.from}
        to={agg.data?.to}
        title={t("logsExplorer.volume.title")}
        isLoading={agg.isPending}
        error={agg.isError && !agg.data ? agg.error : undefined}
        onRetry={() => void agg.refetch()}
        colorFor={severity ? severityColor : undefined}
        order={severity ? [...SEVERITY_ORDER] : undefined}
        onSelectRange={onZoom}
        showLegend={grouped}
      />
      <p className="mt-1 text-[11px] text-muted-foreground">{t("logsExplorer.volume.zoomHint")}</p>
    </section>
  );
}
