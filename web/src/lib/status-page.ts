// Presentation helpers of the public status page (routes/status.tsx, docs/contracts/api.md "Status page").

export type ComponentStatus = "operational" | "degraded" | "partial_outage" | "major_outage" | "maintenance" | "unknown";
export type DayStatus = "operational" | "degraded" | "outage" | "no_data";

/** Tailwind classes of a status dot or banner. */
export function statusTone(status: string): { dot: string; banner: string } {
  switch (status) {
    case "operational":
      return { dot: "bg-emerald-500", banner: "border-emerald-500/40 bg-emerald-500/10" };
    case "degraded":
      return { dot: "bg-amber-500", banner: "border-amber-500/40 bg-amber-500/10" };
    case "partial_outage":
      return { dot: "bg-orange-500", banner: "border-orange-500/40 bg-orange-500/10" };
    case "major_outage":
      return { dot: "bg-red-600", banner: "border-red-600/40 bg-red-600/10" };
    case "maintenance":
      return { dot: "bg-sky-500", banner: "border-sky-500/40 bg-sky-500/10" };
    default:
      return { dot: "bg-muted-foreground", banner: "border-border bg-muted" };
  }
}

/** Bar color of one day of the uptime history. */
export function dayTone(status: string): string {
  switch (status) {
    case "operational":
      return "bg-emerald-500";
    case "degraded":
      return "bg-amber-500";
    case "outage":
      return "bg-red-600";
    default:
      return "bg-muted";
  }
}

/** Uptime percentage with two decimals below 100 % ("99.95%"), "100%" and "—" without data. */
export function formatUptime(pct: number | null | undefined, locale = "en"): string {
  if (pct === null || pct === undefined || !Number.isFinite(pct)) return "—";
  if (pct >= 100) return "100%";
  const floored = Math.floor(pct * 100) / 100;
  return `${floored.toLocaleString(locale, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}%`;
}

/** The last n days (default 30) of a 90-day history for narrow screens. */
export function visibleDays<T>(days: T[], narrow: boolean, n = 30): T[] {
  return narrow ? days.slice(Math.max(0, days.length - n)) : days;
}
