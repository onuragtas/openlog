// Locale-aware time axis labels for uPlot (x values in seconds).

const DAY_S = 86_400;

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
