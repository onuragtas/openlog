// MSW handlers for integration settings (docs/contracts/api.md "Integration settings") with an in-memory
// store. The mock "agent" applies a host's settings on the second GET after a change, so the UI can show
// the pending state and then the applied state without a backend.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { IntegrationSetting, IntegrationSettingInput, IntegrationSettingsHost } from "@/api/integrationSettings";
import type { Role } from "@/api/roles";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1/integrations/settings";
const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };
const FIELDS: Record<string, string[]> = {
  nginx: ["endpoint"],
  redis: ["endpoint", "username", "password"],
  mysql: ["endpoint", "username", "password"],
  postgresql: ["endpoint", "username", "password", "database", "databases"],
  docker: [],
  mssql: ["endpoint", "username", "password"],
  iis: [],
};

type Code = "invalid_argument" | "permission_denied" | "not_found" | "already_exists";
const STATUS: Record<Code, number> = { invalid_argument: 400, permission_denied: 403, not_found: 404, already_exists: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

interface Stored extends IntegrationSetting {
  password: string;
}

const state = { settings: [] as Stored[], seq: 0, revisions: new Map<string, number>(), applied: new Map<string, { rev: number; reads: number }>() };

export function resetMockIntegrationSettings(): void {
  state.settings = [];
  state.seq = 0;
  state.revisions.clear();
  state.applied.clear();
}

/** Last password stored for a setting (tests check it was sent, the API never returns it). */
export function mockStoredPassword(id: string): string | undefined {
  return state.settings.find((s) => s.id === id)?.password;
}

function bump(hostId: string | null) {
  const hosts = hostId ? [hostId] : [...new Set([...state.revisions.keys(), ...state.applied.keys()])];
  for (const h of hosts) state.revisions.set(h, (state.revisions.get(h) ?? 0) + 1);
}

function hostState(hostId: string): IntegrationSettingsHost {
  const rev = state.revisions.get(hostId) ?? 0;
  const applied = state.applied.get(hostId) ?? { rev: 0, reads: 0 };
  if (applied.rev !== rev) {
    applied.reads++;
    if (applied.reads >= 2) {
      applied.rev = rev;
      applied.reads = 0;
    }
    state.applied.set(hostId, applied);
  }
  return {
    host_id: hostId,
    revision: `sha256:${rev}`,
    applied_revision: `sha256:${applied.rev}`,
    applied_at: formatTs(Date.now() - 5_000),
    remote_config_disabled: false,
  };
}

function write(fn: (info: Parameters<HttpResponseResolver>[0]) => Response | Promise<Response>): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
    if (RANK[ctx.role] < RANK.admin) return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
    return fn(info);
  };
}

function validate(in_: Partial<IntegrationSettingInput>): string | null {
  if (!in_.integration || !(in_.integration in FIELDS)) return "integration must be one of docker, iis, mssql, mysql, nginx, postgresql, redis";
  const allowed = FIELDS[in_.integration]!;
  for (const f of ["endpoint", "username", "password", "database"] as const) {
    if (in_[f] && !allowed.includes(f)) return `${f} is not supported by the ${in_.integration} integration`;
  }
  if (in_.endpoint) {
    if (in_.integration === "nginx" ? !/^https?:\/\/[^/]+/.test(in_.endpoint) : !/^(unix:\/.+|[^\s:]+:\d+|\[[^\]]+\]:\d+)$/.test(in_.endpoint)) {
      return `endpoint ${in_.endpoint} is invalid`;
    }
  }
  return null;
}

function apply(target: Stored, in_: Partial<IntegrationSettingInput>) {
  target.host_id = in_.host_id || null;
  target.integration = in_.integration!;
  target.match = { port: in_.match?.port ?? null, container: in_.match?.container ?? "", endpoint: in_.match?.endpoint ?? "", instance: in_.match?.instance ?? "" };
  target.enabled = in_.enabled ?? true;
  target.endpoint = in_.endpoint ?? "";
  target.username = in_.username ?? "";
  target.database = in_.database ?? "";
  target.databases = in_.databases ?? [];
  if (in_.password !== undefined && in_.password !== null) target.password = in_.password;
  target.password_set = target.password !== "";
  target.updated_at = formatTs(Date.now());
}

const view = ({ password: _password, ...s }: Stored): IntegrationSetting => s;

function sameScope(a: Stored, b: Stored) {
  return a.host_id === b.host_id && a.integration === b.integration && JSON.stringify(a.match) === JSON.stringify(b.match);
}

async function readBody(request: Request): Promise<Partial<IntegrationSettingInput>> {
  try {
    return ((await request.json()) as Partial<IntegrationSettingInput>) ?? {};
  } catch {
    return {};
  }
}

export const integrationSettingsHandlers = [
  http.get(API, (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    const hostId = new URL(info.request.url).searchParams.get("host_id") ?? "";
    const items = state.settings.filter((s) => !hostId || s.host_id === null || s.host_id === hostId).map(view);
    return HttpResponse.json({ items, host: hostId ? hostState(hostId) : null });
  }),
  http.post(
    API,
    write(async ({ request }) => {
      const in_ = await readBody(request);
      const msg = validate(in_);
      if (msg) return fail("invalid_argument", msg);
      const now = formatTs(Date.now());
      const s: Stored = {
        id: `is-${++state.seq}`, host_id: null, integration: "nginx", match: { port: null, container: "", endpoint: "", instance: "" }, enabled: true,
        endpoint: "", username: "", password: "", password_set: false, database: "", databases: [], created_at: now, updated_at: now, updated_by_email: "admin@example.com",
      };
      apply(s, in_);
      if (state.settings.some((o) => sameScope(o, s))) return fail("already_exists", "a setting for this integration, host and match already exists");
      state.settings.push(s);
      bump(s.host_id);
      return HttpResponse.json(view(s), { status: 201 });
    }),
  ),
  http.put(
    `${API}/:id`,
    write(async ({ request, params }) => {
      const s = state.settings.find((o) => o.id === params.id);
      if (!s) return fail("not_found", "not found");
      const in_ = await readBody(request);
      const msg = validate(in_);
      if (msg) return fail("invalid_argument", msg);
      apply(s, in_);
      bump(s.host_id);
      return HttpResponse.json(view(s));
    }),
  ),
  http.delete(
    `${API}/:id`,
    write(({ params }) => {
      const i = state.settings.findIndex((o) => o.id === params.id);
      if (i < 0) return fail("not_found", "not found");
      const [s] = state.settings.splice(i, 1);
      bump(s!.host_id);
      return new HttpResponse(null, { status: 204 });
    }),
  ),
];
