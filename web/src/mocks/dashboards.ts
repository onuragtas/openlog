// MSW handlers for custom dashboards (api.md "Dashboards") with in-memory documents: optimistic concurrency (409 on a
// stale version), can_edit per role and creator, private visibility, add widget, duplicate, export and import. The seed
// has an "Infrastructure overview" dashboard with widgets of every visualization and a `host` query variable.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { Dashboard, DashboardExport, DashboardImport, DashboardInput, DashboardPage, DashboardVariable, DashboardWidget, DashboardWidgetInput } from "@/api/dashboards";
import type { Role } from "@/api/roles";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";
import { validateOql } from "./oql";

const API = "*/api/v1/dashboards";
const ADMIN_ID = "7c1e2d9a-3b4f-4e5a-8b6c-000000000001";
const GRACE_ID = "7c1e2d9a-3b4f-4e5a-8b6c-000000000002";
const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };

export const MOCK_DASHBOARD_IDS = {
  infra: "d0000000-0000-4000-8000-000000000001",
  checkout: "d0000000-0000-4000-8000-000000000002",
  gracePrivate: "d0000000-0000-4000-8000-000000000003",
};

type Code = "invalid_argument" | "permission_denied" | "not_found" | "failed_precondition";
const STATUS: Record<Code, number> = { invalid_argument: 400, permission_denied: 403, not_found: 404, failed_precondition: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

type Stored = Omit<Dashboard, "can_edit">;

interface State {
  dashboards: Stored[];
  seq: number;
}

function widget(id: string, p: Partial<DashboardWidget> & Pick<DashboardWidget, "title" | "visualization" | "layout">): DashboardWidget {
  return { id, query: "", markdown: "", unit: "", thresholds: [], options: { legend: true }, ...p };
}

const HOST_VAR: DashboardVariable = {
  name: "host", label: "Host", type: "query", query: "SELECT count(*) FROM Log FACET host.name LIMIT 100", values: [], default: [], multi: true, include_all: true,
};

function seed(): State {
  const now = Date.now();
  const ts = (ago: number) => formatTs(now - ago);
  const infra: Stored = {
    id: MOCK_DASHBOARD_IDS.infra,
    name: "Infrastructure overview",
    description: "Logs and transactions across hosts",
    visibility: "org",
    version: 3,
    variables: [HOST_VAR],
    pages: [
      {
        id: "p0000000-0000-4000-8000-000000000001",
        name: "Overview",
        widgets: [
          widget("w-logs-total", { title: "Log volume", visualization: "billboard", layout: { x: 0, y: 0, w: 4, h: 2 }, query: "SELECT count(*) FROM Log WHERE host.name IN ({{host}})", unit: "number", thresholds: [{ value: 3000, severity: "warning" }, { value: 20000, severity: "critical" }] }),
          widget("w-p95", { title: "p95 latency", visualization: "billboard", layout: { x: 4, y: 0, w: 4, h: 2 }, query: "SELECT percentile(duration.ms, 95) FROM Transaction COMPARE WITH 1 day ago", unit: "ms" }),
          widget("w-about", { title: "About", visualization: "markdown", layout: { x: 8, y: 0, w: 4, h: 2 }, markdown: "## Infrastructure\nLogs and transactions of **all hosts**. Pick hosts in the *Host* variable.\n\n- [openlog on GitHub](https://github.com/onuragtas/openlog)\n- Queries use `OQL`" }),
          widget("w-logs-host", { title: "Logs by host", visualization: "line", layout: { x: 0, y: 2, w: 8, h: 3 }, query: "SELECT count(*) FROM Log WHERE host.name IN ({{host}}) FACET host.name TIMESERIES" }),
          widget("w-severity", { title: "Logs by severity", visualization: "pie", layout: { x: 8, y: 2, w: 4, h: 3 }, query: "SELECT count(*) FROM Log FACET severity" }),
          widget("w-slowest", { title: "Slowest transactions", visualization: "table", layout: { x: 0, y: 5, w: 6, h: 3 }, query: "SELECT average(duration.ms), count(*) FROM Transaction FACET transaction.name LIMIT 5", unit: "ms" }),
          widget("w-services", { title: "Requests by service", visualization: "bar", layout: { x: 6, y: 5, w: 6, h: 3 }, query: "SELECT count(*) FROM Transaction FACET service.name" }),
          widget("w-heat", { title: "Errors by service", visualization: "heatmap", layout: { x: 0, y: 8, w: 12, h: 3 }, query: "SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name TIMESERIES" }),
        ],
      },
      {
        id: "p0000000-0000-4000-8000-000000000002",
        name: "Latency",
        widgets: [
          widget("w-latency-area", { title: "Average latency by service", visualization: "area", layout: { x: 0, y: 0, w: 12, h: 4 }, query: "SELECT average(duration.ms) FROM Transaction FACET service.name TIMESERIES", unit: "ms", options: { stacked: true, legend: true } }),
          widget("w-hist", { title: "Latency distribution", visualization: "bar", layout: { x: 0, y: 4, w: 12, h: 3 }, query: "SELECT histogram(duration.ms, 1000, 20) FROM Transaction" }),
        ],
      },
    ],
    created_by_user_id: ADMIN_ID,
    created_by_email: "admin@openlog.local",
    created_at: ts(20 * 86_400_000),
    updated_at: ts(3_600_000),
  };
  const checkout: Stored = {
    id: MOCK_DASHBOARD_IDS.checkout,
    name: "Checkout service",
    description: "",
    visibility: "org",
    version: 1,
    variables: [],
    pages: [{ id: "p0000000-0000-4000-8000-000000000003", name: "Page 1", widgets: [
      widget("w-co-1", { title: "Throughput", visualization: "line", layout: { x: 0, y: 0, w: 12, h: 3 }, query: "SELECT count(*) FROM Transaction WHERE service.name = 'checkout' TIMESERIES" }),
    ] }],
    created_by_user_id: GRACE_ID,
    created_by_email: "grace@example.com",
    created_at: ts(5 * 86_400_000),
    updated_at: ts(2 * 86_400_000),
  };
  const priv: Stored = { ...checkout, id: MOCK_DASHBOARD_IDS.gracePrivate, name: "Grace's scratchpad", visibility: "private", pages: [{ id: "p-priv", name: "Page 1", widgets: [] }] };
  return { dashboards: [infra, checkout, priv], seq: 100 };
}

let db = seed();

/** Restores the seed data (Vitest). */
export function resetMockDashboards(): void {
  db = seed();
}

type Info = Parameters<HttpResponseResolver>[0];
interface Ctx {
  role: Role;
  session: boolean;
  userId: string | null;
}

function guarded(write: boolean, fn: (info: Info, ctx: Ctx) => Response | Promise<Response>): HttpResponseResolver {
  return (info) => {
    const auth = authenticate(info.request);
    if (auth instanceof Response) return auth;
    const ctx: Ctx = { role: auth.role, session: auth.kind === "session", userId: auth.kind === "session" ? ADMIN_ID : null };
    if (write) {
      if (!ctx.session) return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
      if (RANK[ctx.role] < RANK.member) return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
    }
    return fn(info, ctx);
  };
}

const visible = (d: Stored, ctx: Ctx) => d.visibility === "org" || d.created_by_user_id === ctx.userId;
const canEdit = (d: Stored, ctx: Ctx) =>
  ctx.session && RANK[ctx.role] >= RANK.member && (d.created_by_user_id === ctx.userId || (RANK[ctx.role] >= RANK.admin && d.visibility === "org"));
const withEdit = (d: Stored, ctx: Ctx): Dashboard => ({ ...d, can_edit: canEdit(d, ctx) });

const uuid = () => `${Math.random().toString(16).slice(2, 10)}-0000-4000-8000-${(++db.seq).toString(16).padStart(12, "0")}`;

async function json<T>(request: Request): Promise<Partial<T>> {
  try {
    return ((await request.json()) as Partial<T>) ?? {};
  } catch {
    return {};
  }
}

function validateWidget(w: Partial<DashboardWidgetInput>, where: string): string | null {
  const l = w.layout;
  if (!w.visualization || !["line", "area", "bar", "table", "billboard", "pie", "heatmap", "markdown"].includes(w.visualization)) return `${where}.visualization: invalid`;
  if (!l || l.x < 0 || l.w < 1 || l.w > 12 || l.x + l.w > 12 || l.y < 0 || l.y > 10000 || l.h < 1 || l.h > 50) return `${where}.layout: must fit the 12-column grid`;
  if (w.visualization === "markdown") return (w.markdown ?? "").length > 20000 ? `${where}.markdown: at most 20000 characters` : null;
  const v = validateOql(w.query ?? "");
  if (!v.valid) return `${where}.query: line ${v.errors[0]!.line}, column ${v.errors[0]!.column}: ${v.errors[0]!.message}`;
  if ((w.thresholds ?? []).length > 10) return `${where}.thresholds: at most 10`;
  return null;
}

function validateInput(input: Partial<DashboardInput>): string | null {
  const name = input.name?.trim() ?? "";
  if (name.length < 1 || name.length > 200) return "name: must be 1-200 characters";
  if ((input.description ?? "").length > 2000) return "description: at most 2000 characters";
  const pages = input.pages ?? [];
  if (pages.length > 20) return "pages: at most 20";
  for (const [pi, p] of pages.entries()) {
    if (!p.name?.trim() || p.name.length > 100) return `pages[${pi}].name: must be 1-100 characters`;
    if ((p.widgets ?? []).length > 100) return `pages[${pi}].widgets: at most 100`;
    for (const [wi, w] of (p.widgets ?? []).entries()) {
      const e = validateWidget(w, `pages[${pi}].widgets[${wi}]`);
      if (e) return e;
    }
  }
  const vars = input.variables ?? [];
  if (vars.length > 10) return "variables: at most 10";
  const names = new Set<string>();
  for (const [i, v] of vars.entries()) {
    if (!/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(v.name ?? "")) return `variables[${i}].name: invalid`;
    if (names.has(v.name)) return `variables[${i}].name: duplicate`;
    names.add(v.name);
    if (v.type === "query") {
      const r = validateOql(v.query ?? "");
      if (!r.valid) return `variables[${i}].query: ${r.errors[0]!.message}`;
      if (r.kind !== "facets") return `variables[${i}].query: needs FACET`;
    }
    if ((v.values ?? []).length > 200) return `variables[${i}].values: at most 200`;
  }
  return null;
}

function normalizeWidget(w: DashboardWidgetInput, keep: Set<string>): DashboardWidget {
  return {
    id: w.id && keep.has(w.id) ? w.id : uuid(),
    title: w.title ?? "",
    visualization: w.visualization,
    layout: w.layout,
    query: w.visualization === "markdown" ? "" : (w.query ?? ""),
    markdown: w.visualization === "markdown" ? (w.markdown ?? "") : "",
    unit: w.unit ?? "",
    thresholds: w.thresholds ?? [],
    options: w.options ?? {},
  };
}

function toStored(input: Partial<DashboardInput>, ctx: Ctx, base?: Stored): Stored {
  const now = formatTs(Date.now());
  const pageIds = new Set(base?.pages.map((p) => p.id) ?? []);
  const widgetIds = new Set(base?.pages.flatMap((p) => p.widgets.map((w) => w.id)) ?? []);
  const pagesIn = input.pages && input.pages.length > 0 ? input.pages : [{ name: "Page 1", widgets: [] }];
  const pages: DashboardPage[] = pagesIn.map((p) => ({
    id: p.id && pageIds.has(p.id) ? p.id : uuid(),
    name: p.name,
    widgets: (p.widgets ?? []).map((w) => normalizeWidget(w, widgetIds)),
  }));
  return {
    id: base?.id ?? uuid(),
    name: input.name!.trim(),
    description: input.description ?? "",
    visibility: input.visibility ?? base?.visibility ?? "org",
    version: (base?.version ?? 0) + 1,
    variables: input.variables ?? [],
    pages,
    created_by_user_id: base?.created_by_user_id ?? ctx.userId,
    created_by_email: base?.created_by_email ?? "admin@openlog.local",
    created_at: base?.created_at ?? now,
    updated_at: now,
  };
}

function exportDoc(d: Stored): DashboardExport {
  return {
    openlog_dashboard: 1,
    name: d.name,
    description: d.description,
    variables: d.variables,
    pages: d.pages.map((p) => ({ name: p.name, widgets: p.widgets.map(({ id: _id, ...w }) => w) })),
  };
}

const find = (info: Info, ctx: Ctx) => {
  const d = db.dashboards.find((x) => x.id === String(info.params.id ?? ""));
  return d && visible(d, ctx) ? d : null;
};

/** The stored mock dashboard (mocks/dashboardSharing.ts: versions, share links). */
export function mockDashboard(id: string): Stored | undefined {
  return db.dashboards.find((x) => x.id === id);
}

/** Replaces a stored mock dashboard (version restore). */
export function replaceMockDashboard(d: Stored): void {
  db.dashboards = db.dashboards.map((x) => (x.id === d.id ? d : x));
}

export const dashboardHandlers = [
  http.get(API, guarded(false, ({ request }, ctx) => {
    const q = (new URL(request.url).searchParams.get("q") ?? "").toLowerCase();
    const list = db.dashboards
      .filter((d) => visible(d, ctx) && (!q || d.name.toLowerCase().includes(q)))
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((d) => ({
        id: d.id, name: d.name, description: d.description, visibility: d.visibility, page_count: d.pages.length,
        widget_count: d.pages.reduce((n, p) => n + p.widgets.length, 0), created_by_email: d.created_by_email, updated_at: d.updated_at, can_edit: canEdit(d, ctx),
      }));
    return HttpResponse.json({ dashboards: list });
  })),
  http.post(`${API}/import`, guarded(true, async ({ request }, ctx) => {
    const doc = await json<DashboardImport>(request);
    if (doc.openlog_dashboard !== 1) return fail("invalid_argument", "openlog_dashboard: unsupported document version");
    const input: Partial<DashboardInput> = { name: doc.name, description: doc.description, visibility: doc.visibility, variables: doc.variables, pages: doc.pages };
    const err = validateInput(input);
    if (err) return fail("invalid_argument", err);
    const d = toStored(input, ctx);
    db.dashboards.push(d);
    return HttpResponse.json(withEdit(d, ctx), { status: 201 });
  })),
  http.post(API, guarded(true, async ({ request }, ctx) => {
    const input = await json<DashboardInput>(request);
    const err = validateInput(input);
    if (err) return fail("invalid_argument", err);
    const d = toStored(input, ctx);
    db.dashboards.push(d);
    return HttpResponse.json(withEdit(d, ctx), { status: 201 });
  })),
  http.get(`${API}/:id`, guarded(false, (info, ctx) => {
    const d = find(info, ctx);
    return d ? HttpResponse.json(withEdit(d, ctx)) : fail("not_found", "dashboard not found");
  })),
  http.put(`${API}/:id`, guarded(true, async (info, ctx) => {
    const d = find(info, ctx);
    if (!d) return fail("not_found", "dashboard not found");
    if (!canEdit(d, ctx)) return fail("permission_denied", "you can only change dashboards you created");
    const input = await json<DashboardInput>(info.request);
    if (input.version !== d.version) return fail("failed_precondition", `the dashboard was changed meanwhile (version ${d.version}); reload it`);
    const err = validateInput(input);
    if (err) return fail("invalid_argument", err);
    const next = toStored(input, ctx, d);
    db.dashboards = db.dashboards.map((x) => (x.id === d.id ? next : x));
    return HttpResponse.json(withEdit(next, ctx));
  })),
  http.delete(`${API}/:id`, guarded(true, (info, ctx) => {
    const d = find(info, ctx);
    if (!d) return fail("not_found", "dashboard not found");
    if (!canEdit(d, ctx)) return fail("permission_denied", "you can only delete dashboards you created");
    db.dashboards = db.dashboards.filter((x) => x.id !== d.id);
    return new HttpResponse(null, { status: 204 });
  })),
  http.post(`${API}/:id/widgets`, guarded(true, async (info, ctx) => {
    const d = find(info, ctx);
    if (!d) return fail("not_found", "dashboard not found");
    if (!canEdit(d, ctx)) return fail("permission_denied", "you can only change dashboards you created");
    const b = await json<{ page_id: string; widget: DashboardWidgetInput }>(info.request);
    const page = b.page_id ? d.pages.find((p) => p.id === b.page_id) : d.pages[0];
    if (!page) return fail("invalid_argument", "page_id: no such page");
    if (!b.widget) return fail("invalid_argument", "widget: required");
    const y = page.widgets.reduce((m, w) => Math.max(m, w.layout.y + w.layout.h), 0);
    const input = { ...b.widget, layout: { ...b.widget.layout, y } };
    const err = validateWidget(input, "widget");
    if (err) return fail("invalid_argument", err);
    page.widgets.push(normalizeWidget(input, new Set()));
    d.version++;
    d.updated_at = formatTs(Date.now());
    return HttpResponse.json(withEdit(d, ctx));
  })),
  http.post(`${API}/:id/duplicate`, guarded(true, async (info, ctx) => {
    const d = find(info, ctx);
    if (!d) return fail("not_found", "dashboard not found");
    const b = await json<{ name: string }>(info.request);
    const copy = toStored({ ...exportDoc(d), visibility: d.visibility, name: b.name?.trim() || `${d.name} (copy)` }, { ...ctx }, undefined);
    copy.created_by_user_id = ctx.userId;
    db.dashboards.push(copy);
    return HttpResponse.json(withEdit(copy, ctx), { status: 201 });
  })),
  http.get(`${API}/:id/export`, guarded(false, (info, ctx) => {
    const d = find(info, ctx);
    return d ? HttpResponse.json(exportDoc(d)) : fail("not_found", "dashboard not found");
  })),
];
