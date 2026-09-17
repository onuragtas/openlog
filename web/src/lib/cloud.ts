// Pure helpers for the Cloud screens (docs/contracts/api.md "Cloud connections"): the state of a connection,
// formatting, and the form's parsing and validation. Rendering-independent and unit-tested through the
// component tests.
import type {
  CloudConnection,
  CloudConnectionInput,
  CloudCredentialField,
  CloudCredentials,
  CloudProviderName,
  CloudScopeStatus,
} from "@/api/cloud";

export type CloudState = "ok" | "partial" | "error" | "paused" | "unknown";

/**
 * The state shown next to a connection. A disabled connection is paused whatever its history; otherwise the
 * worst outcome of its scopes decides, because one failing region is a real problem even when the others are
 * healthy. A connection that has not polled yet has no state.
 */
export function connectionState(c: Pick<CloudConnection, "enabled" | "status">): CloudState {
  if (!c.enabled) return "paused";
  const outcomes = c.status.map((s) => s.last_status).filter((s) => s !== "");
  if (outcomes.length === 0) return "unknown";
  if (outcomes.includes("error")) return "error";
  if (outcomes.includes("partial")) return "partial";
  return "ok";
}

export function stateBadgeVariant(state: CloudState): "success" | "warning" | "destructive" | "muted" {
  switch (state) {
    case "ok":
      return "success";
    case "partial":
      return "warning";
    case "error":
      return "destructive";
    default:
      return "muted";
  }
}

export function runBadgeVariant(status: string): "success" | "warning" | "destructive" | "muted" {
  switch (status) {
    case "ok":
      return "success";
    case "partial":
      return "warning";
    case "error":
      return "destructive";
    default:
      return "muted";
  }
}

/** The most recent poll over all scopes, for the "last poll" column. */
export function lastRunAt(status: CloudScopeStatus[]): string | null {
  const times = status.map((s) => s.last_run_at).filter((t): t is string => !!t);
  if (times.length === 0) return null;
  return times.reduce((a, b) => (Date.parse(a) >= Date.parse(b) ? a : b));
}

/** The first error any scope reported, so the list can say what is wrong without opening the detail. */
export function firstError(status: CloudScopeStatus[]): string {
  return status.find((s) => s.last_error !== "")?.last_error ?? "";
}

/** Data points collected by the last poll of every scope. */
export function lastMetrics(status: CloudScopeStatus[]): number {
  return status.reduce((n, s) => n + s.last_metrics, 0);
}

/** What one scope is called, as an i18n key under cloud.scopeLabel / cloud.scopeLabelPlural. */
export function scopeLabelKey(provider: CloudProviderName | string): "region" | "subscription" | "project" | "scope" {
  switch (provider) {
    case "aws":
      return "region";
    case "azure":
      return "subscription";
    case "gcp":
      return "project";
    default:
      return "scope";
  }
}

// ---- form parsing ----

/** A scope becomes part of a provider URL and of cloud.region / cloud.account.id (internal/cloudconnect). */
const SCOPE_TOKEN = /^[A-Za-z0-9._-]+$/;

/** Parses the scopes textarea (one per line); null when a line is not a usable scope. */
export function parseScopes(text: string): string[] | null {
  const out: string[] = [];
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;
    if (!SCOPE_TOKEN.test(line) || line.length > 200) return null;
    if (!out.includes(line)) out.push(line);
  }
  return out;
}

export function formatScopes(scopes: string[] | undefined): string {
  return (scopes ?? []).join("\n");
}

/** Only the fields the chosen provider uses, so a submitted credential is never one the server rejects. */
export function credentialsFor(fields: CloudCredentialField[], values: Record<string, string>): CloudCredentials {
  const out: Record<string, string> = {};
  for (const f of fields) {
    const v = (values[f.key] ?? "").trim();
    if (v !== "") out[f.key] = v;
  }
  return out as CloudCredentials;
}

/** Whether anything was typed into the credential fields of the chosen provider. */
export function hasCredentials(fields: CloudCredentialField[], values: Record<string, string>): boolean {
  return fields.some((f) => (values[f.key] ?? "").trim() !== "");
}

export type CloudValidationKey =
  | "required"
  | "scopes"
  | "services"
  | "interval"
  | "maxMetrics"
  | "maxCalls"
  | "credentials";

export type CloudFormErrors = Partial<Record<keyof CloudConnectionInput, CloudValidationKey>>;

/**
 * Client-side checks mirroring internal/cloudconnect validation; the server remains authoritative.
 * `requiredCredentials` are the provider's required fields — empty when the connection already has stored
 * credentials and none were typed, because omitting them keeps what is stored.
 */
export function validateCloudInput(
  input: CloudConnectionInput,
  opts: { requiredCredentials: CloudCredentialField[]; values: Record<string, string> },
): CloudFormErrors {
  const e: CloudFormErrors = {};
  if (!input.name.trim()) e.name = "required";
  if (!input.scopes || input.scopes.length === 0) e.scopes = "scopes";
  if (!input.services || input.services.length === 0) e.services = "services";

  const interval = input.poll_interval_seconds ?? 300;
  if (!Number.isInteger(interval) || interval < 60 || interval > 86400) e.poll_interval_seconds = "interval";
  const maxMetrics = input.max_metrics_per_poll ?? 5000;
  if (!Number.isInteger(maxMetrics) || maxMetrics < 100 || maxMetrics > 200000) e.max_metrics_per_poll = "maxMetrics";
  const maxCalls = input.max_api_calls_per_poll ?? 200;
  if (!Number.isInteger(maxCalls) || maxCalls < 1 || maxCalls > 5000) e.max_api_calls_per_poll = "maxCalls";

  for (const f of opts.requiredCredentials) {
    if ((opts.values[f.key] ?? "").trim() === "") {
      e.credentials = "credentials";
      break;
    }
  }
  return e;
}

// ---- formatting ----

export function formatCount(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(v);
}

/** A duration in milliseconds, with fewer digits the larger it gets. */
export function formatDurationMs(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  if (v >= 1000) return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(v / 1000)} s`;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(v)} ms`;
}

/** An absolute timestamp, for the poll history where the exact moment matters. */
export function formatRunTime(iso: string | null | undefined, locale?: string): string {
  const ms = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(ms)) return "–";
  return new Intl.DateTimeFormat(locale, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(ms);
}

/** A poll time relative to now ("3 minutes ago", "in 30 seconds"). */
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
