import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import uPlot from "uplot";
import { ErrorState, EmptyState } from "@/components/StateViews";
import { Skeleton } from "@/components/ui/skeleton";
import { axisTickLabels, timeFormatter } from "@/lib/chart-axis";
import { formatDateTime, formatValue, type UnitKind } from "@/lib/format";
import {
  alignSeries,
  dataStartHint,
  defaultVisibility,
  legendValues,
  stackBands,
  stackVisible,
  visibleMax,
  yRange,
  type AlignedData,
  type ChartSeriesInput,
} from "@/lib/series";
import { useTheme } from "@/lib/theme";
import { cn, paletteColor } from "@/lib/utils";

export interface TimeSeriesChartProps {
  series: ChartSeriesInput[] | undefined;
  unit: UnitKind;
  stacked?: boolean;
  /** Preferred series order (labels); others follow alphabetically. */
  order?: string[];
  /** x range in ms, so empty edges of the selected range stay visible. */
  from?: number;
  to?: number;
  height?: number;
  isLoading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  /** Accessible name of the chart. */
  title: string;
  /** Fixed y max (e.g. 1 for utilization). */
  yMax?: number;
  /** Auto-scale y to the visible series, never above this (e.g. 1 = 100%). */
  yCap?: number;
  /** Series labels hidden until enabled in the legend (e.g. cpu "idle"). */
  hidden?: readonly string[];
}

/** Legend entries shown before "show all". */
const LEGEND_COLLAPSED = 6;
const AXIS_FONT = "12px system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif";

interface Live {
  data: AlignedData;
  drawn: (number | null)[][];
  visible: boolean[];
  from?: number;
  to?: number;
}

function cssVar(name: string, fallback: string): string {
  if (typeof window === "undefined") return fallback;
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

function withAlpha(hex: string, alpha: number): string {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}

function measureText(text: string): number {
  try {
    const ctx = document.createElement("canvas").getContext("2d");
    if (ctx) {
      ctx.font = AXIS_FONT;
      return ctx.measureText(text).width;
    }
  } catch {
    // no canvas (tests)
  }
  return text.length * 7;
}

/** Tooltip plugin: shows the hovered time and each visible series' raw value; reports the hovered index. */
function cursorPlugin(
  getLive: () => Live | null,
  fmt: (v: number | null) => string,
  fmtTime: (s: number) => string,
  colorOf: (i: number) => string,
  onIdx: (idx: number | null) => void,
): uPlot.Plugin {
  let el: HTMLDivElement | null = null;
  return {
    hooks: {
      init: (u) => {
        el = document.createElement("div");
        el.className = "pointer-events-none absolute z-10 hidden rounded-md border bg-card px-2 py-1 text-xs text-card-foreground shadow-md";
        el.setAttribute("aria-hidden", "true");
        u.over.appendChild(el);
      },
      setCursor: (u) => {
        const idx = u.cursor.idx;
        const left = u.cursor.left ?? -1;
        const live = getLive();
        if (!el || !live || idx === null || idx === undefined || left < 0) {
          el?.classList.add("hidden");
          onIdx(null);
          return;
        }
        onIdx(idx);
        const { data, visible } = live;
        const x = data.xs[idx];
        const rows = data.labels
          .map((l, i) => ({ l, v: data.ys[i]?.[idx] ?? null, c: colorOf(i), show: visible[i] !== false }))
          .filter((r) => r.show)
          .reverse();
        el.replaceChildren();
        const head = document.createElement("div");
        head.className = "mb-1 font-medium";
        head.textContent = x !== undefined ? fmtTime(x) : "";
        el.appendChild(head);
        for (const r of rows) {
          const row = document.createElement("div");
          row.className = "flex items-center gap-2 whitespace-nowrap";
          const dot = document.createElement("span");
          dot.style.cssText = `display:inline-block;width:8px;height:8px;border-radius:2px;background:${r.c}`;
          const label = document.createElement("span");
          label.className = "text-muted-foreground";
          label.textContent = r.l;
          const val = document.createElement("span");
          val.className = "ml-auto pl-3 font-mono";
          val.textContent = fmt(r.v);
          row.append(dot, label, val);
          el.appendChild(row);
        }
        el.classList.remove("hidden");
        const w = el.offsetWidth;
        const flip = left + w + 16 > u.over.clientWidth;
        el.style.left = `${flip ? left - w - 12 : left + 12}px`;
        el.style.top = `${Math.max(0, (u.cursor.top ?? 0) - 10)}px`;
      },
    },
  };
}

interface LegendItem {
  label: string;
  color: string;
  value: string;
  visible: boolean;
}

function ChartLegend({ items, time, since, onToggle }: { items: LegendItem[]; time: string; since: string | null; onToggle: (i: number) => void }) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const overflow = items.length > LEGEND_COLLAPSED;
  const shown = overflow && !expanded ? items.slice(0, LEGEND_COLLAPSED) : items;
  return (
    <div className="mt-2 flex flex-wrap items-start gap-x-2 gap-y-1 text-xs">
      <ul aria-label={t("charts.legend")} className={cn("flex min-w-0 flex-1 flex-wrap items-center gap-x-1 gap-y-0.5", expanded && "max-h-28 overflow-y-auto")}>
        <li className="px-1 py-0.5 text-muted-foreground">
          {t("charts.time")}: <span className="font-mono text-foreground">{time}</span>
        </li>
        {shown.map((it, i) => (
          <li key={it.label} className="min-w-0">
            <button
              type="button"
              aria-pressed={it.visible}
              title={t("charts.toggleSeries", { label: it.label })}
              onClick={() => onToggle(i)}
              className="inline-flex max-w-full items-center gap-1.5 rounded px-1 py-0.5 hover:bg-muted"
            >
              <span
                aria-hidden="true"
                className="inline-block size-2.5 shrink-0 rounded-[3px] border-2"
                style={{ borderColor: it.color, background: it.visible ? it.color : "transparent" }}
              />
              <span className={cn("truncate", it.visible ? "text-foreground" : "text-muted-foreground line-through")}>{it.label}</span>
              {it.visible && <span className="font-mono text-muted-foreground">{it.value}</span>}
            </button>
          </li>
        ))}
      </ul>
      <div className="ml-auto flex items-center gap-2">
        {since && <span className="py-0.5 text-muted-foreground">{since}</span>}
        {overflow && (
          <button type="button" aria-expanded={expanded} onClick={() => setExpanded((e) => !e)} className="rounded px-1 py-0.5 font-medium text-primary hover:underline">
            {expanded ? t("charts.legendLess") : t("charts.legendMore", { count: items.length - LEGEND_COLLAPSED })}
          </button>
        )}
      </div>
    </div>
  );
}

/**
 * Responsive, theme-aware uPlot wrapper. Data transformation lives in
 * lib/series.ts (tested separately). The plot is rebuilt only when its
 * structure (series labels, unit, theme, language, size options) changes;
 * data refreshes and legend toggles go through setSeries/setData.
 */
export function TimeSeriesChart({ series, unit, stacked, order, from, to, height = 200, isLoading, error, onRetry, title, yMax, yCap, hidden }: TimeSeriesChartProps) {
  const { t, i18n } = useTranslation();
  const { resolved } = useTheme();
  const locale = i18n.resolvedLanguage ?? "en";
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
  const plotKeyRef = useRef("");
  const liveRef = useRef<Live | null>(null);
  const widthRef = useRef(0);
  const [width, setWidth] = useState(0);
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);
  const [overrides, setOverrides] = useState<Record<string, boolean>>({});

  const aligned = useMemo(() => (series ? alignSeries(series, { order }) : undefined), [series, order]);
  const visible = useMemo(() => {
    if (!aligned) return [];
    const defaults = defaultVisibility(aligned.labels, hidden);
    return aligned.labels.map((l, i) => overrides[l] ?? defaults[i]!);
  }, [aligned, hidden, overrides]);
  const drawn = useMemo(() => (aligned ? (stacked ? stackVisible(aligned.ys, visible) : aligned.ys) : []), [aligned, stacked, visible]);

  const hasData = !!aligned && aligned.xs.length > 0;
  // The plot is created once the container has a measured width; later
  // width changes only resize it (see the setSize effect).
  const measured = width > 0;
  const structureKey = hasData ? JSON.stringify([aligned.labels, unit, !!stacked, height, resolved, locale, yMax ?? null, yCap ?? null]) : "";

  useLayoutEffect(() => {
    liveRef.current = aligned ? { data: aligned, drawn, visible, from, to } : null;
    widthRef.current = width;
  });

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const w = Math.floor(entries[0]?.contentRect.width ?? 0);
      setWidth((prev) => (Math.abs(prev - w) >= 1 ? w : prev));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [hasData]);

  // Create the plot when its structure changes.
  useEffect(() => {
    const el = containerRef.current;
    const live = liveRef.current;
    if (!el || !structureKey || !measured || !live) return;
    const fmt = (v: number | null) => formatValue(v, unit, locale);
    const colorOf = (i: number) => paletteColor(i, resolved);
    const axisColor = cssVar("--chart-axis", "#6b7280");
    const gridColor = cssVar("--chart-grid", "#e5e7eb");
    // Half the widest time label, so the last x tick label is not clipped.
    const rightPad = Math.ceil(measureText(timeFormatter(locale).format(new Date(2026, 0, 1, 23, 58))) / 2) + 6;

    const opts: uPlot.Options = {
      width: widthRef.current,
      height,
      padding: [8, rightPad, 0, 0],
      cursor: { drag: { x: false, y: false }, points: { size: 6 } },
      legend: { show: false },
      scales: {
        x: {
          time: true,
          range: (_u, min, max) => {
            const l = liveRef.current;
            return l?.from !== undefined && l.to !== undefined ? [l.from / 1000, l.to / 1000] : [min, max];
          },
        },
        y: {
          range: (_u, min) => {
            const l = liveRef.current;
            return yRange(min, l ? visibleMax(l.drawn, l.visible) : null, { yMax, yCap });
          },
        },
      },
      axes: [
        {
          stroke: axisColor,
          font: AXIS_FONT,
          space: 64,
          grid: { stroke: gridColor, width: 1 },
          ticks: { stroke: gridColor, width: 1 },
          values: (_u, splits, _axisIdx, _space, incr) => axisTickLabels(splits, incr, locale),
        },
        {
          stroke: axisColor,
          font: AXIS_FONT,
          grid: { stroke: gridColor, width: 1 },
          ticks: { stroke: gridColor, width: 1 },
          size: 64,
          values: (_u, vals) => vals.map((v) => fmt(v)),
        },
      ],
      series: [
        {},
        ...live.data.labels.map((label, i) => {
          const color = colorOf(i);
          return {
            label,
            show: live.visible[i] !== false,
            stroke: color,
            width: 1.5,
            fill: stacked ? withAlpha(color, resolved === "dark" ? 0.3 : 0.35) : undefined,
            spanGaps: false,
            points: { show: false },
          } satisfies uPlot.Series;
        }),
      ],
      bands: stacked ? stackBands(live.data.labels.length) : undefined,
      plugins: [
        cursorPlugin(
          () => liveRef.current,
          fmt,
          (s) => formatDateTime(s * 1000, locale),
          colorOf,
          (idx) => setHoverIdx((prev) => (prev === idx ? prev : idx)),
        ),
      ],
    };
    const plot = new uPlot(opts, [live.data.xs, ...live.drawn] as uPlot.AlignedData, el);
    plotRef.current = plot;
    plotKeyRef.current = structureKey;
    return () => {
      plot.destroy();
      plotRef.current = null;
      plotKeyRef.current = "";
    };
    // Everything structural is encoded in structureKey; data goes through setData below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [structureKey, measured]);

  // Update data / visibility in place.
  useEffect(() => {
    const plot = plotRef.current;
    if (!plot || !aligned || plotKeyRef.current !== structureKey) return;
    visible.forEach((v, i) => {
      if (plot.series[i + 1] && plot.series[i + 1]!.show !== v) plot.setSeries(i + 1, { show: v });
    });
    plot.setData([aligned.xs, ...drawn] as uPlot.AlignedData);
  }, [aligned, drawn, visible, from, to, structureKey]);

  useEffect(() => {
    if (plotRef.current && width > 0) plotRef.current.setSize({ width, height });
  }, [width, height]);

  if (error) {
    return <ErrorState error={error} onRetry={onRetry} className="py-6" />;
  }
  if (isLoading && !aligned) {
    return (
      <div role="status" aria-label={t("common.loading")}>
        <Skeleton style={{ height }} className="w-full" />
      </div>
    );
  }
  if (!hasData) {
    return (
      <EmptyState className="py-6">
        <span style={{ minHeight: height / 2 }}>{t("charts.empty")}</span>
      </EmptyState>
    );
  }

  const fmt = (v: number | null) => formatValue(v, unit, locale);
  const lv = legendValues(aligned, hoverIdx);
  const items: LegendItem[] = aligned.labels.map((label, i) => ({ label, color: paletteColor(i, resolved), value: fmt(lv.values[i] ?? null), visible: visible[i] !== false }));
  const sinceMs = dataStartHint((aligned.xs[0] ?? 0) * 1000, from, to);
  const since = sinceMs !== null ? t("charts.dataSince", { time: timeFormatter(locale).format(new Date(sinceMs)) }) : null;

  return (
    <div className="min-w-0">
      <div ref={containerRef} role="img" aria-label={title} className="relative w-full min-w-0" data-testid="timeseries-chart" />
      <ChartLegend
        items={items}
        time={lv.time !== null ? formatDateTime(lv.time * 1000, locale) : "–"}
        since={since}
        onToggle={(i) => {
          const label = aligned.labels[i]!;
          setOverrides((o) => ({ ...o, [label]: !(visible[i] !== false) }));
        }}
      />
    </div>
  );
}
