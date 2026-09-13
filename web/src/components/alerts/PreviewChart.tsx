import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import { axisTickLabels, timeFormatter } from "@/lib/chart-axis";
import type { PreviewBand } from "@/lib/alerts";
import { formatDateTime, formatValue, type UnitKind } from "@/lib/format";
import { alignSeries, type ChartSeriesInput } from "@/lib/series";
import { useTheme } from "@/lib/theme";
import { paletteColor } from "@/lib/utils";

export interface PreviewChartProps {
  series: ChartSeriesInput[];
  unit: UnitKind;
  threshold: number | null;
  recoveryThreshold: number | null;
  bands: PreviewBand[];
  fires: number[];
  from: number;
  to: number;
  title: string;
  height?: number;
}

const AXIS_FONT = "12px system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif";

function cssVar(name: string, fallback: string): string {
  if (typeof window === "undefined") return fallback;
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

/**
 * Rule preview chart: one line per series, the threshold (solid) and recovery threshold (dashed) as horizontal lines,
 * shaded bands where the rule would have had an open incident and a marker where it would have fired.
 */
export function PreviewChart({ series, unit, threshold, recoveryThreshold, bands, fires, from, to, title, height = 240 }: PreviewChartProps) {
  const { t, i18n } = useTranslation();
  const { resolved } = useTheme();
  const locale = i18n.resolvedLanguage ?? "en";
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const aligned = useMemo(() => alignSeries(series), [series]);

  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return; // jsdom: no layout, no chart
    const ro = new ResizeObserver((entries) => setWidth(Math.floor(entries[0]?.contentRect.width ?? 0)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    const el = ref.current;
    if (!el || width <= 0) return;
    const axis = cssVar("--chart-axis", "#6b7280");
    const grid = cssVar("--chart-grid", "#e5e7eb");
    const danger = cssVar("--destructive", "#dc2626");
    const fmt = (v: number | null) => formatValue(v, unit, locale);
    const overlay: uPlot.Plugin = {
      hooks: {
        drawClear: (u) => {
          const ctx = u.ctx;
          ctx.save();
          ctx.fillStyle = danger;
          ctx.globalAlpha = resolved === "dark" ? 0.18 : 0.1;
          for (const b of bands) {
            const x0 = u.valToPos(Math.max(b.from, from) / 1000, "x", true);
            const x1 = u.valToPos(Math.min(b.to, to) / 1000, "x", true);
            ctx.fillRect(x0, u.bbox.top, Math.max(2, x1 - x0), u.bbox.height);
          }
          ctx.restore();
        },
        draw: (u) => {
          const ctx = u.ctx;
          const line = (value: number, color: string, dash: number[]) => {
            const y = u.valToPos(value, "y", true);
            if (y < u.bbox.top || y > u.bbox.top + u.bbox.height) return;
            ctx.save();
            ctx.strokeStyle = color;
            ctx.lineWidth = 1.5 * devicePixelRatio;
            ctx.setLineDash(dash.map((d) => d * devicePixelRatio));
            ctx.beginPath();
            ctx.moveTo(u.bbox.left, y);
            ctx.lineTo(u.bbox.left + u.bbox.width, y);
            ctx.stroke();
            ctx.restore();
          };
          if (recoveryThreshold !== null && recoveryThreshold !== threshold) line(recoveryThreshold, axis, [6, 4]);
          if (threshold !== null) line(threshold, danger, []);
          ctx.save();
          ctx.fillStyle = danger;
          const s = 5 * devicePixelRatio;
          for (const f of fires) {
            const x = u.valToPos(f / 1000, "x", true);
            const y = u.bbox.top + 1;
            ctx.beginPath();
            ctx.moveTo(x - s, y);
            ctx.lineTo(x + s, y);
            ctx.lineTo(x, y + 1.6 * s);
            ctx.closePath();
            ctx.fill();
          }
          ctx.restore();
        },
      },
    };
    const values = aligned.ys.flat().filter((v): v is number => v !== null);
    for (const v of [threshold, recoveryThreshold]) if (v !== null) values.push(v);
    const min = values.length ? Math.min(...values) : 0;
    const max = values.length ? Math.max(...values) : 1;
    const pad = (max - min || Math.abs(max) || 1) * 0.08;
    const opts: uPlot.Options = {
      width,
      height,
      padding: [12, 12, 0, 0],
      legend: { show: false },
      cursor: { drag: { x: false, y: false }, points: { size: 6 } },
      scales: { x: { time: true, range: () => [from / 1000, to / 1000] }, y: { range: () => [min < 0 ? min - pad : Math.max(0, min - pad), max + pad] } },
      axes: [
        { stroke: axis, font: AXIS_FONT, space: 64, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid, width: 1 }, values: (_u, splits, _i, _s, incr) => axisTickLabels(splits, incr, locale) },
        { stroke: axis, font: AXIS_FONT, size: 64, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid, width: 1 }, values: (_u, vals) => vals.map((v) => fmt(v)) },
      ],
      series: [{}, ...aligned.labels.map((label, i) => ({ label, stroke: paletteColor(i, resolved), width: 1.5, spanGaps: false, points: { show: false } }))],
      plugins: [overlay],
    };
    const plot = new uPlot(opts, [aligned.xs, ...aligned.ys] as uPlot.AlignedData, el);
    return () => plot.destroy();
  }, [aligned, width, height, unit, locale, resolved, threshold, recoveryThreshold, bands, fires, from, to]);

  const fmt = (v: number | null) => formatValue(v, unit, locale);
  return (
    <div className="min-w-0">
      <div ref={ref} role="img" aria-label={title} data-testid="alert-preview-chart" className="relative w-full min-w-0" />
      <ul aria-label={t("charts.legend")} className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        {aligned.labels.map((l, i) => (
          <li key={l} className="inline-flex items-center gap-1.5">
            <span aria-hidden="true" className="inline-block size-2.5 rounded-[3px]" style={{ background: paletteColor(i, resolved) }} />
            {l}
          </li>
        ))}
        {threshold !== null && (
          <li className="inline-flex items-center gap-1.5 text-muted-foreground">
            <span aria-hidden="true" className="inline-block h-0.5 w-4 bg-destructive" />
            {t("alerts.preview.thresholdLine", { value: fmt(threshold) })}
          </li>
        )}
        {recoveryThreshold !== null && recoveryThreshold !== threshold && (
          <li className="inline-flex items-center gap-1.5 text-muted-foreground">
            <span aria-hidden="true" className="inline-block h-0 w-4 border-t-2 border-dashed border-muted-foreground" />
            {t("alerts.preview.recoveryLine", { value: fmt(recoveryThreshold) })}
          </li>
        )}
        {bands.length > 0 && (
          <li className="inline-flex items-center gap-1.5 text-muted-foreground">
            <span aria-hidden="true" className="inline-block size-2.5 rounded-[3px] bg-destructive/20" />
            {t("alerts.preview.wouldFireBand")}
          </li>
        )}
        <li className="ml-auto text-muted-foreground">{formatDateTime(from, locale)} – {timeFormatter(locale).format(new Date(to))}</li>
      </ul>
    </div>
  );
}
