// MSW handlers of the SaaS operator console, organization SaaS state and support access (docs/contracts/api.md
// "SaaS operations"). The mock user (admin@openlog.local) is an operator.
import { http, HttpResponse } from "msw";
import type { AbuseFlag, OperatorOrg, OperatorOrgDetail, OrgLifecycle, OrgSaaSState, SupportSession } from "@/api/operator";
import { authenticate, MOCK_DEFAULT_ORG_ID, MOCK_EMAIL, MOCK_STAGING_ORG_ID } from "./account";

const API = "*/api/v1";
const GiB = 2 ** 30;

interface MockSaaS {
  orgs: OperatorOrg[];
  flags: AbuseFlag[];
  sessions: SupportSession[];
  supportUntil: Record<string, string | null>;
  operator: boolean;
}

function iso(ms: number): string {
  return new Date(ms).toISOString();
}

function seed(): MockSaaS {
  const now = Date.now();
  const day = 86_400_000;
  const base = {
    plan_assigned: false, suspended_at: null, suspend_reason: "", trial_plan_id: "", trial_ends_at: null, support_access_until: null,
    quota_level: "ok", open_flags: 0,
  } as const;
  return {
    operator: true,
    orgs: [
      { ...base, id: MOCK_DEFAULT_ORG_ID, tenant_id: "default", name: "Default", created_at: iso(now - 40 * day), plan_id: "free", state: "active",
        members: 3, active_hosts: 3, ingest_bytes: 85 * GiB, quota_level: "warning", last_ingest_at: iso(now - 60_000) },
      { ...base, id: MOCK_STAGING_ORG_ID, tenant_id: "staging", name: "Staging", created_at: iso(now - 12 * day), plan_id: "pro", plan_assigned: true,
        state: "trial", trial_plan_id: "pro", trial_ends_at: iso(now + 5 * day), members: 1, active_hosts: 1, ingest_bytes: 2 * GiB, last_ingest_at: null },
      { ...base, id: "0b8a4d52-7f3e-4c1a-9e6d-00000000abcd", tenant_id: "spammy", name: "Spammy Ltd", created_at: iso(now - 2 * day), plan_id: "free",
        state: "active", members: 1, active_hosts: 120, ingest_bytes: 40 * GiB, last_ingest_at: iso(now - 5_000), open_flags: 1 },
    ],
    flags: [
      { id: 7, org_id: "0b8a4d52-7f3e-4c1a-9e6d-00000000abcd", org_name: "Spammy Ltd", tenant_id: "spammy", kind: "new_org_hosts", status: "open",
        details: { active_hosts: 120, threshold: 50 }, occurrences: 3, first_seen_at: iso(now - 3_600_000), last_seen_at: iso(now - 600_000),
        auto_suspended: false, resolved_by_email: "", resolved_at: null, resolution_note: "", org_suspended: false },
    ],
    sessions: [],
    supportUntil: {},
  };
}

let state = seed();

/** Resets the operator mock state (tests). */
export function resetMockOperator(): void {
  state = seed();
}

/** Makes the mock user an operator or not (tests). */
export function setMockOperator(v: boolean): void {
  state.operator = v;
}

function fail(status: number, code: string, message: string) {
  return HttpResponse.json({ error: { code, message } }, { status });
}

function findOrg(ref: string): OperatorOrg | undefined {
  return state.orgs.find((o) => o.id === ref || o.tenant_id === ref);
}

/** Organization of the operator console by id or tenant id (admin deletion mocks in privacy.ts). */
export function mockOperatorOrg(ref: string): Pick<OperatorOrg, "id" | "tenant_id" | "name"> | undefined {
  return findOrg(ref);
}

/** Suspends or reactivates a mock organization (tests of the suspended read-only UI). */
export function setMockOrgSuspended(ref: string, suspended: boolean): void {
  const o = findOrg(ref);
  if (!o) return;
  o.state = suspended ? "suspended" : "active";
  o.suspended_at = suspended ? iso(Date.now()) : null;
  o.suspend_reason = suspended ? "test" : "";
}

function lifecycle(o: OperatorOrg): OrgLifecycle {
  return {
    org_id: o.id, tenant_id: o.tenant_id, suspended: o.state === "suspended", suspended_at: o.suspended_at, suspend_reason: o.suspend_reason,
    trial_plan_id: o.trial_plan_id, trial_started_at: null, trial_ends_at: o.trial_ends_at, trial_ended_at: null,
    support_access_until: state.supportUntil[o.id] ?? null, support_access_granted_at: null,
  };
}

async function reasonOf(request: Request): Promise<string | Response> {
  const body = (await request.json().catch(() => ({}))) as { reason?: string };
  const reason = (body.reason ?? "").trim();
  if (reason.length < 3) return fail(400, "invalid_argument", "reason is required (3-1000 characters); it is written to the organization's audit log");
  return reason;
}

function operatorOnly(request: Request): Response | null {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (!state.operator) return fail(403, "permission_denied", "only openlog operators (OPENLOG_SUPERADMIN_EMAILS) can use the operator console");
  return null;
}

function detail(o: OperatorOrg): OperatorOrgDetail {
  const now = Date.now();
  return {
    saas_mode: true,
    organization: {
      ...o,
      support_access_until: state.supportUntil[o.id] ?? null,
      member_list: o.tenant_id === "default"
        ? [
            { user_id: "u1", email: MOCK_EMAIL, name: "Ada Admin", role: "owner", joined_at: iso(now - 40 * 86_400_000), last_login_at: iso(now - 3_600_000), email_verified: true, disabled: false },
            { user_id: "u2", email: "grace@example.com", name: "Grace Hopper", role: "admin", joined_at: iso(now - 30 * 86_400_000), last_login_at: null, email_verified: false, disabled: false },
          ]
        : [{ user_id: "u9", email: `owner@${o.tenant_id}.test`, name: "", role: "owner", joined_at: o.created_at, last_login_at: null, email_verified: false, disabled: false }],
      pending_invitations: 1,
      keys: { license_keys_active: 2, license_keys_revoked: 1, api_keys_active: 1, scim_tokens_active: 0, last_ingest_at: o.last_ingest_at },
      sso_connections: o.tenant_id === "default" ? [{ id: "c1", protocol: "oidc", name: "Okta", enabled: true, enforce: false, jit_enabled: true, last_test_ok: true }] : [],
      verified_domains: 1,
      flags: state.flags.filter((f) => f.org_id === o.id),
      support_sessions: state.sessions.filter((s) => s.org_id === o.id),
      support_access_granted_by: state.supportUntil[o.id] ? MOCK_EMAIL : "",
    },
    lifecycle: lifecycle(o),
    quota: { level: o.quota_level, ingest_blocked: false, metrics: [{ metric: "ingest_bytes", used: o.ingest_bytes, limit: 100 * GiB, percent: (o.ingest_bytes / (100 * GiB)) * 100, level: o.quota_level }], evaluated_at: iso(now - 30_000), period_start: iso(now).slice(0, 8) + "01" },
    audit: [{ id: 1, actor_email: MOCK_EMAIL, action: "member.role_change", target_type: "user", target_id: "u2", details: { to: "admin" }, created_at: iso(now - 7_200_000) }],
    usage: {
      period: { id: iso(now).slice(0, 7), start: iso(now).slice(0, 8) + "01T00:00:00Z", end: iso(now + 20 * 86_400_000), data_until: iso(now) },
      days: [],
      available: true,
    },
  };
}

export const operatorHandlers = [
  http.get(`${API}/operator/me`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    return HttpResponse.json({ operator: state.operator && ctx.kind === "session", saas_mode: true });
  }),
  http.get(`${API}/operator/orgs`, ({ request }) => {
    const denied = operatorOnly(request);
    if (denied) return denied;
    const url = new URL(request.url);
    const q = (url.searchParams.get("q") ?? "").toLowerCase();
    const plan = url.searchParams.get("plan");
    const st = url.searchParams.get("state");
    const orgs = state.orgs.filter(
      (o) =>
        (!q || o.name.toLowerCase().includes(q) || o.tenant_id.includes(q)) &&
        (!plan || o.plan_id === plan) &&
        (!st || (st === "flagged" ? o.open_flags > 0 : o.state === st)),
    );
    return HttpResponse.json({ organizations: orgs, total: orgs.length, saas_mode: true });
  }),
  http.get(`${API}/operator/orgs/:org`, ({ request, params }) => {
    const denied = operatorOnly(request);
    if (denied) return denied;
    const o = findOrg(String(params.org));
    return o ? HttpResponse.json(detail(o)) : fail(404, "not_found", "not found");
  }),
  http.post(`${API}/operator/orgs/:org/:action`, async ({ request, params }) => {
    const denied = operatorOnly(request);
    if (denied) return denied;
    const o = findOrg(String(params.org));
    if (!o) return fail(404, "not_found", "not found");
    const action = String(params.action);
    if (action === "trial") {
      const body = (await request.json()) as { plan_id?: string; days?: number; reason?: string };
      if ((body.reason ?? "").trim().length < 3) return fail(400, "invalid_argument", "reason is required");
      o.state = "trial";
      o.trial_plan_id = body.plan_id || o.trial_plan_id || "pro";
      o.trial_ends_at = iso(Date.now() + (body.days ?? 14) * 86_400_000);
      return HttpResponse.json(lifecycle(o));
    }
    const reason = await reasonOf(request);
    if (reason instanceof Response) return reason;
    switch (action) {
      case "suspend":
        o.state = "suspended";
        o.suspended_at = iso(Date.now());
        o.suspend_reason = reason;
        return HttpResponse.json(lifecycle(o));
      case "unsuspend":
        if (o.state !== "suspended") return fail(409, "failed_precondition", "the organization is not suspended");
        o.state = o.trial_ends_at ? "trial" : "active";
        o.suspended_at = null;
        o.suspend_reason = "";
        return HttpResponse.json(lifecycle(o));
      case "reset-quota-notifications":
        return HttpResponse.json({ deleted: 2, period: iso(Date.now()).slice(0, 7) });
      case "force-logout":
        return HttpResponse.json({ sessions_revoked: 4 });
      case "resend-verification":
        return HttpResponse.json({ sent_to: [`owner@${o.tenant_id}.test`] });
      case "support-sessions": {
        const until = state.supportUntil[o.id];
        if (!until || Date.parse(until) <= Date.now()) return fail(403, "support_access_required", "the organization has not granted openlog support access, or it has ended");
        const s: SupportSession = { id: `ss-${state.sessions.length + 1}`, org_id: o.id, org_name: o.name, tenant_id: o.tenant_id, operator_email: MOCK_EMAIL,
          reason, started_at: iso(Date.now()), expires_at: until, ended_at: null };
        state.sessions.push(s);
        return HttpResponse.json(s, { status: 201 });
      }
    }
    return fail(404, "not_found", "no such endpoint");
  }),
  http.get(`${API}/operator/support-sessions`, ({ request }) => {
    const denied = operatorOnly(request);
    if (denied) return denied;
    return HttpResponse.json({ support_sessions: state.sessions.filter((s) => !s.ended_at) });
  }),
  http.delete(`${API}/operator/support-sessions/:id`, ({ request, params }) => {
    const denied = operatorOnly(request);
    if (denied) return denied;
    const s = state.sessions.find((x) => x.id === params.id);
    if (!s) return fail(404, "not_found", "not found");
    s.ended_at = iso(Date.now());
    return HttpResponse.json(s);
  }),
  http.post(`${API}/operator/support-sessions/:id/views`, ({ request }) => {
    const denied = operatorOnly(request);
    return denied ?? new HttpResponse(null, { status: 204 });
  }),
  http.get(`${API}/operator/flags`, ({ request }) => {
    const denied = operatorOnly(request);
    if (denied) return denied;
    const status = new URL(request.url).searchParams.get("status") ?? "open";
    return HttpResponse.json({ flags: state.flags.filter((f) => status === "all" || f.status === status) });
  }),
  http.post(`${API}/operator/flags/:id/resolve`, async ({ request, params }) => {
    const denied = operatorOnly(request);
    if (denied) return denied;
    const f = state.flags.find((x) => x.id === Number(params.id) && x.status === "open");
    if (!f) return fail(404, "not_found", "not found");
    const body = (await request.json()) as { status: "dismissed" | "actioned"; note: string };
    if ((body.note ?? "").trim().length < 3) return fail(400, "invalid_argument", "note is required (3-1000 characters)");
    Object.assign(f, { status: body.status, resolution_note: body.note, resolved_by_email: MOCK_EMAIL, resolved_at: iso(Date.now()) });
    const o = findOrg(f.org_id);
    if (o) o.open_flags = Math.max(0, o.open_flags - 1);
    return HttpResponse.json(f);
  }),
  http.get(`${API}/orgs/current/saas`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const o = findOrg(ctx.org.id);
    const until = state.supportUntil[ctx.org.id];
    const body: OrgSaaSState = {
      saas_mode: true,
      suspended: o?.state === "suspended",
      trial: o?.trial_ends_at ? { plan_id: o.trial_plan_id, plan_name: "Pro", ends_at: o.trial_ends_at, fallback_plan_id: "free" } : null,
      support_access: until && Date.parse(until) > Date.now() ? { until, granted_at: null } : null,
      support_session: null,
      can_manage_support_access: ctx.kind === "session" && ctx.role === "owner",
    };
    return HttpResponse.json(body);
  }),
  http.put(`${API}/orgs/current/support-access`, async ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session" || ctx.role !== "owner") return fail(403, "permission_denied", "only owners can manage openlog support access");
    const { duration } = (await request.json()) as { duration: string };
    const ms = duration === "24h" ? 86_400_000 : duration === "7d" ? 7 * 86_400_000 : 0;
    if (!ms) return fail(400, "invalid_argument", "duration must be 24h or 7d");
    state.supportUntil[ctx.org.id] = iso(Date.now() + ms);
    return HttpResponse.json({ saas_mode: true, suspended: false, trial: null, support_access: { until: state.supportUntil[ctx.org.id]!, granted_at: iso(Date.now()) },
      support_session: null, can_manage_support_access: true } satisfies OrgSaaSState);
  }),
  http.delete(`${API}/orgs/current/support-access`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session" || ctx.role !== "owner") return fail(403, "permission_denied", "only owners can manage openlog support access");
    state.supportUntil[ctx.org.id] = null;
    return HttpResponse.json({ saas_mode: true, suspended: false, trial: null, support_access: null, support_session: null, can_manage_support_access: true } satisfies OrgSaaSState);
  }),
];
