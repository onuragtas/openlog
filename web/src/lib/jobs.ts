// Pure helpers for the Jobs screens (docs/contracts/api.md "Job monitoring", D-141): the state a monitor is
// shown in, its schedule as a sentence, the ping commands and the form's validation. Rendering-independent
// and unit-tested through jobs.test.tsx.
import type { JobMonitor, JobMonitorInput, JobRunStatus } from "@/api/jobs";

export type JobState = "ok" | "late" | "failing" | "running" | "paused" | "unknown";

/**
 * The state shown next to a monitor. A disabled monitor is paused whatever its history; a monitor past its
 * grace period is late even before the sweeper concludes it, because that is what the person needs to see;
 * otherwise the last concluded run decides.
 */
export function jobState(m: Pick<JobMonitor, "enabled" | "state">): JobState {
  if (!m.enabled) return "paused";
  if (m.state.late) return "late";
  switch (m.state.status) {
    case "success":
      return "ok";
    case "failure":
    case "missed":
    case "overrun":
      return "failing";
    case "running":
      return "running";
    default:
      return "unknown";
  }
}

export function jobStateBadgeVariant(state: JobState): "success" | "destructive" | "warning" | "muted" {
  switch (state) {
    case "ok":
      return "success";
    case "failing":
      return "destructive";
    case "late":
      return "warning";
    default:
      return "muted";
  }
}

export function runStatusBadgeVariant(status: JobRunStatus | string): "success" | "destructive" | "warning" | "muted" {
  switch (status) {
    case "success":
      return "success";
    case "failure":
    case "missed":
      return "destructive";
    case "overrun":
      return "warning";
    default:
      return "muted";
  }
}

/** The schedule as one line: the cron expression with its zone, or the reporting interval. */
export function scheduleText(m: Pick<JobMonitor, "kind" | "cron" | "time_zone" | "interval_seconds">, everyLabel: (seconds: string) => string): string {
  if (m.kind === "interval") return everyLabel(formatSeconds(m.interval_seconds));
  return m.time_zone ? `${m.cron} · ${m.time_zone}` : m.cron;
}

/** A duration in seconds as the largest whole unit that fits ("15m", "6h", "2d"). */
export function formatSeconds(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "–";
  const units: [string, number][] = [
    ["d", 86400],
    ["h", 3600],
    ["m", 60],
  ];
  for (const [suffix, size] of units) {
    if (seconds % size === 0 && seconds >= size) return `${seconds / size}${suffix}`;
  }
  return `${seconds}s`;
}

/** A duration in milliseconds, for a run that openlog saw both ends of. */
export function formatDuration(ms: number | null | undefined, locale?: string): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms <= 0) return "–";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(seconds)} s`;
  const minutes = Math.floor(seconds / 60);
  const rest = Math.round(seconds % 60);
  return minutes < 60 ? `${minutes}m ${rest}s` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/** How late a run was, signed ("+2m", "−30s"); "–" when it is not worth showing. */
export function formatLate(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) return "–";
  const rounded = Math.round(seconds);
  if (rounded === 0) return "0s";
  const sign = rounded > 0 ? "+" : "−";
  return sign + formatSeconds(Math.abs(rounded));
}

/** A timestamp relative to now ("3 minutes ago", "in 30 seconds"). */
export function formatAgo(iso: string | null | undefined, locale?: string, now = Date.now()): string {
  const ms = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(ms)) return "–";
  const seconds = Math.round((ms - now) / 1000);
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["day", 86400],
    ["hour", 3600],
    ["minute", 60],
  ];
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) return rtf.format(Math.round(seconds / size), unit);
  }
  return rtf.format(seconds, "second");
}

/** An absolute timestamp, for the run list where the exact moment matters. */
export function formatRunTime(iso: string | null | undefined, locale?: string): string {
  const ms = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(ms)) return "–";
  return new Intl.DateTimeFormat(locale, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(ms);
}

/**
 * The crontab line that reports this job. `$?` is the shell's exit status of the command before it, so one
 * line reports both "it ran" and "it worked"; `-fsS -m 10 --retry 3` keeps a slow openlog from holding the
 * job up or failing it.
 */
export function pingCommand(pingURL: string): string {
  return `curl -fsS -m 10 --retry 3 "${pingURL}/$?" >/dev/null`;
}

/** The pair of commands for a job whose start matters too (so an overrun is visible). */
export function pingCommandWithStart(pingURL: string, command = "/usr/local/bin/job.sh"): string {
  return [
    `curl -fsS -m 10 "${pingURL}/start" >/dev/null`,
    `${command} 2>&1 | tail -c 4000 | curl -fsS -m 10 --retry 3 --data-binary @- "${pingURL}/$?" >/dev/null`,
  ].join("\n");
}

export type JobValidationKey = "required" | "cron" | "interval" | "grace" | "timeZone";

export type JobFormErrors = Partial<Record<keyof JobMonitorInput, JobValidationKey>>;

/** The five-field grammar, mirroring internal/jobs/cron.go; the server stays authoritative. */
const CRON_MACROS = ["@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly"];

export function cronValid(expr: string): boolean {
  const value = expr.trim().toLowerCase();
  if (value === "") return false;
  if (value.startsWith("@")) return CRON_MACROS.includes(value);
  const fields = value.split(/\s+/);
  if (fields.length !== 5) return false;
  const ranges: [number, number][] = [
    [0, 59],
    [0, 23],
    [1, 31],
    [1, 12],
    [0, 7],
  ];
  const names: Record<string, number>[] = [
    {},
    {},
    {},
    { jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6, jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12 },
    { sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6 },
  ];
  return fields.every((field, i) => fieldValid(field, ranges[i]!, names[i]!));
}

function fieldValid(field: string, [min, max]: [number, number], names: Record<string, number>): boolean {
  if (field === "*" || field === "?") return true;
  return field.split(",").every((part) => {
    if (part === "") return false;
    let body = part;
    const slash = body.indexOf("/");
    if (slash >= 0) {
      const step = Number(body.slice(slash + 1));
      if (!Number.isInteger(step) || step < 1 || step > max - min + 1) return false;
      body = body.slice(0, slash);
      if (body === "" || body === "*") return true;
    }
    const bounds = body.split("-");
    if (bounds.length > 2) return false;
    const values = bounds.map((b) => (b.toLowerCase() in names ? names[b.toLowerCase()]! : Number(b)));
    if (values.some((v) => !Number.isInteger(v) || v < min || v > max)) return false;
    return values.length === 1 || values[0]! <= values[1]!;
  });
}

/** Client-side checks mirroring internal/jobs validation; the server remains authoritative. */
export function validateJobInput(input: JobMonitorInput): JobFormErrors {
  const e: JobFormErrors = {};
  if (!input.name.trim()) e.name = "required";
  const kind = input.kind ?? "cron";
  if (kind === "cron") {
    if (!cronValid(input.cron ?? "")) e.cron = "cron";
    const zone = (input.time_zone ?? "").trim();
    if (zone && !timeZoneValid(zone)) e.time_zone = "timeZone";
  } else {
    const seconds = input.interval_seconds ?? 0;
    if (!Number.isInteger(seconds) || seconds < 60 || seconds > 7_776_000) e.interval_seconds = "interval";
  }
  const grace = input.grace_seconds ?? 300;
  if (!Number.isInteger(grace) || grace < 0 || grace > 86_400) e.grace_seconds = "grace";
  return e;
}

/** Whether the browser knows the zone; the server checks it against its own database as well. */
export function timeZoneValid(zone: string): boolean {
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

/** The zone the browser is in, offered as the default of a new cron monitor. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
