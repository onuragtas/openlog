// Read-only organization UI (suspended organizations, D-105; operator support views): one source decides whether the
// current organization accepts changes, and pages hide or disable their mutating actions with an explanation. The server
// enforces it anyway (403 org_suspended / support views are read-only); sign-in, queries, data exports, account and
// organization deletion and support access stay available (internal/api/operator_access.go allowedWhileSuspended) and
// must not use these helpers.
import { MutationCache, useQuery, type QueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { ApiError } from "@/api/client";
import { orgSaaSStateQuery } from "@/api/operator";
import { atLeast, can, type Permission, type Role } from "@/api/roles";
import { useSupportSessionId } from "@/components/operator/useSupportSession";

export interface OrgWritable {
  /** false: the organization is read-only for this user right now. */
  writable: boolean;
  /** Why it is read-only (shown as tooltip and notice); null when writable. */
  reason: string | null;
  /** "suspended" | "support" | null */
  cause: "suspended" | "support" | null;
}

/** Whether the current organization accepts changes (GET /api/v1/orgs/current/saas, the operator support view). */
export function useOrgWritable(): OrgWritable {
  const { t } = useTranslation();
  const me = useMe().data;
  const supportId = useSupportSessionId();
  const state = useQuery({ ...orgSaaSStateQuery(), enabled: !!me?.organization }).data;
  if (state?.suspended) return { writable: false, reason: t("saas.readOnly.suspended"), cause: "suspended" };
  if (supportId) return { writable: false, reason: t("saas.readOnly.support"), cause: "support" };
  return { writable: true, reason: null, cause: null };
}

/** Permissions that only read; every other permission is a change and needs a writable organization. */
const READ_PERMISSIONS = new Set<Permission>(["license_keys.list", "api_keys.list", "audit.read"]);

/** Role checks of the current user combined with the organization's read-only state. */
export function usePermissions() {
  const me = useMe().data;
  const role: Role | null = me?.role ?? null;
  const w = useOrgWritable();
  return {
    ...w,
    role,
    /** The role allows the permission and, for changes, the organization is writable. */
    can: (permission: Permission) => can(role, permission) && (READ_PERMISSIONS.has(permission) || w.writable),
    /** The role is at least `min` and the organization is writable (for changes gated by rank). */
    canWriteAs: (min: Role) => atLeast(role, min) && w.writable,
  };
}

/** A mutation failed with 403 org_suspended: the organization was suspended meanwhile. */
export function isOrgSuspendedError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 403 && error.code === "org_suspended";
}

/**
 * Mutation cache of the app's QueryClient: when any mutation is rejected with org_suspended the SaaS state is refetched,
 * so every page switches to read-only at once instead of failing action by action.
 */
export function createMutationCache(getClient: () => QueryClient | undefined): MutationCache {
  return new MutationCache({
    onError: (error) => {
      if (isOrgSuspendedError(error)) void getClient()?.invalidateQueries({ queryKey: orgSaaSStateQuery().queryKey });
    },
  });
}
