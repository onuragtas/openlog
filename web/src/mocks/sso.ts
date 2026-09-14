// MSW handlers for single sign-on, domain verification and SCIM tokens (docs/contracts/api.md "Single sign-on")
// with in-memory data mirroring the server rules (internal/sso). In the browser (dev:mock, Playwright) the data is
// kept in sessionStorage, because a test sign-in is a full page navigation; Vitest calls resetMockSso().
import { http, HttpResponse } from "msw";
import type { Role } from "@/api/roles";
import type { ScimToken, SsoConnection, SsoDomain, SsoRoleMapping, SsoState } from "@/api/sso";
import { authenticate, mockAuth, mockMembers } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1";
const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };
export const MOCK_DOMAIN_TOKEN = "oldv_mock-domain-verification";
const STORAGE_KEY = "openlog.mock.sso";
/** sessionStorage key a Playwright test sets to "enabled-oidc" to start with an enabled, tested OIDC connection. */
export const MOCK_SSO_PRESET_KEY = "openlog.mock.sso.preset";

type Code = "invalid_argument" | "unauthenticated" | "permission_denied" | "not_found" | "already_exists" | "failed_precondition";
const STATUS: Record<Code, number> = { invalid_argument: 400, unauthenticated: 401, permission_denied: 403, not_found: 404, already_exists: 409, failed_precondition: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

interface SsoDb {
  connection: SsoConnection | null;
  spCertificate: string;
  domains: SsoDomain[];
  mappings: SsoRoleMapping[];
  tokens: ScimToken[];
  seq: number;
}

const origin = () => (typeof window !== "undefined" && window.location ? window.location.origin : "http://localhost");

function seed(): SsoDb {
  const now = Date.now();
  return {
    connection: null,
    spCertificate: "-----BEGIN CERTIFICATE-----\nMIIBmock\n-----END CERTIFICATE-----\n",
    domains: [
      {
        id: "dom-1", domain: "openlog.local", verified: true, verified_at: formatTs(now - 86_400_000), verification_method: "dns_txt",
        dns_record: { type: "TXT", name: "_openlog-verification.openlog.local", value: "openlog-domain-verification=mock1" },
        email_address: null, email_expires_at: null, last_checked_at: formatTs(now - 86_400_000), created_at: formatTs(now - 2 * 86_400_000),
      },
    ],
    mappings: [],
    tokens: [],
    seq: 10,
  };
}

function defaultConnection(protocol: "oidc" | "saml"): SsoConnection {
  const ts = formatTs(Date.now());
  return {
    id: "0f7b3c1e-5a2d-4c1b-9e8f-0000000055c0", protocol, name: "", enabled: false, oidc: null, saml: null,
    email_attribute: "", name_attribute: "", groups_attribute: "", jit_enabled: true, default_role: "viewer", session_max_age_seconds: 0,
    enforce: false, break_glass_user_ids: [], config_version: 0, tested: false, last_test: null, created_at: ts, updated_at: ts,
  };
}

function storage(): Storage | null {
  try {
    return import.meta.env.MODE !== "test" && typeof sessionStorage !== "undefined" ? sessionStorage : null;
  } catch {
    return null;
  }
}

function load(): SsoDb {
  let data = seed();
  const s = storage();
  if (!s) return data;
  try {
    const raw = s.getItem(STORAGE_KEY);
    if (raw) data = JSON.parse(raw) as SsoDb;
    if (s.getItem(MOCK_SSO_PRESET_KEY) === "enabled-oidc" && !data.connection) {
      data.connection = {
        ...defaultConnection("oidc"), name: "Mock IdP", enabled: true, tested: true, config_version: 1,
        oidc: { issuer: "https://idp.example.com", client_id: "openlog", scopes: [], require_email_verified: true, client_secret_set: true },
      };
    }
  } catch {
    // ignore unreadable mock state
  }
  return data;
}

let db = load();

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
  save();
}

/** Replaces the mock connection (tests). */
export function setMockSsoConnection(c: Partial<SsoConnection> | null): void {
  db.connection = c === null ? null : { ...defaultConnection("oidc"), ...c };
  save();
}

function state(): SsoState {
  const c = db.connection;
  const saml = c?.protocol === "saml";
  const entity = c ? `${origin()}/api/v1/sso/saml/${c.id}/metadata` : null;
  return {
    available: true, secrets_encrypted: true, scim_enabled: true, email_verification_available: true,
    domain_email_local_parts: ["admin", "administrator", "hostmaster", "postmaster", "webmaster"],
    service_provider: {
      oidc_redirect_uri: `${origin()}/api/v1/sso/oidc/callback`, scim_base_url: `${origin()}/api/scim/v2`,
      saml_entity_id: saml ? entity : null, saml_acs_url: saml && c ? `${origin()}/api/v1/sso/saml/${c.id}/acs` : null,
      saml_metadata_url: saml ? entity : null, saml_certificate_pem: saml ? db.spCertificate : null,
    },
    connection: c,
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
const ssoDomainVerified = (email: string) => db.domains.some((x) => x.domain === domainOf(email) && x.verified);

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
  oidc: { issuer?: string; client_id?: string; client_secret?: string | null; scopes?: string[]; require_email_verified?: boolean };
  saml: { idp_metadata_url?: string; idp_metadata_xml?: string; allow_idp_initiated?: boolean; relay_state_allowlist?: string[]; sign_authn_requests?: boolean };
}

export const ssoHandlers = [
  http.get(`${API}/sso/connection`, ({ request }) => {
    const ctx = gate(request, "admin");
    return ctx instanceof Response ? ctx : HttpResponse.json(state());
  }),

  http.put(`${API}/sso/connection`, async ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const b = await body<ConnectionBody>(request);
    if (b.protocol !== "oidc" && b.protocol !== "saml") return fail("invalid_argument", "protocol must be oidc or saml");
    const role = b.default_role ?? "viewer";
    if (!assignable(role)) return fail("invalid_argument", "default_role must be admin, member or viewer (owners are managed in openlog)");
    const prev = db.connection;
    if (prev?.enforce && !b.enabled) return fail("failed_precondition", "turn off single sign-on enforcement before disabling the connection");
    const next: SsoConnection = { ...(prev ?? defaultConnection(b.protocol)), protocol: b.protocol, name: b.name ?? "", enabled: !!b.enabled };
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
      next.saml = {
        idp_metadata_url: url, idp_entity_id: "https://idp.example.com/metadata", idp_sso_url: "https://idp.example.com/sso",
        idp_certificates: ["3A1F9C427B00DEADBEEF112233445566778899AABBCCDDEEFF00112233445566"], idp_cert_not_after: formatTs(Date.now() + 365 * 86_400_000),
        allow_idp_initiated: !!b.saml?.allow_idp_initiated, relay_state_allowlist: b.saml?.relay_state_allowlist ?? [], sign_authn_requests: !!b.saml?.sign_authn_requests,
      };
      next.oidc = null;
    }
    Object.assign(next, {
      default_role: role, jit_enabled: b.jit_enabled ?? true, session_max_age_seconds: b.session_max_age_seconds ?? 0,
      email_attribute: b.email_attribute ?? "", name_attribute: b.name_attribute ?? "", groups_attribute: b.groups_attribute ?? "",
      config_version: next.config_version + 1, tested: false, updated_at: formatTs(Date.now()),
    });
    if (next.last_test?.ok) next.last_test = { ...next.last_test, current: false };
    db.connection = next;
    return persist(HttpResponse.json(state()));
  }),

  http.delete(`${API}/sso/connection`, ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    if (!db.connection) return fail("not_found", "single sign-on is not configured");
    if (db.connection.enforce) return fail("failed_precondition", "turn off single sign-on enforcement before deleting the connection");
    db.connection = null;
    return noContent();
  }),

  http.post(`${API}/sso/connection/test`, ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = db.connection;
    if (!c) return fail("not_found", "single sign-on is not configured");
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
    return HttpResponse.json({ ok: true, checks });
  }),

  // The mock "identity provider" signs the test user in at once and returns to the settings page.
  http.post(`${API}/sso/connection/test/start`, ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const c = db.connection;
    if (!c) return fail("not_found", "single sign-on is not configured");
    db.connection = {
      ...c, tested: true,
      last_test: { at: formatTs(Date.now()), ok: true, current: true, error: "", details: { email: "admin@openlog.local", groups: ["openlog-admins"], role: "admin", role_mapped: true } },
    };
    return persist(HttpResponse.json({ redirect_url: "/settings/sso?sso_test=ok" }));
  }),

  http.put(`${API}/sso/enforcement`, async ({ request }) => {
    const ctx = gate(request, "owner");
    if (ctx instanceof Response) return ctx;
    const b = await body<{ enforce: boolean; break_glass_user_ids: string[] }>(request);
    const c = db.connection;
    if (!c) return fail("not_found", "single sign-on is not configured");
    const ids = b.break_glass_user_ids ?? [];
    const owners = mockMembers().filter((m) => m.role === "owner").map((m) => m.user_id);
    if (ids.some((id) => !owners.includes(id))) return fail("invalid_argument", "break-glass accounts must be owners of the organization");
    if (b.enforce) {
      if (!c.enabled) return fail("failed_precondition", "enable the connection before enforcing single sign-on");
      if (!c.tested) return fail("failed_precondition", "run a successful test sign-in of the current settings before enforcing single sign-on");
      if (ids.length === 0) return fail("failed_precondition", "name at least one break-glass owner who can still sign in with a password");
      if (!db.domains.some((d) => d.verified)) return fail("failed_precondition", "verify at least one e-mail domain before enforcing single sign-on");
    }
    db.connection = { ...c, enforce: !!b.enforce, break_glass_user_ids: ids };
    return persist(HttpResponse.json(state()));
  }),

  http.get(`${API}/sso/role-mappings`, ({ request }) => {
    const ctx = gate(request, "admin");
    return ctx instanceof Response ? ctx : HttpResponse.json({ mappings: db.mappings });
  }),

  http.put(`${API}/sso/role-mappings`, async ({ request }) => {
    const ctx = gate(request, "admin");
    if (ctx instanceof Response) return ctx;
    const b = await body<{ mappings: { group: string; role: string }[] }>(request);
    const out: SsoRoleMapping[] = [];
    for (const m of b.mappings ?? []) {
      const group = (m.group ?? "").trim();
      if (!group) return fail("invalid_argument", "group is required");
      if (!assignable(m.role)) return fail("invalid_argument", `role of group "${group}" must be admin, member or viewer`);
      if (out.some((x) => x.group === group)) return fail("invalid_argument", `group "${group}" is mapped twice`);
      out.push({ group, role: m.role });
    }
    db.mappings = out.sort((a, b) => a.group.localeCompare(b.group));
    return persist(HttpResponse.json({ mappings: db.mappings }));
  }),

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
      email_address: null, email_expires_at: null, last_checked_at: null, created_at: formatTs(Date.now()),
    };
    db.domains = [...db.domains, d].sort((a, b) => a.domain.localeCompare(b.domain));
    return persist(HttpResponse.json(d, { status: 201 }));
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
    if (d.verified && db.connection?.enforce && db.domains.filter((x) => x.verified).length <= 1) {
      return fail("failed_precondition", "turn off single sign-on enforcement before removing the last verified domain");
    }
    db.domains = db.domains.filter((x) => x.id !== d.id);
    return noContent();
  }),

  http.post(`${API}/sso/domains/verify-email`, async ({ request }) => {
    const token = (await body<{ token: string }>(request)).token ?? "";
    if (token !== MOCK_DOMAIN_TOKEN) return fail("invalid_argument", "the verification link is invalid or expired");
    return HttpResponse.json({ domain: "acme.example" });
  }),

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

  http.post(`${API}/auth/sso/discover`, async ({ request }) => {
    const email = (await body<{ email: string }>(request)).email ?? "";
    if (!/^[^@\s]+@[^@\s]+$/.test(email.trim())) return fail("invalid_argument", "a valid email address is required");
    const c = db.connection;
    const ok = !!c?.enabled && ssoDomainVerified(email);
    return HttpResponse.json({ sso: ok, organization_name: ok ? "Default" : null, protocol: ok && c ? c.protocol : null, enforced: ok && !!c?.enforce });
  }),

  // The mock identity provider signs the user in at once: the returned URL is the post-login target.
  http.post(`${API}/auth/sso/start`, async ({ request }) => {
    const b = await body<{ email: string; redirect: string }>(request);
    if (!db.connection?.enabled || !ssoDomainVerified(b.email ?? "")) return fail("not_found", "single sign-on is not set up for this e-mail domain");
    mockAuth.signIn();
    const target = b.redirect && b.redirect.startsWith("/") && !b.redirect.startsWith("//") ? b.redirect : "/hosts";
    return HttpResponse.json({ redirect_url: target });
  }),
];
