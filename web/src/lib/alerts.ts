// Pure alerting logic for the UI: rule drafts ↔ API input, client validation (mirrors internal/alert), durations,
// editor prefill from host charts, permission helpers and preview → chart data. Unit-tested in alerts.test.ts.
import type {
  AlertCondition,
  AlertFilter,
  AlertFlapping,
  AlertOperator,
  AlertRule,
  AlertRuleInput,
  AlertRulePreview,
  AlertRuleType,
  AlertSeverity,
} from "@/api/alerts";
import { can, type Role } from "@/api/roles";
import type { UnitKind } from "@/lib/format";
import { alertQueryIssues } from "@/lib/oql";
import type { ChartSeriesInput } from "@/lib/series";

// ---- durations ----

export const DURATION_UNITS = { s: 1, m: 60, h: 3600, d: 86400 } as const;
export type DurationUnit = keyof typeof DURATION_UNITS;

/** Largest unit that divides seconds evenly (0 → minutes). */
export function splitDuration(seconds: number): { value: number; unit: DurationUnit } {
  if (!seconds) return { value: 0, unit: "m" };
  for (const unit of ["d", "h", "m"] as const) {
    if (seconds % DURATION_UNITS[unit] === 0) return { value: seconds / DURATION_UNITS[unit], unit };
  }
  return { value: seconds, unit: "s" };
}

export function joinDuration(value: number, unit: DurationUnit): number {
  return Math.round(value * DURATION_UNITS[unit]);
}

/** 90 → "1m 30s", 3600 → "1h", 0 → "0s". */
export function formatDurationShort(seconds: number): string {
  if (!seconds) return "0s";
  const parts: string[] = [];
  let rest = Math.round(seconds);
  for (const [unit, size] of [["d", 86400], ["h", 3600], ["m", 60], ["s", 1]] as const) {
    if (rest >= size) {
      parts.push(`${Math.floor(rest / size)}${unit}`);
      rest %= size;
    }
  }
  return parts.join(" ");
}

// ---- drafts ----

export const FILTER_OPS = ["eq", "neq", "in", "not_in", "contains"] as const;
export type FilterOp = (typeof FILTER_OPS)[number];

export interface FilterDraft {
  field: string;
  op: FilterOp;
  /** Comma-separated for in/not_in. */
  values: string;
}

export interface LabelDraft {
  key: string;
  value: string;
}

/** Editable form state; numbers the user types stay strings until conversion. */
export interface RuleDraft {
  name: string;
  description: string;
  type: AlertRuleType;
  severity: AlertSeverity;
  enabled: boolean;
  interval_seconds: number;
  for_seconds: number;
  recovery_for_seconds: number;
  renotify_interval_seconds: number;
  channel_ids: string[];
  runbook_url: string;
  labels: LabelDraft[];
  flapping: AlertFlapping;
  version?: number;
  // condition
  metric: string;
  aggregation: NonNullable<AlertCondition["aggregation"]>;
  series_aggregation: "" | NonNullable<AlertCondition["series_aggregation"]>;
  window_seconds: number;
  lookback_seconds: number;
  filters: FilterDraft[];
  group_by: string[];
  operator: AlertOperator;
  threshold: string;
  recovery_threshold: string;
  missing_data: NonNullable<AlertCondition["missing_data"]>;
  query: string;
  severity_min: string;
  signal: NonNullable<AlertCondition["signal"]>;
  event: NonNullable<AlertCondition["event"]>;
  match: string;
  service_name: string;
  environment: string;
  transaction_name: string;
  min_requests: string;
  /** apm_error new_group: minimum weighted occurrences in the window */
  min_count: string;
  /** slo_burn: the SLO whose error budget is watched and its two burn windows (alerting.md §2.11) */
  slo_id: string;
  fast_factor: string;
  fast_long_seconds: number;
  fast_short_seconds: number;
  slow_factor: string;
  slow_long_seconds: number;
  slow_short_seconds: number;
  /** anomaly: the baseline of the watched signal (alerting.md §2.12); `signal` picks metric or apm */
  seasonality: NonNullable<AlertCondition["seasonality"]>;
  lookback_days: number;
  direction: NonNullable<AlertCondition["direction"]>;
  sensitivity: string;
  min_samples: string;
  min_deviation: string;
}

/** Seasons an anomaly baseline can use and their length in minutes (0 = the preceding windows). */
export const ANOMALY_SEASONS = { none: 0, hourly: 60, daily: 1440, weekly: 10080 } as const;

export const DEFAULT_FLAPPING: AlertFlapping = { enabled: true, transitions: 4, window_seconds: 3600, hold_seconds: 600 };

const TYPE_DEFAULTS: Record<AlertRuleType, Partial<RuleDraft>> = {
  metric_threshold: { window_seconds: 300, operator: "gt", group_by: ["host"], interval_seconds: 60 },
  log_match: { window_seconds: 300, operator: "gte", threshold: "1", group_by: ["host"], interval_seconds: 60 },
  no_data: { window_seconds: 300, lookback_seconds: 86400, group_by: ["host"], interval_seconds: 60 },
  discovery: { window_seconds: 900, lookback_seconds: 86400, group_by: [], interval_seconds: 300 },
  apm: { window_seconds: 300, operator: "gt", metric: "p95_ms", group_by: [], interval_seconds: 60 },
  apm_no_data: { window_seconds: 600, lookback_seconds: 86400, group_by: [], interval_seconds: 60 },
  oql: { window_seconds: 300, operator: "gt", group_by: [], interval_seconds: 60 },
  apm_error: { window_seconds: 300, group_by: [], interval_seconds: 60, event: "new_group" },
  slo_burn: { window_seconds: 3600, group_by: [], interval_seconds: 60 },
  anomaly: { window_seconds: 300, group_by: ["host"], interval_seconds: 60, signal: "metric", metric: "" },
};

/** Event rules ignore for_seconds (discovery, apm_error). */
export const isEventRule = (t: AlertRuleType) => t === "discovery" || t === "apm_error";

export function emptyDraft(type: AlertRuleType = "metric_threshold"): RuleDraft {
  return {
    name: "",
    description: "",
    type,
    severity: "warning",
    enabled: true,
    interval_seconds: 60,
    for_seconds: 0,
    recovery_for_seconds: 0,
    renotify_interval_seconds: 0,
    channel_ids: [],
    runbook_url: "",
    labels: [],
    flapping: { ...DEFAULT_FLAPPING },
    metric: "",
    aggregation: "avg",
    series_aggregation: "",
    window_seconds: 300,
    lookback_seconds: 86400,
    filters: [],
    group_by: ["host"],
    operator: "gt",
    threshold: "",
    recovery_threshold: "",
    missing_data: "keep",
    query: "",
    severity_min: "",
    signal: "host",
    event: "service_disappeared",
    match: "",
    service_name: "",
    environment: "",
    transaction_name: "",
    min_requests: "",
    min_count: "",
    slo_id: "",
    fast_factor: "14.4",
    fast_long_seconds: 3600,
    fast_short_seconds: 300,
    slow_factor: "6",
    slow_long_seconds: 21600,
    slow_short_seconds: 1800,
    seasonality: "daily",
    lookback_days: 7,
    direction: "upper",
    sensitivity: "3",
    min_samples: "3",
    min_deviation: "",
    ...TYPE_DEFAULTS[type],
  };
}

/** Switches the type, keeping common fields and applying the new type's condition defaults. */
export function changeType(d: RuleDraft, type: AlertRuleType): RuleDraft {
  const fresh = emptyDraft(type);
  return {
    ...fresh,
    name: d.name,
    description: d.description,
    severity: d.severity,
    enabled: d.enabled,
    for_seconds: isEventRule(type) ? 0 : d.for_seconds,
    recovery_for_seconds: d.recovery_for_seconds,
    renotify_interval_seconds: d.renotify_interval_seconds,
    channel_ids: d.channel_ids,
    runbook_url: d.runbook_url,
    labels: d.labels,
    flapping: d.flapping,
    version: d.version,
    filters: keepFilters(d.filters, type),
  };
}

/** The filters a type can still evaluate after a type switch. */
function keepFilters(filters: FilterDraft[], type: AlertRuleType): FilterDraft[] {
  if (type === "apm" || type === "apm_no_data" || type === "apm_error" || type === "slo_burn") return [];
  // A baseline reads the 1-minute rollup, which has no host name and no resource attributes (alerting.md §2.12).
  if (type === "anomaly") return filters.filter((f) => f.field === "host.id" || f.field === "service.name" || f.field.startsWith("attr."));
  if (type === "discovery" || type === "no_data") return filters.filter((f) => !f.field.startsWith("attr."));
  return filters;
}

const numStr = (v: number | null | undefined) => (v === null || v === undefined ? "" : String(v));

export function draftFromRule(rule: AlertRule): RuleDraft {
  const c = rule.condition;
  const base = emptyDraft(rule.type);
  return {
    ...base,
    name: rule.name,
    description: rule.description,
    severity: rule.severity,
    enabled: rule.enabled,
    interval_seconds: rule.interval_seconds,
    for_seconds: rule.for_seconds,
    recovery_for_seconds: rule.recovery_for_seconds,
    renotify_interval_seconds: rule.renotify_interval_seconds,
    channel_ids: [...rule.channel_ids],
    runbook_url: rule.runbook_url,
    labels: Object.entries(rule.labels).map(([key, value]) => ({ key, value })),
    flapping: { ...rule.flapping },
    version: rule.version,
    metric: c.metric ?? base.metric,
    aggregation: c.aggregation ?? base.aggregation,
    series_aggregation: c.series_aggregation ?? "",
    window_seconds: c.window_seconds ?? base.window_seconds,
    lookback_seconds: c.lookback_seconds ?? base.lookback_seconds,
    filters: (c.filters ?? []).map((f) => ({ field: f.field, op: f.op, values: f.values.join(", ") })),
    group_by: c.group_by ?? [],
    operator: c.operator ?? base.operator,
    threshold: numStr(c.threshold),
    recovery_threshold: numStr(c.recovery_threshold),
    missing_data: c.missing_data ?? "keep",
    query: c.query ?? "",
    severity_min: c.severity_min ?? "",
    signal: c.signal ?? "host",
    event: c.event ?? "service_disappeared",
    match: c.match ?? "",
    service_name: c.service_name ?? "",
    environment: c.environment ?? "",
    transaction_name: c.transaction_name ?? "",
    min_requests: c.min_requests ? String(c.min_requests) : "",
    min_count: c.min_count ? String(c.min_count) : "",
    slo_id: c.slo_id ?? "",
    fast_factor: numStr(c.windows?.[0]?.factor) || base.fast_factor,
    fast_long_seconds: c.windows?.[0]?.long_seconds ?? base.fast_long_seconds,
    fast_short_seconds: c.windows?.[0]?.short_seconds ?? base.fast_short_seconds,
    slow_factor: numStr(c.windows?.[1]?.factor) || base.slow_factor,
    slow_long_seconds: c.windows?.[1]?.long_seconds ?? base.slow_long_seconds,
    slow_short_seconds: c.windows?.[1]?.short_seconds ?? base.slow_short_seconds,
    seasonality: c.seasonality ?? base.seasonality,
    lookback_days: c.lookback_days ?? base.lookback_days,
    direction: c.direction ?? base.direction,
    sensitivity: numStr(c.sensitivity) || base.sensitivity,
    min_samples: numStr(c.min_samples) || base.min_samples,
    min_deviation: c.min_deviation ? String(c.min_deviation) : "",
  };
}

/** Draft of an unsaved rule definition, e.g. a rendered alert template opened in the editor. */
export function draftFromInput(input: AlertRuleInput): RuleDraft {
  const base = emptyDraft(input.type);
  const d = draftFromRule({
    name: input.name,
    description: input.description ?? "",
    type: input.type,
    severity: input.severity ?? "warning",
    enabled: input.enabled ?? true,
    interval_seconds: input.interval_seconds || base.interval_seconds,
    for_seconds: input.for_seconds ?? 0,
    recovery_for_seconds: input.recovery_for_seconds ?? 0,
    renotify_interval_seconds: input.renotify_interval_seconds ?? 0,
    channel_ids: input.channel_ids ?? [],
    runbook_url: input.runbook_url ?? "",
    labels: input.labels ?? {},
    flapping: input.flapping ?? DEFAULT_FLAPPING,
    condition: input.condition,
  } as unknown as AlertRule);
  return { ...d, version: undefined };
}

function parseNumber(s: string): number | null {
  const t = s.trim().replace(",", ".");
  if (t === "") return null;
  const n = Number(t);
  return Number.isFinite(n) ? n : null;
}

function filtersToInput(fs: FilterDraft[]): AlertFilter[] {
  return fs
    .filter((f) => f.field.trim() !== "")
    .map((f) => {
      const values = f.values
        .split(",")
        .map((v) => v.trim())
        .filter((v) => v !== "");
      return { field: f.field.trim(), op: f.op, values: f.op === "in" || f.op === "not_in" ? values : values.slice(0, 1) };
    });
}

export function draftToInput(d: RuleDraft): AlertRuleInput {
  const threshold = parseNumber(d.threshold) ?? undefined;
  const recovery = parseNumber(d.recovery_threshold);
  let condition: AlertCondition;
  switch (d.type) {
    case "metric_threshold":
      condition = {
        metric: d.metric.trim(),
        aggregation: d.aggregation,
        ...(d.series_aggregation ? { series_aggregation: d.series_aggregation } : {}),
        window_seconds: d.window_seconds,
        filters: filtersToInput(d.filters),
        group_by: d.group_by,
        operator: d.operator,
        threshold,
        recovery_threshold: recovery,
        missing_data: d.missing_data,
      };
      break;
    case "log_match":
      condition = {
        query: d.query,
        severity_min: d.severity_min,
        filters: filtersToInput(d.filters),
        group_by: d.group_by,
        window_seconds: d.window_seconds,
        operator: d.operator,
        threshold,
        recovery_threshold: recovery,
      };
      break;
    case "no_data":
      condition = {
        signal: d.signal,
        ...(d.signal === "metric" ? { metric: d.metric.trim() } : {}),
        filters: filtersToInput(d.filters),
        group_by: d.group_by.length ? d.group_by : ["host"],
        window_seconds: d.window_seconds,
        lookback_seconds: d.lookback_seconds,
      };
      break;
    case "discovery":
      condition = { event: d.event, filters: filtersToInput(d.filters), match: d.match, window_seconds: d.window_seconds, lookback_seconds: d.lookback_seconds };
      break;
    case "apm":
      condition = {
        service_name: d.service_name.trim(),
        environment: d.environment.trim() === "" ? null : d.environment.trim(),
        transaction_name: d.transaction_name.trim(),
        metric: d.metric,
        group_by: d.group_by,
        window_seconds: d.window_seconds,
        min_requests: parseNumber(d.min_requests) ?? 0,
        operator: d.operator,
        threshold,
        recovery_threshold: recovery,
        missing_data: d.missing_data,
      };
      break;
    case "apm_no_data":
      condition = {
        service_name: d.service_name.trim(),
        environment: d.environment.trim() === "" ? null : d.environment.trim(),
        group_by: d.group_by,
        window_seconds: d.window_seconds,
        lookback_seconds: d.lookback_seconds,
      };
      break;
    case "oql":
      // FACET attributes of the query become the series labels (no group_by/filters).
      condition = { query: d.query.trim(), window_seconds: d.window_seconds, operator: d.operator, threshold, recovery_threshold: recovery, missing_data: d.missing_data };
      break;
    case "apm_error":
      condition = {
        event: d.event,
        service_name: d.service_name.trim(),
        environment: d.environment.trim() === "" ? null : d.environment.trim(),
        match: d.match,
        window_seconds: d.window_seconds,
        ...(d.event === "new_group" ? { min_count: parseNumber(d.min_count) ?? 0 } : {}),
      };
      break;
    case "slo_burn":
      // The comparison is fixed (gte 1): the factors of the windows carry the configuration (alerting.md §2.11).
      condition = {
        slo_id: d.slo_id,
        windows: [
          { name: "fast", factor: parseNumber(d.fast_factor) ?? 14.4, long_seconds: d.fast_long_seconds, short_seconds: d.fast_short_seconds },
          { name: "slow", factor: parseNumber(d.slow_factor) ?? 6, long_seconds: d.slow_long_seconds, short_seconds: d.slow_short_seconds },
        ],
      };
      break;
    case "anomaly":
      // The comparison is fixed (gte 1): the sensitivity is the band, not a threshold (alerting.md §2.12).
      condition = {
        signal: d.signal === "apm" ? "apm" : "metric",
        ...(d.signal === "apm"
          ? {
              service_name: d.service_name.trim(),
              environment: d.environment.trim() === "" ? null : d.environment.trim(),
              transaction_name: d.transaction_name.trim(),
              metric: d.metric,
              min_requests: parseNumber(d.min_requests) ?? 0,
            }
          : {
              metric: d.metric.trim(),
              aggregation: d.aggregation,
              ...(d.series_aggregation ? { series_aggregation: d.series_aggregation } : {}),
              filters: filtersToInput(d.filters),
            }),
        group_by: d.group_by,
        window_seconds: d.window_seconds,
        seasonality: d.seasonality,
        lookback_days: d.lookback_days,
        direction: d.direction,
        sensitivity: parseNumber(d.sensitivity) ?? 3,
        min_samples: parseNumber(d.min_samples) ?? 3,
        min_deviation: parseNumber(d.min_deviation) ?? 0,
      };
      break;
  }
  const labels: Record<string, string> = {};
  for (const l of d.labels) if (l.key.trim()) labels[l.key.trim()] = l.value;
  return {
    name: d.name.trim(),
    description: d.description,
    type: d.type as AlertRuleInput["type"],
    severity: d.severity,
    enabled: d.enabled,
    interval_seconds: d.interval_seconds,
    for_seconds: isEventRule(d.type) ? 0 : d.for_seconds,
    recovery_for_seconds: d.recovery_for_seconds,
    condition,
    channel_ids: d.channel_ids,
    renotify_interval_seconds: d.renotify_interval_seconds,
    flapping: d.flapping,
    runbook_url: d.runbook_url.trim(),
    labels,
    ...(d.version ? { version: d.version } : {}),
  };
}

// ---- validation ----

export type ValidationKey =
  | "required" | "number" | "range" | "recoverySide" | "labelKey" | "filterValues" | "renotify" | "url" | "oqlQuery"
  | "anomalySeason" | "anomalySamples";

export interface ValidationIssue {
  key: ValidationKey;
  params?: Record<string, string | number>;
}

export type DraftErrors = Partial<Record<string, ValidationIssue>>;

const LABEL_KEY = /^[a-zA-Z0-9_.-]{1,64}$/;
const ATTR_KEY = /^[A-Za-z0-9_.\-/]{1,128}$/;

function inRange(errors: DraftErrors, field: string, v: number, min: number, max: number) {
  if (!Number.isFinite(v) || v < min || v > max) errors[field] = { key: "range", params: { min, max } };
}

/** Client-side checks mirroring internal/alert validation; the server remains authoritative. */
export function validateDraft(d: RuleDraft): DraftErrors {
  const e: DraftErrors = {};
  if (!d.name.trim()) e.name = { key: "required" };
  inRange(e, "interval_seconds", d.interval_seconds, 10, 3600);
  if (!isEventRule(d.type)) inRange(e, "for_seconds", d.for_seconds, 0, 86400);
  inRange(e, "recovery_for_seconds", d.recovery_for_seconds, 0, 86400);
  if (d.renotify_interval_seconds !== 0 && (d.renotify_interval_seconds < 300 || d.renotify_interval_seconds > 604800)) {
    e.renotify_interval_seconds = { key: "renotify" };
  }
  if (d.runbook_url.trim() && !/^https?:\/\/[^\s/]+/.test(d.runbook_url.trim())) e.runbook_url = { key: "url" };
  d.labels.forEach((l, i) => {
    if (l.key.trim() && !LABEL_KEY.test(l.key.trim())) e[`labels.${i}`] = { key: "labelKey" };
  });
  const needsThreshold = d.type === "metric_threshold" || d.type === "log_match" || d.type === "apm" || d.type === "oql";
  if (needsThreshold) {
    const th = parseNumber(d.threshold);
    if (d.threshold.trim() === "") e.threshold = { key: "required" };
    else if (th === null) e.threshold = { key: "number" };
    const rec = parseNumber(d.recovery_threshold);
    if (d.recovery_threshold.trim() !== "" && rec === null) e.recovery_threshold = { key: "number" };
    if (th !== null && rec !== null) {
      const up = d.operator === "gt" || d.operator === "gte";
      if ((up && rec > th) || (!up && rec < th)) e.recovery_threshold = { key: "recoverySide" };
    }
  }
  switch (d.type) {
    case "metric_threshold":
      if (!d.metric.trim()) e.metric = { key: "required" };
      inRange(e, "window_seconds", d.window_seconds, 10, 21600);
      break;
    case "log_match":
      inRange(e, "window_seconds", d.window_seconds, 10, 21600);
      break;
    case "no_data":
      if (d.signal === "metric" && !d.metric.trim()) e.metric = { key: "required" };
      inRange(e, "window_seconds", d.window_seconds, 60, 86400);
      inRange(e, "lookback_seconds", d.lookback_seconds, Math.max(600, d.window_seconds + 1), 604800);
      break;
    case "discovery":
      inRange(e, "window_seconds", d.window_seconds, 300, 86400);
      inRange(e, "lookback_seconds", d.lookback_seconds, d.window_seconds + 1, 604800);
      break;
    case "apm":
      if (!d.service_name.trim()) e.service_name = { key: "required" };
      inRange(e, "window_seconds", d.window_seconds, 60, 21600);
      if (d.min_requests.trim() !== "" && (parseNumber(d.min_requests) ?? -1) < 0) e.min_requests = { key: "number" };
      break;
    case "apm_no_data":
      inRange(e, "window_seconds", d.window_seconds, 60, 86400);
      inRange(e, "lookback_seconds", d.lookback_seconds, Math.max(600, d.window_seconds + 1), 604800);
      break;
    case "apm_error":
      inRange(e, "window_seconds", d.window_seconds, 60, 86400);
      if (d.event === "new_group" && d.min_count.trim() !== "" && (parseNumber(d.min_count) ?? -1) < 0) e.min_count = { key: "number" };
      break;
    case "slo_burn":
      if (!d.slo_id) e.slo_id = { key: "required" };
      for (const w of ["fast", "slow"] as const) {
        const factor = parseNumber(d[`${w}_factor`]);
        if (factor === null || factor <= 0 || factor > 1000) e[`${w}_factor`] = { key: "number" };
        inRange(e, `${w}_long_seconds`, d[`${w}_long_seconds`], 300, 86400);
        inRange(e, `${w}_short_seconds`, d[`${w}_short_seconds`], 60, d[`${w}_long_seconds`]);
      }
      break;
    case "anomaly": {
      if (d.signal === "apm") {
        if (!d.service_name.trim()) e.service_name = { key: "required" };
      } else if (!d.metric.trim()) {
        e.metric = { key: "required" };
      }
      inRange(e, "window_seconds", d.window_seconds, 60, 21600);
      inRange(e, "lookback_days", d.lookback_days, 1, 28);
      const sensitivity = parseNumber(d.sensitivity);
      if (sensitivity === null || sensitivity < 0.5 || sensitivity > 20) e.sensitivity = { key: "range", params: { min: 0.5, max: 20 } };
      const samples = parseNumber(d.min_samples);
      if (samples === null || samples < 2 || samples > 50) e.min_samples = { key: "range", params: { min: 2, max: 50 } };
      if (d.min_deviation.trim() !== "" && (parseNumber(d.min_deviation) ?? -1) < 0) e.min_deviation = { key: "number" };
      // The window must divide the season so every baseline sample covers the same slot of it.
      const season = ANOMALY_SEASONS[d.seasonality];
      const windowMinutes = Math.round(d.window_seconds / 60);
      if (season > 0 && windowMinutes > 0 && season % windowMinutes !== 0) e.window_seconds = { key: "anomalySeason" };
      const lags = Math.floor((d.lookback_days * 1440) / (season > 0 ? season : Math.max(windowMinutes, 1)));
      if (samples !== null && lags < samples) e.lookback_days = { key: "anomalySamples", params: { count: lags } };
      break;
    }
    case "oql":
      if (!d.query.trim()) e.query = { key: "required" };
      else if (alertQueryIssues(d.query).length > 0) e.query = { key: "oqlQuery" };
      inRange(e, "window_seconds", d.window_seconds, 60, 21600);
      break;
  }
  d.filters.forEach((f, i) => {
    const key = f.field.replace(/^(attr|resource)\./, "");
    if ((f.field.startsWith("attr.") || f.field.startsWith("resource.")) && !ATTR_KEY.test(key)) e[`filters.${i}`] = { key: "labelKey" };
    else if (f.field && f.values.split(",").every((v) => v.trim() === "")) e[`filters.${i}`] = { key: "filterValues" };
  });
  return e;
}

export function hasErrors(e: DraftErrors): boolean {
  return Object.keys(e).length > 0;
}

// ---- prefill ("create alert from this metric") ----

/** Search params of /alerts/rules/new. */
export interface RuleEditorSearch {
  type?: string;
  metric?: string;
  host?: string;
  hostName?: string;
  agg?: string;
  seriesAgg?: string;
  groupBy?: string;
  /** "attr.cpu.mode=idle": excluded attribute value (not_in filter). */
  exclude?: string;
  name?: string;
  /** JSON array of AlertFilter ({field, op, values}), e.g. an integration instance's resource attributes. */
  filters?: string;
  operator?: string;
  threshold?: string;
  /** window_seconds */
  window?: string;
  forSeconds?: string;
  severity?: string;
  /** Recommended template id and its API params (JSON), rendered by the server into the draft. */
  template?: string;
  tparams?: string;
}

const AGGS = ["avg", "min", "max", "sum", "last", "count", "rate", "p50", "p95", "p99"] as const;
const SERIES_AGGS = ["avg", "sum", "min", "max"] as const;
const TYPES: readonly AlertRuleType[] = ["metric_threshold", "log_match", "no_data", "discovery", "apm", "apm_no_data", "oql", "apm_error", "slo_burn", "anomaly"];
const OPERATORS = ["gt", "gte", "lt", "lte"] as const;
const SEVERITIES = ["critical", "warning", "info"] as const;

/** Parses the `filters` prefill (JSON); invalid entries are dropped, at most 20 are kept. */
export function parsePrefillFilters(raw: string | undefined): FilterDraft[] {
  if (!raw) return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  const out: FilterDraft[] = [];
  for (const f of parsed) {
    if (!f || typeof f !== "object") continue;
    const { field, op, values } = f as { field?: unknown; op?: unknown; values?: unknown };
    if (typeof field !== "string" || field === "" || field.length > 140) continue;
    if (!(FILTER_OPS as readonly string[]).includes(String(op))) continue;
    if (!Array.isArray(values) || values.length === 0 || values.length > 100 || !values.every((v) => typeof v === "string")) continue;
    out.push({ field, op: op as FilterOp, values: (values as string[]).join(", ") });
    if (out.length === 20) break;
  }
  return out;
}

const intIn = (s: string | undefined, min: number, max: number): number | null => {
  if (!s || !/^\d+$/.test(s)) return null;
  const n = Number(s);
  return n >= min && n <= max ? n : null;
};

export function applyPrefill(s: RuleEditorSearch): RuleDraft {
  const type = TYPES.includes(s.type as AlertRuleType) ? (s.type as AlertRuleType) : "metric_threshold";
  const d = emptyDraft(type);
  if (s.metric) d.metric = s.metric;
  if (s.agg && (AGGS as readonly string[]).includes(s.agg)) d.aggregation = s.agg as RuleDraft["aggregation"];
  if (s.seriesAgg && (SERIES_AGGS as readonly string[]).includes(s.seriesAgg)) d.series_aggregation = s.seriesAgg as RuleDraft["series_aggregation"];
  if (s.groupBy !== undefined) d.group_by = s.groupBy.split(",").map((g) => g.trim()).filter(Boolean);
  if (s.host) d.filters.push({ field: "host.id", op: "eq", values: s.host });
  if (s.exclude) {
    const [field, value] = s.exclude.split("=", 2);
    if (field && value) d.filters.push({ field, op: "not_in", values: value });
  }
  d.filters.push(...parsePrefillFilters(s.filters).filter((f) => f.field !== "host.id" || !s.host));
  if (s.operator && (OPERATORS as readonly string[]).includes(s.operator)) d.operator = s.operator as AlertOperator;
  if (s.threshold !== undefined && s.threshold.trim() !== "" && Number.isFinite(Number(s.threshold))) d.threshold = s.threshold.trim();
  const window = intIn(s.window, 10, 86400);
  if (window !== null) d.window_seconds = window;
  const forSeconds = intIn(s.forSeconds, 0, 86400);
  if (forSeconds !== null) d.for_seconds = forSeconds;
  if (s.severity && (SEVERITIES as readonly string[]).includes(s.severity)) d.severity = s.severity as AlertSeverity;
  if (s.name) d.name = s.name;
  else if (s.metric) d.name = s.hostName ? `${s.metric} on ${s.hostName}` : s.metric;
  return d;
}

/** Prefill for a host chart metric: one host, grouped by host; CPU utilization alerts on busy time (not idle). */
export function createAlertSearch(spec: { metric: string; hostId: string; hostName?: string; agg: string }): RuleEditorSearch {
  const s: RuleEditorSearch = { type: "metric_threshold", metric: spec.metric, host: spec.hostId, hostName: spec.hostName, agg: spec.agg, groupBy: "host" };
  if (spec.metric === "system.cpu.utilization") {
    s.exclude = "attr.cpu.mode=idle";
    s.seriesAgg = "sum";
  }
  return s;
}

// ---- permissions ----

/** Members change the rules and mutes they created; admins and owners change everything. */
export function canEditOwned(role: Role | null | undefined, createdByUserId: string | null | undefined, userId: string | null | undefined): boolean {
  if (can(role, "alerts.manage")) return true;
  return can(role, "alerts.write") && !!createdByUserId && createdByUserId === userId;
}

// ---- preview ----

export function labelsText(labels: Record<string, string>): string {
  if (labels["host.name"]) return labels["host.name"];
  const entries = Object.entries(labels).filter(([k]) => k !== "host.id" || !labels["host.name"]);
  if (entries.length === 0) return "";
  return entries.map(([k, v]) => `${k}=${v}`).join(", ");
}

export interface PreviewBand {
  from: number;
  to: number;
}

export interface PreviewChartData {
  series: ChartSeriesInput[];
  bands: PreviewBand[];
  fires: number[];
  hiddenSeries: number;
  incidents: number;
}

/** Converts a preview into chart series (≤ maxSeries, incident series first), would-fire bands and markers. */
export function previewChartData(p: AlertRulePreview, fallbackLabel: string, maxSeries = 10): PreviewChartData {
  const toMs = Date.parse(p.to.replace(/(\.\d{3})\d+/, "$1"));
  const sorted = [...p.series].sort((a, b) => b.incidents.length - a.incidents.length || a.key.localeCompare(b.key));
  const shown = sorted.slice(0, maxSeries);
  const parse = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));
  const bands: PreviewBand[] = [];
  const fires: number[] = [];
  let incidents = 0;
  for (const s of p.series) incidents += s.incidents.length;
  for (const s of shown) {
    for (const inc of s.incidents) bands.push({ from: parse(inc.opened_at), to: inc.resolved_at ? parse(inc.resolved_at) : toMs });
    for (const tr of s.transitions) if (tr.state === "firing") fires.push(parse(tr.at));
  }
  const used = new Map<string, number>();
  const series = shown.map((s) => {
    let label = labelsText(s.labels) || fallbackLabel;
    const n = used.get(label) ?? 0;
    used.set(label, n + 1);
    if (n > 0) label = `${label} (${n + 1})`;
    return {
      label,
      points: s.points.filter((pt): pt is [number, number] => pt[1] !== null && pt[1] !== undefined).map(([t, v]) => [t, v] as [number, number]),
    };
  });
  return { series, bands, fires: fires.sort((a, b) => a - b), hiddenSeries: Math.max(0, p.series.length - shown.length), incidents };
}

/** Chart unit for a preview: OTel unit "1" is a ratio (percent) except Apdex and anomaly deviation ratios. */
export function unitKindFor(unit: string, type: AlertRuleType, metric?: string): UnitKind {
  if (unit === "ms") return "ms";
  if (unit === "By") return "bytes";
  if (unit === "By/s") return "bytesPerSec";
  if (unit === "1") return type === "anomaly" || (type === "apm" && metric === "apdex") ? "number" : "percent";
  return "number";
}
