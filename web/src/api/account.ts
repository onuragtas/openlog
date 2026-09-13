// Account, organization and key management (docs/contracts/api.md). Query
// factories follow queries.ts; mutations are plain async functions for
// useMutation. The server enforces every permission; roles.ts only hides UI.
import { queryOptions, useQuery } from "@tanstack/react-query";
import { clearAuthState, getSelectedOrg, setCsrfToken, setSelectedOrg } from "./auth";
import { api, expectOk, unwrap } from "./client";
import type { Role } from "./roles";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type Me = S["Me"];
export type Organization = S["Organization"];
export type Member = S["Member"];
export type Invitation = S["Invitation"];
export type InvitationLookup = S["InvitationLookup"];
export type LicenseKey = S["LicenseKey"];
export type ApiKey = S["APIKey"];
export type Session = S["Session"];
export type AuthConfig = S["AuthConfig"];

function remember(me: Me): Me {
  setCsrfToken(me.csrf_token);
  return me;
}

export const meQuery = () =>
  queryOptions({
    queryKey: ["auth", "me"],
    queryFn: async ({ signal }) => {
      const res = await api.GET("/api/v1/auth/me", { signal });
      // A remembered organization the user no longer belongs to: use the default one.
      if (res.response.status === 403 && getSelectedOrg()) {
        setSelectedOrg(null);
        return remember(unwrap(await api.GET("/api/v1/auth/me", { signal })));
      }
      return remember(unwrap(res));
    },
    staleTime: 5 * 60_000,
  });

export function useMe() {
  return useQuery(meQuery());
}

export const authConfigQuery = () =>
  queryOptions({
    queryKey: ["auth", "config"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/auth/config", { signal })),
    staleTime: Infinity,
  });

export const currentOrgQuery = () =>
  queryOptions({
    queryKey: ["settings", "org"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/orgs/current", { signal })),
  });

export const membersQuery = () =>
  queryOptions({
    queryKey: ["settings", "members"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/members", { signal })).members,
  });

export const invitationsQuery = () =>
  queryOptions({
    queryKey: ["settings", "invitations"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/invitations", { signal })).invitations,
  });

export const licenseKeysQuery = () =>
  queryOptions({
    queryKey: ["settings", "license-keys"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/license-keys", { signal })).license_keys,
  });

export const apiKeysQuery = () =>
  queryOptions({
    queryKey: ["settings", "api-keys"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/api-keys", { signal })).api_keys,
  });

export const sessionsQuery = () =>
  queryOptions({
    queryKey: ["settings", "sessions"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/sessions", { signal })).sessions,
  });

/** Signs in; the session cookie is set by the response. */
export async function login(email: string, password: string): Promise<Me> {
  setSelectedOrg(null);
  return remember(unwrap(await api.POST("/api/v1/auth/login", { body: { email, password } })));
}

export async function logout(): Promise<void> {
  try {
    const res = await api.POST("/api/v1/auth/logout");
    if (res.response.status !== 401) expectOk(res);
  } finally {
    clearAuthState();
    setSelectedOrg(null);
  }
}

export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  expectOk(await api.POST("/api/v1/auth/password", { body: { current_password: currentPassword, new_password: newPassword } }));
}

export async function renameOrg(name: string): Promise<Organization> {
  return unwrap(await api.PATCH("/api/v1/orgs/current", { body: { name } }));
}

export async function updateMemberRole(userId: string, role: Role): Promise<void> {
  expectOk(await api.PATCH("/api/v1/members/{user_id}", { params: { path: { user_id: userId } }, body: { role } }));
}

export async function removeMember(userId: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/members/{user_id}", { params: { path: { user_id: userId } } }));
}

export async function createInvitation(email: string, role: Role) {
  return unwrap(await api.POST("/api/v1/invitations", { body: { email, role } }));
}

export async function revokeInvitation(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/invitations/{id}", { params: { path: { id } } }));
}

export async function lookupInvitation(token: string): Promise<InvitationLookup> {
  return unwrap(await api.POST("/api/v1/invitations/lookup", { body: { token } }));
}

export async function acceptInvitation(token: string, password: string, name: string): Promise<Me> {
  return remember(unwrap(await api.POST("/api/v1/invitations/accept", { body: { token, password, name } })));
}

/** Creates an ingest key. With `key`, that value is imported (response `key` is null). */
export async function createLicenseKey(name: string, key?: string) {
  return unwrap(await api.POST("/api/v1/license-keys", { body: key === undefined ? { name } : { name, key } }));
}

export async function revokeLicenseKey(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/license-keys/{id}", { params: { path: { id } } }));
}

export async function createApiKey(name: string, expiresAt: string | null) {
  return unwrap(await api.POST("/api/v1/api-keys", { body: { name, expires_at: expiresAt } }));
}

export async function revokeApiKey(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/api-keys/{id}", { params: { path: { id } } }));
}

export async function revokeSession(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/sessions/{id}", { params: { path: { id } } }));
}

/** Accept link for an invitation token. The token is in the fragment, so it never reaches server logs. */
export function invitationLink(token: string, origin = window.location.origin): string {
  return `${origin}/invite#token=${encodeURIComponent(token)}`;
}
