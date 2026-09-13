// MSW handlers for the alerting API (docs/contracts/alerting.md, api.md "Alerting") with in-memory data: a firing
// CPU incident with a timeline and delivered notifications, an acknowledged disk incident, resolved incidents, channels
// of every type, an active mute, and a preview that simulates the state machine over synthetic series.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type {
  AlertChannel,
  AlertDelivery,
  AlertIncident,
  AlertIncidentDetail,
  AlertIncidentEvent,
  AlertMute,
  AlertRule,
  AlertRuleInput,
  AlertRulePreview,
  AlertRuleTypeInfo,
} from "@/api/alerts";
import type { Role } from "@/api/roles";
import { authenticate } from "./account";
import { formatTs, HOST_IDS } from "./fixtures";

const API = "*/api/v1/alerts";
const MOCK_USER_ID = "7c1e2d9a-3b4f-4e5a-8b6c-000000000001";
const GRACE_ID = "7c1e2d9a-3b4f-4e5a-8b6c-000000000002";
const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };

type Code = "invalid_argument" | "permission_denied" | "not_found" | "failed_precondition";
const STATUS: Record<Code, number> = { invalid_argument: 400, permission_denied: 403, not_found: 404, failed_precondition: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

const uuid = () => `${Math.random().toString(16).slice(2, 10)}-0000-4000-8000-${Math.random().toString(16).slice(2, 14).padEnd(12, "0")}`;

interface MockState {
  rules: AlertRule[];
  incidents: AlertIncident[];
  events: Record<string, AlertIncidentEvent[]>;
  deliveries: AlertDelivery[];
  channels: AlertChannel[];
  mutes: AlertMute[];
  seq: number;
}

const CH = { slack: "c0000000-0000-4000-8000-000000000001", email: "c0000000-0000-4000-8000-000000000002", webhook: "c0000000-0000-4000-8000-000000000003", teams: "c0000000-0000-4000-8000-000000000004" };
const RULE = { cpu: "r0000000-0000-4000-8000-000000000001", disk: "r0000000-0000-4000-8000-000000000002", logs: "r0000000-0000-4000-8000-000000000003", nodata: "r0000000-0000-4000-8000-000000000004" };
const INC = { cpu: "i0000000-0000-4000-8000-000000000001", disk: "i0000000-0000-4000-8000-000000000002", old: "i0000000-0000-4000-8000-000000000003" };

function status(state: AlertRule["status"]["state"], extra: Partial<AlertRule["status"]> = {}, now = Date.now()): AlertRule["status"] {
  return {
    state, series_pending: 0, series_firing: state === "firing" ? 1 : 0, open_incidents: state === "firing" ? 1 : 0,
    last_evaluated_at: state === "disabled" ? null : formatTs(now - 20_000), last_result: state === "error" ? "error" : "ok",
    last_error: "", last_duration_ms: 42, next_evaluation_at: state === "disabled" ? null : formatTs(now + 40_000), owner: state === "disabled" ? null : "openlog-alert-7d9f-1a2b",
    ...extra,
  };
}

function seed(): MockState {
  const now = Date.now();
  const ago = (ms: number) => formatTs(now - ms);
  const min = 60_000;
  const flapping = { enabled: true, transitions: 4, window_seconds: 3600, hold_seconds: 600 };
  const rule = (r: Partial<AlertRule> & Pick<AlertRule, "id" | "name" | "type" | "condition">): AlertRule => ({
    description: "", severity: "warning", enabled: true, interval_seconds: 60, for_seconds: 0, recovery_for_seconds: 0, channel_ids: [],
    renotify_interval_seconds: 0, flapping, runbook_url: "", labels: {}, version: 1, created_by_user_id: MOCK_USER_ID,
    created_by_email: "admin@openlog.local", created_at: ago(9 * 86_400_000), updated_at: ago(86_400_000), status: status("ok", {}, now), ...r,
  });
  const rules: AlertRule[] = [
    rule({
      id: RULE.cpu, name: "High CPU", type: "metric_threshold", severity: "critical", for_seconds: 120, channel_ids: [CH.slack, CH.email, CH.webhook],
      runbook_url: "https://runbooks.example.com/cpu", labels: { team: "infra" }, status: status("firing", {}, now),
      condition: { metric: "system.cpu.utilization", aggregation: "avg", series_aggregation: "sum", window_seconds: 300, group_by: ["host"],
        filters: [{ field: "attr.cpu.mode", op: "not_in", values: ["idle"] }], operator: "gt", threshold: 0.9, recovery_threshold: 0.8, missing_data: "keep" },
    }),
    rule({
      id: RULE.disk, name: "Disk almost full", type: "metric_threshold", severity: "warning", channel_ids: [CH.slack], created_by_user_id: GRACE_ID,
      created_by_email: "grace@example.com", status: status("firing", {}, now),
      condition: { metric: "system.filesystem.utilization", aggregation: "max", window_seconds: 600, group_by: ["host", "attr.system.filesystem.mountpoint"],
        filters: [], operator: "gt", threshold: 0.85, recovery_threshold: null, missing_data: "keep" },
    }),
    rule({
      id: RULE.logs, name: "Payment timeouts", type: "log_match", severity: "critical", enabled: false, channel_ids: [CH.teams],
      status: status("disabled", {}, now),
      condition: { query: "timeout", severity_min: "ERROR", window_seconds: 300, group_by: ["host"], filters: [], operator: "gte", threshold: 5, recovery_threshold: null },
    }),
    rule({
      id: RULE.nodata, name: "Host stopped reporting", type: "no_data", severity: "critical", channel_ids: [CH.email], status: status("ok", {}, now),
      condition: { signal: "host", group_by: ["host"], window_seconds: 300, lookback_seconds: 86400, filters: [{ field: "resource.env", op: "eq", values: ["prod"] }] },
    }),
  ];
  const incident = (i: Partial<AlertIncident> & Pick<AlertIncident, "id" | "rule_id" | "rule_name" | "state" | "summary" | "opened_at">): AlertIncident => ({
    rule_type: "metric_threshold", severity: "critical", series_key: "host.id=" + HOST_IDS.web, labels: { "host.id": HOST_IDS.web, "host.name": "web-1", team: "infra", "alert.severity": "critical", "alert.rule_name": i.rule_name },
    value: 0.94, last_value: 0.96, threshold: 0.9, flapping: false, muted: false, acknowledged_at: null, acknowledged_by_email: null, resolved_at: null,
    resolved_by_email: null, resolve_reason: null, channel_ids: [CH.slack, CH.email, CH.webhook], ...i,
  });
  const incidents: AlertIncident[] = [
    incident({ id: INC.cpu, rule_id: RULE.cpu, rule_name: "High CPU", state: "open", opened_at: ago(14 * min), summary: "system.cpu.utilization avg over 5m is 0.94 (> 0.9) on web-1" }),
    incident({
      id: INC.disk, rule_id: RULE.disk, rule_name: "Disk almost full", severity: "warning", state: "acknowledged", opened_at: ago(3 * 3_600_000),
      series_key: `host.id=${HOST_IDS.db}|system.filesystem.mountpoint=/var/lib/postgresql`, value: 0.87, last_value: 0.88, threshold: 0.85,
      labels: { "host.id": HOST_IDS.db, "host.name": "db-1", "system.filesystem.mountpoint": "/var/lib/postgresql", "alert.severity": "warning" },
      summary: "system.filesystem.utilization max over 10m is 0.87 (> 0.85) on db-1", acknowledged_at: ago(2 * 3_600_000),
      acknowledged_by_email: "grace@example.com", channel_ids: [CH.slack], muted: true,
    }),
    incident({
      id: INC.old, rule_id: RULE.cpu, rule_name: "High CPU", state: "resolved", opened_at: ago(26 * 3_600_000), resolved_at: ago(25 * 3_600_000),
      resolve_reason: "recovered", series_key: `host.id=${HOST_IDS.worker}`, labels: { "host.id": HOST_IDS.worker, "host.name": "worker-1" },
      summary: "system.cpu.utilization avg over 5m is 0.92 (> 0.9) on worker-1", flapping: true,
    }),
  ];
  const ev = (id: number, at: number, kind: AlertIncidentEvent["kind"], message: string, details: Record<string, unknown> = {}, actor: string | null = null): AlertIncidentEvent => ({
    id, at: ago(at), kind, actor_email: actor, message, details,
  });
  const events: MockState["events"] = {
    [INC.cpu]: [
      ev(1, 14 * min, "opened", "system.cpu.utilization avg over 5m is 0.94 (> 0.9) on web-1", { value: 0.94, threshold: 0.9 }),
      ev(2, 14 * min - 1500, "notification_delivered", "delivered to #ops-alerts", { channel_type: "slack", attempt: 1, status_code: 200 }),
      ev(3, 14 * min - 1700, "notification_delivered", "delivered to On-call e-mail", { channel_type: "email", attempt: 1, status_code: 250 }),
      ev(4, 14 * min - 2100, "notification_delivered", "delivered to PagerBridge webhook", { channel_type: "webhook", attempt: 2, status_code: 202 }),
      ev(5, 6 * min, "note", "Checking the batch job that started at 10:00.", {}, "grace@example.com"),
    ],
    [INC.disk]: [
      ev(11, 3 * 3_600_000, "opened", "system.filesystem.utilization max over 10m is 0.87 (> 0.85) on db-1"),
      ev(12, 3 * 3_600_000 - 1200, "notification_delivered", "delivered to #ops-alerts", { channel_type: "slack", attempt: 1, status_code: 200 }),
      ev(13, 2 * 3_600_000, "acknowledged", "acknowledged by grace@example.com", {}, "grace@example.com"),
    ],
    [INC.old]: [
      ev(21, 26 * 3_600_000, "opened", "system.cpu.utilization avg over 5m is 0.92 (> 0.9) on worker-1"),
      ev(22, 25.8 * 3_600_000, "flapping", "flapping: recovery is held", { hold_seconds: 600 }),
      ev(23, 25 * 3_600_000, "resolved", "recovered: value 0.41", { reason: "recovered" }),
    ],
  };
  const delivery = (d: Partial<AlertDelivery> & Pick<AlertDelivery, "id" | "channel_id" | "channel_name" | "channel_type" | "kind" | "status">): AlertDelivery => ({
    incident_id: INC.cpu, rule_id: RULE.cpu, rule_name: "High CPU", attempts: 1, idempotency_key: `${INC.cpu}:${d.kind}:${d.channel_id}`,
    created_at: ago(14 * min), finished_at: ago(14 * min - 2000), next_attempt_at: null, last_error: "",
    attempt_log: [{ attempt: 1, at: ago(14 * min - 1000), duration_ms: 184, success: d.status === "delivered", status_code: 200, error: "" }], ...d,
  });
  const deliveries: AlertDelivery[] = [
    delivery({ id: "n-1", channel_id: CH.slack, channel_name: "#ops-alerts", channel_type: "slack", kind: "opened", status: "delivered" }),
    delivery({ id: "n-2", channel_id: CH.email, channel_name: "On-call e-mail", channel_type: "email", kind: "opened", status: "delivered",
      attempt_log: [{ attempt: 1, at: ago(14 * min - 1200), duration_ms: 412, success: true, status_code: 250, error: "" }] }),
    delivery({ id: "n-3", channel_id: CH.webhook, channel_name: "PagerBridge webhook", channel_type: "webhook", kind: "opened", status: "delivered", attempts: 2,
      attempt_log: [
        { attempt: 1, at: ago(14 * min - 1000), duration_ms: 10_001, success: false, status_code: 0, error: "post: context deadline exceeded" },
        { attempt: 2, at: ago(14 * min - 2000), duration_ms: 96, success: true, status_code: 202, error: "" },
      ] }),
    delivery({ id: "n-4", incident_id: INC.disk, rule_id: RULE.disk, rule_name: "Disk almost full", channel_id: CH.slack, channel_name: "#ops-alerts",
      channel_type: "slack", kind: "opened", status: "delivered", created_at: ago(3 * 3_600_000), finished_at: ago(3 * 3_600_000 - 1200) }),
  ];
  const channels: AlertChannel[] = [
    { id: CH.slack, name: "#ops-alerts", type: "slack", enabled: true, config: {}, secret_hints: { url: "https://hooks.slack.com/…/•••Xk2p" },
      created_by_email: "admin@openlog.local", created_at: ago(30 * 86_400_000), updated_at: ago(30 * 86_400_000),
      last_delivery: { at: ago(14 * min), status: "delivered", error: "" } },
    { id: CH.email, name: "On-call e-mail", type: "email", enabled: true, config: { to: ["oncall@example.com", "sre@example.com"] }, secret_hints: {},
      created_by_email: "admin@openlog.local", created_at: ago(30 * 86_400_000), updated_at: ago(2 * 86_400_000),
      last_delivery: { at: ago(14 * min), status: "delivered", error: "" } },
    { id: CH.webhook, name: "PagerBridge webhook", type: "webhook", enabled: true, config: {}, secret_hints: { url: "https://bridge.example.com/…/•••hook", hmac_secret: "•••••••• (set)" },
      created_by_email: "grace@example.com", created_at: ago(12 * 86_400_000), updated_at: ago(12 * 86_400_000),
      last_delivery: { at: ago(14 * min), status: "delivered", error: "" } },
    { id: CH.teams, name: "Payments Teams", type: "teams", enabled: false, config: {}, secret_hints: { url: "https://prod-12.westeurope.logic.azure.com/…/•••aB9c" },
      created_by_email: "admin@openlog.local", created_at: ago(5 * 86_400_000), updated_at: ago(86_400_000), last_delivery: null },
  ];
  const mutes: AlertMute[] = [
    { id: "m0000000-0000-4000-8000-000000000001", name: "db-1 maintenance", comment: "PostgreSQL major upgrade", starts_at: ago(3 * 3_600_000),
      ends_at: formatTs(now + 2 * 3_600_000), rule_ids: [], matchers: [{ label: "host.name", op: "eq", value: "db-1" }], schedule: null, active: true,
      created_by_user_id: GRACE_ID, created_by_email: "grace@example.com", created_at: ago(4 * 3_600_000), updated_at: ago(4 * 3_600_000) },
    { id: "m0000000-0000-4000-8000-000000000002", name: "Weekend load test", comment: "", starts_at: ago(5 * 86_400_000), ends_at: ago(4 * 86_400_000),
      rule_ids: [RULE.cpu], matchers: [], schedule: null, active: false, created_by_user_id: MOCK_USER_ID, created_by_email: "admin@openlog.local",
      created_at: ago(6 * 86_400_000), updated_at: ago(6 * 86_400_000) },
  ];
  return { rules, incidents, events, deliveries, channels, mutes, seq: 100 };
}

let db = seed();

/** Restores the seed data (Vitest). */
export function resetMockAlerts(): void {
  db = seed();
}

type Info = Parameters<HttpResponseResolver>[0];
type Access = "read" | "write" | "manage";

function guarded(access: Access, fn: (info: Info, role: Role) => Response | Promise<Response>): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    if (access !== "read") {
      if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
      const min: Role = access === "write" ? "member" : "admin";
      if (RANK[ctx.role] < RANK[min]) return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
    }
    return fn(info, ctx.role);
  };
}

async function json<T>(request: Request): Promise<Partial<T>> {
  try {
    return ((await request.json()) as Partial<T>) ?? {};
  } catch {
    return {};
  }
}

const idOf = (info: Info) => String(info.params.id ?? "");

function validateRule(r: Partial<AlertRuleInput>): string | null {
  if (!r.name?.trim()) return "name: must be 1-200 characters";
  if (!r.type) return "type: must be one of metric_threshold, log_match, no_data, discovery, apm";
  const c = r.condition ?? {};
  if ((r.type === "metric_threshold" || r.type === "log_match" || r.type === "apm") && (c.threshold === undefined || c.threshold === null)) return "condition.threshold: required";
  if (r.type === "metric_threshold" && !c.metric) return "condition.metric: required, at most 256 bytes";
  if (r.type === "apm" && !c.service_name) return "condition.service_name: required, at most 512 bytes";
  return null;
}

function toRule(input: Partial<AlertRuleInput>, base?: AlertRule): AlertRule {
  const now = Date.now();
  return {
    id: base?.id ?? uuid(),
    name: input.name!.trim(),
    description: input.description ?? "",
    type: input.type!,
    severity: input.severity ?? "warning",
    enabled: input.enabled ?? true,
    interval_seconds: input.interval_seconds || 60,
    for_seconds: input.for_seconds ?? 0,
    recovery_for_seconds: input.recovery_for_seconds ?? 0,
    condition: input.condition ?? {},
    channel_ids: input.channel_ids ?? [],
    renotify_interval_seconds: input.renotify_interval_seconds ?? 0,
    flapping: input.flapping ?? { enabled: true, transitions: 4, window_seconds: 3600, hold_seconds: 600 },
    runbook_url: input.runbook_url ?? "",
    labels: input.labels ?? {},
    version: (base?.version ?? 0) + 1,
    created_by_user_id: base?.created_by_user_id ?? MOCK_USER_ID,
    created_by_email: base?.created_by_email ?? "admin@openlog.local",
    created_at: base?.created_at ?? formatTs(now),
    updated_at: formatTs(now),
    status: base?.status ?? status(input.enabled === false ? "disabled" : "unknown", { last_evaluated_at: null, owner: null }),
  };
}

/** Deterministic series: a busy host that crosses the threshold twice and a quiet one (per rule type). */
export function mockPreview(input: AlertRuleInput, hours: number, now = Date.now()): AlertRulePreview {
  const c = input.condition;
  const step = Math.max(60, input.interval_seconds || 60, Math.ceil((hours * 3600) / 1440 / 60) * 60) * 1000;
  const to = Math.floor(now / step) * step;
  const from = to - hours * 3_600_000;
  const threshold = c.threshold ?? (input.type === "no_data" ? c.window_seconds ?? 300 : 1);
  const recovery = c.recovery_threshold ?? threshold;
  const op = c.operator ?? "gte";
  const up = op === "gt" || op === "gte";
  const breach = (v: number) => (op === "gt" ? v > threshold : op === "gte" ? v >= threshold : op === "lt" ? v < threshold : v <= threshold);
  const recovered = (v: number) => !(op === "gt" ? v > recovery : op === "gte" ? v >= recovery : op === "lt" ? v < recovery : v <= recovery);
  const scale = Math.abs(threshold) || 1;
  const hosts = [
    { id: HOST_IDS.web, name: "web-1", base: up ? 0.62 : 1.4, amp: 0.25, spike: up ? 0.45 : -0.9 },
    { id: HOST_IDS.db, name: "db-1", base: up ? 0.35 : 1.8, amp: 0.1, spike: 0 },
  ];
  const series = hosts.map((h, hi) => {
    const points: [number, number | null][] = [];
    const transitions: AlertRulePreview["series"][number]["transitions"] = [];
    const incidents: AlertRulePreview["series"][number]["incidents"] = [];
    let state: "ok" | "pending" | "firing" = "ok";
    let pendingSince = 0;
    let open: (typeof incidents)[number] | null = null;
    for (let t = from + step, i = 0; t <= to; t += step, i++) {
      const phase = i / 24 + hi;
      let v = (h.base + h.amp * Math.sin(phase)) * scale;
      const frac = (t - from) / (to - from);
      if ((frac > 0.3 && frac < 0.42) || frac > 0.86) v += h.spike * scale;
      v = Math.round(v * 1000) / 1000;
      points.push([t, v]);
      const at = formatTs(t);
      if (state === "firing") {
        if (breach(v)) continue;
        if (recovered(v)) {
          state = "ok";
          transitions.push({ at, state: "ok", value: v });
          if (open) open.resolved_at = at;
          open = null;
        }
      } else if (breach(v)) {
        if (state === "ok") {
          pendingSince = t;
          if ((input.for_seconds ?? 0) > 0) {
            state = "pending";
            transitions.push({ at, state: "pending", value: v });
            continue;
          }
        }
        if (t - pendingSince >= (input.for_seconds ?? 0) * 1000) {
          state = "firing";
          transitions.push({ at, state: "firing", value: v });
          open = { opened_at: at, resolved_at: null, peak: v };
          incidents.push(open);
        }
      } else if (state === "pending") {
        state = "ok";
        transitions.push({ at, state: "ok", value: v });
      } else if (open) {
        open.peak = up ? Math.max(open.peak ?? v, v) : Math.min(open.peak ?? v, v);
      }
    }
    return { key: `host.id=${h.id}`, labels: { "host.id": h.id, "host.name": h.name }, points, transitions, incidents };
  });
  const hasOperator = input.type === "metric_threshold" || input.type === "log_match" || input.type === "apm";
  return {
    from: formatTs(from), to: formatTs(to), step_seconds: step / 1000, operator: hasOperator ? op : null, threshold, recovery_threshold: recovery,
    unit: input.type === "metric_threshold" && (c.metric ?? "").endsWith("utilization") ? "1" : input.type === "apm" ? "ms" : "", series, truncated: false, approximate: false,
  };
}

function incidentDetail(inc: AlertIncident): AlertIncidentDetail {
  return { ...inc, events: db.events[inc.id] ?? [], deliveries: db.deliveries.filter((d) => d.incident_id === inc.id) };
}

function addEvent(incidentId: string, kind: AlertIncidentEvent["kind"], message: string, actor: string | null): AlertIncidentEvent {
  const e: AlertIncidentEvent = { id: ++db.seq, at: formatTs(Date.now()), kind, actor_email: actor, message, details: {} };
  (db.events[incidentId] ??= []).push(e);
  return e;
}

const RULE_TYPES: AlertRuleTypeInfo[] = [
  { type: "metric_threshold", available: true, reason: "" },
  { type: "log_match", available: true, reason: "" },
  { type: "no_data", available: true, reason: "" },
  { type: "discovery", available: true, reason: "" },
  { type: "apm", available: true, reason: "" },
];

const canChange = (role: Role, createdBy: string | null) => RANK[role] >= RANK.admin || createdBy === MOCK_USER_ID;

export const alertHandlers = [
  http.get(`${API}/rule-types`, guarded("read", () => HttpResponse.json({ types: RULE_TYPES }))),
  http.get(`${API}/rules`, guarded("read", () => HttpResponse.json({ rules: [...db.rules].sort((a, b) => a.name.localeCompare(b.name)) }))),
  http.post(`${API}/rules/preview`, guarded("read", async ({ request }) => {
    const b = await json<{ rule: AlertRuleInput; hours: number }>(request);
    const err = b.rule ? validateRule(b.rule) : "rule: required";
    if (err) return fail("invalid_argument", err);
    return HttpResponse.json(mockPreview(b.rule!, b.hours ?? 6));
  })),
  http.post(`${API}/rules`, guarded("write", async ({ request }) => {
    const input = await json<AlertRuleInput>(request);
    const err = validateRule(input);
    if (err) return fail("invalid_argument", err);
    const r = toRule(input);
    db.rules.push(r);
    return HttpResponse.json(r, { status: 201 });
  })),
  http.get(`${API}/rules/:id`, guarded("read", (info) => {
    const r = db.rules.find((x) => x.id === idOf(info));
    if (!r) return fail("not_found", "not found");
    const series = db.incidents.filter((i) => i.rule_id === r.id && i.state !== "resolved").map((i) => ({
      series_key: i.series_key, labels: i.labels, state: "firing" as const, value: i.last_value, pending_since: null, firing_since: i.opened_at,
      recovering_since: null, incident_id: i.id, flapping: i.flapping, updated_at: i.opened_at,
    }));
    return HttpResponse.json({ ...r, series });
  })),
  http.get(`${API}/rules/:id/evaluations`, guarded("read", (info) => {
    const r = db.rules.find((x) => x.id === idOf(info));
    if (!r) return fail("not_found", "not found");
    // A quiet hour of 1-minute evaluations for one series (alerting.md §3.6 shape).
    const now = Math.floor(Date.now() / 60_000) * 60_000;
    const at = (i: number) => now - (59 - i) * 60_000;
    const evaluations = Array.from({ length: 60 }, (_, i) => ({ at: formatTs(at(i)), firing_series: 0, evaluations: 1, errors: 0, duration_ms: 6, max_duration_ms: 6 }));
    const points = Array.from({ length: 60 }, (_, i) => [at(i), 0.3 + 0.1 * Math.sin(i / 6), "ok"] as [number, number, "ok"]);
    return HttpResponse.json({ from: formatTs(now - 3_600_000), to: formatTs(now), step_seconds: 60, evaluations,
      series: [{ series_key: "host.id=web-1", labels: { "host.name": "web-1" }, points }], truncated: false });
  })),
  http.put(`${API}/rules/:id`, guarded("write", async (info, role) => {
    const i = db.rules.findIndex((x) => x.id === idOf(info));
    if (i < 0) return fail("not_found", "not found");
    if (!canChange(role, db.rules[i]!.created_by_user_id)) return fail("permission_denied", "members can only change rules and mutes they created");
    const input = await json<AlertRuleInput>(info.request);
    const err = validateRule(input);
    if (err) return fail("invalid_argument", err);
    db.rules[i] = toRule(input, db.rules[i]);
    return HttpResponse.json(db.rules[i]);
  })),
  http.delete(`${API}/rules/:id`, guarded("write", (info, role) => {
    const r = db.rules.find((x) => x.id === idOf(info));
    if (!r) return fail("not_found", "not found");
    if (!canChange(role, r.created_by_user_id)) return fail("permission_denied", "members can only change rules and mutes they created");
    db.rules = db.rules.filter((x) => x.id !== r.id);
    return new HttpResponse(null, { status: 204 });
  })),
  ...(["enable", "disable"] as const).map((action) =>
    http.post(`${API}/rules/:id/${action}`, guarded("write", (info, role) => {
      const r = db.rules.find((x) => x.id === idOf(info));
      if (!r) return fail("not_found", "not found");
      if (!canChange(role, r.created_by_user_id)) return fail("permission_denied", "members can only change rules and mutes they created");
      r.enabled = action === "enable";
      r.version++;
      r.status = status(r.enabled ? "unknown" : "disabled", r.enabled ? { last_evaluated_at: null } : {});
      if (!r.enabled) {
        for (const inc of db.incidents) {
          if (inc.rule_id === r.id && inc.state !== "resolved") Object.assign(inc, { state: "resolved", resolved_at: formatTs(Date.now()), resolve_reason: "rule_disabled" });
        }
      }
      return HttpResponse.json(r);
    })),
  ),

  http.get(`${API}/incidents`, guarded("read", ({ request }) => {
    const url = new URL(request.url);
    const states = (url.searchParams.get("state") ?? "").split(",").filter(Boolean);
    const severity = url.searchParams.get("severity");
    if (states.some((s) => !["open", "acknowledged", "resolved"].includes(s))) return fail("invalid_argument", "state must be a comma-separated list of open, acknowledged, resolved");
    const list = db.incidents
      .filter((i) => (states.length === 0 || states.includes(i.state)) && (!severity || i.severity === severity))
      .sort((a, b) => b.opened_at.localeCompare(a.opened_at));
    const count = (s: string) => db.incidents.filter((i) => i.state === s).length;
    return HttpResponse.json({ incidents: list, next_cursor: null, counts: { open: count("open"), acknowledged: count("acknowledged"), resolved: count("resolved") } });
  })),
  http.get(`${API}/incidents/:id`, guarded("read", (info) => {
    const inc = db.incidents.find((i) => i.id === idOf(info));
    return inc ? HttpResponse.json(incidentDetail(inc)) : fail("not_found", "not found");
  })),
  http.post(`${API}/incidents/:id/acknowledge`, guarded("write", (info) => {
    const inc = db.incidents.find((i) => i.id === idOf(info));
    if (!inc) return fail("not_found", "not found");
    if (inc.state === "resolved") return fail("failed_precondition", "the incident is already resolved");
    if (inc.state === "open") {
      Object.assign(inc, { state: "acknowledged", acknowledged_at: formatTs(Date.now()), acknowledged_by_email: "admin@openlog.local" });
      addEvent(inc.id, "acknowledged", "acknowledged by admin@openlog.local", "admin@openlog.local");
    }
    return HttpResponse.json(inc);
  })),
  http.post(`${API}/incidents/:id/resolve`, guarded("write", async (info) => {
    const inc = db.incidents.find((i) => i.id === idOf(info));
    if (!inc) return fail("not_found", "not found");
    if (inc.state === "resolved") return fail("failed_precondition", "the incident is already resolved");
    const b = await json<{ note: string }>(info.request);
    Object.assign(inc, { state: "resolved", resolved_at: formatTs(Date.now()), resolved_by_email: "admin@openlog.local", resolve_reason: "manual" });
    addEvent(inc.id, "resolved", `resolved by admin@openlog.local${b.note ? `: ${b.note}` : ""}`, "admin@openlog.local");
    return HttpResponse.json(inc);
  })),
  http.post(`${API}/incidents/:id/notes`, guarded("write", async (info) => {
    const inc = db.incidents.find((i) => i.id === idOf(info));
    if (!inc) return fail("not_found", "not found");
    const b = await json<{ text: string }>(info.request);
    if (!b.text?.trim()) return fail("invalid_argument", "text: must be 1-4000 characters");
    return HttpResponse.json(addEvent(inc.id, "note", b.text, "admin@openlog.local"), { status: 201 });
  })),

  http.get(`${API}/channels`, guarded("read", () => HttpResponse.json({ channels: db.channels, secrets_configured: true }))),
  http.post(`${API}/channels`, guarded("manage", async ({ request }) => {
    const b = await json<{ name: string; type: AlertChannel["type"]; enabled: boolean; config: AlertChannel["config"]; secrets: Record<string, string> }>(request);
    if (!b.name?.trim()) return fail("invalid_argument", "name: must be 1-200 characters");
    if (!b.type) return fail("invalid_argument", "type: must be slack, email, webhook or teams");
    if (b.type !== "email" && !b.secrets?.url) return fail("invalid_argument", "secrets.url: required");
    if (b.type === "email" && !(b.config?.to ?? []).length) return fail("invalid_argument", "config.to: 1-50 recipients");
    const generated = b.type === "webhook" && !b.secrets?.hmac_secret ? { hmac_secret: "5f0c".padEnd(64, "a1b2") } : undefined;
    const hints: Record<string, string> = {};
    if (b.secrets?.url) {
      const u = new URL(b.secrets.url);
      hints.url = `${u.protocol}//${u.host}/…/•••${b.secrets.url.slice(-4)}`;
    }
    if (b.secrets?.hmac_secret || generated) hints.hmac_secret = "•••••••• (set)";
    if (b.secrets?.smtp_password) hints.smtp_password = "•••••••• (set)";
    const ch: AlertChannel = { id: uuid(), name: b.name.trim(), type: b.type, enabled: b.enabled ?? true, config: b.config ?? {}, secret_hints: hints,
      created_by_email: "admin@openlog.local", created_at: formatTs(Date.now()), updated_at: formatTs(Date.now()), last_delivery: null };
    db.channels.push(ch);
    return HttpResponse.json({ ...ch, generated_secrets: generated }, { status: 201 });
  })),
  http.put(`${API}/channels/:id`, guarded("manage", async (info) => {
    const ch = db.channels.find((c) => c.id === idOf(info));
    if (!ch) return fail("not_found", "not found");
    const b = await json<{ name: string; enabled: boolean; config: AlertChannel["config"] }>(info.request);
    Object.assign(ch, { name: b.name ?? ch.name, enabled: b.enabled ?? ch.enabled, config: b.config ?? ch.config, updated_at: formatTs(Date.now()) });
    return HttpResponse.json(ch);
  })),
  http.delete(`${API}/channels/:id`, guarded("manage", (info) => {
    db.channels = db.channels.filter((c) => c.id !== idOf(info));
    return new HttpResponse(null, { status: 204 });
  })),
  http.post(`${API}/channels/:id/test`, guarded("manage", (info) => {
    const ch = db.channels.find((c) => c.id === idOf(info));
    if (!ch) return fail("not_found", "not found");
    const broken = (ch.secret_hints.url ?? "").includes("fail") || ch.name.toLowerCase().includes("fail");
    const res = broken
      ? { success: false, status_code: 500, error: "HTTP 500 Internal Server Error", duration_ms: 231, notification_id: uuid() }
      : { success: true, status_code: ch.type === "email" ? 250 : 200, error: "", duration_ms: 142, notification_id: uuid() };
    ch.last_delivery = { at: formatTs(Date.now()), status: res.success ? "delivered" : "failed", error: res.error };
    return HttpResponse.json(res);
  })),

  http.get(`${API}/mutes`, guarded("read", () => HttpResponse.json({ mutes: [...db.mutes].sort((a, b) => b.ends_at.localeCompare(a.ends_at)) }))),
  http.post(`${API}/mutes`, guarded("write", async ({ request }) => {
    const b = await json<AlertMute>(request);
    if (!b.name?.trim()) return fail("invalid_argument", "name: must be 1-200 characters");
    const starts = Date.parse(b.starts_at ?? "");
    const ends = Date.parse(b.ends_at ?? "");
    if (!Number.isFinite(starts) || !Number.isFinite(ends) || ends <= starts) return fail("invalid_argument", "ends_at: must be after starts_at");
    const now = Date.now();
    const m: AlertMute = { id: uuid(), name: b.name.trim(), comment: b.comment ?? "", starts_at: formatTs(starts), ends_at: formatTs(ends), rule_ids: b.rule_ids ?? [],
      matchers: b.matchers ?? [], schedule: null, active: starts <= now && now < ends, created_by_user_id: MOCK_USER_ID, created_by_email: "admin@openlog.local",
      created_at: formatTs(now), updated_at: formatTs(now) };
    db.mutes.push(m);
    return HttpResponse.json(m, { status: 201 });
  })),
  http.put(`${API}/mutes/:id`, guarded("write", async (info, role) => {
    const m = db.mutes.find((x) => x.id === idOf(info));
    if (!m) return fail("not_found", "not found");
    if (!canChange(role, m.created_by_user_id)) return fail("permission_denied", "members can only change rules and mutes they created");
    const b = await json<AlertMute>(info.request);
    Object.assign(m, { name: b.name ?? m.name, comment: b.comment ?? m.comment, matchers: b.matchers ?? m.matchers, rule_ids: b.rule_ids ?? m.rule_ids });
    return HttpResponse.json(m);
  })),
  http.delete(`${API}/mutes/:id`, guarded("write", (info, role) => {
    const m = db.mutes.find((x) => x.id === idOf(info));
    if (!m) return fail("not_found", "not found");
    if (!canChange(role, m.created_by_user_id)) return fail("permission_denied", "members can only change rules and mutes they created");
    db.mutes = db.mutes.filter((x) => x.id !== m.id);
    return new HttpResponse(null, { status: 204 });
  })),

  http.get(`${API}/deliveries`, guarded("read", ({ request }) => {
    const url = new URL(request.url);
    const ch = url.searchParams.get("channel_id");
    const inc = url.searchParams.get("incident_id");
    return HttpResponse.json({ deliveries: db.deliveries.filter((d) => (!ch || d.channel_id === ch) && (!inc || d.incident_id === inc)) });
  })),
];

export const MOCK_ALERT_IDS = { rules: RULE, incidents: INC, channels: CH };
