// Single sign-on, domain verification and SCIM tokens (docs/contracts/api.md "Single sign-on", "SCIM").
// The server enforces every permission (admin+; enforcement owner-only); the UI only hides actions.
import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { clearAuthState, setSelectedOrg } from "./auth";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type SsoState = S["SSOState"];
export type SsoConnection = S["SSOConnection"];
export type SsoConnectionInput = S["SSOConnectionInput"];
export type SsoTestResult = S["SSOTestResult"];
export type SsoDomain = S["SSODomain"];
export type SsoRoleMapping = S["SSORoleMapping"];
export type SsoRoleMappings = S["SSORoleMappings"];
export type ScimToken = S["SCIMToken"];
export type SsoDiscovery = S["SSODiscovery"];
export type SsoProtocol = SsoConnection["protocol"];
export type SsoHealth = S["SSOHealth"];
export type SsoHealthStatus = SsoHealth["status"];
export type SsoSessionInfo = S["SSOSessionInfo"];
export type SsoLogoutResult = S["SSOLogoutResult"];
export type InvitationSso = NonNullable<S["InvitationLookup"]["sso"]>;

/** Single-connection API: the organization's default connection. */
export const ssoStateQuery = () =>
  queryOptions({
    queryKey: ["settings", "sso", "default"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/sso/connection", { signal })),
  });

/** All connections of the organization (`connections`, the default first; `connection` is null). */
export const ssoConnectionsQuery = () =>
  queryOptions({
    queryKey: ["settings", "sso", "connections"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/sso/connections", { signal })),
  });

/** One connection; `service_provider` carries its SAML values. */
export const ssoConnectionQuery = (id: string) =>
  queryOptions({
    queryKey: ["settings", "sso", "connections", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/sso/connections/{id}", { params: { path: { id } }, signal })),
  });

export const ssoDomainsQuery = () =>
  queryOptions({
    queryKey: ["settings", "sso", "domains"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/sso/domains", { signal })).domains,
  });

/** Role mappings: organization-wide (connectionId null) or of one connection. */
export const ssoRoleMappingsQuery = (connectionId: string | null = null) =>
  queryOptions({
    queryKey: ["settings", "sso", "role-mappings", connectionId ?? ""],
    queryFn: async ({ signal }) =>
      connectionId
        ? unwrap(await api.GET("/api/v1/sso/connections/{id}/role-mappings", { params: { path: { id: connectionId } }, signal })).mappings
        : unwrap(await api.GET("/api/v1/sso/role-mappings", { signal })).mappings,
  });

/** Whether the current session was created by single sign-on (user menu "Sign out everywhere"). */
export const ssoSessionQuery = () =>
  queryOptions({
    queryKey: ["auth", "sso-session"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/auth/sso/session", { signal })),
    retry: false,
    staleTime: 5 * 60_000,
  });

/** Stores a state response of a connection mutation in the query cache (list and the connection itself). */
export function storeSsoState(qc: QueryClient, res: SsoState): void {
  qc.setQueryData(ssoConnectionsQuery().queryKey, (old: SsoState | undefined) => ({ ...(old ?? res), connections: res.connections, connection: null }));
  if (res.connection) qc.setQueryData(ssoConnectionQuery(res.connection.id).queryKey, res);
}

/** The input that saves a connection's current settings unchanged (the stored secret and metadata are kept). */
export function connectionInput(c: SsoConnection, patch: Partial<SsoConnectionInput> = {}): SsoConnectionInput {
  return {
    protocol: c.protocol,
    name: c.name,
    enabled: c.enabled,
    email_attribute: c.email_attribute,
    name_attribute: c.name_attribute,
    groups_attribute: c.groups_attribute,
    jit_enabled: c.jit_enabled,
    default_role: c.default_role,
    session_max_age_seconds: c.session_max_age_seconds,
    logout_redirect_allowlist: c.logout_redirect_allowlist,
    allow_external_invitations: c.allow_external_invitations,
    ...(c.oidc ? { oidc: { issuer: c.oidc.issuer, client_id: c.oidc.client_id, scopes: c.oidc.scopes, require_email_verified: c.oidc.require_email_verified } } : {}),
    ...(c.saml
      ? {
          saml: {
            idp_metadata_url: c.saml.idp_metadata_url,
            // The pinned metadata signing certificate is kept when omitted; the unsigned choice must be repeated (D-098).
            allow_unsigned_metadata: c.saml.allow_unsigned_metadata,
            allow_idp_initiated: c.saml.allow_idp_initiated,
            relay_state_allowlist: c.saml.relay_state_allowlist,
            sign_authn_requests: c.saml.sign_authn_requests,
          },
        }
      : {}),
    ...patch,
  };
}

export async function listSsoConnections(): Promise<SsoState> {
  return unwrap(await api.GET("/api/v1/sso/connections"));
}

export async function getSsoConnection(id: string): Promise<SsoState> {
  return unwrap(await api.GET("/api/v1/sso/connections/{id}", { params: { path: { id } } }));
}

export async function createSsoConnection(body: SsoConnectionInput): Promise<SsoState> {
  return unwrap(await api.POST("/api/v1/sso/connections", { body }));
}

export async function updateSsoConnection(id: string, body: SsoConnectionInput): Promise<SsoState> {
  return unwrap(await api.PUT("/api/v1/sso/connections/{id}", { params: { path: { id } }, body }));
}

export async function deleteSsoConnectionById(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/sso/connections/{id}", { params: { path: { id } } }));
}

export async function testSsoConnectionById(id: string): Promise<SsoTestResult> {
  return unwrap(await api.POST("/api/v1/sso/connections/{id}/test", { params: { path: { id } } }));
}

/** Starts a test sign-in of a connection; the browser then goes to the returned identity provider URL. */
export async function startSsoTestById(id: string): Promise<string> {
  return unwrap(await api.POST("/api/v1/sso/connections/{id}/test/start", { params: { path: { id } } })).redirect_url;
}

/** Fetches the IdP documents of a connection now; the returned connection carries the updated health. */
export async function refreshSsoConnection(id: string): Promise<SsoState> {
  return unwrap(await api.POST("/api/v1/sso/connections/{id}/refresh", { params: { path: { id } } }));
}

/** Confirms the pending IdP metadata change of a SAML connection (`saml.pending_metadata.digest`, D-098). */
export async function acceptSsoMetadata(id: string, digest: string): Promise<SsoState> {
  return unwrap(await api.POST("/api/v1/sso/connections/{id}/metadata/accept", { params: { path: { id } }, body: { digest } }));
}

export async function updateSsoConnectionEnforcement(id: string, enforce: boolean, breakGlassUserIds: string[]): Promise<SsoState> {
  return unwrap(
    await api.PUT("/api/v1/sso/connections/{id}/enforcement", { params: { path: { id } }, body: { enforce, break_glass_user_ids: breakGlassUserIds } }),
  );
}

/** Replaces role mappings: organization-wide (connectionId null) or of one connection (empty = use the organization-wide ones). */
export async function replaceSsoRoleMappingsFor(connectionId: string | null, mappings: SsoRoleMapping[]): Promise<SsoRoleMapping[]> {
  if (!connectionId) return replaceSsoRoleMappings(mappings);
  return unwrap(await api.PUT("/api/v1/sso/connections/{id}/role-mappings", { params: { path: { id: connectionId } }, body: { mappings } })).mappings;
}

/** Routes a domain's sign-ins to a connection (null = the default connection). */
export async function assignSsoDomain(id: string, connectionId: string | null): Promise<SsoDomain> {
  return unwrap(await api.PUT("/api/v1/sso/domains/{id}", { params: { path: { id } }, body: { connection_id: connectionId } }));
}

export async function getSsoSession(): Promise<SsoSessionInfo> {
  return unwrap(await api.GET("/api/v1/auth/sso/session"));
}

/** "Sign out everywhere": ends the openlog session(s); the result says where the browser ends the IdP session. */
export async function ssoLogout(redirect = "/login"): Promise<SsoLogoutResult> {
  try {
    return unwrap(await api.POST("/api/v1/auth/sso/logout", { body: { redirect } }));
  } finally {
    clearAuthState();
    setSelectedOrg(null);
  }
}

/** Continues a logout at the identity provider: navigation (redirect_url) or an auto-submitted form (post). Returns false when nothing is left to do. */
export function continueSsoLogout(res: SsoLogoutResult, assign: (url: string) => void = (url) => window.location.assign(url)): boolean {
  if (res.redirect_url) {
    assign(res.redirect_url);
    return true;
  }
  if (res.post) {
    const form = document.createElement("form");
    form.method = "POST";
    form.action = res.post.url;
    form.hidden = true;
    for (const [name, value] of Object.entries(res.post.fields)) {
      const input = document.createElement("input");
      input.type = "hidden";
      input.name = name;
      input.value = value;
      form.appendChild(input);
    }
    document.body.appendChild(form);
    form.submit();
    return true;
  }
  return false;
}

/** Known ?sso_logout= values of the login page. */
export const SSO_LOGOUT_RESULTS = ["ok", "partial"] as const;
export type SsoLogoutStatus = (typeof SSO_LOGOUT_RESULTS)[number];

/** Returns a known ?sso_logout= value of the login page, else undefined. */
export function ssoLogoutStatus(v: unknown): SsoLogoutStatus | undefined {
  return typeof v === "string" && (SSO_LOGOUT_RESULTS as readonly string[]).includes(v) ? (v as SsoLogoutStatus) : undefined;
}

/** Session storage key: the connection whose test sign-in is running (the settings page reopens it on return). */
export const SSO_TEST_CONNECTION_KEY = "openlog.sso.testConnection";

/** Display name of a connection: its name, else the protocol. */
export function ssoConnectionLabel(c: Pick<SsoConnection, "name" | "protocol">): string {
  return c.name || (c.protocol === "oidc" ? "OpenID Connect" : "SAML 2.0");
}

export const scimTokensQuery = () =>
  queryOptions({
    queryKey: ["settings", "sso", "scim-tokens"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/scim/tokens", { signal })),
  });

export async function saveSsoConnection(body: SsoConnectionInput): Promise<SsoState> {
  return unwrap(await api.PUT("/api/v1/sso/connection", { body }));
}

export async function deleteSsoConnection(): Promise<void> {
  expectOk(await api.DELETE("/api/v1/sso/connection"));
}

export async function testSsoConnection(): Promise<SsoTestResult> {
  return unwrap(await api.POST("/api/v1/sso/connection/test"));
}

/** Starts a test sign-in; the browser then goes to the returned identity provider URL. */
export async function startSsoTest(): Promise<string> {
  return unwrap(await api.POST("/api/v1/sso/connection/test/start")).redirect_url;
}

export async function updateSsoEnforcement(enforce: boolean, breakGlassUserIds: string[]): Promise<SsoState> {
  return unwrap(await api.PUT("/api/v1/sso/enforcement", { body: { enforce, break_glass_user_ids: breakGlassUserIds } }));
}

export async function replaceSsoRoleMappings(mappings: SsoRoleMapping[]): Promise<SsoRoleMapping[]> {
  return unwrap(await api.PUT("/api/v1/sso/role-mappings", { body: { mappings } })).mappings;
}

export async function addSsoDomain(domain: string): Promise<SsoDomain> {
  return unwrap(await api.POST("/api/v1/sso/domains", { body: { domain } }));
}

/** Administrative mailboxes a domain verification e-mail can be sent to (server: sso.DomainEmailLocalParts). */
export type DomainEmailLocalPart = "admin" | "administrator" | "hostmaster" | "postmaster" | "webmaster";

export async function verifySsoDomain(id: string, method: "dns_txt" | "email", emailLocalPart?: string): Promise<SsoDomain> {
  return unwrap(
    await api.POST("/api/v1/sso/domains/{id}/verify", {
      params: { path: { id } },
      // The server validates the mailbox; the UI offers the server's list (SsoState.domain_email_local_parts).
      body: method === "email" ? { method, email_local_part: (emailLocalPart ?? "admin") as DomainEmailLocalPart } : { method },
    }),
  );
}

/** Returns a known ?sso_error= code of the login page, else undefined. */
export function ssoErrorCode(v: unknown): SsoErrorCode | undefined {
  return typeof v === "string" && (SSO_ERROR_CODES as readonly string[]).includes(v) ? (v as SsoErrorCode) : undefined;
}

export async function deleteSsoDomain(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/sso/domains/{id}", { params: { path: { id } } }));
}

/** Confirms a domain from the e-mailed link (public). */
export async function verifySsoDomainEmail(token: string): Promise<string> {
  return unwrap(await api.POST("/api/v1/sso/domains/verify-email", { body: { token } })).domain;
}

export async function createScimToken(name: string, expiresAt: string | null) {
  return unwrap(await api.POST("/api/v1/scim/tokens", { body: { name, expires_at: expiresAt } }));
}

export async function revokeScimToken(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/scim/tokens/{id}", { params: { path: { id } } }));
}

/** Whether an e-mail address signs in with single sign-on (public). */
export async function discoverSso(email: string): Promise<SsoDiscovery> {
  return unwrap(await api.POST("/api/v1/auth/sso/discover", { body: { email } }));
}

/** Starts an SSO sign-in; returns the identity provider URL (the binding cookie is set by the response). */
export async function startSsoLogin(email: string, redirect?: string): Promise<string> {
  setSelectedOrg(null);
  return unwrap(await api.POST("/api/v1/auth/sso/start", { body: { email, redirect: redirect ?? "/hosts" } })).redirect_url;
}

/** Known ?sso_error= codes of the login page (docs/contracts/api.md). */
export const SSO_ERROR_CODES = [
  "expired",
  "invalid_request",
  "idp_error",
  "invalid_response",
  "replay",
  "disabled",
  "email_missing",
  "email_not_verified",
  "domain_not_verified",
  "not_member",
  "deprovisioned",
  "account_disabled",
  "unavailable",
  "user_limit",
] as const;
export type SsoErrorCode = (typeof SSO_ERROR_CODES)[number];
