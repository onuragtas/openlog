// Single sign-on, domain verification and SCIM tokens (docs/contracts/api.md "Single sign-on", "SCIM").
// The server enforces every permission (admin+; enforcement owner-only); the UI only hides actions.
import { queryOptions } from "@tanstack/react-query";
import { setSelectedOrg } from "./auth";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type SsoState = S["SSOState"];
export type SsoConnection = S["SSOConnection"];
export type SsoConnectionInput = S["SSOConnectionInput"];
export type SsoTestResult = S["SSOTestResult"];
export type SsoDomain = S["SSODomain"];
export type SsoRoleMapping = S["SSORoleMapping"];
export type ScimToken = S["SCIMToken"];
export type SsoDiscovery = S["SSODiscovery"];
export type SsoProtocol = SsoConnection["protocol"];

export const ssoStateQuery = () =>
  queryOptions({
    queryKey: ["settings", "sso"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/sso/connection", { signal })),
  });

export const ssoDomainsQuery = () =>
  queryOptions({
    queryKey: ["settings", "sso", "domains"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/sso/domains", { signal })).domains,
  });

export const ssoRoleMappingsQuery = () =>
  queryOptions({
    queryKey: ["settings", "sso", "role-mappings"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/sso/role-mappings", { signal })).mappings,
  });

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
] as const;
export type SsoErrorCode = (typeof SSO_ERROR_CODES)[number];
