// MSW handlers for dashboard version history, sharing settings, share links, scheduled reports and the public share
// link endpoints (api.md "Dashboards" › "Version history" … "Scheduled reports"). Share links start disabled; the
// "Infrastructure overview" dashboard has a stored earlier version and one daily report.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { Dashboard, DashboardPage, DashboardVariable } from "@/api/dashboards";
import type { DashboardDiff, DashboardReport, DashboardReportInput, DashboardShare, DashboardShareInput, DashboardVersion } from "@/api/dashboardSharing";
import type { OqlResult } from "@/api/oql";
import type { Role } from "@/api/roles";
import { authenticate } from "./account";
import { MOCK_DASHBOARD_IDS, mockDashboard, replaceMockDashboard } from "./dashboards";
import { formatTs } from "./fixtures";
import { runMockQuery } from "./oql";

const API = "*/api/v1/dashboards";
const PUBLIC = "*/api/v1/public/dashboards";
const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };
const MEMBERS = ["admin@openlog.local", "grace@example.com"];

type Code = "invalid_argument" | "permission_denied" | "not_found" | "failed_precondition";
const STATUS: Record<Code, number> = { invalid_argument: 400, permission_denied: 403, not_found: 404, failed_precondition: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

type Doc = Pick<Dashboard, "name" | "description" | "visibility" | "variables" | "pages">;
interface Snapshot extends DashboardVersion {
  document: Doc;
}
type StoredShare = DashboardShare & { dashboard_id: string; token: string };
type StoredReport = DashboardReport & { dashboard_id: string };

interface State {
  settings: { share_links_enabled: boolean; report_domains: string[]; updated_at: string | null };
  versions: Map<string, Snapshot[]>;
  shares: StoredShare[];
  reports: StoredReport[];
  seq: number;
}

const docOf = (d: Doc): Doc => structuredClone({ name: d.name, description: d.description, visibility: d.visibility, variables: d.variables, pages: d.pages });
const counts = (doc: Doc) => ({ page_count: doc.pages.length, widget_count: doc.pages.reduce((n, p) => n + p.widgets.length, 0) });

function seed(): State {
  const now = Date.now();
  return {
    settings: { share_links_enabled: false, report_domains: [], updated_at: null },
    versions: new Map(),
    shares: [],
    reports: [
      {
        id: "r0000000-0000-4000-8000-000000000001",
        dashboard_id: MOCK_DASHBOARD_IDS.infra,
        name: "Morning infrastructure summary",
        frequency: "daily",
        weekday: 1,
        hour: 9,
        minute: 0,
        timezone: "Europe/Istanbul",
        recipients: ["admin@openlog.local"],
        language: "en",
        range: "24h",
        variables: {},
        enabled: true,
        created_by_email: "admin@openlog.local",
        created_at: formatTs(now - 3 * 86_400_000),
        updated_at: formatTs(now - 3 * 86_400_000),
        next_run_at: formatTs(now + 20 * 3_600_000),
        last_run: { period: new Date(now).toISOString().slice(0, 10), status: "sent", error: "", recipients: 1, started_at: formatTs(now - 4 * 3_600_000), finished_at: formatTs(now - 4 * 3_600_000 + 2000) },
      },
    ],
    seq: 100,
  };
}

let state = seed();

/** Restores the seed data (Vitest). */
export function resetMockDashboardSharing(): void {
  state = seed();
}

type Info = Parameters<HttpResponseResolver>[0];
interface Ctx {
  role: Role;
  session: boolean;
}

function guarded(write: boolean, fn: (info: Info, ctx: Ctx) => Response | Promise<Response>): HttpResponseResolver {
  return (info) => {
    const auth = authenticate(info.request);
    if (auth instanceof Response) return auth;
    const ctx: Ctx = { role: auth.role, session: auth.kind === "session" };
    if (write) {
      if (!ctx.session) return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
      if (RANK[ctx.role] < RANK.member) return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
    }
    return fn(info, ctx);
  };
}

async function json<T>(request: Request): Promise<Partial<T>> {
  try {
    return ((await request.json()) as Partial<T>) ?? {};
  } catch {
    return {};
  }
}

const uuid = () => `${Math.random().toString(16).slice(2, 10)}-0000-4000-8000-${(++state.seq).toString(16).padStart(12, "0")}`;

/** Stored versions of a dashboard, seeded with an earlier draft and kept in step with saves of the dashboards mock. */
function versionsOf(d: Dashboard | Omit<Dashboard, "can_edit">): Snapshot[] {
  let list = state.versions.get(d.id);
  if (!list) {
    list = [];
    if (d.version > 1) {
      const earlier = docOf(d);
      earlier.name = `${d.name} (draft)`;
      const first = earlier.pages[0];
      if (first && first.widgets.length > 1) first.widgets = first.widgets.slice(0, -1);
      list.push({ version: d.version - 1, author_user_id: d.created_by_user_id, author_email: d.created_by_email, created_at: formatTs(Date.parse(d.updated_at) - 86_400_000), restored_from: null, ...counts(earlier), document: earlier });
    }
    state.versions.set(d.id, list);
  }
  if (!list.some((v) => v.version === d.version)) {
    const doc = docOf(d);
    list.push({ version: d.version, author_user_id: d.created_by_user_id, author_email: "admin@openlog.local", created_at: d.updated_at, restored_from: null, ...counts(doc), document: doc });
  }
  list.sort((a, b) => b.version - a.version);
  return list.slice(0, 50);
}

function diff(from: Doc, to: Doc): DashboardDiff {
  const eq = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
  const pagesFrom = new Map(from.pages.map((p) => [p.id, p]));
  const pagesTo = new Map(to.pages.map((p) => [p.id, p]));
  const widgets = (pages: DashboardPage[]) => new Map(pages.flatMap((p) => p.widgets.map((w) => [w.id, { w, page: p }] as const)));
  const wFrom = widgets(from.pages);
  const wTo = widgets(to.pages);
  const out: DashboardDiff = {
    name: from.name !== to.name, description: from.description !== to.description, visibility: from.visibility !== to.visibility,
    variables: !eq(from.variables as DashboardVariable[], to.variables as DashboardVariable[]),
    pages_added: to.pages.filter((p) => !pagesFrom.has(p.id)).map((p) => ({ id: p.id, name: p.name })),
    pages_removed: from.pages.filter((p) => !pagesTo.has(p.id)).map((p) => ({ id: p.id, name: p.name })),
    pages_renamed: to.pages.filter((p) => pagesFrom.has(p.id) && pagesFrom.get(p.id)!.name !== p.name).map((p) => ({ id: p.id, name: p.name })),
    widgets_added: [], widgets_removed: [], widgets_changed: [],
  };
  for (const [id, { w, page }] of wTo) {
    const old = wFrom.get(id);
    if (!old) {
      out.widgets_added.push({ id, title: w.title, page: page.name });
      continue;
    }
    const fields = (["title", "visualization", "layout", "query", "markdown", "unit", "thresholds", "options"] as const).filter((f) => !eq(old.w[f], w[f]));
    const all: NonNullable<DashboardDiff["widgets_changed"][number]["fields"]> = [...fields];
    if (old.page.id !== page.id) all.push("page");
    if (all.length > 0) out.widgets_changed.push({ id, title: w.title, page: page.name, fields: all });
  }
  for (const [id, { w, page }] of wFrom) if (!wTo.has(id)) out.widgets_removed.push({ id, title: w.title, page: page.name });
  return out;
}

const versionJSON = ({ document: _doc, ...v }: Snapshot): DashboardVersion => v;
const shareJSON = ({ dashboard_id: _d, token: _t, ...s }: StoredShare): DashboardShare => ({ ...s, active: !s.revoked_at && Date.parse(s.expires_at) > Date.now() });
const reportJSON = ({ dashboard_id: _d, ...r }: StoredReport): DashboardReport => r;

function dashboardFor(info: Info) {
  return mockDashboard(String(info.params.id ?? ""));
}

function validateReport(b: Partial<DashboardReportInput>, visibility: string): string | null {
  if (b.frequency !== "daily" && b.frequency !== "weekly") return "frequency must be daily or weekly";
  if (typeof b.hour !== "number" || b.hour < 0 || b.hour > 23) return "hour must be 0-23 and minute 0-59";
  const rcpt = b.recipients ?? [];
  if (rcpt.length === 0 || rcpt.length > 20) return "recipients: 1-20 e-mail addresses";
  for (const [i, r] of rcpt.entries()) {
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(r)) return `recipients[${i}] is not a valid e-mail address`;
    const addr = r.toLowerCase();
    if (visibility === "private" && addr !== "admin@openlog.local") return "reports of a private dashboard can only be sent to its creator";
    if (!MEMBERS.includes(addr) && !state.settings.report_domains.includes(addr.split("@")[1]!)) return `${addr} is neither a member of the organization nor in an allowed report domain`;
  }
  return null;
}

function publicShare(token: string): { share: StoredShare; dashboard: Omit<Dashboard, "can_edit"> } | null {
  const share = state.shares.find((s) => s.token === token);
  if (!share || share.revoked_at || Date.parse(share.expires_at) <= Date.now() || !state.settings.share_links_enabled) return null;
  const dashboard = mockDashboard(share.dashboard_id);
  return dashboard ? { share, dashboard } : null;
}

const PUBLIC_HEADERS = { "Cache-Control": "no-store", "Referrer-Policy": "no-referrer" };
const gone = () => HttpResponse.json({ error: { code: "not_found", message: "this share link does not exist or is no longer valid" } }, { status: 404, headers: PUBLIC_HEADERS });

export const dashboardSharingHandlers = [
  http.get(`${API}/settings`, guarded(false, (_info, ctx) => HttpResponse.json({ ...state.settings, can_edit: ctx.session && RANK[ctx.role] >= RANK.admin }))),
  http.put(`${API}/settings`, guarded(true, async ({ request }, ctx) => {
    if (RANK[ctx.role] < RANK.admin) return fail("permission_denied", "only admins and owners can change the dashboard sharing settings");
    const b = await json<{ share_links_enabled: boolean; report_domains: string[] }>(request);
    if (typeof b.share_links_enabled !== "boolean") return fail("invalid_argument", "share_links_enabled is required");
    const domains = [...new Set((b.report_domains ?? []).map((d) => d.trim().toLowerCase().replace(/^@/, "")))];
    const bad = domains.findIndex((d) => !/^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/.test(d));
    if (bad >= 0) return fail("invalid_argument", `report_domains[${bad}] must be a domain name such as example.com`);
    state.settings = { share_links_enabled: b.share_links_enabled, report_domains: domains, updated_at: formatTs(Date.now()) };
    return HttpResponse.json({ ...state.settings, can_edit: true });
  })),

  http.get(`${API}/:id/versions`, guarded(false, (info, ctx) => {
    const d = dashboardFor(info);
    if (!d) return fail("not_found", "dashboard not found");
    return HttpResponse.json({ versions: versionsOf(d).map(versionJSON), current_version: d.version, can_restore: ctx.session && RANK[ctx.role] >= RANK.member });
  })),
  http.get(`${API}/:id/versions/:version`, guarded(false, (info) => {
    const d = dashboardFor(info);
    if (!d) return fail("not_found", "dashboard not found");
    const list = versionsOf(d);
    const v = list.find((x) => x.version === Number(info.params.version));
    if (!v) return fail("not_found", "dashboard not found");
    const prev = list.find((x) => x.version < v.version);
    return HttpResponse.json({
      ...versionJSON(v),
      document: v.document,
      current_version: d.version,
      previous_version: prev?.version ?? null,
      changes: prev ? diff(prev.document, v.document) : null,
      differences_from_current: diff(docOf(d), v.document),
    });
  })),
  http.post(`${API}/:id/versions/:version/restore`, guarded(true, async (info) => {
    const d = dashboardFor(info);
    if (!d) return fail("not_found", "dashboard not found");
    const v = versionsOf(d).find((x) => x.version === Number(info.params.version));
    if (!v) return fail("not_found", "dashboard not found");
    const b = await json<{ version: number }>(info.request);
    if (!b.version) return fail("invalid_argument", "version is required (the current version of the dashboard that was read)");
    if (b.version !== d.version) return fail("failed_precondition", "the dashboard was changed by someone else; reload and try again");
    const next = { ...d, ...structuredClone(v.document), visibility: d.visibility, version: d.version + 1, updated_at: formatTs(Date.now()) };
    replaceMockDashboard(next);
    const doc = docOf(next);
    state.versions.get(d.id)!.unshift({ version: next.version, author_user_id: d.created_by_user_id, author_email: "admin@openlog.local", created_at: next.updated_at, restored_from: v.version, ...counts(doc), document: doc });
    return HttpResponse.json({ ...next, can_edit: true });
  })),

  http.get(`${API}/:id/shares`, guarded(false, (info, ctx) => {
    const d = dashboardFor(info);
    if (!d) return fail("not_found", "dashboard not found");
    if (!ctx.session || RANK[ctx.role] < RANK.member) return fail("permission_denied", "only the creator of this dashboard or an admin can change it");
    const shares = state.shares.filter((s) => s.dashboard_id === d.id).sort((a, b) => b.created_at.localeCompare(a.created_at)).map(shareJSON);
    return HttpResponse.json({ shares, share_links_enabled: state.settings.share_links_enabled });
  })),
  http.post(`${API}/:id/shares`, guarded(true, async (info) => {
    const d = dashboardFor(info);
    if (!d) return fail("not_found", "dashboard not found");
    if (!state.settings.share_links_enabled) return fail("failed_precondition", "share links are disabled for this organization; an admin or owner can enable them in the dashboard sharing settings");
    const b = await json<DashboardShareInput>(info.request);
    const expires = Date.parse(b.expires_at ?? "");
    if (Number.isNaN(expires)) return fail("invalid_argument", "expires_at is required");
    if (expires < Date.now() + 5 * 60_000 || expires > Date.now() + 90 * 86_400_000) return fail("invalid_argument", "expires_at must be between 5 minutes and 90 days from now");
    if (!b.range && !(b.from && b.to)) return fail("invalid_argument", "range or from and to is required");
    if (b.range && !/^[1-9][0-9]{0,3}[mhd]$/.test(b.range)) return fail("invalid_argument", "range must be a relative range such as 15m, 24h or 7d (at most 31 days)");
    if (state.shares.filter((s) => s.dashboard_id === d.id && !s.revoked_at && Date.parse(s.expires_at) > Date.now()).length >= 20) {
      return fail("invalid_argument", "a dashboard has at most 20 active share links; revoke one first");
    }
    const bytes = crypto.getRandomValues(new Uint8Array(32));
    const token = `olds_${btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "")}`;
    const share: StoredShare = {
      id: uuid(), dashboard_id: d.id, token, label: b.label ?? "", range: b.range ?? null, from: b.range ? null : (b.from ?? null), to: b.range ? null : (b.to ?? null),
      variables: b.variables ?? {}, created_by_email: "admin@openlog.local", created_at: formatTs(Date.now()), expires_at: formatTs(expires),
      revoked_at: null, last_used_at: null, use_count: 0, active: true,
    };
    state.shares.push(share);
    return HttpResponse.json({ ...shareJSON(share), token, path: `/shared/dashboards/${token}` }, { status: 201 });
  })),
  http.delete(`${API}/:id/shares/:shareId`, guarded(true, (info) => {
    const s = state.shares.find((x) => x.id === info.params.shareId && x.dashboard_id === info.params.id);
    if (!s) return fail("not_found", "dashboard not found");
    s.revoked_at ??= formatTs(Date.now());
    return HttpResponse.json(shareJSON(s));
  })),

  http.get(`${API}/:id/reports`, guarded(false, (info, ctx) => {
    const d = dashboardFor(info);
    if (!d) return fail("not_found", "dashboard not found");
    if (!ctx.session || RANK[ctx.role] < RANK.member) return fail("permission_denied", "only the creator of this dashboard or an admin can change it");
    return HttpResponse.json({ reports: state.reports.filter((r) => r.dashboard_id === d.id).map(reportJSON) });
  })),
  http.post(`${API}/:id/reports`, guarded(true, async (info) => {
    const d = dashboardFor(info);
    if (!d) return fail("not_found", "dashboard not found");
    const b = await json<DashboardReportInput>(info.request);
    const err = validateReport(b, d.visibility);
    if (err) return fail("invalid_argument", err);
    const now = formatTs(Date.now());
    const r: StoredReport = {
      id: uuid(), dashboard_id: d.id, name: b.name ?? "", frequency: b.frequency!, weekday: b.weekday ?? 1, hour: b.hour!, minute: b.minute ?? 0, timezone: b.timezone || "UTC",
      recipients: [...new Set(b.recipients!.map((x) => x.toLowerCase()))], language: b.language ?? "en", range: b.range || (b.frequency === "weekly" ? "7d" : "24h"),
      variables: b.variables ?? {}, enabled: b.enabled ?? true, created_by_email: "admin@openlog.local", created_at: now, updated_at: now,
      next_run_at: b.enabled === false ? null : formatTs(Date.now() + 86_400_000), last_run: null,
    };
    state.reports.push(r);
    return HttpResponse.json(reportJSON(r), { status: 201 });
  })),
  http.put(`${API}/:id/reports/:reportId`, guarded(true, async (info) => {
    const d = dashboardFor(info);
    const r = state.reports.find((x) => x.id === info.params.reportId && x.dashboard_id === info.params.id);
    if (!d || !r) return fail("not_found", "dashboard not found");
    const b = await json<DashboardReportInput>(info.request);
    const err = validateReport(b, d.visibility);
    if (err) return fail("invalid_argument", err);
    Object.assign(r, {
      name: b.name ?? "", frequency: b.frequency, weekday: b.weekday ?? 1, hour: b.hour, minute: b.minute ?? 0, timezone: b.timezone || "UTC",
      recipients: [...new Set(b.recipients!.map((x) => x.toLowerCase()))], language: b.language ?? "en", range: b.range || (b.frequency === "weekly" ? "7d" : "24h"),
      variables: b.variables ?? {}, enabled: b.enabled ?? true, updated_at: formatTs(Date.now()), next_run_at: b.enabled === false ? null : formatTs(Date.now() + 86_400_000),
    });
    return HttpResponse.json(reportJSON(r));
  })),
  http.delete(`${API}/:id/reports/:reportId`, guarded(true, (info) => {
    const i = state.reports.findIndex((x) => x.id === info.params.reportId && x.dashboard_id === info.params.id);
    if (i < 0) return fail("not_found", "dashboard not found");
    state.reports.splice(i, 1);
    return new HttpResponse(null, { status: 204 });
  })),

  // Public share link endpoints: no authentication.
  http.get(`${PUBLIC}/:token`, ({ params }) => {
    const found = publicShare(String(params.token));
    if (!found) return gone();
    const { share, dashboard: d } = found;
    return HttpResponse.json(
      {
        name: d.name,
        description: d.description,
        pages: d.pages.map((p) => ({ id: p.id, name: p.name, widgets: p.widgets.map(({ query: _q, ...w }) => w) })),
        variables: d.variables.map((v) => ({ name: v.name, label: v.label, values: share.variables[v.name] ?? [] })),
        time_range: { range: share.range, from: share.from, to: share.to },
        expires_at: share.expires_at,
      },
      { headers: PUBLIC_HEADERS },
    );
  }),
  http.get(`${PUBLIC}/:token/widgets/:widgetId/result`, ({ params }) => {
    const found = publicShare(String(params.token));
    if (!found) return gone();
    const { share, dashboard: d } = found;
    const w = d.pages.flatMap((p) => p.widgets).find((x) => x.id === params.widgetId && x.visualization !== "markdown");
    if (!w) return HttpResponse.json({ error: { code: "not_found", message: "widget not found" } }, { status: 404, headers: PUBLIC_HEADERS });
    share.last_used_at = formatTs(Date.now());
    const units: Record<string, number> = { m: 60_000, h: 3_600_000, d: 86_400_000 };
    const to = share.range ? Date.now() : Date.parse(share.to ?? "");
    const from = share.range ? to - Number(share.range.slice(0, -1)) * units[share.range.slice(-1)]! : Date.parse(share.from ?? "");
    const vars = Object.fromEntries(Object.entries(share.variables).map(([k, v]) => [k, v]));
    const { bucketSeconds, truncated, ...rest } = runMockQuery(w.query, from, to, vars);
    const result: OqlResult = {
      ...rest,
      compare: null,
      metadata: { from: formatTs(from), to: formatTs(to), bucket_seconds: bucketSeconds, rollup: false, table: "", rows_read: 0, bytes_read: 0, elapsed_ms: 12, queries: 1, facet_limit: 10, truncated, warnings: [] },
    };
    return HttpResponse.json(result, { headers: PUBLIC_HEADERS });
  }),
];
