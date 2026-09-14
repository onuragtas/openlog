// Recurring mute schedule forms (alerting.md §5.2): weekly days or monthly rules (day of month, nth weekday,
// nth weekday of the week Mon–Fri) ↔ the RFC 5545 subset the API accepts, exception dates and summaries.
// Unit-tested in mute-schedule.test.ts.

export const WEEK_DAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"] as const;
export type WeekDay = (typeof WEEK_DAYS)[number];

const CODE: Record<WeekDay, string> = { mon: "MO", tue: "TU", wed: "WE", thu: "TH", fri: "FR", sat: "SA", sun: "SU" };
const DAY_OF: Record<string, WeekDay> = Object.fromEntries(Object.entries(CODE).map(([d, c]) => [c, d as WeekDay]));

export type Recurrence = "weekly" | "monthly";
export type MonthlyMode = "monthday" | "weekday";
/** Weekday choice of the nth-weekday mode: a day, or "weekday" = any of Mon–Fri. */
export type MonthlyDay = WeekDay | "weekday";
/** 1..4 = first … fourth, -1 = last. */
export type Ordinal = 1 | 2 | 3 | 4 | -1;
export const ORDINALS: Ordinal[] = [1, 2, 3, 4, -1];

export interface MonthlyForm {
  mode: MonthlyMode;
  /** Comma-separated days of month: 1..31, -1 = last day. */
  monthDays: string;
  ordinal: Ordinal;
  day: MonthlyDay;
}

export const DEFAULT_MONTHLY: MonthlyForm = { mode: "monthday", monthDays: "1", ordinal: 1, day: "mon" };

/** Parses "1, 15, -1" into sorted unique day numbers, or null when a value is invalid or the list is empty. */
export function parseMonthDays(s: string): number[] | null {
  const parts = s.split(/[,\s]+/).filter(Boolean);
  if (parts.length === 0) return null;
  const out = new Set<number>();
  for (const p of parts) {
    if (!/^[-+]?\d+$/.test(p)) return null;
    const n = Number(p);
    if (n === 0 || n < -31 || n > 31) return null;
    out.add(n);
  }
  return [...out].sort((a, b) => a - b);
}

/** RRULE of a monthly form, or null when the day list is invalid. */
export function monthlyRRule(f: MonthlyForm): string | null {
  if (f.mode === "monthday") {
    const days = parseMonthDays(f.monthDays);
    return days ? `FREQ=MONTHLY;BYMONTHDAY=${days.join(",")}` : null;
  }
  if (f.day === "weekday") return `FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=${f.ordinal}`;
  return `FREQ=MONTHLY;BYDAY=${f.ordinal}${CODE[f.day]}`;
}

/** Monthly form of a stored rule; null for rules the form cannot represent (weekly, or other monthly shapes). */
export function parseMonthlyRRule(rrule: string | null | undefined): MonthlyForm | null {
  if (!rrule) return null;
  const parts = Object.fromEntries(
    rrule
      .toUpperCase()
      .replace(/^RRULE:/, "")
      .split(";")
      .filter(Boolean)
      .map((p) => p.split("=", 2) as [string, string]),
  );
  if (parts.FREQ !== "MONTHLY") return null;
  const keys = Object.keys(parts).filter((k) => k !== "FREQ" && k !== "INTERVAL");
  if (keys.length === 1 && parts.BYMONTHDAY) {
    const days = parseMonthDays(parts.BYMONTHDAY.replace(/,/g, ", "));
    return days ? { ...DEFAULT_MONTHLY, mode: "monthday", monthDays: days.join(", ") } : null;
  }
  const ord = (n: number): Ordinal | null => ((ORDINALS as number[]).includes(n) ? (n as Ordinal) : null);
  if (keys.length === 2 && parts.BYDAY === "MO,TU,WE,TH,FR" && parts.BYSETPOS) {
    const o = ord(Number(parts.BYSETPOS));
    return o ? { ...DEFAULT_MONTHLY, mode: "weekday", ordinal: o, day: "weekday" } : null;
  }
  if (keys.length === 1 && parts.BYDAY) {
    const m = /^([-+]?\d)([A-Z]{2})$/.exec(parts.BYDAY);
    const o = m ? ord(Number(m[1])) : null;
    const d = m ? DAY_OF[m[2]!] : undefined;
    return o && d ? { ...DEFAULT_MONTHLY, mode: "weekday", ordinal: o, day: d } : null;
  }
  return null;
}

/** Validates, deduplicates and sorts YYYY-MM-DD exception dates; returns the invalid entries separately. */
export function normalizeDates(values: string[]): { dates: string[]; invalid: string[] } {
  const dates = new Set<string>();
  const invalid: string[] = [];
  for (const raw of values) {
    const v = raw.trim();
    if (!v) continue;
    const m = /^(\d{4})-?(\d{2})-?(\d{2})$/.exec(v);
    const d = m ? new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]))) : null;
    if (!m || !d || d.getUTCMonth() !== Number(m[2]) - 1 || d.getUTCDate() !== Number(m[3])) {
      invalid.push(v);
      continue;
    }
    dates.add(`${m[1]}-${m[2]}-${m[3]}`);
  }
  return { dates: [...dates].sort(), invalid };
}

/** Holiday calendar dates, one per line or comma-separated: YYYY-MM-DD, or MM-DD / --MM-DD for every year. */
export function parseCalendarDates(text: string): { dates: string[]; invalid: string[] } {
  const dates = new Set<string>();
  const invalid: string[] = [];
  for (const raw of text.split(/[\n,]+/)) {
    const v = raw.trim().replace(/^--/, "");
    if (!v) continue;
    const yearly = /^(\d{2})-(\d{2})$/.exec(v);
    if (yearly) {
      const d = new Date(Date.UTC(2000, Number(yearly[1]) - 1, Number(yearly[2])));
      if (d.getUTCMonth() === Number(yearly[1]) - 1 && d.getUTCDate() === Number(yearly[2])) dates.add(v);
      else invalid.push(raw.trim());
      continue;
    }
    const one = normalizeDates([v]);
    if (one.dates.length) dates.add(one.dates[0]!);
    else invalid.push(raw.trim());
  }
  return { dates: [...dates].sort(), invalid };
}

/** Summary parts of a monthly rule for display: i18n key + values (null when the rule is not a form shape). */
export type MonthlySummary = { key: "monthday"; days: string } | { key: "nthDay"; ordinal: Ordinal; day: WeekDay } | { key: "nthWeekday"; ordinal: Ordinal };

export function monthlySummary(rrule: string | null | undefined): MonthlySummary | null {
  const f = parseMonthlyRRule(rrule);
  if (!f) return null;
  if (f.mode === "monthday") return { key: "monthday", days: f.monthDays };
  if (f.day === "weekday") return { key: "nthWeekday", ordinal: f.ordinal };
  return { key: "nthDay", ordinal: f.ordinal, day: f.day };
}
