// MSW handlers for authentication and the management API (docs/contracts/api.md)
// with in-memory data. The session cookie is emulated by `mockAuth`: in the
// browser (dev:mock, Playwright) the state lives in sessionStorage, so reloads
// keep the session and tests can end it; in Vitest it lives in memory and
// `resetMockAccounts()` runs after every test.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { ApiKey, Invitation, LicenseKey, Member, Session } from "@/api/account";
import { customKeyProblem } from "@/api/licenseKeyValue";
import type { Role } from "@/api/roles";
import { formatTs } from "./fixtures";

export const MOCK_EMAIL = "admin@openlog.local";
export const MOCK_PASSWORD = "openlog-dev-password";
export const MOCK_API_KEY = "ola_mock-read-only-api-key";
export const MOCK_INVITE_TOKEN = "oli_mock-invitation";
export const MOCK_DEFAULT_ORG_ID = "0f7b3c1e-5a2d-4c1b-9e8f-00000000000a";
export const MOCK_STAGING_ORG_ID = "0f7b3c1e-5a2d-4c1b-9e8f-00000000000b";
const SESSION_KEY = "openlog.mock.session";

const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };

type Code = "invalid_argument" | "unauthenticated" | "permission_denied" | "not_found" | "already_exists" | "failed_precondition";
const STATUS: Record<Code, number> = {
  invalid_argument: 400,
  unauthenticated: 401,
  permission_denied: 403,
  not_found: 404,
  already_exists: 409,
  failed_precondition: 409,
};
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });
const noContent = () => new HttpResponse(null, { status: 204 });

interface SessionState {
  signedIn: boolean;
  csrf: string;
}
const SIGNED_OUT: SessionState = { signedIn: false, csrf: "" };
let memory: SessionState = SIGNED_OUT;
let storage: Storage | null = null;

export const mockAuth = {
  /** Keeps the emulated session in `s` (the browser's sessionStorage). */
  persistIn(s: Storage): void {
    storage = s;
  },
  get(): SessionState {
    if (storage) {
      try {
        const raw = storage.getItem(SESSION_KEY);
        return raw ? (JSON.parse(raw) as SessionState) : SIGNED_OUT;
      } catch {
        return SIGNED_OUT;
      }
    }
    return memory;
  },
  set(s: SessionState): void {
    memory = s;
    if (storage) {
      try {
        storage.setItem(SESSION_KEY, JSON.stringify(s));
      } catch {
        // ignore
      }
    }
  },
  signIn(): SessionState {
    const s = { signedIn: true, csrf: `mock-csrf-${Math.random().toString(36).slice(2)}` };
    mockAuth.set(s);
    return s;
  },
  signOut(): void {
    mockAuth.set(SIGNED_OUT);
  },
};

interface MockOrg {
  id: string;
  tenant_id: string;
  name: string;
  role: Role;
  created_at: string;
}

function seed() {
  const now = Date.now();
  const ago = (ms: number) => formatTs(now - ms);
  const day = 86_400_000;
  const user = { id: "7c1e2d9a-3b4f-4e5a-8b6c-000000000001", email: MOCK_EMAIL, name: "Ada Admin" };
  return {
    user,
    orgs: [
      { id: MOCK_DEFAULT_ORG_ID, tenant_id: "default", name: "Default", role: "owner", created_at: ago(40 * day) },
      { id: MOCK_STAGING_ORG_ID, tenant_id: "staging", name: "Staging", role: "viewer", created_at: ago(12 * day) },
    ] as MockOrg[],
    members: [
      { user_id: user.id, email: user.email, name: user.name, role: "owner", joined_at: ago(40 * day) },
      { user_id: "7c1e2d9a-3b4f-4e5a-8b6c-000000000002", email: "grace@example.com", name: "Grace Hopper", role: "admin", joined_at: ago(30 * day) },
      { user_id: "7c1e2d9a-3b4f-4e5a-8b6c-000000000003", email: "linus@example.com", name: "Linus", role: "viewer", joined_at: ago(3 * day) },
    ] as Member[],
    invitations: [
      { id: "inv-1", email: "new@example.com", role: "member", invited_by_email: user.email, created_at: ago(day), expires_at: formatTs(now + 6 * day) },
    ] as Invitation[],
    licenseKeys: [
      { id: "lk-1", name: "production hosts", prefix: "olk_9f3c2a71", custom: false, created_by_email: user.email, created_at: ago(20 * day), last_used_at: ago(45_000), revoked_at: null },
      { id: "lk-2", name: "old staging", prefix: "olk_77aa01b3", custom: false, created_by_email: "grace@example.com", created_at: ago(35 * day), last_used_at: ago(9 * day), revoked_at: ago(8 * day) },
    ] as LicenseKey[],
    customKeyValues: new Set<string>(),
    apiKeys: [
      {
        id: "ak-1", name: "grafana", prefix: "ola_5d1e0c9a", scope: "read", created_by_user_id: "7c1e2d9a-3b4f-4e5a-8b6c-000000000002",
        created_by_email: "grace@example.com", created_at: ago(15 * day), last_used_at: ago(3_600_000), expires_at: null, revoked_at: null,
      },
    ] as ApiKey[],
    sessions: [
      { id: "s-current", created_at: ago(2 * 3_600_000), last_seen_at: ago(10_000), expires_at: formatTs(now + 7 * day), ip: "127.0.0.1", user_agent: "Mozilla/5.0 (this browser)", current: true },
      { id: "s-2", created_at: ago(3 * day), last_seen_at: ago(5 * 3_600_000), expires_at: formatTs(now + 4 * day), ip: "203.0.113.20", user_agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) Gecko/20100101 Firefox/130.0", current: false },
    ] as Session[],
    seq: 100,
  };
}

let db = seed();

/** Restores the seed data and signs out (Vitest setup). */
export function resetMockAccounts(): void {
  db = seed();
  mockAuth.signOut();
}

const hex = (n: number) => Array.from({ length: n }, () => Math.floor(Math.random() * 16).toString(16)).join("");
const nextId = (prefix: string) => `${prefix}-${++db.seq}`;

interface Ctx {
  org: MockOrg;
  role: Role;
  kind: "session" | "api_key";
}

/** Emulates internal/auth Service.Authenticate: API key bearer or session + CSRF + organization header. */
export function authenticate(request: Request): Ctx | Response {
  const authz = request.headers.get("authorization") ?? "";
  if (authz.toLowerCase().startsWith("bearer ")) {
    if (authz.slice(7).trim() !== MOCK_API_KEY) return fail("unauthenticated", "invalid API key");
    return { org: db.orgs[0]!, role: "viewer", kind: "api_key" };
  }
  const state = mockAuth.get();
  if (!state.signedIn) return fail("unauthenticated", "missing credentials");
  const orgId = request.headers.get("x-openlog-org-id");
  const org = orgId ? db.orgs.find((o) => o.id === orgId) : db.orgs[0];
  if (!org) return fail("permission_denied", "you are not a member of this organization");
  if (!["GET", "HEAD", "OPTIONS"].includes(request.method) && request.headers.get("x-csrf-token") !== state.csrf) {
    return fail("permission_denied", "missing or invalid CSRF token");
  }
  return { org, role: org.role, kind: "session" };
}

type Info = Parameters<HttpResponseResolver>[0];
type Handler = (ctx: Ctx, info: Info) => Response | Promise<Response>;

/** Authenticated management handler; `min` requires a session with at least that role. */
function authed(min: Role | null, fn: Handler): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    if (min) {
      if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
      if (RANK[ctx.role] < RANK[min]) return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
    }
    return fn(ctx, info);
  };
}

function sessionOnly(fn: Handler): HttpResponseResolver {
  return authed(null, (ctx, info) => (ctx.kind === "session" ? fn(ctx, info) : fail("permission_denied", "this operation requires a signed-in user; API keys are read-only")));
}

async function body<T>(request: Request): Promise<Partial<T>> {
  try {
    return ((await request.json()) as Partial<T>) ?? {};
  } catch {
    return {};
  }
}

function me(ctx: Ctx) {
  const session = ctx.kind === "session";
  return {
    auth: ctx.kind,
    user: session ? db.user : null,
    organization: { id: ctx.org.id, tenant_id: ctx.org.tenant_id, name: ctx.org.name },
    role: ctx.role,
    organizations: session ? db.orgs.map((o) => ({ id: o.id, tenant_id: o.tenant_id, name: o.name, role: o.role })) : [],
    csrf_token: session ? mockAuth.get().csrf : null,
  };
}

const param = (info: Info, name: string) => String(info.params[name] ?? "");

const API = "*/api/v1";

export const accountHandlers = [
  http.get(`${API}/auth/config`, () => HttpResponse.json({ mode: "postgres", signup_enabled: false, password_min_length: 8 })),

  http.post(`${API}/auth/login`, async ({ request }) => {
    const { email, password } = await body<{ email: string; password: string }>(request);
    if (!email || !password) return fail("invalid_argument", "email and password are required");
    if (email.trim().toLowerCase() !== MOCK_EMAIL || password !== MOCK_PASSWORD) return fail("unauthenticated", "invalid email or password");
    mockAuth.signIn();
    return HttpResponse.json(me({ org: db.orgs[0]!, role: db.orgs[0]!.role, kind: "session" }));
  }),

  http.get(`${API}/auth/me`, authed(null, (ctx) => HttpResponse.json(me(ctx)))),

  http.get(`${API}/version`, authed(null, () =>
    HttpResponse.json(
      { version: "0.1.0-mock", commit: "mock", date: "", latest_available: null, update_check: "disabled", updater: null },
      { headers: { "X-Openlog-Version": "0.1.0-mock" } },
    ),
  )),

  http.post(`${API}/auth/logout`, sessionOnly(() => {
    mockAuth.signOut();
    return noContent();
  })),

  http.post(`${API}/auth/password`, sessionOnly(async (_ctx, { request }) => {
    const b = await body<{ current_password: string; new_password: string }>(request);
    if (b.current_password !== MOCK_PASSWORD) return fail("permission_denied", "current password is incorrect");
    if ((b.new_password ?? "").length < 8) return fail("invalid_argument", "password must be at least 8 characters");
    return noContent();
  })),

  http.get(`${API}/orgs/current`, authed(null, (ctx) => HttpResponse.json({ ...ctx.org, role: ctx.role }))),

  http.patch(`${API}/orgs/current`, authed("admin", async (ctx, { request }) => {
    const name = (await body<{ name: string }>(request)).name?.trim();
    if (!name) return fail("invalid_argument", "name is required");
    ctx.org.name = name;
    return HttpResponse.json({ ...ctx.org, role: ctx.role });
  })),

  http.get(`${API}/members`, sessionOnly(() => HttpResponse.json({ members: db.members }))),

  http.patch(`${API}/members/:userId`, authed("admin", async (ctx, info) => {
    const role = (await body<{ role: Role }>(info.request)).role;
    const target = db.members.find((m) => m.user_id === param(info, "userId"));
    if (!target) return fail("not_found", "member not found");
    if (!role || !(role in RANK)) return fail("invalid_argument", "role must be one of owner, admin, member, viewer");
    if ((target.role === "owner" || role === "owner") && ctx.role !== "owner") return fail("permission_denied", "only owners can grant or remove the owner role");
    if (target.role === "owner" && role !== "owner" && db.members.filter((m) => m.role === "owner").length <= 1) {
      return fail("failed_precondition", "an organization must keep at least one owner");
    }
    target.role = role;
    return noContent();
  })),

  http.delete(`${API}/members/:userId`, sessionOnly((ctx, info) => {
    const id = param(info, "userId");
    const target = db.members.find((m) => m.user_id === id);
    if (!target) return fail("not_found", "member not found");
    if (id !== db.user.id) {
      if (RANK[ctx.role] < RANK.admin) return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
      if (target.role === "owner" && ctx.role !== "owner") return fail("permission_denied", "only owners can remove an owner");
    }
    if (target.role === "owner" && db.members.filter((m) => m.role === "owner").length <= 1) {
      return fail("failed_precondition", "an organization must keep at least one owner");
    }
    db.members = db.members.filter((m) => m.user_id !== id);
    return noContent();
  })),

  http.get(`${API}/invitations`, authed("admin", () => HttpResponse.json({ invitations: db.invitations }))),

  http.post(`${API}/invitations`, authed("admin", async (ctx, { request }) => {
    const b = await body<{ email: string; role: Role }>(request);
    const email = (b.email ?? "").trim().toLowerCase();
    if (!/^[^@\s]+@[^@\s]+$/.test(email)) return fail("invalid_argument", "a valid email address is required");
    if (!b.role || !(b.role in RANK)) return fail("invalid_argument", "role must be one of owner, admin, member, viewer");
    if (b.role === "owner" && ctx.role !== "owner") return fail("permission_denied", "only owners can invite owners");
    if (db.members.some((m) => m.email === email)) return fail("already_exists", "this user is already a member");
    if (db.invitations.some((i) => i.email === email)) return fail("already_exists", "a pending invitation for this email already exists");
    const now = Date.now();
    const invitation: Invitation = { id: nextId("inv"), email, role: b.role, invited_by_email: db.user.email, created_at: formatTs(now), expires_at: formatTs(now + 7 * 86_400_000) };
    db.invitations.unshift(invitation);
    return HttpResponse.json({ invitation, token: `oli_${hex(48)}` }, { status: 201 });
  })),

  http.delete(`${API}/invitations/:id`, authed("admin", (_ctx, info) => {
    const before = db.invitations.length;
    db.invitations = db.invitations.filter((i) => i.id !== param(info, "id"));
    return db.invitations.length === before ? fail("not_found", "not found") : noContent();
  })),

  http.post(`${API}/invitations/lookup`, async ({ request }) => {
    const { token } = await body<{ token: string }>(request);
    if (token !== MOCK_INVITE_TOKEN) return fail("not_found", "invitation is invalid or has expired");
    return HttpResponse.json({ organization_name: "Default", email: "new@example.com", role: "member", expires_at: formatTs(Date.now() + 86_400_000), user_exists: false });
  }),

  http.post(`${API}/invitations/accept`, async ({ request }) => {
    const b = await body<{ token: string; password: string; name: string }>(request);
    if (b.token !== MOCK_INVITE_TOKEN) return fail("not_found", "invitation is invalid or has expired");
    if ((b.password ?? "").length < 8) return fail("invalid_argument", "password must be at least 8 characters");
    mockAuth.signIn();
    return HttpResponse.json(me({ org: db.orgs[0]!, role: db.orgs[0]!.role, kind: "session" }));
  }),

  http.get(`${API}/license-keys`, authed("member", () => HttpResponse.json({ license_keys: db.licenseKeys }))),

  http.post(`${API}/license-keys`, authed("admin", async (_ctx, { request }) => {
    const b = await body<{ name: string; key?: string | null }>(request);
    const name = b.name?.trim();
    if (!name) return fail("invalid_argument", "name is required");
    if (typeof b.key === "string") {
      // Imported value: same rules as the API (docs/contracts/api.md); only a "hash" is kept.
      const value = b.key.trim();
      const problem = customKeyProblem(value);
      if (problem) return fail("invalid_argument", problem === "length" ? "key must be 16–256 characters" : "key may contain only printable ASCII characters without spaces, quotes or backslashes");
      if (db.customKeyValues.has(value)) return fail("already_exists", "this key value is already in use; choose another value");
      db.customKeyValues.add(value);
      const license_key: LicenseKey = { id: nextId("lk"), name, prefix: value.slice(0, Math.min(8, Math.floor(value.length / 2))), custom: true, created_by_email: db.user.email, created_at: formatTs(Date.now()), last_used_at: null, revoked_at: null };
      db.licenseKeys.unshift(license_key);
      return HttpResponse.json({ license_key, key: null }, { status: 201 });
    }
    const key = `olk_${hex(48)}`;
    const license_key: LicenseKey = { id: nextId("lk"), name, prefix: key.slice(0, 12), custom: false, created_by_email: db.user.email, created_at: formatTs(Date.now()), last_used_at: null, revoked_at: null };
    db.licenseKeys.unshift(license_key);
    return HttpResponse.json({ license_key, key }, { status: 201 });
  })),

  http.delete(`${API}/license-keys/:id`, authed("admin", (_ctx, info) => {
    const k = db.licenseKeys.find((x) => x.id === param(info, "id"));
    if (!k) return fail("not_found", "not found");
    k.revoked_at ??= formatTs(Date.now());
    return noContent();
  })),

  http.get(`${API}/api-keys`, authed("member", () => HttpResponse.json({ api_keys: db.apiKeys }))),

  http.post(`${API}/api-keys`, authed("member", async (_ctx, { request }) => {
    const b = await body<{ name: string; expires_at: string | null }>(request);
    const name = b.name?.trim();
    if (!name) return fail("invalid_argument", "name is required");
    const key = `ola_${hex(48)}`;
    const api_key: ApiKey = {
      id: nextId("ak"), name, prefix: key.slice(0, 12), scope: "read", created_by_user_id: db.user.id, created_by_email: db.user.email,
      created_at: formatTs(Date.now()), last_used_at: null, expires_at: b.expires_at ? formatTs(Date.parse(b.expires_at)) : null, revoked_at: null,
    };
    db.apiKeys.unshift(api_key);
    return HttpResponse.json({ api_key, key }, { status: 201 });
  })),

  http.delete(`${API}/api-keys/:id`, authed("member", (ctx, info) => {
    const k = db.apiKeys.find((x) => x.id === param(info, "id"));
    if (!k) return fail("not_found", "not found");
    if (k.created_by_user_id !== db.user.id && RANK[ctx.role] < RANK.admin) return fail("permission_denied", "only the key's creator or an admin can revoke it");
    k.revoked_at ??= formatTs(Date.now());
    return noContent();
  })),

  http.get(`${API}/sessions`, sessionOnly(() => HttpResponse.json({ sessions: db.sessions }))),

  http.delete(`${API}/sessions/:id`, sessionOnly((_ctx, info) => {
    const id = param(info, "id");
    if (!db.sessions.some((s) => s.id === id)) return fail("not_found", "session not found");
    db.sessions = db.sessions.filter((s) => s.id !== id);
    if (id === "s-current") mockAuth.signOut();
    return noContent();
  })),
];
