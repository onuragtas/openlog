import type { components } from "./schema.gen";

export type Role = components["schemas"]["Role"];

export const ROLES: readonly Role[] = ["owner", "admin", "member", "viewer"];

const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };

export type Permission =
  | "license_keys.list"
  | "api_keys.list"
  | "api_keys.create"
  | "org.update"
  | "members.manage"
  | "invitations.manage"
  | "license_keys.manage"
  | "api_keys.revoke_any"
  | "audit.read"
  | "fleet.manage";

/** Mirrors internal/auth/roles.go. The server enforces permissions; the UI only hides actions. */
const MIN_ROLE: Record<Permission, Role> = {
  "license_keys.list": "member",
  "api_keys.list": "member",
  "api_keys.create": "member",
  "org.update": "admin",
  "members.manage": "admin",
  "invitations.manage": "admin",
  "license_keys.manage": "admin",
  "api_keys.revoke_any": "admin",
  "audit.read": "admin",
  "fleet.manage": "admin",
};

export function atLeast(role: Role | null | undefined, min: Role): boolean {
  return !!role && RANK[role] >= RANK[min];
}

export function can(role: Role | null | undefined, permission: Permission): boolean {
  return atLeast(role, MIN_ROLE[permission]);
}

/** Roles `actor` may give a member whose role is `from`; only owners grant or remove "owner". */
export function assignableRoles(actor: Role | null | undefined, from: Role): Role[] {
  if (!can(actor, "members.manage")) return [];
  if (from === "owner" && actor !== "owner") return [];
  return ROLES.filter((r) => r !== "owner" || actor === "owner");
}
