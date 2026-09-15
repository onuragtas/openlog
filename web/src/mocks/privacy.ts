// MSW handlers for data exports, account/organization deletion and the status page (docs/contracts/api.md
// "Data export and deletion", "Status page") with in-memory state.
import { http, HttpResponse } from "msw";
import { subjectHash, type AccountPrivacy, type DataExport, type DeletionCertificate, type OrgDeletion } from "@/api/privacy";
import type { StatusIncident, StatusPage } from "@/api/statusPage";
import { authenticate, MOCK_EMAIL, MOCK_PASSWORD } from "./account";
import { formatTs } from "./fixtures";
import { mockOperatorOrg } from "./operator";

const API = "*/api/v1";
const DAY = 86_400_000;

const state: { exports: DataExport[]; deletions: OrgDeletion[]; incidents: StatusIncident[]; certificates: { subject: string; cert: DeletionCertificate }[] } = {
  exports: [],
  deletions: [],
  incidents: [],
  certificates: [],
};

export function resetMockPrivacy(): void {
  state.exports = [];
  state.deletions = [];
  state.incidents = [];
  state.certificates = [];
}

/** Adds a deletion certificate of a tenant id or user id (tests). */
export function addMockDeletionCertificate(subject: string, cert: DeletionCertificate): void {
  state.certificates.push({ subject, cert });
}

function fail(code: string, message: string, status: number) {
  return HttpResponse.json({ error: { code, message } }, { status });
}

function newExport(kind: DataExport["kind"], signals: DataExport["signals"], from?: string, to?: string): DataExport {
  const now = Date.now();
  // Completed at once so the download can be tried in dev:mock.
  return {
    id: crypto.randomUUID(), kind, status: "completed", signals, from: from ?? null, to: to ?? null, requested_by: MOCK_EMAIL,
    size_bytes: 48_213, telemetry_rows: signals.length > 0 ? 1200 : 0, truncated: false, error: "", created_at: formatTs(now),
    started_at: formatTs(now), completed_at: formatTs(now), expires_at: formatTs(now + 7 * DAY), download_available: true,
  };
}

function page(): StatusPage {
  const today = Date.now();
  const days = Array.from({ length: 90 }, (_, i) => {
    const date = new Date(today - (89 - i) * DAY).toISOString().slice(0, 10);
    const outage = i === 70;
    return { date, status: outage ? ("outage" as const) : ("operational" as const), uptime: outage ? 98.6 : 100 };
  });
  const open = state.incidents.filter((i) => i.status !== "resolved" && i.status !== "completed");
  return {
    status: open.some((i) => i.kind === "incident") ? "degraded" : "operational",
    checked_at: formatTs(today - 20_000),
    components: (["ingest", "query_api", "alerting", "processing"] as const).map((id) => ({ id, status: "operational" as const, uptime_90d: 99.98, days })),
    incidents: open.filter((i) => i.kind === "incident"),
    maintenance: open.filter((i) => i.kind === "maintenance"),
    history: state.incidents.filter((i) => i.status === "resolved" || i.status === "completed"),
  };
}

export const privacyHandlers = [
  http.get(`${API}/account/privacy`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const body: AccountPrivacy = { has_password: true, data_export_enabled: true, org_deletion_grace_seconds: 7 * 86_400, reauth_max_age_seconds: 600, org_deletions: state.deletions.filter((d) => d.status === "scheduled" || d.status === "deleting") };
    return HttpResponse.json(body);
  }),
  http.get(`${API}/account/data-exports`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    return HttpResponse.json({ exports: state.exports.filter((e) => e.kind === "user") });
  }),
  http.post(`${API}/account/data-exports`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const e = newExport("user", []);
    state.exports.unshift(e);
    return HttpResponse.json({ export: e }, { status: 202 });
  }),
  http.get(`${API}/data-exports/download`, () => new HttpResponse(new Blob(["PK"]), { headers: { "Content-Type": "application/zip" } })),
  http.get(`${API}/data-exports`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.role !== "owner") return fail("permission_denied", "only owners of the organization can do this", 403);
    return HttpResponse.json({ exports: state.exports.filter((e) => e.kind === "organization") });
  }),
  http.post(`${API}/data-exports`, async ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.role !== "owner") return fail("permission_denied", "only owners of the organization can do this", 403);
    const b = (await request.json()) as { from?: string; to?: string; signals?: DataExport["signals"] };
    const signals = b.signals ?? [];
    if (signals.length > 0 && (!b.from || !b.to || Date.parse(b.from) >= Date.parse(b.to))) return fail("invalid_argument", "from must be before to", 400);
    const e = newExport("organization", signals, b.from, b.to);
    state.exports.unshift(e);
    return HttpResponse.json({ export: e }, { status: 202 });
  }),
  http.get(`${API}/data-exports/:id/download`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    return new HttpResponse(new Blob(["PK"]), { headers: { "Content-Type": "application/zip" } });
  }),
  http.post(`${API}/orgs/current/deletion`, async ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.role !== "owner") return fail("permission_denied", "only owners of the organization can do this", 403);
    const b = (await request.json()) as { confirm_name?: string; password?: string };
    if (b.confirm_name !== ctx.org.name) return fail("invalid_argument", "type the organization name to confirm", 400);
    if (b.password !== MOCK_PASSWORD) return fail("permission_denied", "the password is incorrect", 403);
    const now = Date.now();
    const d: OrgDeletion = {
      id: crypto.randomUUID(), organization_id: ctx.org.id, organization_name: ctx.org.name, tenant_id: ctx.org.tenant_id, status: "scheduled",
      initiator: "owner", requested_at: formatTs(now), purge_after: formatTs(now + 7 * DAY), cancelled_at: null, started_at: null, completed_at: null,
      cancellable: true, certificate_id: null,
    };
    state.deletions.push(d);
    return HttpResponse.json({ deletion: d }, { status: 202 });
  }),
  http.post(`${API}/org-deletions/:id/cancel`, ({ request, params }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const d = state.deletions.find((x) => x.id === params.id);
    if (!d) return fail("not_found", "not found", 404);
    state.deletions = state.deletions.filter((x) => x.id !== d.id);
    return HttpResponse.json({ deletion: { ...d, status: "cancelled", cancellable: false, cancelled_at: formatTs(Date.now()) } });
  }),
  http.post(`${API}/admin/orgs/:org/deletion`, async ({ request, params }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const org = mockOperatorOrg(String(params.org));
    if (!org) return fail("not_found", "not found", 404);
    const b = (await request.json()) as { reason?: string; immediate?: boolean };
    const reason = (b.reason ?? "").trim();
    if (!reason || reason.length > 1000) return fail("invalid_argument", "reason is required (at most 1000 characters)", 400);
    if (state.deletions.some((d) => d.organization_id === org.id && (d.status === "scheduled" || d.status === "deleting"))) {
      return fail("conflict", "the organization is already scheduled for deletion", 409);
    }
    const now = Date.now();
    const d: OrgDeletion = {
      id: crypto.randomUUID(), organization_id: org.id, organization_name: org.name, tenant_id: org.tenant_id, status: "scheduled", initiator: "operator",
      reason, requested_by_email: MOCK_EMAIL, requested_at: formatTs(now), purge_after: formatTs(b.immediate ? now : now + 7 * DAY), cancelled_at: null,
      started_at: null, completed_at: null, cancellable: true, certificate_id: null, last_error: "",
    };
    state.deletions.push(d);
    return HttpResponse.json({ deletion: d }, { status: 202 });
  }),
  http.get(`${API}/admin/org-deletions`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    return HttpResponse.json({ deletions: [...state.deletions].reverse() });
  }),
  http.post(`${API}/admin/org-deletions/:id/cancel`, ({ request, params }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const d = state.deletions.find((x) => x.id === params.id);
    if (!d) return fail("not_found", "not found", 404);
    if (d.status !== "scheduled") return fail("conflict", "the deletion can no longer be cancelled", 409);
    Object.assign(d, { status: "cancelled", cancellable: false, cancelled_at: formatTs(Date.now()) });
    return HttpResponse.json({ deletion: d });
  }),
  http.get(`${API}/admin/deletion-certificates`, async ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const want = new URL(request.url).searchParams.get("subject_hash") ?? "";
    const out: DeletionCertificate[] = [];
    for (const c of state.certificates) {
      if (!want || (await subjectHash(c.subject)) === want) out.push(c.cert);
    }
    return HttpResponse.json({ certificates: out });
  }),
  http.post(`${API}/account/delete`, async ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const b = (await request.json()) as { confirm_email?: string; password?: string };
    if ((b.confirm_email ?? "").trim().toLowerCase() !== MOCK_EMAIL) return fail("invalid_argument", "type your e-mail address to confirm", 400);
    if (b.password !== MOCK_PASSWORD) return fail("permission_denied", "the password is incorrect", 403);
    return new HttpResponse(null, { status: 204 });
  }),
  http.get(`${API}/status`, () => HttpResponse.json(page(), { headers: { "Cache-Control": "public, max-age=30" } })),
  http.get(`${API}/admin/status/incidents`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    return HttpResponse.json({ incidents: state.incidents, components: ["ingest", "query_api", "alerting", "processing"] });
  }),
  http.post(`${API}/admin/status/incidents`, async ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const b = (await request.json()) as Partial<StatusIncident> & { message?: string };
    if (!b.title?.trim()) return fail("invalid_argument", "title must be 1-200 characters", 400);
    const now = formatTs(Date.now());
    const inc: StatusIncident = {
      id: crypto.randomUUID(), kind: b.kind ?? "incident", title: b.title.trim(), status: b.status ?? "investigating", impact: b.impact ?? "minor",
      components: b.components ?? [], starts_at: b.starts_at ?? now, ends_at: b.ends_at ?? null, created_at: now, updated_at: now,
      updates: b.message ? [{ id: 1, status: b.status ?? "investigating", message: b.message, created_at: now }] : [],
    };
    state.incidents.unshift(inc);
    return HttpResponse.json({ incident: inc }, { status: 201 });
  }),
  http.post(`${API}/admin/status/incidents/:id/updates`, async ({ request, params }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const inc = state.incidents.find((x) => x.id === params.id);
    if (!inc) return fail("not_found", "incident not found", 404);
    const b = (await request.json()) as { status: StatusIncident["status"]; message: string };
    inc.status = b.status;
    inc.updates.unshift({ id: inc.updates.length + 1, status: b.status, message: b.message, created_at: formatTs(Date.now()) });
    return HttpResponse.json({ incident: inc });
  }),
  http.delete(`${API}/admin/status/incidents/:id`, ({ request, params }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    state.incidents = state.incidents.filter((x) => x.id !== params.id);
    return new HttpResponse(null, { status: 204 });
  }),
];
