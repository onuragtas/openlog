// Locale-aware time axis labels for uPlot (x values in seconds) and axis sizing.
import type uPlot from "uplot";

const DAY_S = 86_400;

/** Width of the widest label line (labels may contain "\n"). */
export function axisLabelWidth(values: readonly (string | null | undefined)[] | null | undefined, measure: (text: string) => number): number {
  let max = 0;
  for (const v of values ?? []) {
    if (!v) continue;
    for (const line of String(v).split("\n")) max = Math.max(max, measure(line));
  }
  return Math.ceil(max);
}

let measureCtx: CanvasRenderingContext2D | null | undefined;

/** Rendered text width in px for a CSS font (canvas; approximated where canvas is unavailable). */
export function measureText(text: string, font: string): number {
  if (measureCtx === undefined) {
    try {
      measureCtx = typeof document !== "undefined" && typeof navigator !== "undefined" && !/jsdom/i.test(navigator.userAgent) ? document.createElement("canvas").getContext("2d") : null;
    } catch {
      measureCtx = null;
    }
  }
  if (!measureCtx) return text.length * 7;
  measureCtx.font = font;
  return measureCtx.measureText(text).width;
}

/**
 * uPlot `Axis.size` for a y axis: as wide as its formatted tick labels (e.g. "953.7 MiB") plus
 * tick length and gap, so long values are never clipped and short ones leave no empty gutter.
 */
export function yAxisSize(font: string, min = 36): uPlot.Axis.Size {
  return (u, values, axisIdx, cycleNum) => {
    const axis = u.axes[axisIdx] as uPlot.Axis & { _size?: number };
    // uPlot repeats sizing until the layout converges; keep the first measurement on later cycles.
    if (cycleNum > 1 && axis._size) return axis._size;
    const ticks = axis.ticks?.size ?? 10;
    const gap = axis.gap ?? 5;
    return Math.max(min, axisLabelWidth(values, (s) => measureText(s, font)) + ticks + gap + 4);
  };
}

function dtf(locale: string, opts: Intl.DateTimeFormatOptions, timeZone?: string): Intl.DateTimeFormat {
  return new Intl.DateTimeFormat(locale, { ...opts, ...(timeZone ? { timeZone } : {}) });
}

/** Hour/minute(/second) format. `tr` (and any locale defaulting to it) is 24h; `en` follows its 12h convention. */
export function timeFormatter(locale: string, withSeconds = false, timeZone?: string): Intl.DateTimeFormat {
  return dtf(locale, { hour: "2-digit", minute: "2-digit", ...(withSeconds ? { second: "2-digit" } : {}) }, timeZone);
}

function dayKey(sec: number, timeZone?: string): string {
  return dtf("en-CA", { year: "numeric", month: "2-digit", day: "2-digit" }, timeZone).format(new Date(sec * 1000));
}

/**
 * Tick labels. Day-or-longer increments show dates only; shorter increments
 * show times, and when the ticks span more than one calendar day the first
 * tick also carries the date (second line).
 */
export function axisTickLabels(splits: number[], incrSec: number, locale: string, timeZone?: string): string[] {
  if (splits.length === 0) return [];
  const date = dtf(locale, { day: "numeric", month: "short" }, timeZone);
  if (incrSec >= DAY_S) return splits.map((s) => date.format(new Date(s * 1000)));
  const time = timeFormatter(locale, incrSec < 60, timeZone);
  const spansDays = dayKey(splits[0]!, timeZone) !== dayKey(splits[splits.length - 1]!, timeZone);
  return splits.map((s, i) => {
    const d = new Date(s * 1000);
    const label = time.format(d);
    return i === 0 && spansDays ? `${label}\n${date.format(d)}` : label;
  });
}
