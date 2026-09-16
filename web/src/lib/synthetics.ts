// Pure helpers for the Synthetics screens (docs/contracts/api.md "Synthetic monitoring"): the current state
// of a check, formatting, the latency sparkline and the form's parsing and validation. Rendering-independent
// and unit-tested through the component tests.
import type { SyntheticCheck, SyntheticCheckInput, SyntheticLocationStatus, SyntheticSummary } from "@/api/synthetics";

export type CheckState = "up" | "down" | "paused" | "unknown";

/**
 * The state shown next to a check: a disabled check is paused whatever its history; otherwise the worst last
 * outcome of its locations decides, and a check that has not run yet has no state.
 */
export function checkState(check: Pick<SyntheticCheck, "enabled" | "status">): CheckState {
  if (!check.enabled) return "paused";
  const outcomes = check.status.map((s) => s.last_success).filter((v) => v !== null && v !== undefined);
  if (outcomes.length === 0) return "unknown";
  return outcomes.every((ok) => ok) ? "up" : "down";
}

export function stateBadgeVariant(state: CheckState): "success" | "destructive" | "muted" {
  switch (state) {
    case "up":
      return "success";
    case "down":
      return "destructive";
    default:
      return "muted";
  }
}

/** The most recent run over all locations, for the "last run" column. */
export function lastRunAt(status: SyntheticLocationStatus[]): string | null {
  const times = status.map((s) => s.last_run_at).filter((t): t is string => !!t);
  if (times.length === 0) return null;
  return times.reduce((a, b) => (Date.parse(a) >= Date.parse(b) ? a : b));
}

/** Uptime as a percentage ("99.95%"); null without runs in the range. */
export function formatUptime(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 2 }).format(v)}%`;
}

/** A duration in milliseconds, with fewer digits the larger it gets. */
export function formatDuration(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  const digits = v < 10 ? 1 : 0;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: digits }).format(v)} ms`;
}

export function formatCount(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(v);
}

/** An absolute run timestamp, for the failure list where the exact moment matters. */
export function formatRunTime(iso: string | null | undefined, locale?: string): string {
  const ms = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(ms)) return "–";
  return new Intl.DateTimeFormat(locale, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(ms);
}

/** A run time relative to now ("3 minutes ago", "in 30 seconds"), for the list and the schedule rows. */
export function formatRunAgo(iso: string | null | undefined, locale?: string, now = Date.now()): string {
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

/** "GET https://shop.example.com/health" — what the check actually calls. */
export function checkTarget(check: Pick<SyntheticCheck, "method" | "url">): string {
  return `${check.method} ${check.url}`;
}

/** Latency sparkline points ([x, y]); buckets without a p95 are skipped. */
export function latencyPoints(summary: SyntheticSummary | null | undefined): [number, number][] {
  if (!summary) return [];
  return summary.points
    .filter((p) => p.p95_ms !== null && p.p95_ms !== undefined && Number.isFinite(p.p95_ms))
    .map((p): [number, number] => [p.t, p.p95_ms as number]);
}

// ---- form parsing ----

/** Parses the headers textarea ("Name: value" per line); null when a line has no colon. */
export function parseHeaders(text: string): Record<string, string> | null {
  const out: Record<string, string> = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;
    const at = line.indexOf(":");
    if (at <= 0) return null;
    const name = line.slice(0, at).trim();
    if (name === "" || !/^[A-Za-z0-9_-]+$/.test(name)) return null;
    out[name] = line.slice(at + 1).trim();
  }
  return out;
}

export function formatHeaders(headers: Record<string, string> | undefined): string {
  return Object.entries(headers ?? {})
    .map(([k, v]) => `${k}: ${v}`)
    .join("\n");
}

/** Parses the expected status codes ("200, 204"); null when an entry is not a code. */
export function parseExpectedStatus(text: string): number[] | null {
  const parts = text
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
  if (parts.length === 0) return null;
  const out: number[] = [];
  for (const p of parts) {
    if (!/^\d{3}$/.test(p)) return null;
    const n = Number(p);
    if (n < 100 || n > 599) return null;
    if (!out.includes(n)) out.push(n);
  }
  return out.length > 10 ? null : out.sort((a, b) => a - b);
}

export function formatExpectedStatus(codes: number[] | undefined): string {
  return (codes ?? []).join(", ");
}

/** Validation issues of the form; the keys are translated as synthetics.validation.<key>. */
export type SyntheticValidationKey =
  | "required"
  | "url"
  | "expectedStatus"
  | "headers"
  | "timeout"
  | "interval"
  | "assertionValue"
  | "assertionPath";

export type SyntheticFormErrors = Partial<Record<keyof SyntheticCheckInput, SyntheticValidationKey>>;

/** Client-side checks mirroring internal/synthetics validation; the server remains authoritative. */
export function validateSyntheticInput(input: SyntheticCheckInput): SyntheticFormErrors {
  const e: SyntheticFormErrors = {};
  if (!input.name.trim()) e.name = "required";
  const url = input.url.trim();
  if (!url) {
    e.url = "required";
  } else {
    try {
      const u = new URL(url);
      if ((u.protocol !== "http:" && u.protocol !== "https:") || u.username || u.password || u.hash) e.url = "url";
    } catch {
      e.url = "url";
    }
  }
  if (input.expected_status !== undefined && input.expected_status.length === 0) e.expected_status = "expectedStatus";
  const timeout = input.timeout_ms ?? 10000;
  const interval = input.interval_seconds ?? 300;
  if (!Number.isInteger(timeout) || timeout < 500 || timeout > 60000 || timeout > interval * 1000) e.timeout_ms = "timeout";
  if (!Number.isInteger(interval) || interval < 30 || interval > 86400) e.interval_seconds = "interval";
  if (input.assertion_type === "contains" || input.assertion_type === "not_contains") {
    if (!input.assertion_value) e.assertion_value = "assertionValue";
  }
  if (input.assertion_type === "json_path") {
    const path = (input.assertion_path ?? "").trim();
    if (!path || path.startsWith(".") || path.endsWith(".") || path.includes("..")) e.assertion_path = "assertionPath";
  }
  return e;
}
