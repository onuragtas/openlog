import { AlertTriangle } from "lucide-react";
import { useMemo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { DashboardThreshold, DashboardUnit, DashboardVisualization, DashboardWidgetOptions } from "@/api/dashboards";
import type { OqlResult } from "@/api/oql";
import { EmptyState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDateTime, formatNumber, formatValue, type UnitKind } from "@/lib/format";
import {
  barItems,
  deltaPercent,
  heatmapModel,
  pieSlices,
  seriesLabel,
  summarize,
  summaryColumns,
  thresholdState,
  toChartData,
  unitKind,
  type CellValue,
  type ThresholdState,
} from "@/lib/oql-result";
import { useTheme } from "@/lib/theme";
import { cn, paletteColor } from "@/lib/utils";
import { queryErrorMessage } from "./query-error";

/** Called with the facet values of a clicked group (dashboard cross-widget filters). */
export type SelectFacets = (values: string[]) => void;

export interface QueryResultProps {
  result: OqlResult | undefined;
  visualization: DashboardVisualization;
  title: string;
  unit?: DashboardUnit;
  thresholds?: DashboardThreshold[];
  options?: DashboardWidgetOptions;
  /** Chart plot height in px. */
  height?: number;
  isLoading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  /** Rows read, elapsed time, bucket and rollup below the result (console). */
  showMetadata?: boolean;
  /** Makes facet values clickable (results with FACET only). */
  onSelectFacets?: SelectFacets;
}

export function QueryError({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  const { t } = useTranslation();
  return (
    <div role="alert" className={cn("flex flex-col items-center justify-center gap-2 py-6 text-center text-sm", className)} data-testid="query-error">
      <AlertTriangle className="size-5 text-destructive" aria-hidden="true" />
      <p className="font-medium">{t("oql.errors.title")}</p>
      <p className="max-w-prose font-mono text-xs break-words text-muted-foreground">{queryErrorMessage(error, t)}</p>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry}>
          {t("common.retry")}
        </Button>
      )}
    </div>
  );
}

const parseTs = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));

function isEmpty(r: OqlResult): boolean {
  switch (r.kind) {
    case "timeseries":
      return r.series.length === 0;
    case "histogram":
      return r.buckets.length === 0;
    default:
      return r.rows.length === 0;
  }
}

function fmtCell(v: CellValue | undefined, unit: UnitKind, locale: string): string {
  if (v === null || v === undefined) return "–";
  return typeof v === "number" ? formatValue(v, unit, locale) : v;
}

/** A facet value that filters the dashboard when clicked; plain text without a handler. */
function FacetValue({ facets, onSelect, className, children }: { facets: string[] | undefined; onSelect?: SelectFacets; className?: string; children: ReactNode }) {
  const { t } = useTranslation();
  if (!onSelect || !facets || facets.length === 0) return <span className={className}>{children}</span>;
  const label = t("dashboards.filters.add", { value: facets.join(" · ") || t("oql.result.emptyValue") });
  return (
    <button
      type="button"
      className={cn("max-w-full cursor-pointer truncate rounded-sm text-left underline-offset-2 hover:underline focus-visible:underline", className)}
      title={label}
      aria-label={label}
      onClick={() => onSelect(facets)}
      data-testid="facet-filter"
    >
      {children}
    </button>
  );
}

function Delta({ current, previous }: { current: CellValue | undefined; previous: CellValue | undefined }) {
  const { i18n } = useTranslation();
  const d = deltaPercent(current, previous);
  if (d === null) return <span className="text-muted-foreground">–</span>;
  const sign = d > 0 ? "▲" : d < 0 ? "▼" : "";
  return (
    <span className="whitespace-nowrap text-muted-foreground">
      {sign} {formatNumber(Math.abs(d), i18n.resolvedLanguage)}%
    </span>
  );
}

const STATE_CLASS: Record<Exclude<ThresholdState, null>, string> = {
  critical: "border-destructive/60 bg-destructive/10 text-destructive-text",
  warning: "border-warning/70 bg-warning/15 text-warning-text",
};

export function ResultTable({ result, unit, onSelect }: { result: OqlResult; unit: UnitKind; onSelect?: SelectFacets }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const rows = useMemo(() => summarize(result), [result]);
  const columns = summaryColumns(result);
  const facetHeads = result.kind === "histogram" ? [t("oql.result.bucket")] : result.facets;
  const select = result.kind === "histogram" ? undefined : onSelect;
  const compare = rows.some((r) => r.previous !== null);
  return (
    <Table data-testid="result-table">
      <TableHeader>
        <TableRow>
          {facetHeads.map((f) => (
            <TableHead key={`f-${f}`}>{f}</TableHead>
          ))}
          {columns.map((c, i) => [
            <TableHead key={`c-${i}`} className="text-right">
              {c.name}
            </TableHead>,
            compare && (
              <TableHead key={`p-${i}`} className="text-right">
                {t("oql.result.previousColumn", { name: c.name })}
              </TableHead>
            ),
            compare && (
              <TableHead key={`d-${i}`} className="text-right">
                {t("oql.result.delta")}
              </TableHead>
            ),
          ])}
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((r, ri) => (
          <TableRow key={ri}>
            {facetHeads.map((_, fi) => (
              <TableCell key={`f-${fi}`} className="max-w-72 truncate" title={r.facets[fi]}>
                <FacetValue facets={r.facets} onSelect={select}>
                  {r.facets[fi] === "" ? <span className="text-muted-foreground">{t("oql.result.emptyValue")}</span> : r.facets[fi]}
                </FacetValue>
              </TableCell>
            ))}
            {columns.map((_, ci) => [
              <TableCell key={`c-${ci}`} className="text-right font-mono tabular-nums">
                {fmtCell(r.values[ci], unit, locale)}
              </TableCell>,
              compare && (
                <TableCell key={`p-${ci}`} className="text-right font-mono text-muted-foreground tabular-nums">
                  {fmtCell(r.previous?.[ci], unit, locale)}
                </TableCell>
              ),
              compare && (
                <TableCell key={`d-${ci}`} className="text-right text-xs">
                  <Delta current={r.values[ci]} previous={r.previous?.[ci]} />
                </TableCell>
              ),
            ])}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export function Billboard({ result, unit, thresholds, onSelect }: { result: OqlResult; unit: UnitKind; thresholds?: DashboardThreshold[]; onSelect?: SelectFacets }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const items = useMemo(() => {
    const rows = summarize(result);
    const columns = summaryColumns(result);
    if (result.kind === "single") {
      const r = rows[0];
      return columns.map((c, i) => ({ label: c.name, value: r?.values[i] ?? null, previous: r?.previous?.[i] ?? null, compare: !!r?.previous, facets: undefined as string[] | undefined }));
    }
    return rows.slice(0, 12).map((r) => ({
      label: r.facets.join(" · "),
      value: r.values[0] ?? null,
      previous: r.previous?.[0] ?? null,
      compare: !!r.previous,
      facets: result.kind === "histogram" ? undefined : r.facets,
    }));
  }, [result]);
  return (
    <div className="grid min-w-0 grid-cols-[repeat(auto-fit,minmax(9rem,1fr))] gap-2" data-testid="billboard">
      {items.map((it, i) => {
        const state = thresholdState(it.value, thresholds);
        return (
          <div key={i} data-state={state ?? "ok"} className={cn("flex min-w-0 flex-col justify-center rounded-lg border px-3 py-2", state ? STATE_CLASS[state] : "border-transparent")}>
            <FacetValue facets={it.facets} onSelect={onSelect} className="truncate text-xs text-muted-foreground">
              <span title={it.label}>{it.label}</span>
            </FacetValue>
            <span className="truncate text-2xl font-semibold tabular-nums sm:text-3xl" data-testid="billboard-value">
              {fmtCell(it.value, unit, locale)}
            </span>
            {state && <span className="text-xs font-medium">{t(`oql.result.threshold.${state}`)}</span>}
            {it.compare && (
              <span className="flex flex-wrap gap-x-2 text-xs text-muted-foreground">
                <span>{t("oql.result.previousValue", { value: fmtCell(it.previous, unit, locale) })}</span>
                <Delta current={it.value} previous={it.previous} />
              </span>
            )}
          </div>
        );
      })}
    </div>
  );
}

export function BarList({ result, unit, onSelect }: { result: OqlResult; unit: UnitKind; onSelect?: SelectFacets }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const items = useMemo(() => barItems(result), [result]);
  const max = Math.max(0, ...items.map((b) => Math.max(b.value, b.previous ?? 0)));
  return (
    <ul className="flex min-w-0 flex-col gap-1.5" aria-label={t("oql.result.bars")} data-testid="bar-list">
      {items.map((b, i) => (
        <li key={i} className="min-w-0 text-xs">
          <div className="flex items-baseline justify-between gap-2">
            <FacetValue facets={b.facets} onSelect={onSelect} className="min-w-0 truncate">
              <span title={b.label}>{b.label || t("oql.result.emptyValue")}</span>
            </FacetValue>
            <span className="shrink-0 font-mono tabular-nums">{formatValue(b.value, unit, locale)}</span>
          </div>
          <div className="mt-0.5 h-2 rounded bg-muted" aria-hidden="true">
            <div className="h-2 rounded bg-primary" style={{ width: `${max > 0 ? Math.max(0.5, (b.value / max) * 100) : 0}%` }} />
          </div>
          {b.previous !== null && (
            <div className="mt-0.5 h-1 rounded bg-muted" aria-hidden="true">
              <div className="h-1 rounded bg-muted-foreground/50" style={{ width: `${max > 0 ? (b.previous / max) * 100 : 0}%` }} />
            </div>
          )}
        </li>
      ))}
    </ul>
  );
}

export function PieChart({ result, unit, title, showLegend = true, onSelect }: { result: OqlResult; unit: UnitKind; title: string; showLegend?: boolean; onSelect?: SelectFacets }) {
  const { t, i18n } = useTranslation();
  const { resolved } = useTheme();
  const locale = i18n.resolvedLanguage ?? "en";
  const slices = useMemo(() => pieSlices(result, t("oql.result.other")), [result, t]);
  const r = 15.9155; // circumference 100
  // Each slice starts where the previous ended (clockwise from 12 o'clock).
  const offsets = slices.map((_, i) => (125 - slices.slice(0, i).reduce((sum, s) => sum + s.fraction * 100, 0)) % 100);
  return (
    <div className="flex min-w-0 flex-wrap items-center justify-center gap-4" data-testid="pie-chart">
      <svg viewBox="0 0 42 42" className="size-32 shrink-0 sm:size-40" role="img" aria-label={title}>
        <circle cx="21" cy="21" r={r} fill="none" stroke="var(--muted)" strokeWidth="6" />
        {slices.map((s, i) => {
          const len = s.fraction * 100;
          return (
            <circle
              key={i}
              cx="21"
              cy="21"
              r={r}
              fill="none"
              stroke={paletteColor(i, resolved)}
              strokeWidth="6"
              strokeDasharray={`${len} ${100 - len}`}
              strokeDashoffset={offsets[i]}
              className={cn(onSelect && s.facets && "cursor-pointer")}
              onClick={onSelect && s.facets ? () => onSelect(s.facets!) : undefined}
            >
              <title>{`${s.label}: ${formatValue(s.value, unit, locale)}`}</title>
            </circle>
          );
        })}
      </svg>
      {showLegend && (
        <ul className="flex min-w-0 flex-col gap-1 text-xs" aria-label={t("charts.legend")}>
          {slices.map((s, i) => (
            <li key={i} className="flex min-w-0 items-center gap-2">
              <span aria-hidden="true" className="inline-block size-2.5 shrink-0 rounded-[3px]" style={{ background: paletteColor(i, resolved) }} />
              <FacetValue facets={s.facets} onSelect={onSelect} className="min-w-0 truncate">
                <span title={s.label}>{s.label || t("oql.result.emptyValue")}</span>
              </FacetValue>
              <span className="ml-auto shrink-0 pl-2 font-mono tabular-nums">{formatValue(s.value, unit, locale)}</span>
              <span className="w-10 shrink-0 text-right text-muted-foreground tabular-nums">{Math.round(s.fraction * 100)}%</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function Heatmap({ result, unit, title, onSelect }: { result: OqlResult; unit: UnitKind; title: string; onSelect?: SelectFacets }) {
  const { i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const model = useMemo(() => heatmapModel(result), [result]);
  if (!model) return <BarList result={result} unit={unit} onSelect={onSelect} />;
  const colLabel = (c: (typeof model.columns)[number]) => c.label ?? formatDateTime(c.time ?? 0, locale);
  const n = model.columns.length;
  const ticks = n > 0 ? [0, Math.floor((n - 1) / 2), n - 1] : [];
  return (
    <div className="min-w-0 overflow-x-auto" role="img" aria-label={title} data-testid="heatmap">
      <div className="grid min-w-full gap-px text-[10px]" style={{ gridTemplateColumns: `minmax(4rem, 9rem) repeat(${n}, minmax(4px, 1fr))` }}>
        {model.rows.map((row, ri) => [
          <div key={`l-${ri}`} className="truncate pr-2 text-xs text-muted-foreground" title={row.label}>
            <FacetValue facets={row.facets} onSelect={onSelect}>
              {row.label}
            </FacetValue>
          </div>,
          ...row.cells.map((v, ci) => (
            <div
              key={`${ri}-${ci}`}
              className="h-5 rounded-[2px]"
              title={`${row.label} · ${colLabel(model.columns[ci]!)}: ${formatValue(v, unit, locale)}`}
              style={{
                background: v === null ? "var(--muted)" : `color-mix(in oklch, var(--primary) ${Math.round((model.max > 0 ? v / model.max : 0) * 90 + 8)}%, var(--card))`,
              }}
            />
          )),
        ])}
        <div />
        {model.columns.map((c, ci) => (
          <div key={`t-${ci}`} className="overflow-visible whitespace-nowrap text-muted-foreground">
            {ticks.includes(ci) && ci === n - 1 && n > 1 ? <span className="float-right">{colLabel(c)}</span> : ticks.includes(ci) ? colLabel(c) : null}
          </div>
        ))}
      </div>
    </div>
  );
}

export function ResultMetadata({ result }: { result: OqlResult }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const m = result.metadata;
  const parts = [
    t("oql.result.rowsRead", { count: m.rows_read, formatted: formatNumber(m.rows_read, locale) }),
    t("oql.result.elapsed", { ms: m.elapsed_ms }),
    m.bucket_seconds ? t("oql.result.bucketSize", { seconds: m.bucket_seconds }) : null,
    t(m.rollup ? "oql.result.rollup" : "oql.result.raw", { table: m.table }),
    m.truncated ? t("oql.result.truncated", { limit: m.facet_limit }) : null,
  ].filter(Boolean);
  return (
    <div className="flex flex-col gap-1 text-xs text-muted-foreground" data-testid="result-metadata">
      <p>{parts.join(" · ")}</p>
      {m.warnings.map((w, i) => (
        <p key={i} className="text-warning-text">
          {w}
        </p>
      ))}
    </div>
  );
}

/** Draws an OQL result as the chosen visualization (markdown widgets are rendered by the caller). */
export function QueryResult({ result, visualization, title, unit, thresholds, options, height = 220, isLoading, error, onRetry, showMetadata, onSelectFacets }: QueryResultProps) {
  const { t } = useTranslation();
  const u = unitKind(unit);
  const chart = useMemo(() => (result?.kind === "timeseries" ? toChartData(result, t("oql.result.previous")) : null), [result, t]);
  const select = result && result.facets.length > 0 ? onSelectFacets : undefined;
  // Chart legend labels → facet values of the current (not COMPARE WITH) series.
  const seriesFacets = useMemo(() => {
    const m = new Map<string, string[]>();
    if (result?.kind === "timeseries") {
      for (const s of result.series) {
        if (s.facets.length > 0) m.set(seriesLabel(result.columns, s.facets, s.column), s.facets);
      }
    }
    return m;
  }, [result]);

  if (error && !result) return <QueryError error={error} onRetry={onRetry} />;
  if (!result) {
    return isLoading ? (
      <div role="status" aria-label={t("common.loading")}>
        <Skeleton style={{ height }} className="w-full" />
      </div>
    ) : null;
  }

  let body: React.ReactNode;
  if (isEmpty(result)) {
    body = <EmptyState className="py-6">{t("oql.result.empty")}</EmptyState>;
  } else {
    switch (visualization) {
      case "line":
      case "area":
      case "bar":
        if (chart) {
          body = (
            <TimeSeriesChart
              title={title}
              series={chart.series}
              dashed={chart.dashed}
              unit={u}
              stacked={visualization === "area" ? options?.stacked !== false : !!options?.stacked}
              bars={visualization === "bar"}
              showLegend={options?.legend !== false}
              from={parseTs(result.metadata.from)}
              to={parseTs(result.metadata.to)}
              height={height}
              selectLabel={select ? (label) => (seriesFacets.has(label) ? t("dashboards.filters.add", { value: label }) : null) : undefined}
              onSelectSeries={select ? (label) => select(seriesFacets.get(label) ?? []) : undefined}
            />
          );
        } else {
          body = <BarList result={result} unit={u} onSelect={select} />;
        }
        break;
      case "table":
        body = <ResultTable result={result} unit={u} onSelect={select} />;
        break;
      case "billboard":
        body = <Billboard result={result} unit={u} thresholds={thresholds} onSelect={select} />;
        break;
      case "pie":
        body = <PieChart result={result} unit={u} title={title} showLegend={options?.legend !== false} onSelect={select} />;
        break;
      case "heatmap":
        body = <Heatmap result={result} unit={u} title={title} onSelect={select} />;
        break;
      default:
        body = <ResultTable result={result} unit={u} onSelect={select} />;
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-2" data-testid="query-result" data-kind={result.kind} data-visualization={visualization}>
      {error ? <QueryError error={error} onRetry={onRetry} className="py-2" /> : null}
      {body}
      {showMetadata && <ResultMetadata result={result} />}
    </div>
  );
}
