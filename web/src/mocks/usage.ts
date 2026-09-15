// MSW handlers for usage, plans and quota status (docs/contracts/api.md "Usage and plans").
import { http, HttpResponse } from "msw";
import type { OrgPlan, OrgQueryLimits, OrgQueryLimitsInput, Plan, QuotaMetric, UsageDay, UsageOverview, UsageStatus } from "@/api/usage";
import { authenticate } from "./account";

const API = "*/api/v1";
const GiB = 2 ** 30;

export const MOCK_PLANS: Plan[] = [
  {
    id: "free",
    name: "Free",
    description: "For evaluation",
    limits: { ingest_gb_month: 100, hosts: 5, users: 3, retention_days: { logs: 7, traces: 7 }, query: {} },
    enforcement: { hard_ingest_limit: true, grace_percent: 10 },
    trial_days: 0,
    trial_fallback_plan: "",
  },
  {
    id: "pro", name: "Pro", description: "", limits: { ingest_gb_month: 1000, hosts: 100, retention_days: { logs: 30 }, query: {} }, enforcement: {},
    trial_days: 14, trial_fallback_plan: "free",
  },
  { id: "enterprise", name: "Enterprise", description: "", limits: { retention_days: {}, query: {} }, enforcement: {}, trial_days: 0, trial_fallback_plan: "" },
];

interface Assignment {
  plan_id: string;
  overrides: OrgPlan["overrides"];
  note: string;
  updated_at: string;
}
let assignments: Record<string, Assignment> = {};
let queryLimits: Record<string, NonNullable<OrgQueryLimits["organization"]>> = {};

/** Resets plan assignments and query limits (tests). */
export function resetMockUsage(): void {
  assignments = {};
  queryLimits = {};
}

const QUERY_DEFAULTS = { max_memory_usage: 2 * GiB, max_rows_to_read: 2_000_000_000, max_bytes_to_read: 0 };

function queryLimitsOf(tenant: string, canManage: boolean): OrgQueryLimits {
  const plan = { max_memory_usage: 0, max_rows_to_read: 0, max_bytes_to_read: 0, ...planOf(tenant).plan.limits.query };
  const org = queryLimits[tenant] ?? null;
  const effective = { ...QUERY_DEFAULTS };
  const sources: OrgQueryLimits["sources"] = { max_memory_usage: "default", max_rows_to_read: "default", max_bytes_to_read: "default" };
  for (const k of ["max_memory_usage", "max_rows_to_read", "max_bytes_to_read"] as const) {
    if (plan[k] > 0) [effective[k], sources[k]] = [plan[k], "plan"];
    const o = org?.[k];
    if (o !== null && o !== undefined) [effective[k], sources[k]] = [o, "organization"];
  }
  return { defaults: QUERY_DEFAULTS, plan, organization: org, environment: null, effective, sources, can_manage: canManage, refresh_seconds: 30 };
}

const USED: Record<string, { ingest: number; hosts: number; users: number }> = {
  default: { ingest: 85 * GiB, hosts: 3, users: 3 },
  staging: { ingest: 2 * GiB, hosts: 1, users: 1 },
};

function planOf(tenant: string): { plan: Plan; assigned: boolean } {
  const a = assignments[tenant];
  const plan = MOCK_PLANS.find((p) => p.id === (a?.plan_id ?? "free")) ?? MOCK_PLANS[0]!;
  return { plan, assigned: !!a };
}

function metric(name: QuotaMetric["metric"], used: number, limit: number | undefined): QuotaMetric {
  const l = limit ?? 0;
  const percent = l > 0 ? Math.round((used / l) * 10000) / 100 : 0;
  const level = l <= 0 ? "ok" : used >= l ? "exceeded" : percent >= 80 ? "warning" : "ok";
  return { metric: name, used, limit: l, percent, level };
}

function limitsOf(tenant: string): QuotaMetric[] {
  const u = USED[tenant] ?? { ingest: 0, hosts: 0, users: 1 };
  const { plan } = planOf(tenant);
  const gb = plan.limits.ingest_gb_month;
  return [metric("ingest_bytes", u.ingest, gb ? gb * GiB : 0), metric("hosts", u.hosts, plan.limits.hosts), metric("users", u.users, plan.limits.users)];
}

function worstLevel(ms: QuotaMetric[]): QuotaMetric["level"] {
  return ms.some((m) => m.level === "exceeded") ? "exceeded" : ms.some((m) => m.level === "warning") ? "warning" : "ok";
}

function period(v: string | null) {
  const now = new Date();
  let y = now.getUTCFullYear();
  let m = now.getUTCMonth();
  if (v === "previous") m -= 1;
  else if (v && /^\d{4}-\d{2}$/.test(v)) {
    y = Number(v.slice(0, 4));
    m = Number(v.slice(5)) - 1;
  }
  const start = new Date(Date.UTC(y, m, 1));
  const end = new Date(Date.UTC(y, m + 1, 1));
  const until = now < end ? now : end;
  const id = `${start.getUTCFullYear()}-${String(start.getUTCMonth() + 1).padStart(2, "0")}`;
  return { id, start: start.toISOString(), end: end.toISOString(), data_until: until.toISOString(), startMs: start.getTime(), untilMs: until.getTime() };
}

function days(tenant: string, p: ReturnType<typeof period>): UsageDay[] {
  const total = USED[tenant]?.ingest ?? 0;
  const n = Math.max(1, Math.ceil((p.untilMs - p.startMs) / 86_400_000));
  const out: UsageDay[] = [];
  for (let i = 0; i < n; i++) {
    const ingest = total / n;
    out.push({
      day: new Date(p.startMs + i * 86_400_000).toISOString().slice(0, 10),
      signals: [
        { signal: "traces", items: 1000 * (i + 1), bytes: ingest * 0.5, ingest_bytes: ingest * 0.5, ingest_requests: 100 },
        { signal: "logs", items: 5000, bytes: ingest * 0.4, ingest_bytes: ingest * 0.4, ingest_requests: 80 },
        { signal: "metrics", items: 20000, bytes: ingest * 0.1, ingest_bytes: ingest * 0.1, ingest_requests: 60 },
      ],
      ingest_bytes: ingest,
      hosts: USED[tenant]?.hosts ?? 0,
      containers: 4,
      services: 2,
      query: { queries: 12, failed: 0, read_rows: 1e6, read_bytes: 5e7, cpu_seconds: 3.5, memory_bytes: 1e8 },
    });
  }
  return out;
}

function forbidden(message: string) {
  return HttpResponse.json({ error: { code: "permission_denied", message } }, { status: 403 });
}

export const usageHandlers = [
  http.get(`${API}/usage`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const url = new URL(request.url);
    const p = period(url.searchParams.get("period"));
    const tenant = ctx.org.tenant_id;
    const { plan, assigned } = planOf(tenant);
    const limits = limitsOf(tenant);
    const u = USED[tenant] ?? { ingest: 0, hosts: 0, users: 1 };
    const body: UsageOverview = {
      organization: { id: ctx.org.id, name: ctx.org.name, tenant_id: tenant },
      period: { id: p.id, start: p.start, end: p.end, data_until: p.data_until },
      saas_mode: true,
      plan,
      plan_assigned: assigned,
      usage: {
        signals: [
          { signal: "traces", items: 30000, bytes: u.ingest * 0.5, ingest_bytes: u.ingest * 0.5, ingest_requests: 3000 },
          { signal: "logs", items: 150000, bytes: u.ingest * 0.4, ingest_bytes: u.ingest * 0.4, ingest_requests: 2400 },
          { signal: "metrics", items: 600000, bytes: u.ingest * 0.1, ingest_bytes: u.ingest * 0.1, ingest_requests: 1800 },
        ],
        ingest_bytes: u.ingest,
        hosts: u.hosts,
        containers: 4,
        services: 2,
        active_hosts: u.hosts,
        query: { queries: 360, failed: 2, read_rows: 3e7, read_bytes: 1.5e9, cpu_seconds: 105, memory_bytes: 3e9 },
      },
      stored: [
        { signal: "traces", retention_days: plan.limits.retention_days.traces ?? 7, bytes: u.ingest * 0.2, compressed_bytes: u.ingest * 0.02 },
        { signal: "logs", retention_days: plan.limits.retention_days.logs ?? 14, bytes: u.ingest * 0.3, compressed_bytes: u.ingest * 0.03 },
        { signal: "metrics", retention_days: plan.limits.retention_days.metrics ?? 30, bytes: u.ingest * 0.1, compressed_bytes: u.ingest * 0.01 },
      ],
      limits,
      level: worstLevel(limits),
      ingest_blocked: false,
      projection: plan.limits.ingest_gb_month
        ? { ingest_bytes: u.ingest * 2, ingest_percent: ((u.ingest * 2) / (plan.limits.ingest_gb_month * GiB)) * 100 }
        : { ingest_bytes: u.ingest * 2 },
      can_manage_plan: ctx.kind === "session",
      billing_enabled: false,
    };
    return HttpResponse.json(body);
  }),

  http.get(`${API}/usage/daily`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const p = period(new URL(request.url).searchParams.get("period"));
    return HttpResponse.json({ period: { id: p.id, start: p.start, end: p.end, data_until: p.data_until }, days: days(ctx.org.tenant_id, p) });
  }),

  http.get(`${API}/usage/top`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const url = new URL(request.url);
    const by = url.searchParams.get("by") ?? "service";
    if (by !== "service" && by !== "host") {
      return HttpResponse.json({ error: { code: "invalid_argument", message: "by must be service or host" } }, { status: 400 });
    }
    const p = period(url.searchParams.get("period"));
    const keys = by === "service" ? ["checkout", "frontend"] : ["web-1", "db-1", "worker-1"];
    const entries = keys.map((key, i) => {
      const bytes = (keys.length - i) * GiB;
      return { key, items: 1000 * (keys.length - i), bytes, bytes_by_signal: { traces: bytes * 0.5, logs: bytes * 0.4, metrics: bytes * 0.1 } };
    });
    return HttpResponse.json({ period: { id: p.id, start: p.start, end: p.end, data_until: p.data_until }, by, entries });
  }),

  http.get(`${API}/usage/export`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.role !== "admin" && ctx.role !== "owner") return forbidden("only admins and owners can export usage");
    const url = new URL(request.url);
    const p = period(url.searchParams.get("period"));
    const format = url.searchParams.get("format") ?? "csv";
    if (format === "json") return HttpResponse.json({ plan_id: planOf(ctx.org.tenant_id).plan.id, days: days(ctx.org.tenant_id, p) });
    const csv = ["tenant_id,period,day,metric,value", ...days(ctx.org.tenant_id, p).map((d) => `${ctx.org.tenant_id},${p.id},${d.day},ingest_bytes,${d.ingest_bytes}`)].join("\n");
    return new HttpResponse(`${csv}\n`, { headers: { "Content-Type": "text/csv; charset=utf-8" } });
  }),

  http.get(`${API}/usage/status`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const metrics = limitsOf(ctx.org.tenant_id);
    const body: UsageStatus = {
      level: worstLevel(metrics),
      ingest_blocked: false,
      metrics,
      saas_mode: true,
      plan_id: planOf(ctx.org.tenant_id).plan.id,
      evaluated_at: new Date().toISOString(),
      period_start: period(null).start.slice(0, 10),
    };
    return HttpResponse.json(body);
  }),

  http.get(`${API}/usage/query-limits`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    return HttpResponse.json(queryLimitsOf(ctx.org.tenant_id, ctx.kind === "session" && ctx.role === "owner"));
  }),

  http.put(`${API}/usage/query-limits`, async ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session" || ctx.role !== "owner") return forbidden("only organization owners can change query limits");
    const body = (await request.json()) as OrgQueryLimitsInput;
    const v = (x: number | null | undefined) => (x === undefined ? null : x);
    const org = { max_memory_usage: v(body.max_memory_usage), max_rows_to_read: v(body.max_rows_to_read), max_bytes_to_read: v(body.max_bytes_to_read) };
    if (Object.values(org).some((x) => x !== null && x < 0)) {
      return HttpResponse.json({ error: { code: "invalid_argument", message: "values must be >= 0" } }, { status: 400 });
    }
    if (Object.values(org).every((x) => x === null)) delete queryLimits[ctx.org.tenant_id];
    else queryLimits[ctx.org.tenant_id] = { ...org, updated_at: new Date().toISOString(), updated_by: "admin@openlog.local" };
    return HttpResponse.json(queryLimitsOf(ctx.org.tenant_id, true));
  }),

  http.delete(`${API}/usage/query-limits`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session" || ctx.role !== "owner") return forbidden("only organization owners can change query limits");
    delete queryLimits[ctx.org.tenant_id];
    return HttpResponse.json(queryLimitsOf(ctx.org.tenant_id, true));
  }),

  http.get(`${API}/plans`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    return HttpResponse.json({ plans: MOCK_PLANS, default: "free" });
  }),

  http.get(`${API}/admin/orgs/:org/plan`, ({ request, params }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session") return forbidden("only openlog operators can change plans");
    return HttpResponse.json(orgPlan(String(params.org), ctx.org));
  }),

  http.put(`${API}/admin/orgs/:org/plan`, async ({ request, params }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session") return forbidden("only openlog operators can change plans");
    const body = (await request.json()) as { plan_id?: string; overrides?: OrgPlan["overrides"]; note?: string };
    if (!MOCK_PLANS.some((p) => p.id === body.plan_id)) {
      return HttpResponse.json({ error: { code: "invalid_argument", message: `unknown plan_id "${body.plan_id}"` } }, { status: 400 });
    }
    assignments[ctx.org.tenant_id] = { plan_id: body.plan_id!, overrides: body.overrides ?? {}, note: body.note ?? "", updated_at: new Date().toISOString() };
    return HttpResponse.json(orgPlan(String(params.org), ctx.org));
  }),
];

function orgPlan(orgId: string, org: { id: string; name: string; tenant_id: string }): OrgPlan {
  const a = assignments[org.tenant_id];
  const { plan } = planOf(org.tenant_id);
  return {
    organization: { id: orgId, name: org.name, tenant_id: org.tenant_id },
    plan_id: plan.id,
    assigned: !!a,
    overrides: a?.overrides ?? {},
    billing: { provider: "", customer_id: "", subscription_id: "" },
    note: a?.note ?? "",
    updated_at: a?.updated_at ?? null,
    updated_by: a ? "admin@openlog.local" : "",
    effective: plan,
  };
}
