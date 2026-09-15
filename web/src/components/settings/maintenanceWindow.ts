import i18n from "@/i18n";
import type { components } from "@/api/schema.gen";

type Window = components["schemas"]["MaintenanceWindow"];
type Day = Window["days"][number];

type Translate = (key: string, options?: Record<string, string>) => string;
const t = (key: string, options?: Record<string, string>) => (i18n.t as unknown as Translate)(key, options);

/** By JavaScript getUTCDay() (0 = Sunday). */
const DAYS: Day[] = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];

function minutes(hhmm: string): number {
  const [h = 0, m = 0] = hhmm.split(":").map(Number);
  return h * 60 + m;
}

function clock(total: number): string {
  const m = ((total % 1440) + 1440) % 1440;
  return `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
}

/** Days as "Sat, Sun" / "Mon–Fri" (runs of three or more days, also across the week end); empty = every day. */
export function formatDays(days: readonly Day[]): string {
  const on = new Set(days);
  if (on.size === 0 || on.size === 7) return t("update.info.everyDay");
  // Start after an unselected day (Monday first when possible), so "sat, sun, mon" stays one run.
  let first = 1;
  for (let d = 0; d < 7; d++) {
    if (!on.has(DAYS[d]!)) {
      first = (d + 1) % 7;
      break;
    }
  }
  const runs: { start: number; len: number }[] = [];
  for (let i = 0; i < 7; i++) {
    const d = (first + i) % 7;
    if (!on.has(DAYS[d]!)) continue;
    const last = runs[runs.length - 1];
    if (last && (last.start + last.len) % 7 === d) last.len++;
    else runs.push({ start: d, len: 1 });
  }
  const name = (d: number) => t(`fleet.policy.weekdays.${DAYS[d % 7]}`);
  return runs
    .flatMap((r) => (r.len >= 3 ? [`${name(r.start)}–${name(r.start + r.len - 1)}`] : Array.from({ length: r.len }, (_, i) => name(r.start + i))))
    .join(", ");
}

/** The windows in UTC (without the "UTC" suffix): "Sat, Sun 02:00–05:00; Mon–Fri 03:00–04:00". */
export function formatWindowsUtc(windows: readonly Window[]): string {
  return windows.map((w) => t("update.info.windowRange", { days: formatDays(w.days), start: w.start, end: w.end })).join("; ");
}

/**
 * The windows shifted by a UTC offset in minutes (east positive; default: the browser's current offset), or null when
 * the offset is zero. Days move with the start time when it crosses midnight in local time.
 */
export function formatWindowsLocal(windows: readonly Window[], offsetMinutes = -new Date().getTimezoneOffset()): string | null {
  if (offsetMinutes === 0 || windows.length === 0) return null;
  return windows
    .map((w) => {
      const start = minutes(w.start) + offsetMinutes;
      const shift = Math.floor(start / 1440);
      const days = w.days.map((d) => DAYS[(((DAYS.indexOf(d) + shift) % 7) + 7) % 7]!);
      return t("update.info.windowRange", { days: formatDays(days), start: clock(start), end: clock(minutes(w.end) + offsetMinutes) });
    })
    .join("; ");
}
