// MSW handlers for single sign-on, domain verification and SCIM tokens (docs/contracts/api.md "Single sign-on")
// with in-memory data mirroring the server rules (internal/sso). In the browser (dev:mock, Playwright) the data is
// kept in sessionStorage, because a test sign-in is a full page navigation; Vitest calls resetMockSso().
import { http, HttpResponse } from "msw";
import type { Role } from "@/api/roles";
import type { ScimToken, SsoConnection, SsoDomain, SsoHealth, SsoRoleMapping, SsoState } from "@/api/sso";
import { authenticate, mockAuth, mockMembers, setMockClaimedDomainPolicy } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1";
const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };
export const MOCK_DOMAIN_TOKEN = "oldv_mock-domain-verification";
const STORAGE_KEY = "openlog.mock.sso";
/** sessionStorage key a Playwright test sets to "enabled-oidc" to start with an enabled, tested OIDC connection. */
export const MOCK_SSO_PRESET_KEY = "openlog.mock.sso.preset";
/** sessionStorage key set to "sso" when the mock session was created by single sign-on (GET /auth/sso/session). */
export const MOCK_SSO_SESSION_KEY = "openlog.mock.sso.session";
const MAX_CONNECTIONS = 10;
const ORG_NAME = "Default";

type Code = "invalid_argument" | "unauthenticated" | "permission_denied" | "not_found" | "already_exists" | "failed_precondition";
const STATUS: Record<Code, number> = { invalid_argument: 400, unauthenticated: 401, permission_denied: 403, not_found: 404, already_exists: 409, failed_precondition: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

interface SsoDb {
  /** The default (oldest) connection first. */
  connections: SsoConnection[];
  spCertificate: string;
  domains: SsoDomain[];
  /** Organization-wide mappings. */
  mappings: SsoRoleMapping[];
  connectionMappings: Record<string, SsoRoleMapping[]>;
  tokens: ScimToken[];
  seq: number;
}

const origin = () => (typeof window !== "undefined" && window.location ? window.location.origin : "http://localhost");

function seed(): SsoDb {
  const now = Date.now();
  return {
    connections: [],
    spCertificate: "-----BEGIN CERTIFICATE-----\nMIIBmock\n-----END CERTIFICATE-----\n",
    domains: [
      {
        id: "dom-1", domain: "openlog.local", verified: true, verified_at: formatTs(now - 86_400_000), verification_method: "dns_txt",
        dns_record: { type: "TXT", name: "_openlog-verification.openlog.local", value: "openlog-domain-verification=mock1" },
        email_address: null, email_expires_at: null, last_checked_at: formatTs(now - 86_400_000), connection_id: null, created_at: formatTs(now - 2 * 86_400_000),
      },
    ],
    mappings: [],
    connectionMappings: {},
    tokens: [],
    seq: 10,
  };
}

const unknownHealth = (): SsoHealth => ({ status: "unknown", message: "", checked_at: null, next_at: null, failures: 0, metadata_valid_until: null });
const okHealth = (): SsoHealth => ({
  status: "ok", message: "", checked_at: formatTs(Date.now()), next_at: formatTs(Date.now() + 3_600_000), failures: 0, metadata_valid_until: null,
});

function defaultConnection(protocol: "oidc" | "saml", id = "0f7b3c1e-5a2d-4c1b-9e8f-0000000055c0"): SsoConnection {
  const ts = formatTs(Date.now());
  return {
    id, protocol, name: "", enabled: false, default: false, oidc: null, saml: null,
    email_attribute: "", name_attribute: "", groups_attribute: "", jit_enabled: true, default_role: "viewer", session_max_age_seconds: 0,
    logout_redirect_allowlist: [], allow_external_invitations: true,
    enforce: false, break_glass_user_ids: [], config_version: 0, tested: false, last_test: null, health: unknownHealth(), created_at: ts, updated_at: ts,
  };
}

function storage(): Storage | null {
  try {
    return import.meta.env.MODE !== "test" && typeof sessionStorage !== "undefined" ? sessionStorage : null;
  } catch {
    return null;
  }
}

/** Upgrades mock data stored by the single-connection mock. */
function migrate(raw: Partial<SsoDb> & { connection?: SsoConnection | null }): SsoDb {
  const base = seed();
  const connections = raw.connections ?? (raw.connection ? [raw.connection] : []);
  return {
    ...base,
    ...raw,
    connections: connections.map((c) => ({ ...defaultConnection(c.protocol, c.id), ...c })),
    domains: (raw.domains ?? base.domains).map((d) => ({ ...d, connection_id: d.connection_id ?? null })),
    connectionMappings: raw.connectionMappings ?? {},
  };
}

function load(): SsoDb {
  let data = seed();
  const s = storage();
  if (!s) return data;
  try {
    const raw = s.getItem(STORAGE_KEY);
    if (raw) data = migrate(JSON.parse(raw) as Partial<SsoDb>);
    if (s.getItem(MOCK_SSO_PRESET_KEY) === "enabled-oidc" && data.connections.length === 0) {
      data.connections = [
        {
          ...defaultConnection("oidc"), name: "Mock IdP", enabled: true, tested: true, config_version: 1, health: okHealth(),
          oidc: { issuer: "https://idp.example.com", client_id: "openlog", scopes: [], require_email_verified: true, client_secret_set: true },
        },
      ];
    }
  } catch {
    // ignore unreadable mock state
  }
  return data;
}

let db = load();
let memorySsoSession = false;

function save(): void {
  try {
    storage()?.setItem(STORAGE_KEY, JSON.stringify(db));
  } catch {
    // ignore
  }
}

/** Restores the seed data (tests). */
export function resetMockSso(): void {
  db = seed();
  memorySsoSession = false;
  save();
}

/** Replaces the mock connections with one default connection, or none (tests). */
export function setMockSsoConnection(c: Partial<SsoConnection> | null): void {
  db.connections = c === null ? [] : [{ ...defaultConnection(c.protocol ?? "oidc"), ...c }];
  save();
}

/** Adds a connection after the existing ones (tests); returns it. */
export function addMockSsoConnection(c: Partial<SsoConnection>): SsoConnection {
  const next = { ...defaultConnection(c.protocol ?? "oidc", newConnectionId()), ...c };
  db.connections = [...db.connections, next];
  save();
  return next;
}

/** Marks the mock session as created by single sign-on (tests; the browser uses MOCK_SSO_SESSION_KEY). */
export function setMockSsoSession(sso: boolean): void {
  memorySsoSession = sso;
  try {
    const s = storage();
    if (s) {
      if (sso) s.setItem(MOCK_SSO_SESSION_KEY, "sso");
      else s.removeItem(MOCK_SSO_SESSION_KEY);
    }
  } catch {
    // ignore
  }
}

function ssoSession(): boolean {
  try {
    const s = storage();
    if (s) return s.getItem(MOCK_SSO_SESSION_KEY) === "sso";
  } catch {
    // ignore
  }
  return memorySsoSession;
}

function newConnectionId(): string {
  return `0f7b3c1e-5a2d-4c1b-9e8f-${String(++db.seq).padStart(12, "0")}`;
}

const withDefault = () => db.connections.map((c, i) => ({ ...c, default: i === 0 }));
const findConnection = (id: string) => withDefault().find((c) => c.id === id) ?? null;

function state(c: SsoConnection | null): SsoState {
  const saml = c?.protocol === "saml" && !!c.saml;
  const entity = c ? `${origin()}/api/v1/sso/saml/${c.id}/metadata` : null;
  return {
    available: true, secrets_encrypted: true, scim_enabled: true, email_verification_available: true,
    domain_email_local_parts: ["admin", "administrator", "hostmaster", "postmaster", "webmaster"],
    service_provider: {
      oidc_redirect_uri: `${origin()}/api/v1/sso/oidc/callback`, oidc_post_logout_redirect_uri: `${origin()}/api/v1/sso/oidc/logout/callback`,
      scim_base_url: `${origin()}/api/scim/v2`,
      saml_entity_id: saml ? entity : null, saml_acs_url: saml && c ? `${origin()}/api/v1/sso/saml/${c.id}/acs` : null,
      saml_slo_url: saml && c ? `${origin()}/api/v1/sso/saml/${c.id}/slo` : null,
      saml_metadata_url: saml ? entity : null, saml_certificate_pem: saml ? db.spCertificate : null,
    },
    connection: c ? { ...c, default: db.connections[0]?.id === c.id } : null,
    connections: withDefault(),
  };
}

type Ctx = Exclude<ReturnType<typeof authenticate>, Response>;

function gate(request: Request, min: Role): Ctx | Response {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
  if (RANK[ctx.role] < RANK[min]) return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
  return ctx;
}

async function body<T>(request: Request): Promise<Partial<T>> {
  try {
    return ((await request.json()) as Partial<T>) ?? {};
  } catch {
    return {};
  }
}

/** Persists the mock data and returns r (every successful mutation). */
function persist<R>(r: R): R {
  save();
  return r;
}

const noContent = () => persist(new HttpResponse(null, { status: 204 }));
const hex = (n: number) => Array.from({ length: n }, () => Math.floor(Math.random() * 16).toString(16)).join("");
const assignable = (r: unknown): r is "admin" | "member" | "viewer" => r === "admin" || r === "member" || r === "viewer";
const domainOf = (email: string) => email.trim().toLowerCase().split("@")[1] ?? "";
const labelOf = (c: SsoConnection) => c.name || `${c.protocol} ${c.id}`;

/** Whether domain d routes to connection id (internal/sso routesTo). */
const routesTo = (d: SsoDomain, id: string) => (d.connection_id ? d.connection_id === id : db.connections[0]?.id === id);

/** The enabled connection a verified e-mail domain routes to, else null. */
function connectionForEmail(email: string): SsoConnection | null {
  const d = db.domains.find((x) => x.domain === domainOf(email) && x.verified);
  if (!d) return null;
  const c = db.connections.find((x) => routesTo(d, x.id));
  return c?.enabled ? c : null;
}

// Claimed-domain redirection (internal/sso/claimed.go) for sign-up and invitations (mocks/account.ts).
setMockClaimedDomainPolicy((email) => {
  const c = connectionForEmail(email);
  return c ? { organizationName: ORG_NAME, connectionName: labelOf(c), protocol: c.protocol, allowExternalInvitations: c.allow_external_invitations } : null;
});

/** Refuses taking verified domain d away from an enforcing connection when it is its last verified domain. */
function keepEnforcedDomain(d: SsoDomain, verb: string, moved?: SsoDomain): Response | null {
  if (!d.verified) return null;
  for (const c of db.connections) {
    if (!c.enforce || !routesTo(d, c.id) || (moved && routesTo(moved, c.id))) continue;
    if (db.domains.filter((x) => x.verified && routesTo(x, c.id)).length <= 1) {
      return fail("failed_precondition", `turn off single sign-on enforcement of "${labelOf(c)}" before ${verb} its last verified domain`);
    }
  }
  return null;
}

interface ConnectionBody {
  protocol: string;
  name: string;
  enabled: boolean;
  default_role: string;
  jit_enabled: boolean;
  session_max_age_seconds: number;
  email_attribute: string;
  name_attribute: string;
  groups_attribute: string;
  logout_redirect_allowlist: string[];
  allow_external_invitations: boolean;
  oidc: { issuer?: string; client_id?: string; client_secret?: string | null; scopes?: string[]; require_email_verified?: boolean };
  saml: { idp_metadata_url?: string; idp_metadata_xml?: string; allow_idp_initiated?: boolean; relay_state_allowlist?: string[]; sign_authn_requests?: boolean };
}

/** Validates a connection input and applies it to prev (null = a new connection). */
function applyInput(prev: SsoConnection | null, b: Partial<ConnectionBody>): SsoConnection | Response {
  if (b.protocol !== "oidc" && b.protocol !== "saml") return fail("invalid_argument", "protocol must be oidc or saml");
  const role = b.default_role ?? "viewer";
  if (!assignable(role)) return fail("invalid_argument", "default_role must be admin, member or viewer (owners are managed in openlog)");
  if (prev?.enforce && !b.enabled) return fail("failed_precondition", "turn off single sign-on enforcement before disabling the connection");
  const paths = (b.logout_redirect_allowlist ?? prev?.logout_redirect_allowlist ?? []).map((p) => p.trim()).filter(Boolean);
  if (paths.length > 20) return fail("invalid_argument", "logout_redirect_allowlist: at most 20 paths");
  const badPath = paths.find((p) => !p.startsWith("/") || p.startsWith("//"));
  if (badPath) return fail("invalid_argument", `logout_redirect_allowlist: ${badPath} is not a relative path`);
  const next: SsoConnection = { ...(prev ?? defaultConnection(b.protocol, newConnectionId())), protocol: b.protocol, name: b.name ?? "", enabled: !!b.enabled };
  if (b.protocol === "oidc") {
    const issuer = (b.oidc?.issuer ?? "").trim();
    if (!/^https?:\/\/[^/\s]+/.test(issuer)) return fail("invalid_argument", "issuer: an absolute URL is required");
    if (!b.oidc?.client_id) return fail("invalid_argument", "client_id is required");
    const keep = b.oidc.client_secret === undefined || b.oidc.client_secret === null;
    const secretSet = keep ? (prev?.oidc?.client_secret_set ?? false) : b.oidc.client_secret !== "";
    next.oidc = { issuer, client_id: b.oidc.client_id, scopes: b.oidc.scopes ?? [], require_email_verified: b.oidc.require_email_verified ?? true, client_secret_set: secretSet };
    next.saml = null;
  } else {
    const url = b.saml?.idp_metadata_url ?? "";
    const xml = b.saml?.idp_metadata_xml ?? "";
    if (!url && !xml && !prev?.saml) return fail("invalid_argument", "idp_metadata_url or idp_metadata_xml is required");
    if (xml && !xml.includes("IDPSSODescriptor")) return fail("invalid_argument", "the metadata has no IDPSSODescriptor");
    // Mock metadata: only metadata that mentions SingleLogoutService (or a URL containing "slo") has an IdP SLO endpoint.
    const slo = xml ? xml.includes("SingleLogoutService") : url ? url.includes("slo") : (prev?.saml?.idp_slo_url ?? null) !== null;
    next.saml = {
      idp_metadata_url: url, idp_entity_id: "https://idp.example.com/metadata", idp_sso_url: "https://idp.example.com/sso",
      idp_slo_url: slo ? "https://idp.example.com/slo" : null,
      idp_certificates: ["3A1F9C427B00DEADBEEF112233445566778899AABBCCDDEEFF00112233445566"], idp_cert_not_after: formatTs(Date.now() + 365 * 86_400_000),
      allow_idp_initiated: !!b.saml?.allow_idp_initiated, relay_state_allowlist: b.saml?.relay_state_allowlist ?? [], sign_authn_requests: !!b.saml?.sign_authn_requests,
    };
    next.oidc = null;
  }
  Object.assign(next, {
    default_role: role, jit_enabled: b.jit_enabled ?? true, session_max_age_seconds: b.session_max_age_seconds ?? 0,
    email_attribute: b.email_attribute ?? "", name_attribute: b.name_attribute ?? "", groups_attribute: b.groups_attribute ?? "",
    logout_redirect_allowlist: paths, allow_external_invitations: b.allow_external_invitations ?? prev?.allow_external_invitations ?? true,
    config_version: next.config_version + 1, tested: false, updated_at: formatTs(Date.now()),
  });
  if (next.last_test?.ok) next.last_test = { ...next.last_test, current: false };
  return next;
}

function replaceConnection(c: SsoConnection): void {
  db.connections = db.connections.map((x) => (x.id === c.id ? c : x));
}

function checksFor(c: SsoConnection) {
  const checks =
    c.protocol === "oidc"
      ? [
          { name: "public_url", ok: true, message: origin() },
          { name: "discovery", ok: true, message: c.oidc?.issuer ?? "" },
          { name: "jwks", ok: true, message: "2 keys" },
          { name: "pkce", ok: true, message: "S256" },
        ]
      : [
          { name: "public_url", ok: true, message: origin() },
          { name: "idp_metadata", ok: true, message: c.saml?.idp_entity_id ?? "" },
          { name: "idp_certificate", ok: true, message: "valid until 2027-09-14T00:00:00Z" },
        ];
  return { ok: true, checks };
}

// The mock "identity provider" signs the test user in at once and returns to the settings page.
function startTest(c: SsoConnection): Response {
  replaceConnection({
    ...c, tested: true,
    last_test: { at: formatTs(Date.now()), ok: true, current: true, error: "", details: { email: "admin@openlog.local", groups: ["openlog-admins"], role: "admin", role_mapped: true } },
  });
  return persist(HttpResponse.json({ redirect_url: "/settings/sso?sso_test=ok" }));
}

async function enforcement(request: Request, c: SsoConnection): Promise<Response> {
  const b = await body<{ enforce: boolean; break_glass_user_ids: string[] }>(request);
  const ids = b.break_glass_user_ids ?? [];
  const owners = mockMembers().filter((m) => m.role === "owner").map((m) => m.user_id);
  if (ids.some((id) => !owners.includes(id))) return fail("invalid_argument", "break-glass accounts must be owners of the organization");
  if (b.enforce) {
    if (!c.enabled) return fail("failed_precondition", "enable the connection before enforcing single sign-on");
    if (!c.tested) return fail("failed_precondition", "run a successful test sign-in of the current settings before enforcing single sign-on");
    if (ids.length === 0) return fail("failed_precondition", "name at least one break-glass owner who can still sign in with a password");
    if (!db.domains.some((d) => d.verified && routesTo(d, c.id))) {
      return fail("failed_precondition", "verify at least one e-mail domain that signs in through this connection before enforcing single sign-on");
    }
  }
  const next = { ...c, enforce: !!b.enforce, break_glass_user_ids: ids };
  replaceConnection(next);
  return persist(HttpResponse.json(state(next)));
}

function deleteConnection(c: SsoConnection): Response {
  if (c.enforce) return fail("failed_precondition", "turn off single sign-on enforcement before deleting the connection");
  db.connections = db.connections.filter((x) => x.id !== c.id);
  db.domains = db.domains.map((d) => (d.connection_id === c.id ? { ...d, connection_id: null } : d));
  delete db.connectionMappings[c.id];
  if (ssoSession()) setMockSsoSession(false);
  return noContent();
}

async function readMappings(request: Request): Promise<SsoRoleMapping[] | Response> {
  const b = await body<{ mappings: { group: string; role: string }[] }>(request);
  const out: SsoRoleMapping[] = [];
  for (const m of b.mappings ?? []) {
    const group = (m.group ?? "").trim();
    if (!group) return fail("invalid_argument", "group is required");
    if (!assignable(m.role)) return fail("invalid_argument", `role of group "${group}" must be admin, member or viewer`);
    if (out.some((x) => x.group === group)) return fail("invalid_argument", `group "${group}" is mapped twice`);
    out.push({ group, role: m.role });
  }
  return out.sort((a, b) => a.group.localeCompare(b.group));
}

const notConfigured = () => fail("not_found", "single sign-on is not configured");
const connectionNotFound = () => fail("not_found", "single sign-on connection not found");
const param = (params: Record<string, unknown>, name: string) => String(params[name] ?? "");

export const ssoHandlers = [
  // ---- single-connection API: the default connection ----
  http.get(`${API}/sso/connection`, ({ request }) => {
    const ctx = gate(request, "admin");
    return ctx instanceof Response ? ctx : HttpResponse.json(state(db.connections[0] ?? null));
  }),

  http.put(`${API}/sso/connection`, async ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const prev = db.connections[0] ?? null;
    const next = applyInput(prev, await body<ConnectionBody>(request));
    if (next instanceof Response) return next;
    if (prev) replaceConnection(next);
    else db.connections = [{ ...next, id: defaultConnection("oidc").id }];
    return persist(HttpResponse.json(state(db.connections[0]!)));
  }),

  http.delete(`${API}/sso/connection`, ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = db.connections[0];
    return c ? deleteConnection(c) : notConfigured();
  }),

  http.post(`${API}/sso/connection/test`, ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = db.connections[0];
    return c ? HttpResponse.json(checksFor(c)) : notConfigured();
  }),

  http.post(`${API}/sso/connection/test/start`, ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = db.connections[0];
    return c ? startTest(c) : notConfigured();
  }),

  http.put(`${API}/sso/enforcement`, async ({ request }) => {
    const ctx = gate(request, "owner");
    if (ctx instanceof Response) return ctx;
    const c = db.connections[0];
    return c ? enforcement(request, c) : notConfigured();
  }),

  http.get(`${API}/sso/role-mappings`, ({ request }) => {
    const ctx = gate(request, "admin");
    return ctx instanceof Response ? ctx : HttpResponse.json({ mappings: db.mappings });
  }),

  http.put(`${API}/sso/role-mappings`, async ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const out = await readMappings(request);
    if (out instanceof Response) return out;
    db.mappings = out;
    return persist(HttpResponse.json({ mappings: db.mappings }));
  }),

  // ---- several connections ----
  http.get(`${API}/sso/connections`, ({ request }) => {
    const ctx = gate(request, "admin");
    return ctx instanceof Response ? ctx : HttpResponse.json(state(null));
  }),

  http.post(`${API}/sso/connections`, async ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    if (db.connections.length >= MAX_CONNECTIONS) return fail("failed_precondition", `an organization can have at most ${MAX_CONNECTIONS} single sign-on connections`);
    const next = applyInput(null, await body<ConnectionBody>(request));
    if (next instanceof Response) return next;
    if (db.connections.length === 0) next.id = defaultConnection("oidc").id;
    db.connections = [...db.connections, next];
    return persist(HttpResponse.json(state(next), { status: 201 }));
  }),

  http.get(`${API}/sso/connections/:id`, ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = findConnection(param(params, "id"));
    return c ? HttpResponse.json(state(c)) : connectionNotFound();
  }),

  http.put(`${API}/sso/connections/:id`, async ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const prev = findConnection(param(params, "id"));
    if (!prev) return connectionNotFound();
    const next = applyInput(prev, await body<ConnectionBody>(request));
    if (next instanceof Response) return next;
    replaceConnection(next);
    return persist(HttpResponse.json(state(next)));
  }),

  http.delete(`${API}/sso/connections/:id`, ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = findConnection(param(params, "id"));
    return c ? deleteConnection(c) : connectionNotFound();
  }),

  http.post(`${API}/sso/connections/:id/test`, ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = findConnection(param(params, "id"));
    return c ? HttpResponse.json(checksFor(c)) : connectionNotFound();
  }),

  http.post(`${API}/sso/connections/:id/test/start`, ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = findConnection(param(params, "id"));
    return c ? startTest(c) : connectionNotFound();
  }),

  // Mock refresh: an issuer or metadata URL containing "broken" cannot be fetched.
  http.post(`${API}/sso/connections/:id/refresh`, ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = findConnection(param(params, "id"));
    if (!c) return connectionNotFound();
    const source = c.oidc?.issuer ?? c.saml?.idp_metadata_url ?? "";
    const health: SsoHealth = source.includes("broken")
      ? { ...okHealth(), status: "warning", message: `GET ${source}: connection refused`, failures: c.health.failures + 1 }
      : okHealth();
    const next = { ...c, health };
    replaceConnection(next);
    return persist(HttpResponse.json(state(next)));
  }),

  http.put(`${API}/sso/connections/:id/enforcement`, async ({ request, params }) => {
    const ctx = gate(request, "owner");
    if (ctx instanceof Response) return ctx;
    const c = findConnection(param(params, "id"));
    return c ? enforcement(request, c) : connectionNotFound();
  }),

  http.get(`${API}/sso/connections/:id/role-mappings`, ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const id = param(params, "id");
    if (!findConnection(id)) return connectionNotFound();
    return HttpResponse.json({ mappings: db.connectionMappings[id] ?? [], connection_id: id });
  }),

  http.put(`${API}/sso/connections/:id/role-mappings`, async ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const id = param(params, "id");
    if (!findConnection(id)) return connectionNotFound();
    const out = await readMappings(request);
    if (out instanceof Response) return out;
    db.connectionMappings[id] = out;
    return persist(HttpResponse.json({ mappings: out, connection_id: id }));
  }),

  // ---- domains ----
  http.get(`${API}/sso/domains`, ({ request }) => {
    const ctx = gate(request, "admin");
    return ctx instanceof Response ? ctx : HttpResponse.json({ domains: db.domains });
  }),

  http.post(`${API}/sso/domains`, async ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const domain = ((await body<{ domain: string }>(request)).domain ?? "").trim().toLowerCase().replace(/\.$/, "").replace(/^@/, "");
    if (!/^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]*$/.test(domain)) return fail("invalid_argument", "domain must be a DNS name such as example.com");
    if (db.domains.some((d) => d.domain === domain)) return fail("already_exists", "the organization already claimed this domain");
    const d: SsoDomain = {
      id: `dom-${++db.seq}`, domain, verified: false, verified_at: null, verification_method: null,
      dns_record: { type: "TXT", name: `_openlog-verification.${domain}`, value: `openlog-domain-verification=${hex(40)}` },
      email_address: null, email_expires_at: null, last_checked_at: null, connection_id: null, created_at: formatTs(Date.now()),
    };
    db.domains = [...db.domains, d].sort((a, b) => a.domain.localeCompare(b.domain));
    return persist(HttpResponse.json(d, { status: 201 }));
  }),

  http.put(`${API}/sso/domains/:id`, async ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const d = db.domains.find((x) => x.id === params.id);
    if (!d) return fail("not_found", "domain not found");
    const b = await body<{ connection_id: string | null }>(request);
    if (b.connection_id === undefined) return fail("invalid_argument", "connection_id is required (null = the default connection)");
    const connectionId = b.connection_id || null;
    if (connectionId === d.connection_id) return HttpResponse.json(d);
    if (connectionId && !findConnection(connectionId)) return fail("not_found", "domain or connection not found");
    const moved = { ...d, connection_id: connectionId };
    const refused = keepEnforcedDomain(d, "moving", moved);
    if (refused) return refused;
    db.domains = db.domains.map((x) => (x.id === d.id ? moved : x));
    return persist(HttpResponse.json(moved));
  }),

  http.post(`${API}/sso/domains/:id/verify`, async ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const d = db.domains.find((x) => x.id === params.id);
    if (!d) return fail("not_found", "domain not found");
    const b = await body<{ method: string; email_local_part: string }>(request);
    let next: SsoDomain;
    if (b.method === "email") {
      if (!["admin", "administrator", "hostmaster", "postmaster", "webmaster"].includes(b.email_local_part ?? "")) {
        return fail("invalid_argument", "the verification e-mail can be sent to admin, administrator, hostmaster, postmaster, webmaster at the domain");
      }
      next = { ...d, email_address: `${b.email_local_part}@${d.domain}`, email_expires_at: formatTs(Date.now() + 86_400_000) };
    } else if (!/(^|\.)example\.com$|\.test$/.test(d.domain)) {
      // Mock DNS: only *.example.com and *.test publish the expected record.
      db.domains = db.domains.map((x) => (x.id === d.id ? { ...d, last_checked_at: formatTs(Date.now()) } : x));
      save();
      return fail("failed_precondition", `TXT record ${d.dns_record.name} with the value ${d.dns_record.value} was not found (DNS changes can take a while)`);
    } else {
      next = { ...d, verified: true, verified_at: formatTs(Date.now()), verification_method: "dns_txt", last_checked_at: formatTs(Date.now()) };
    }
    db.domains = db.domains.map((x) => (x.id === d.id ? next : x));
    return persist(HttpResponse.json(next));
  }),

  http.delete(`${API}/sso/domains/:id`, ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const d = db.domains.find((x) => x.id === params.id);
    if (!d) return fail("not_found", "domain not found");
    const refused = keepEnforcedDomain(d, "removing");
    if (refused) return refused;
    db.domains = db.domains.filter((x) => x.id !== d.id);
    return noContent();
  }),

  http.post(`${API}/sso/domains/verify-email`, async ({ request }) => {
    const token = (await body<{ token: string }>(request)).token ?? "";
    if (token !== MOCK_DOMAIN_TOKEN) return fail("invalid_argument", "the verification link is invalid or expired");
    return HttpResponse.json({ domain: "acme.example" });
  }),

  // ---- SCIM tokens ----
  http.get(`${API}/scim/tokens`, ({ request }) => {
    const ctx = gate(request, "admin");
    return ctx instanceof Response ? ctx : HttpResponse.json({ tokens: db.tokens, base_url: `${origin()}/api/scim/v2`, enabled: true });
  }),

  http.post(`${API}/scim/tokens`, async ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const b = await body<{ name: string; expires_at: string | null }>(request);
    const name = (b.name ?? "").trim();
    if (!name) return fail("invalid_argument", "name is required");
    const secret = `ols_${hex(48)}`;
    const token: ScimToken = {
      id: `scim-${++db.seq}`, name, prefix: secret.slice(0, 12), created_by_email: "admin@openlog.local", created_at: formatTs(Date.now()),
      last_used_at: null, expires_at: b.expires_at ?? null, revoked_at: null,
    };
    db.tokens = [token, ...db.tokens];
    return persist(HttpResponse.json({ token, secret }, { status: 201 }));
  }),

  http.delete(`${API}/scim/tokens/:id`, ({ request, params }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const t = db.tokens.find((x) => x.id === params.id);
    if (!t) return fail("not_found", "SCIM token not found");
    db.tokens = db.tokens.map((x) => (x.id === t.id ? { ...x, revoked_at: x.revoked_at ?? formatTs(Date.now()) } : x));
    return noContent();
  }),

  // ---- sign-in and single logout ----
  http.post(`${API}/auth/sso/discover`, async ({ request }) => {
    const email = (await body<{ email: string }>(request)).email ?? "";
    if (!/^[^@\s]+@[^@\s]+$/.test(email.trim())) return fail("invalid_argument", "a valid email address is required");
    const c = connectionForEmail(email);
    return HttpResponse.json({ sso: !!c, organization_name: c ? ORG_NAME : null, connection_name: c ? labelOf(c) : null, protocol: c?.protocol ?? null, enforced: !!c?.enforce });
  }),

  // The mock identity provider signs the user in at once: the returned URL is the post-login target.
  http.post(`${API}/auth/sso/start`, async ({ request }) => {
    const b = await body<{ email: string; redirect: string }>(request);
    if (!connectionForEmail(b.email ?? "")) return fail("not_found", "single sign-on is not set up for this e-mail domain");
    mockAuth.signIn();
    setMockSsoSession(true);
    const target = b.redirect && b.redirect.startsWith("/") && !b.redirect.startsWith("//") ? b.redirect : "/hosts";
    return HttpResponse.json({ redirect_url: target });
  }),

  http.get(`${API}/auth/sso/session`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
    if (!ssoSession()) return HttpResponse.json({ sso: false, protocol: null, connection_id: null, connection_name: null, idp_logout: false });
    const c = db.connections[0] ?? null;
    return HttpResponse.json({ sso: true, protocol: c?.protocol ?? "oidc", connection_id: c?.id ?? null, connection_name: c ? labelOf(c) : "Mock IdP", idp_logout: true });
  }),

  // The mock identity provider ends its session at once and returns to the login page.
  http.post(`${API}/auth/sso/logout`, async ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
    const b = await body<{ redirect: string }>(request);
    const sso = ssoSession();
    mockAuth.signOut();
    setMockSsoSession(false);
    if (!sso) return HttpResponse.json({ protocol: null, redirect_url: null, post: null, revoked_sessions: 1 });
    const allowed = ["/login", ...(db.connections[0]?.logout_redirect_allowlist ?? [])];
    const target = b.redirect && allowed.includes(b.redirect) ? b.redirect : "/login";
    return HttpResponse.json({ protocol: db.connections[0]?.protocol ?? "oidc", redirect_url: `${target}?sso_logout=ok`, post: null, revoked_sessions: 1 });
  }),
];
